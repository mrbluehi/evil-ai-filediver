package previews

import (
	"cmp"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/go-gl/gl/v4.3-core/gl"
	fnt "github.com/xypwn/filediver/cmd/filediver-gui/fonts"
	"github.com/xypwn/filediver/cmd/filediver-gui/imutils"
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/entity"
)

const (
	unitGalleryCaptureSize  = 512
	levelThumbnailDim       = 200
	unitGalleryMaxTextures  = 96
	unitGalleryCellWidth    = 190
	unitGalleryThumbnailDim = 160
)

type UnitGalleryStats struct {
	TriangleCount   uint64
	AllLODTriangles uint64
	ModelSize       uint64
	ModelExtent     float64
	FileSize        uint64
}

type UnitGallerySort int

const (
	UnitGallerySortName UnitGallerySort = iota
	UnitGallerySortTriangles
	UnitGallerySortModelScale
	UnitGallerySortFileSize
	UnitGallerySortNew
	UnitGallerySortChanged
	UnitGallerySortRemoved
)

type unitGalleryThumbnail struct {
	textureID uint32
	texture   imgui.TextureRef
	lastUsed  uint64
}

type UnitGallery struct {
	renderer                *UnitPreviewState
	removedRenderer         *UnitPreviewState
	getResource             GetResourceFunc
	removedGetResource      GetResourceFunc
	removedUnitCacheDir     string
	captureRendererOverride *UnitPreviewState
	thinhashes              map[stingray.ThinHash]string
	itemType                stingray.Hash
	armorKits               map[stingray.FileID]uint32
	collectionName          string
	unitCacheDir            string
	levelCacheDir           string
	unitPreviewAngle        UnitCamera
	levelPreviewAngle       UnitCamera
	assetGradingKey         string
	hideLevelGround         bool
	diskChecked             map[stingray.FileID]bool
	forceCapture            map[stingray.FileID]struct{}
	onUnitLevelLookup       func(stingray.FileID)

	query                 string
	sortBy                UnitGallerySort
	descending            bool
	filtered              []stingray.FileID
	filterKey             string
	filterLen             int
	filterSort            UnitGallerySort
	filterDesc            bool
	filterHasAnimations   bool
	filterAnimationsReady bool
	gridScale             float32
	searchActive          bool
	hasAnimationsOnly     bool
	animatedUnits         map[stingray.FileID]struct{}
	animationScanDone     bool
	animationScanProgress int
	animationScanTotal    int
	addedUnits            map[stingray.FileID]struct{}
	changedUnits          map[stingray.FileID]struct{}
	removedUnits          map[stingray.FileID]struct{}

	frame                    uint64
	thumbnails               map[stingray.FileID]*unitGalleryThumbnail
	failed                   map[stingray.FileID]string
	modelExtents             map[stingray.FileID]float64
	cameraProfiles           map[stingray.FileID]UnitCamera
	wanted                   map[stingray.FileID]struct{}
	queue                    []stingray.FileID
	queued                   map[stingray.FileID]struct{}
	capturing                stingray.FileID
	captureAttempt           int
	captureFallbackStage     int
	captureBaseCamera        UnitCamera
	captureStarted           time.Time
	captureBestPixels        []byte
	captureBestScore         int
	captureBestStage         int
	captureOriginalWireframe bool
	captureOriginalAABB      bool
	captureOriginalFlat      bool
}

// ConfigureArmorGrid makes this independent gallery capture assembled kits.
func (g *UnitGallery) ConfigureArmorGrid(cacheDir string, kits map[stingray.FileID]uint32) error {
	g.itemType = ArmorSetTypeHash
	g.collectionName = "armor sets"
	g.armorKits = kits
	_, err := g.ConfigureUnitGrid(cacheDir, [2]float32{}, false)
	return err
}

// MarkArmorUnavailable explains records that cannot produce a preview rather
// than queuing an empty scene indefinitely.
func (g *UnitGallery) MarkArmorUnavailable(id stingray.FileID, reason string) {
	if g.itemType == ArmorSetTypeHash {
		g.failed[id] = reason
	}
}

// AddArmorKit makes a kit discovered after the gallery was initialized renderable.
func (g *UnitGallery) AddArmorKit(id stingray.FileID, kitID uint32) {
	if g.itemType != ArmorSetTypeHash {
		return
	}
	if g.armorKits == nil {
		g.armorKits = make(map[stingray.FileID]uint32)
	}
	if existing, ok := g.armorKits[id]; ok && existing == kitID {
		return
	}
	g.armorKits[id] = kitID
	delete(g.failed, id)
	g.filterKey = "\x00"
}

// SetUnitLevelLookupCallback installs the action shown in a unit card's
// right-click menu. It is ignored for level galleries.
func (g *UnitGallery) SetUnitLevelLookupCallback(callback func(stingray.FileID)) {
	g.onUnitLevelLookup = callback
}

func (g *UnitGallery) HasAnimationsFilterEnabled() bool {
	return g.itemType == stingray.Sum("unit") && g.hasAnimationsOnly
}

func (g *UnitGallery) SetAnimationScanResults(units map[stingray.FileID]struct{}, done bool, progress, total int) {
	if g.animationScanDone == done && g.animationScanProgress == progress && g.animationScanTotal == total && (!done || g.animatedUnits != nil) {
		return
	}
	g.animatedUnits = units
	g.animationScanDone = done
	g.animationScanProgress = progress
	g.animationScanTotal = total
	g.InvalidateFilteredResults()
}

func NewUnitGallery(renderer *UnitPreviewState, getResource GetResourceFunc, thinhashes map[stingray.ThinHash]string) *UnitGallery {
	return &UnitGallery{
		renderer:       renderer,
		getResource:    getResource,
		thinhashes:     thinhashes,
		itemType:       stingray.Sum("unit"),
		collectionName: "units",
		sortBy:         UnitGallerySortName,
		gridScale:      1,
		thumbnails:     make(map[stingray.FileID]*unitGalleryThumbnail),
		failed:         make(map[stingray.FileID]string),
		modelExtents:   make(map[stingray.FileID]float64),
		cameraProfiles: make(map[stingray.FileID]UnitCamera),
		wanted:         make(map[stingray.FileID]struct{}),
		queued:         make(map[stingray.FileID]struct{}),
		diskChecked:    make(map[stingray.FileID]bool),
		forceCapture:   make(map[stingray.FileID]struct{}),
	}
}

// ConfigureUnitGrid enables persistent JPEG thumbnails for the Units gallery.
// The caller scopes this directory to the selected game build. A preference
// angle takes precedence over the legacy per-build angle file.
func (g *UnitGallery) ConfigureUnitGrid(cacheDir string, preferredAngle [2]float32, hasPreference bool) (migratedLegacyAngle bool, err error) {
	g.unitCacheDir = cacheDir
	g.unitPreviewAngle = DefaultUnitCamera()
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return false, fmt.Errorf("creating unit thumbnail cache: %w", err)
	}
	anglePath := filepath.Join(cacheDir, "unit-preview-angle.json")
	if hasPreference {
		g.unitPreviewAngle.Rotation = preferredAngle
	} else if data, err := os.ReadFile(anglePath); err == nil {
		var angle [2]float32
		if json.Unmarshal(data, &angle) == nil {
			g.unitPreviewAngle.Rotation = angle
			migratedLegacyAngle = true
		}
	}
	return migratedLegacyAngle, nil
}

// UnitPreviewAngle reports the rotation used when capturing Units thumbnails.
func (g *UnitGallery) UnitPreviewAngle() [2]float32 {
	return g.unitPreviewAngle.Rotation
}

// ConfigureLevelGrid turns the unit renderer gallery into a level gallery and
// loads its persistent thumbnail angle. The cache directory is scoped to the
// currently opened data directory by the GUI.
func (g *UnitGallery) ConfigureLevelGrid(cacheDir string) error {
	g.itemType = stingray.Sum("level")
	g.collectionName = "levels"
	g.levelCacheDir = cacheDir
	g.levelPreviewAngle = DefaultUnitCamera()
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("creating level thumbnail cache: %w", err)
	}
	anglePath := filepath.Join(cacheDir, "preview-angle.json")
	if data, err := os.ReadFile(anglePath); err == nil {
		var angle [2]float32
		if json.Unmarshal(data, &angle) == nil {
			g.levelPreviewAngle.Rotation[0] = angle[0]
			g.levelPreviewAngle.Rotation[1] = angle[1]
		}
	}
	return nil
}

// SetEntityInfo gives level captures the same biome color-grading entity used
// by the full Preview renderer.
func (g *UnitGallery) SetEntityInfo(entityInfo *entity.Entity) {
	if g.renderer != nil {
		g.renderer.SetEntityInfo(entityInfo)
	}
}

// SetAssetGradingKey separates cached level captures by the active planet
// palette. Old thumbnails captured without that context are not reused.
func (g *UnitGallery) SetAssetGradingKey(key string) {
	if g.itemType != stingray.Sum("level") {
		return
	}
	key = strings.TrimPrefix(strings.ToLower(key), "0x")
	if key == "" {
		key = "none"
	}
	if g.assetGradingKey == key {
		return
	}
	if g.capturing != (stingray.FileID{}) {
		g.finishCapture()
	}
	for _, thumbnail := range g.thumbnails {
		gl.DeleteTextures(1, &thumbnail.textureID)
	}
	clear(g.thumbnails)
	clear(g.failed)
	clear(g.diskChecked)
	clear(g.cameraProfiles)
	g.queue = nil
	clear(g.queued)
	clear(g.forceCapture)
	g.assetGradingKey = key
	g.filterKey = "\x00"
	g.filterLen = -1
}

// SetHideLevelGround toggles only the generated terrain object and keeps
// separate disk thumbnails for the visible and hidden ground variants.
func (g *UnitGallery) SetHideLevelGround(hidden bool, ids []stingray.FileID) int {
	if g.renderer != nil {
		g.renderer.SetHideLevelGround(hidden)
	}
	if g.itemType != stingray.Sum("level") || g.hideLevelGround == hidden {
		return 0
	}
	previous := g.hideLevelGround
	if g.capturing != (stingray.FileID{}) {
		g.finishCapture()
	}
	for _, thumbnail := range g.thumbnails {
		gl.DeleteTextures(1, &thumbnail.textureID)
	}
	clear(g.thumbnails)
	clear(g.failed)
	clear(g.diskChecked)
	clear(g.cameraProfiles)
	g.queue = nil
	clear(g.queued)
	clear(g.forceCapture)
	g.hideLevelGround = hidden

	angleKey := levelAngleKey(g.levelPreviewAngle.Rotation)
	queued := 0
	for _, id := range ids {
		if id.Type != g.itemType {
			continue
		}
		oldPath := g.levelThumbnailPathForGround(id, angleKey, previous)
		newPath := g.levelThumbnailPathForGround(id, angleKey, hidden)
		if _, err := os.Stat(oldPath); err != nil {
			continue
		}
		if _, err := os.Stat(newPath); err == nil {
			continue
		}
		g.queued[id] = struct{}{}
		g.forceCapture[id] = struct{}{}
		g.queue = append(g.queue, id)
		queued++
	}
	g.filterKey = "\x00"
	g.filterLen = -1
	return queued
}

// SetLevelPreviewAngle uses the current Preview camera rotation for level
// captures and queues every existing cached thumbnail for recapture.
func (g *UnitGallery) SetLevelPreviewAngle(rotation [2]float32, ids []stingray.FileID) (int, error) {
	oldAngle := g.levelPreviewAngle.Rotation
	oldKey := levelAngleKey(oldAngle)
	newKey := levelAngleKey(rotation)
	if oldKey == newKey {
		return 0, nil
	}
	if g.capturing != (stingray.FileID{}) {
		g.finishCapture()
	}
	for _, thumbnail := range g.thumbnails {
		gl.DeleteTextures(1, &thumbnail.textureID)
	}
	clear(g.thumbnails)
	clear(g.failed)
	clear(g.diskChecked)
	clear(g.cameraProfiles)
	g.queue = nil
	clear(g.queued)
	clear(g.forceCapture)
	g.levelPreviewAngle.Rotation = rotation

	queued := 0
	for _, id := range ids {
		if id.Type != g.itemType {
			continue
		}
		if _, err := os.Stat(g.levelThumbnailPath(id, oldKey)); err != nil {
			continue
		}
		g.queued[id] = struct{}{}
		g.forceCapture[id] = struct{}{}
		g.queue = append(g.queue, id)
		queued++
	}
	angleData, err := json.Marshal([2]float32{rotation[0], rotation[1]})
	if err != nil {
		return queued, err
	}
	if err := os.WriteFile(filepath.Join(g.levelCacheDir, "preview-angle.json"), angleData, 0o644); err != nil {
		return queued, fmt.Errorf("saving level thumbnail angle: %w", err)
	}
	g.filterKey = "\x00"
	return queued, nil
}

// SetUnitPreviewAngle applies the Preview camera rotation to unit thumbnails
// and queues thumbnails already cached at the previous angle. The GUI stores
// this preference independently from the build-specific thumbnail cache.
func (g *UnitGallery) SetUnitPreviewAngle(rotation [2]float32, ids []stingray.FileID) (int, error) {
	oldAngle := g.unitPreviewAngle.Rotation
	oldKey := levelAngleKey(oldAngle)
	newKey := levelAngleKey(rotation)
	if oldKey == newKey {
		return 0, nil
	}
	if g.capturing != (stingray.FileID{}) {
		g.finishCapture()
	}
	for _, thumbnail := range g.thumbnails {
		gl.DeleteTextures(1, &thumbnail.textureID)
	}
	clear(g.thumbnails)
	clear(g.failed)
	clear(g.diskChecked)
	clear(g.cameraProfiles)
	g.queue = nil
	clear(g.queued)
	clear(g.forceCapture)
	g.unitPreviewAngle.Rotation = rotation

	queued := 0
	for _, id := range ids {
		if id.Type != g.itemType {
			continue
		}
		oldPath := g.unitThumbnailPathForAngle(id, oldKey)
		newPath := g.unitThumbnailPathForAngle(id, newKey)
		if _, err := os.Stat(oldPath); err != nil {
			continue
		}
		if _, err := os.Stat(newPath); err == nil {
			continue
		}
		g.queued[id] = struct{}{}
		g.forceCapture[id] = struct{}{}
		g.queue = append(g.queue, id)
		queued++
	}
	g.filterKey = "\x00"
	g.filterLen = -1
	if g.itemType == ArmorSetTypeHash {
		data, err := json.Marshal(rotation)
		if err != nil {
			return queued, err
		}
		if err := os.WriteFile(filepath.Join(g.unitCacheDir, "unit-preview-angle.json"), data, 0o644); err != nil {
			return queued, fmt.Errorf("saving armor preview angle: %w", err)
		}
	}

	return queued, nil
}

func levelAngleKey(rotation [2]float32) string {
	return fmt.Sprintf("%08x_%08x", math.Float32bits(rotation[0]), math.Float32bits(rotation[1]))
}

func (g *UnitGallery) levelThumbnailPath(id stingray.FileID, angleKey string) string {
	return g.levelThumbnailPathForGround(id, angleKey, g.hideLevelGround)
}

func (g *UnitGallery) levelThumbnailPathForGround(id stingray.FileID, angleKey string, hideGround bool) string {
	variant := ""
	if hideGround {
		variant = "_noground"
	}
	gradingKey := g.assetGradingKey
	if gradingKey == "" {
		gradingKey = "none"
	}
	return filepath.Join(g.levelCacheDir, fmt.Sprintf("%016x_g%s_%s%s.jpg", id.Name.Value, gradingKey, angleKey, variant))
}

func (g *UnitGallery) unitThumbnailPath(id stingray.FileID) string {
	return g.unitThumbnailPathForAngle(id, levelAngleKey(g.unitPreviewAngle.Rotation))
}

func (g *UnitGallery) unitThumbnailPathForAngle(id stingray.FileID, angleKey string) string {
	dir := g.unitCacheDir
	if _, removed := g.removedUnits[id]; removed {
		if g.removedUnitCacheDir != "" {
			dir = g.removedUnitCacheDir
		} else {
			dir = filepath.Join(dir, "removed")
		}
	}
	if g.itemType == ArmorSetTypeHash {
		dir = filepath.Join(dir, "armor-capes-v5")
	}
	filename := fmt.Sprintf("%016x.jpg", id.Name.Value)
	// Preserve existing cache filenames for the default angle.
	if angleKey != levelAngleKey(DefaultUnitCamera().Rotation) {
		filename = fmt.Sprintf("%016x_%s.jpg", id.Name.Value, angleKey)
	}
	return filepath.Join(dir, filename)
}

func (g *UnitGallery) loadUnitThumbnail(id stingray.FileID) bool {
	if g.unitCacheDir == "" || (id.Type != stingray.Sum("unit") && id.Type != ArmorSetTypeHash) || g.diskChecked[id] {
		return false
	}
	g.diskChecked[id] = true
	file, err := os.Open(g.unitThumbnailPath(id))
	if err != nil {
		return false
	}
	defer file.Close()
	decoded, err := jpeg.Decode(file)
	if err != nil {
		return false
	}
	bounds := decoded.Bounds()
	if bounds.Dx() != levelThumbnailDim || bounds.Dy() != levelThumbnailDim {
		return false
	}
	pixels := make([]byte, levelThumbnailDim*levelThumbnailDim*4)
	for y := 0; y < levelThumbnailDim; y++ {
		for x := 0; x < levelThumbnailDim; x++ {
			r, gg, b, a := decoded.At(bounds.Min.X+x, bounds.Min.Y+levelThumbnailDim-1-y).RGBA()
			offset := (y*levelThumbnailDim + x) * 4
			pixels[offset+0] = byte(r >> 8)
			pixels[offset+1] = byte(gg >> 8)
			pixels[offset+2] = byte(b >> 8)
			pixels[offset+3] = byte(a >> 8)
		}
	}
	g.thumbnails[id] = newUnitGalleryThumbnail(pixels, levelThumbnailDim, g.frame)
	if data, err := os.ReadFile(g.unitThumbnailPath(id) + ".extent"); err == nil {
		var extent float64
		if _, err := fmt.Sscanf(string(data), "%f", &extent); err == nil && extent > 0 {
			g.modelExtents[id] = extent
			g.filterLen = -1
		}
	}
	return true
}

func (g *UnitGallery) saveUnitThumbnail(id stingray.FileID, pixels []byte) error {
	if g.unitCacheDir == "" || (id.Type != stingray.Sum("unit") && id.Type != ArmorSetTypeHash) {
		return nil
	}
	if len(pixels) < unitGalleryCaptureSize*unitGalleryCaptureSize*4 {
		return fmt.Errorf("unit capture pixel buffer is incomplete")
	}
	path := g.unitThumbnailPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	preview := image.NewRGBA(image.Rect(0, 0, levelThumbnailDim, levelThumbnailDim))
	for y := 0; y < levelThumbnailDim; y++ {
		// Captures are bottom-up; JPEG images are top-down.
		sy := unitGalleryCaptureSize - 1 - y*unitGalleryCaptureSize/levelThumbnailDim
		for x := 0; x < levelThumbnailDim; x++ {
			sx := x * unitGalleryCaptureSize / levelThumbnailDim
			source := (sy*unitGalleryCaptureSize + sx) * 4
			destination := preview.PixOffset(x, y)
			copy(preview.Pix[destination:destination+4], pixels[source:source+4])
		}
	}
	if err := writeGalleryJPEG(path, preview); err != nil {
		return err
	}
	return nil
}

func writeGalleryJPEG(path string, preview image.Image) error {
	tempPath := path + ".tmp"
	file, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	encodeErr := jpeg.Encode(file, preview, &jpeg.Options{Quality: 90})
	closeErr := file.Close()
	if encodeErr != nil {
		_ = os.Remove(tempPath)
		return encodeErr
	}
	if closeErr != nil {
		_ = os.Remove(tempPath)
		return closeErr
	}
	_ = os.Remove(path)
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

func (g *UnitGallery) loadLevelThumbnail(id stingray.FileID) bool {
	if g.levelCacheDir == "" || id.Type != g.itemType || g.diskChecked[id] {
		return false
	}
	g.diskChecked[id] = true
	file, err := os.Open(g.levelThumbnailPath(id, levelAngleKey(g.levelPreviewAngle.Rotation)))
	if err != nil {
		return false
	}
	defer file.Close()
	decoded, err := jpeg.Decode(file)
	if err != nil {
		return false
	}
	bounds := decoded.Bounds()
	if bounds.Dx() != levelThumbnailDim || bounds.Dy() != levelThumbnailDim {
		return false
	}
	// GL texture row zero is the bottom row. Flip the JPEG's top-down rows so
	// the gallery's established vertically flipped UVs display it upright.
	pixels := make([]byte, levelThumbnailDim*levelThumbnailDim*4)
	for y := 0; y < levelThumbnailDim; y++ {
		for x := 0; x < levelThumbnailDim; x++ {
			r, gg, b, a := decoded.At(bounds.Min.X+x, bounds.Min.Y+levelThumbnailDim-1-y).RGBA()
			offset := (y*levelThumbnailDim + x) * 4
			pixels[offset+0] = byte(r >> 8)
			pixels[offset+1] = byte(gg >> 8)
			pixels[offset+2] = byte(b >> 8)
			pixels[offset+3] = byte(a >> 8)
		}
	}
	g.thumbnails[id] = newUnitGalleryThumbnail(pixels, levelThumbnailDim, g.frame)
	if data, err := os.ReadFile(g.levelThumbnailPath(id, levelAngleKey(g.levelPreviewAngle.Rotation)) + ".extent"); err == nil {
		var extent float64
		if _, err := fmt.Sscanf(string(data), "%f", &extent); err == nil && extent > 0 {
			g.modelExtents[id] = extent
			g.filterLen = -1
		}
	}
	return true
}

func newUnitGalleryThumbnail(pixels []byte, size int, frame uint64) *unitGalleryThumbnail {
	textureID := uint32(0)
	gl.GenTextures(1, &textureID)
	gl.BindTexture(gl.TEXTURE_2D, textureID)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
	gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA, int32(size), int32(size), 0, gl.RGBA, gl.UNSIGNED_BYTE, gl.Ptr(pixels))
	gl.BindTexture(gl.TEXTURE_2D, 0)
	return &unitGalleryThumbnail{textureID: textureID, texture: *imgui.NewTextureRefTextureID(imgui.TextureID(textureID)), lastUsed: frame}
}

func (g *UnitGallery) saveLevelThumbnail(id stingray.FileID, pixels []byte) error {
	if g.levelCacheDir == "" || id.Type != g.itemType {
		return nil
	}
	if len(pixels) < unitGalleryCaptureSize*unitGalleryCaptureSize*4 {
		return fmt.Errorf("level capture pixel buffer is incomplete")
	}
	preview := image.NewRGBA(image.Rect(0, 0, levelThumbnailDim, levelThumbnailDim))
	for y := 0; y < levelThumbnailDim; y++ {
		// ReadPixels returns bottom-up rows; JPEG expects top-down rows.
		sy := unitGalleryCaptureSize - 1 - y*unitGalleryCaptureSize/levelThumbnailDim
		for x := 0; x < levelThumbnailDim; x++ {
			sx := x * unitGalleryCaptureSize / levelThumbnailDim
			source := (sy*unitGalleryCaptureSize + sx) * 4
			destination := preview.PixOffset(x, y)
			copy(preview.Pix[destination:destination+4], pixels[source:source+4])
		}
	}
	path := g.levelThumbnailPath(id, levelAngleKey(g.levelPreviewAngle.Rotation))
	return writeGalleryJPEG(path, preview)
}

func (g *UnitGallery) Delete() {
	for _, thumbnail := range g.thumbnails {
		gl.DeleteTextures(1, &thumbnail.textureID)
	}
	g.thumbnails = nil
	if g.renderer != nil {
		g.renderer.Delete()
	}
	if g.removedRenderer != nil {
		g.removedRenderer.Delete()
	}
}

func (g *UnitGallery) SetRemovedSource(source *UnitGallery) {
	if source == nil || source.renderer == nil {
		return
	}
	if _, capturingRemoved := g.removedUnits[g.capturing]; capturingRemoved && g.capturing != (stingray.FileID{}) {
		g.finishCapture()
	}
	if g.removedRenderer != nil {
		g.removedRenderer.Delete()
	}
	g.removedRenderer = source.renderer
	g.removedGetResource = source.getResource
	g.removedUnitCacheDir = source.unitCacheDir
	source.renderer = nil
	// Cards may have been marked unavailable while only the old kit table was
	// loaded. Clear those failures and disk-cache probes now that old assets are
	// available, so visible removed cards can load cached JPEGs or capture anew.
	for id := range g.removedUnits {
		g.invalidateThumbnail(id)
	}
	g.filterKey = "\x00"
}

func (g *UnitGallery) ClearRemovedSource() {
	if _, isRemovedCapture := g.removedUnits[g.capturing]; isRemovedCapture && g.capturing != (stingray.FileID{}) {
		g.finishCapture()
	}
	queue := g.queue[:0]
	for _, id := range g.queue {
		if _, removed := g.removedUnits[id]; !removed {
			queue = append(queue, id)
		} else {
			delete(g.queued, id)
		}
	}
	g.queue = queue
	if g.removedRenderer != nil {
		g.removedRenderer.Delete()
		g.removedRenderer = nil
	}
	g.removedGetResource = nil
	g.removedUnitCacheDir = ""
	for id := range g.removedUnits {
		if thumbnail := g.thumbnails[id]; thumbnail != nil {
			gl.DeleteTextures(1, &thumbnail.textureID)
		}
		delete(g.thumbnails, id)
		delete(g.failed, id)
	}
	g.filterKey = "\x00"
}

func (g *UnitGallery) HasRemovedSource() bool {
	return g.removedRenderer != nil && g.removedGetResource != nil
}

func (g *UnitGallery) activeCaptureRenderer() *UnitPreviewState {
	if g.captureRendererOverride != nil {
		return g.captureRendererOverride
	}
	return g.renderer
}

func (g *UnitGallery) resourceFor(id stingray.FileID) GetResourceFunc {
	if _, removed := g.removedUnits[id]; removed && g.removedGetResource != nil {
		return g.removedGetResource
	}
	return g.getResource
}

func (g *UnitGallery) InvalidateThumbnails() {
	for _, thumbnail := range g.thumbnails {
		gl.DeleteTextures(1, &thumbnail.textureID)
	}
	clear(g.thumbnails)
	clear(g.failed)
	g.queue = nil
	clear(g.queued)
	if g.capturing != (stingray.FileID{}) {
		g.finishCapture()
	}
}

func (g *UnitGallery) Suspend() {
	if g.capturing != (stingray.FileID{}) {
		id := g.capturing
		_, forced := g.forceCapture[id]
		g.finishCapture()
		if forced {
			g.forceCapture[id] = struct{}{}
			g.queued[id] = struct{}{}
			g.queue = append([]stingray.FileID{id}, g.queue...)
		}
	}
	clear(g.wanted)
}

func (g *UnitGallery) RememberCamera(fileID stingray.FileID, camera UnitCamera) {
	g.cameraProfiles[fileID] = camera
}

func (g *UnitGallery) SetVersionChanges(added, changed, removed map[stingray.FileID]struct{}) {
	setsEqual := func(a, b map[stingray.FileID]struct{}) bool {
		if (a == nil) != (b == nil) || len(a) != len(b) {
			return false
		}
		for id := range a {
			if _, ok := b[id]; !ok {
				return false
			}
		}
		return true
	}
	if setsEqual(g.addedUnits, added) && setsEqual(g.changedUnits, changed) && setsEqual(g.removedUnits, removed) {
		return
	}
	previousRemoved := g.removedUnits
	g.addedUnits = added
	g.changedUnits = changed
	g.removedUnits = removed
	for id := range previousRemoved {
		if _, stillRemoved := removed[id]; stillRemoved {
			continue
		}
		g.invalidateThumbnail(id)
	}
	for id := range removed {
		if _, wasRemoved := previousRemoved[id]; !wasRemoved {
			g.invalidateThumbnail(id)
		}
	}
	g.filterKey = "\x00"
	g.filterLen = -1
	if len(added) == 0 && len(changed) == 0 && len(removed) == 0 && g.sortBy >= UnitGallerySortNew {
		g.sortBy = UnitGallerySortName
	}
}

func (g *UnitGallery) invalidateThumbnail(id stingray.FileID) {
	if thumbnail := g.thumbnails[id]; thumbnail != nil {
		gl.DeleteTextures(1, &thumbnail.textureID)
		delete(g.thumbnails, id)
	}
	delete(g.diskChecked, id)
	delete(g.failed, id)
}

func (g *UnitGallery) CameraFor(fileID stingray.FileID) (UnitCamera, bool) {
	camera, ok := g.cameraProfiles[fileID]
	return camera, ok
}

// InvalidateFilteredResults rebuilds the name filter and sort after an
// asynchronous label source (such as inferred level names) finishes loading.
func (g *UnitGallery) InvalidateFilteredResults() {
	g.filterKey = "\x00"
	g.filterLen = -1
}

func (g *UnitGallery) DrawGrid(
	ids []stingray.FileID,
	activeID stingray.FileID,
	nameFor func(stingray.FileID) string,
	evidenceFor func(stingray.FileID) string,
	statsFor func(stingray.FileID) UnitGalleryStats,
) (stingray.FileID, bool) {
	g.frame++
	clear(g.wanted)
	statsWithMeasuredExtent := func(id stingray.FileID) UnitGalleryStats {
		stats := statsFor(id)
		if extent, ok := g.modelExtents[id]; ok {
			stats.ModelExtent = extent
		}
		return stats
	}

	width := imgui.ContentRegionAvail().X
	searchWidth := max(imutils.S(100), width-imutils.S(520))
	imgui.SetNextItemWidth(searchWidth)
	imgui.InputTextWithHint("##UnitGallerySearch", fnt.I.Search+" Filter "+g.collectionName+"...", &g.query, 0, nil)
	g.searchActive = imgui.IsItemActive()
	imgui.SetItemTooltip("Filter the " + g.collectionName + " grid by name or hash")
	imgui.SameLine()
	imgui.SetNextItemWidth(imutils.S(140))
	imgui.SliderFloat("Grid scale", &g.gridScale, 0.5, 1.5)
	imgui.SameLine()
	imgui.SetNextItemWidth(imutils.S(180))
	sortControlX := imgui.CursorPosX()
	choices := []UnitGallerySort{UnitGallerySortName, UnitGallerySortModelScale, UnitGallerySortFileSize}
	if g.itemType == stingray.Sum("unit") {
		choices = []UnitGallerySort{UnitGallerySortName, UnitGallerySortTriangles, UnitGallerySortModelScale, UnitGallerySortFileSize}
	}
	if g.itemType == ArmorSetTypeHash || ((g.itemType == stingray.Sum("unit") || g.itemType == stingray.Sum("level")) && g.addedUnits != nil) {
		choices = append(choices, UnitGallerySortNew, UnitGallerySortChanged, UnitGallerySortRemoved)
	}
	if imutils.ComboChoiceAny(
		"Sort by",
		&g.sortBy,
		choices,
		func(a, b UnitGallerySort) bool { return a == b },
		unitGallerySortName,
	) {
		g.descending = g.sortBy != UnitGallerySortName
	}
	imgui.SameLine()
	direction := "Ascending"
	if g.descending {
		direction = "Descending"
	}
	if imgui.Button(fnt.I.Sort + " " + direction) {
		g.descending = !g.descending
	}
	imgui.SetItemTooltip("Reverse the current sort order")
	if g.itemType == stingray.Sum("unit") {
		imgui.SetCursorPosX(sortControlX)
		if imgui.Checkbox("Has animations", &g.hasAnimationsOnly) {
			g.InvalidateFilteredResults()
		}
		imgui.SetItemTooltip("Show only units with at least one linked or unit-local animation clip")
	}
	// Keep this status row in a fixed position and always reserve its height.
	// Showing it conditionally after the full-height grid toggled the parent
	// window scrollbar as each capture started/finished, shifting the grid.
	if g.hasAnimationsOnly && !g.animationScanDone {
		if g.animationScanTotal > 0 {
			imgui.TextDisabled(fmt.Sprintf("Scanning animation links: %d / %d units", g.animationScanProgress, g.animationScanTotal))
		} else {
			imgui.TextDisabled("Preparing animation scan...")
		}
	} else if g.capturing != (stingray.FileID{}) {
		status := "Rendering " + g.collectionName + " preview: " + nameFor(g.capturing)
		if renderer := g.activeCaptureRenderer(); renderer != nil && !renderer.MaterialsReady() {
			status = "Loading textures for grid view: " + nameFor(g.capturing)
		}
		imgui.TextUnformatted(status)
	} else if len(g.forceCapture) > 0 {
		imgui.TextDisabled(fmt.Sprintf("Recapturing %d cached level thumbnails", len(g.forceCapture)))
	} else {
		imgui.TextDisabled("Thumbnail capture ready")
	}

	g.updateFiltered(ids, nameFor, statsWithMeasuredExtent)
	if len(g.filtered) == 0 {
		if g.hasAnimationsOnly && !g.animationScanDone {
			imgui.TextDisabled("Units will appear here when the scan finishes.")
			return stingray.FileID{}, false
		}
		if g.itemType == ArmorSetTypeHash && g.sortBy >= UnitGallerySortNew {
			switch {
			case g.addedUnits == nil && g.changedUnits == nil && g.removedUnits == nil:
				imgui.TextUnformatted("Run Compare → Scan older build to populate the armor change filters.")
			case len(g.addedUnits)+len(g.changedUnits)+len(g.removedUnits) == 0:
				imgui.TextUnformatted("No armor kit changes were found between the selected builds.")
			default:
				imgui.TextUnformatted("No armor sets match this compare filter.")
			}
			return stingray.FileID{}, false
		}
		imgui.TextUnformatted("No " + g.collectionName + " match this filter.")
		return stingray.FileID{}, false
	}

	imgui.TextUnformatted(fmt.Sprintf("%d %s", len(g.filtered), g.collectionName))
	imgui.Separator()

	gridContent := imgui.ContentRegionAvail()
	// A vertical table scrollbar consumes horizontal space. Reserve it up front
	// so the fixed-fit columns never reflow when the scrollbar updates.
	gridWidth := max(0, gridContent.X-imgui.CurrentStyle().ScrollbarSize())
	cellWidth := imutils.S(unitGalleryCellWidth * g.gridScale)
	spacing := imgui.CurrentStyle().ItemSpacing().X
	columns := max(1, int((gridWidth+spacing)/(cellWidth+spacing)))
	availableWidth := gridWidth - spacing*float32(columns-1)
	cellWidth = availableWidth / float32(columns)
	thumbnailDim := min(imutils.S(unitGalleryThumbnailDim), cellWidth-imutils.S(16))
	cardHeight := thumbnailDim + imutils.S(58)

	var selected stingray.FileID
	var didSelect bool
	rowCount := (len(g.filtered) + columns - 1) / columns
	if imgui.BeginTableV("##UnitGalleryGrid", int32(columns), imgui.TableFlagsSizingFixedFit|imgui.TableFlagsScrollY, imgui.NewVec2(gridWidth, gridContent.Y), 0) {
		for range columns {
			imgui.TableSetupColumnV("", imgui.TableColumnFlagsWidthFixed, cellWidth, 0)
		}
		clipper := imgui.NewListClipper()
		clipper.Begin(int32(rowCount))
		for clipper.Step() {
			for row := clipper.DisplayStart(); row < clipper.DisplayEnd(); row++ {
				imgui.TableNextRow()
				for column := range columns {
					idx := int(row)*columns + column
					if idx >= len(g.filtered) {
						break
					}
					imgui.TableNextColumn()
					id := g.filtered[idx]
					var evidence string
					if evidenceFor != nil {
						evidence = evidenceFor(id)
					}
					if g.drawCard(id, activeID, nameFor(id), evidence, statsWithMeasuredExtent(id), cellWidth, thumbnailDim, cardHeight) {
						selected = id
						didSelect = true
					}
				}
			}
		}
		imgui.EndTable()
	}

	return selected, didSelect
}

func (g *UnitGallery) DrawCaptureFrame() {
	if g.searchActive {
		return
	}
	if g.capturing == (stingray.FileID{}) {
		g.startNextCapture()
	}
	if g.capturing == (stingray.FileID{}) {
		return
	}
	_, forcedCapture := g.forceCapture[g.capturing]
	if _, visible := g.wanted[g.capturing]; !visible && !forcedCapture {
		g.finishCapture()
		return
	}

	renderer := g.activeCaptureRenderer()
	allowPending := g.itemType == ArmorSetTypeHash && time.Since(g.captureStarted) > 15*time.Second
	pixels, settled, err := renderer.drawCaptureFrame(allowPending)
	if err != nil {
		g.failed[g.capturing] = err.Error()
		g.finishCapture()
		return
	}
	if !settled {
		return
	}
	score := countNonBackgroundPixels(pixels)
	frameFilled := isFrameFillingCapture(pixels)
	if !frameFilled && (score > g.captureBestScore || (g.captureFallbackStage == 2 && score > 0 && g.captureBestStage != 2)) {
		g.captureBestScore = score
		g.captureBestStage = g.captureFallbackStage
		g.captureBestPixels = append(g.captureBestPixels[:0], pixels...)
	}
	// Dark armor is often intentional; forcing the flat fallback washes out
	// its materials even when the normal capture is visible.
	blackModel := g.itemType != ArmorSetTypeHash && score >= 16 && isMostlyBlackCapture(pixels)
	if frameFilled && g.captureAttempt < 3 {
		g.captureAttempt++
		camera := renderer.Camera()
		camera.Distance *= 1.45
		renderer.SetCamera(camera)
		renderer.autoZoomEnabled = false
		renderer.doAutoZoomNextFrame = false
		return
	}
	if score < 16 || frameFilled || (g.captureFallbackStage == 0 && blackModel) {
		if g.captureFallbackStage == 0 && blackModel {
			g.beginFlatCapture()
			return
		}
		if g.captureAttempt < 3 {
			g.captureAttempt++
			camera := renderer.Camera()
			camera.Rotation[0] += float32(math.Pi / 2)
			renderer.SetCamera(camera)
			renderer.autoZoomEnabled = true
			renderer.doAutoZoomNextFrame = true
			return
		}
		switch g.captureFallbackStage {
		case 0:
			g.captureFallbackStage = 1
			g.captureAttempt = 0
			renderer.showWireframe = true
			camera := g.captureBaseCamera
			camera.Rotation[0] += float32(math.Pi / 4)
			renderer.SetCamera(camera)
			renderer.autoZoomEnabled = true
			renderer.doAutoZoomNextFrame = true
			return
		case 1:
			g.beginFlatCapture()
			return
		case 2:
			if g.captureBestScore == 0 {
				g.failed[g.capturing] = "No visible mesh after material, wireframe, and flat-shaded capture retries"
				g.finishCapture()
				return
			}
			pixels = g.captureBestPixels
		}
	}

	if allowPending && !renderer.MaterialsReady() {
		// A stuck texture must not block every other card. Keep this image only
		// for the current session so it can be retried on a later launch.
		g.thumbnails[g.capturing] = newUnitGalleryThumbnail(pixels, unitGalleryCaptureSize, g.frame)
	} else if g.itemType == stingray.Sum("level") {
		if err := g.saveLevelThumbnail(g.capturing, pixels); err != nil {
			g.failed[g.capturing] = "Could not save thumbnail: " + err.Error()
		}
		delete(g.diskChecked, g.capturing)
		if !g.loadLevelThumbnail(g.capturing) {
			g.thumbnails[g.capturing] = newUnitGalleryThumbnail(pixels, unitGalleryCaptureSize, g.frame)
		}
	} else {
		if err := g.saveUnitThumbnail(g.capturing, pixels); err != nil {
			g.failed[g.capturing] = "Could not save thumbnail: " + err.Error()
			g.thumbnails[g.capturing] = newUnitGalleryThumbnail(pixels, unitGalleryCaptureSize, g.frame)
		} else {
			delete(g.diskChecked, g.capturing)
			if !g.loadUnitThumbnail(g.capturing) {
				g.thumbnails[g.capturing] = newUnitGalleryThumbnail(pixels, unitGalleryCaptureSize, g.frame)
			}
		}
	}
	if extent := renderer.VisibleModelExtent(g.capturing); extent > 0 {
		g.modelExtents[g.capturing] = extent
		g.filterLen = -1
		if g.itemType == stingray.Sum("level") {
			path := g.levelThumbnailPath(g.capturing, levelAngleKey(g.levelPreviewAngle.Rotation)) + ".extent"
			_ = os.WriteFile(path, []byte(fmt.Sprintf("%.9g", extent)), 0o644)
		} else {
			path := g.unitThumbnailPath(g.capturing) + ".extent"
			_ = os.WriteFile(path, []byte(fmt.Sprintf("%.9g", extent)), 0o644)
		}
	}
	if g.captureFallbackStage == 0 {
		g.cameraProfiles[g.capturing] = renderer.Camera()
	}
	g.trimThumbnailCache(g.capturing)
	g.finishCapture()
}

func (g *UnitGallery) startNextCapture() {
	for len(g.queue) > 0 {
		id := g.queue[0]
		g.queue = g.queue[1:]
		delete(g.queued, id)
		_, forcedCapture := g.forceCapture[id]
		if _, visible := g.wanted[id]; !visible && !forcedCapture {
			continue
		}
		if _, ok := g.thumbnails[id]; ok && !forcedCapture {
			continue
		}
		if _, failed := g.failed[id]; failed && !forcedCapture {
			continue
		}

		removed := false
		_, removed = g.removedUnits[id]
		g.captureRendererOverride = g.renderer
		if removed && g.removedRenderer != nil {
			g.captureRendererOverride = g.removedRenderer
		}
		renderer := g.activeCaptureRenderer()
		getResource := g.resourceFor(id)
		var mainData []byte
		var exists bool
		var err error
		if g.itemType == ArmorSetTypeHash {
			_, exists = g.armorKits[id]
		} else {
			mainData, exists, err = getResource(id, stingray.DataMain)
		}
		if err != nil {
			g.failed[id] = err.Error()
			delete(g.forceCapture, id)
			g.captureRendererOverride = nil
			continue
		}
		if !exists {
			g.failed[id] = g.collectionName + " main data is missing"
			delete(g.forceCapture, id)
			g.captureRendererOverride = nil
			continue
		}
		var gpuData []byte
		if g.itemType != stingray.Sum("level") && g.itemType != ArmorSetTypeHash {
			gpuData, exists, err = getResource(id, stingray.DataGPU)
			if err != nil {
				g.failed[id] = err.Error()
				delete(g.forceCapture, id)
				g.captureRendererOverride = nil
				continue
			}
			if !exists {
				gpuData = nil
			}
		}

		camera, hasCamera := g.cameraProfiles[id]
		if !hasCamera {
			camera = DefaultUnitCamera()
		}
		if g.itemType == stingray.Sum("level") {
			camera.Rotation = g.levelPreviewAngle.Rotation
		} else {
			camera.Rotation = g.unitPreviewAngle.Rotation
		}
		renderer.ReleaseLoadedUnit()
		renderer.autoZoomEnabled = true
		g.captureAttempt = 0
		g.captureFallbackStage = 0
		g.captureOriginalWireframe = renderer.showWireframe
		g.captureOriginalAABB = renderer.showAABB
		g.captureOriginalFlat = renderer.thumbnailFlat
		renderer.showWireframe = false
		renderer.showAABB = false
		renderer.thumbnailFlat = false
		var loadErr error
		if g.itemType == ArmorSetTypeHash {
			renderer.armorThumbnailOnly = true
			loaded := renderer.LoadArmorSetByKit(g.armorKits[id])
			renderer.armorThumbnailOnly = false
			if !loaded {
				loadErr = fmt.Errorf("this set has no loadable armor meshes in the selected build")
			} else if len(renderer.nodes[stingray.Sum("body_male")].children) > 0 {
				node := renderer.nodes[stingray.Sum("body_female")]
				node.shown = false
				renderer.nodes[stingray.Sum("body_female")] = node
			}
		} else if g.itemType == stingray.Sum("level") {
			loadErr = renderer.LoadLevel(id.Name, mainData)
		} else {
			loadErr = renderer.LoadUnit(id.Name, mainData, gpuData)
		}
		if loadErr != nil {
			g.failed[id] = loadErr.Error()
			delete(g.forceCapture, id)
			g.restoreCaptureMode()
			renderer.ReleaseLoadedUnit()
			g.captureRendererOverride = nil
			continue
		}
		if g.itemType == ArmorSetTypeHash {
			if renderer.centerArmorOnVisibleBounds() {
				camera.ModelPos = renderer.modelPos
			}
		} else if g.itemType == stingray.Sum("level") {
			if renderer.centerUnitOnVisibleBounds(renderer.rootHash) {
				camera.ModelPos = renderer.modelPos
			}
		} else if !hasCamera {
			// LoadUnit centers the unit on its first visible mesh. Preserve that
			// target when using the default camera for a first-time thumbnail.
			camera.ModelPos = renderer.modelPos
		}
		g.captureBaseCamera = camera
		g.captureBestPixels = nil
		g.captureBestScore = 0
		g.captureBestStage = 0
		renderer.SetCamera(camera)
		renderer.maxViewDistance = renderer.getMaxZoom(
			renderer.root,
			renderer.rootHash,
			len(renderer.aabb[renderer.rootHash]) == 0,
		)
		renderer.autoZoomEnabled = true
		renderer.doAutoZoomNextFrame = true
		g.capturing = id
		g.captureStarted = time.Now()
		return
	}
}

func (g *UnitGallery) restoreCaptureMode() {
	if renderer := g.activeCaptureRenderer(); renderer != nil {
		renderer.showWireframe = g.captureOriginalWireframe
		renderer.showAABB = g.captureOriginalAABB
		renderer.thumbnailFlat = g.captureOriginalFlat
	}
	g.captureAttempt = 0
	g.captureFallbackStage = 0
	g.captureBaseCamera = UnitCamera{}
	g.captureBestPixels = nil
	g.captureBestScore = 0
	g.captureBestStage = 0
}

func (g *UnitGallery) finishCapture() {
	g.restoreCaptureMode()
	if renderer := g.activeCaptureRenderer(); renderer != nil {
		renderer.ReleaseLoadedUnit()
	}
	delete(g.forceCapture, g.capturing)
	g.capturing = stingray.FileID{}
	g.captureRendererOverride = nil
}

func (g *UnitGallery) beginFlatCapture() {
	g.captureFallbackStage = 2
	g.captureAttempt = 0
	renderer := g.activeCaptureRenderer()
	renderer.showWireframe = false
	renderer.thumbnailFlat = true
	if (g.itemType == ArmorSetTypeHash && renderer.centerArmorOnVisibleBounds()) || (g.itemType != ArmorSetTypeHash && renderer.centerUnitOnVisibleBounds(renderer.rootHash)) {
		g.captureBaseCamera.ModelPos = renderer.modelPos
	}
	renderer.maxViewDistance = renderer.getMaxZoom(
		renderer.root,
		renderer.rootHash,
		len(renderer.aabb[renderer.rootHash]) == 0,
	)
	renderer.SetCamera(g.captureBaseCamera)
	renderer.autoZoomEnabled = true
	renderer.doAutoZoomNextFrame = true
}

func countNonBackgroundPixels(pixels []byte) int {
	const (
		channels       = 4
		sampleStride   = 4
		background     = 51
		colorTolerance = 8
	)
	count := 0
	for i := 0; i+2 < len(pixels); i += channels * sampleStride {
		redDelta := int(pixels[i]) - background
		greenDelta := int(pixels[i+1]) - background
		blueDelta := int(pixels[i+2]) - background
		if redDelta < 0 {
			redDelta = -redDelta
		}
		if greenDelta < 0 {
			greenDelta = -greenDelta
		}
		if blueDelta < 0 {
			blueDelta = -blueDelta
		}
		if redDelta > colorTolerance || greenDelta > colorTolerance || blueDelta > colorTolerance {
			count++
		}
	}
	return count
}

func isFrameFillingCapture(pixels []byte) bool {
	const (
		sampleStride   = 4
		background     = 51
		colorTolerance = 8
	)
	if len(pixels) < unitGalleryCaptureSize*unitGalleryCaptureSize*4 {
		return false
	}
	minX, minY := unitGalleryCaptureSize, unitGalleryCaptureSize
	maxX, maxY := -1, -1
	foreground := 0
	samples := 0
	for y := 0; y < unitGalleryCaptureSize; y += sampleStride {
		for x := 0; x < unitGalleryCaptureSize; x += sampleStride {
			samples++
			i := (y*unitGalleryCaptureSize + x) * 4
			redDelta := int(pixels[i]) - background
			greenDelta := int(pixels[i+1]) - background
			blueDelta := int(pixels[i+2]) - background
			if redDelta < 0 {
				redDelta = -redDelta
			}
			if greenDelta < 0 {
				greenDelta = -greenDelta
			}
			if blueDelta < 0 {
				blueDelta = -blueDelta
			}
			if redDelta <= colorTolerance && greenDelta <= colorTolerance && blueDelta <= colorTolerance {
				continue
			}
			foreground++
			minX = min(minX, x)
			minY = min(minY, y)
			maxX = max(maxX, x)
			maxY = max(maxY, y)
		}
	}
	if foreground == 0 {
		return false
	}
	boundsFillFrame := maxX-minX >= unitGalleryCaptureSize*94/100 && maxY-minY >= unitGalleryCaptureSize*94/100
	imageMostlyForeground := foreground*100 >= samples*78
	return boundsFillFrame && imageMostlyForeground
}

func isMostlyBlackCapture(pixels []byte) bool {
	const (
		background     = 51
		colorTolerance = 8
		blackThreshold = 18
	)
	foreground, black := 0, 0
	for i := 0; i+2 < len(pixels); i += 16 {
		r, g, b := int(pixels[i]), int(pixels[i+1]), int(pixels[i+2])
		rd, gd, bd := r-background, g-background, b-background
		if rd < 0 {
			rd = -rd
		}
		if gd < 0 {
			gd = -gd
		}
		if bd < 0 {
			bd = -bd
		}
		if rd <= colorTolerance && gd <= colorTolerance && bd <= colorTolerance {
			continue
		}
		foreground++
		if r <= blackThreshold && g <= blackThreshold && b <= blackThreshold {
			black++
		}
	}
	return foreground >= 16 && black*4 >= foreground*3
}

func (g *UnitGallery) drawCard(
	id, activeID stingray.FileID,
	name string,
	evidence string,
	stats UnitGalleryStats,
	width, imageSize, height float32,
) bool {
	imgui.PushIDStr(id.Name.String() + id.Type.String())
	defer imgui.PopID()
	_, removed := g.removedUnits[id]
	removedRenderable := removed && g.removedRenderer != nil && g.removedGetResource != nil
	if !removed || removedRenderable {
		g.wanted[id] = struct{}{}
	}

	pos := imgui.CursorScreenPos()
	cardSize := imgui.NewVec2(width, height)
	dl := imgui.WindowDrawList()
	background := imgui.ColorU32Col(imgui.ColFrameBg)
	if id == activeID {
		background = imgui.ColorU32Col(imgui.ColHeaderActive)
	}
	dl.AddRectFilled(pos, pos.Add(cardSize), background)
	imagePos := pos.Add(imgui.NewVec2((width-imageSize)/2, imutils.S(6)))
	imageMax := imagePos.Add(imgui.NewVec2(imageSize, imageSize))
	failure := g.failed[id]
	if _, loaded := g.thumbnails[id]; !loaded && failure == "" {
		if g.itemType == stingray.Sum("level") {
			g.loadLevelThumbnail(id)
		} else if g.itemType == stingray.Sum("unit") || g.itemType == ArmorSetTypeHash {
			g.loadUnitThumbnail(id)
		}
	}
	if thumbnail, ok := g.thumbnails[id]; ok {
		thumbnail.lastUsed = g.frame
		dl.AddImageV(
			thumbnail.texture,
			imagePos,
			imageMax,
			imgui.NewVec2(0, 1),
			imgui.NewVec2(1, 0),
			0xFFFFFFFF,
		)
	} else {
		dl.AddRectFilled(imagePos, imageMax, imgui.ColorU32Col(imgui.ColWindowBg))
		message := "Queued"
		if removed && !removedRenderable {
			message = "Removed"
		}
		if _, queued := g.queued[id]; queued || g.capturing == id {
			message = "Rendering…"
		}
		if failure != "" {
			message = "No preview"
		}
		messageSize := imgui.CalcTextSize(message)
		dl.AddTextVec2(imagePos.Add(imgui.NewVec2((imageSize-messageSize.X)/2, (imageSize-messageSize.Y)/2)), imgui.ColorU32Col(imgui.ColText), message)
		if failure == "" && (!removed || removedRenderable) {
			g.queueThumbnail(id)
		}
	}

	label := truncateGalleryLabel(name, width, imgui.FontSize())
	labelPos := imgui.NewVec2(pos.X+imutils.S(6), imageMax.Y+imutils.S(5))
	dl.AddTextVec2(labelPos, imgui.ColorU32Col(imgui.ColText), label)
	if removed {
		dl.AddTextVec2(labelPos.Add(imutils.SVec2(0, 15)), imgui.ColorU32Col(imgui.ColTextDisabled), "Removed from current build")
	} else if g.itemType == ArmorSetTypeHash {
		dl.AddTextVec2(labelPos.Add(imutils.SVec2(0, 15)), imgui.ColorU32Col(imgui.ColTextDisabled), "Armor or cape")
	} else if g.itemType == stingray.Sum("level") {
		dl.AddTextVec2(labelPos.Add(imutils.SVec2(0, 15)), imgui.ColorU32Col(imgui.ColTextDisabled), fmt.Sprintf("%s scene units · %s", formatGalleryExtent(stats.ModelExtent), formatGallerySize(stats.FileSize)))
	} else {
		metric := fmt.Sprintf("%s tris HQ / %s all", compactCount(stats.TriangleCount), compactCount(stats.AllLODTriangles))
		dl.AddTextVec2(labelPos.Add(imutils.SVec2(0, 15)), imgui.ColorU32Col(imgui.ColTextDisabled), metric)
		dl.AddTextVec2(labelPos.Add(imutils.SVec2(0, 30)), imgui.ColorU32Col(imgui.ColTextDisabled), fmt.Sprintf("%s u · %s", formatGalleryExtent(stats.ModelExtent), formatGallerySize(stats.ModelSize)))
	}

	imgui.InvisibleButton("##unitCard", cardSize)
	clicked := imgui.IsItemClickedV(imgui.MouseButtonLeft)
	hovered := imgui.IsItemHovered()
	if g.itemType == stingray.Sum("unit") && imgui.BeginPopupContextItemV("##UnitOptions", imgui.PopupFlagsMouseButtonRight) {
		if imgui.MenuItemBool("See what levels use this model") && g.onUnitLevelLookup != nil {
			g.onUnitLevelLookup(id)
		}
		imgui.EndPopup()
	}
	if hovered {
		tooltip := fmt.Sprintf("%s\nUnit resource hash: %s\n%v triangles at highest-quality available LOD\n%v triangles across all LODs\nLargest visible model dimension: %s model units\n%v model resource bytes\n%v total file bytes", name, id.Name, stats.TriangleCount, stats.AllLODTriangles, formatGalleryExtent(stats.ModelExtent), stats.ModelSize, stats.FileSize)
		if evidence != "" {
			tooltip += "\nLabel source: " + evidence
		}
		if g.itemType == stingray.Sum("level") {
			tooltip = fmt.Sprintf("%s\nLevel resource hash: %s\nThe displayed name may be inferred from scene contents.\nLargest visible scene dimension: %s scene units\n%v level resource bytes", name, id.Name, formatGalleryExtent(stats.ModelExtent), stats.FileSize)
		} else if g.itemType == ArmorSetTypeHash {
			tooltip = fmt.Sprintf("%s\nClick to assemble this armor set in Preview", name)
		}
		if failure != "" {
			tooltip += "\n" + failure
		}
		if imgui.BeginTooltip() {
			// The default wrap position follows the active table column. Since a
			// card is narrow, that can collapse this tooltip to one character per
			// line. Give it a readable, column-independent width instead.
			imgui.PushTextWrapPosV(imgui.FontSize() * 35)
			imgui.TextUnformatted(tooltip)
			imgui.PopTextWrapPos()
			imgui.EndTooltip()
		}
	}
	return clicked && (!removed || removedRenderable)
}

func (g *UnitGallery) queueThumbnail(id stingray.FileID) {
	if _, ok := g.queued[id]; ok {
		return
	}
	if _, ok := g.thumbnails[id]; ok {
		return
	}
	if _, ok := g.failed[id]; ok {
		return
	}
	g.queued[id] = struct{}{}
	g.wanted[id] = struct{}{}
	g.queue = append(g.queue, id)
}

func (g *UnitGallery) trimThumbnailCache(keep stingray.FileID) {
	for len(g.thumbnails) > unitGalleryMaxTextures {
		var oldestID stingray.FileID
		var oldest *unitGalleryThumbnail
		for id, thumbnail := range g.thumbnails {
			if id == keep {
				continue
			}
			if _, visible := g.wanted[id]; visible {
				continue
			}
			if oldest == nil || thumbnail.lastUsed < oldest.lastUsed {
				oldestID, oldest = id, thumbnail
			}
		}
		if oldest == nil {
			return
		}
		gl.DeleteTextures(1, &oldest.textureID)
		delete(g.thumbnails, oldestID)
	}
}

func (g *UnitGallery) updateFiltered(
	ids []stingray.FileID,
	nameFor func(stingray.FileID) string,
	statsFor func(stingray.FileID) UnitGalleryStats,
) {
	if g.filterKey == g.query && g.filterLen == len(ids) && g.filterSort == g.sortBy && g.filterDesc == g.descending &&
		g.filterHasAnimations == g.hasAnimationsOnly && g.filterAnimationsReady == g.animationScanDone {
		return
	}
	if g.hasAnimationsOnly && !g.animationScanDone {
		g.filtered = nil
		g.filterKey = g.query
		g.filterLen = len(ids)
		g.filterSort = g.sortBy
		g.filterDesc = g.descending
		g.filterHasAnimations = g.hasAnimationsOnly
		g.filterAnimationsReady = g.animationScanDone
		return
	}

	query := strings.ToLower(strings.TrimSpace(g.query))
	g.filtered = make([]stingray.FileID, 0, len(ids))
	for _, id := range ids {
		if g.hasAnimationsOnly {
			if _, hasAnimations := g.animatedUnits[id]; !hasAnimations {
				continue
			}
		}
		_, isAdded := g.addedUnits[id]
		_, isChanged := g.changedUnits[id]
		_, isRemoved := g.removedUnits[id]
		switch g.sortBy {
		case UnitGallerySortNew:
			if !isAdded {
				continue
			}
		case UnitGallerySortRemoved:
			if !isRemoved {
				continue
			}
		case UnitGallerySortChanged:
			if !isChanged {
				continue
			}
		default:
			if isRemoved {
				continue
			}
		}
		name := nameFor(id)
		if query != "" && !strings.Contains(strings.ToLower(name), query) && !strings.Contains(strings.ToLower(id.Name.String()), query) {
			continue
		}
		g.filtered = append(g.filtered, id)
	}
	slices.SortFunc(g.filtered, func(a, b stingray.FileID) int {
		var result int
		switch g.sortBy {
		case UnitGallerySortTriangles:
			result = cmp.Compare(statsFor(a).TriangleCount, statsFor(b).TriangleCount)
		case UnitGallerySortModelScale:
			scaleA, scaleB := statsFor(a).ModelExtent, statsFor(b).ModelExtent
			if scaleA <= 0 && scaleB > 0 {
				return 1
			}
			if scaleA > 0 && scaleB <= 0 {
				return -1
			}
			result = cmp.Compare(scaleA, scaleB)
		case UnitGallerySortFileSize:
			result = cmp.Compare(statsFor(a).FileSize, statsFor(b).FileSize)
		default:
			result = strings.Compare(strings.ToLower(nameFor(a)), strings.ToLower(nameFor(b)))
		}
		if result != 0 && g.descending {
			return -result
		}
		if result == 0 {
			return strings.Compare(strings.ToLower(nameFor(a)), strings.ToLower(nameFor(b)))
		}
		return result
	})
	g.filterKey = g.query
	g.filterLen = len(ids)
	g.filterSort = g.sortBy
	g.filterDesc = g.descending
	g.filterHasAnimations = g.hasAnimationsOnly
	g.filterAnimationsReady = g.animationScanDone
}

func unitGallerySortName(sortBy UnitGallerySort) string {
	switch sortBy {
	case UnitGallerySortTriangles:
		return "Triangles"
	case UnitGallerySortModelScale:
		return "Model scale"
	case UnitGallerySortFileSize:
		return "File size"
	case UnitGallerySortNew:
		return "New"
	case UnitGallerySortChanged:
		return "Changed"
	case UnitGallerySortRemoved:
		return "Old (removed)"
	default:
		return "Name"
	}
}

func compactCount(value uint64) string {
	switch {
	case value >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	case value >= 1_000:
		return fmt.Sprintf("%.1fk", float64(value)/1_000)
	default:
		return fmt.Sprint(value)
	}
}

func formatGallerySize(value uint64) string {
	switch {
	case value >= 1024*1024:
		return fmt.Sprintf("%.1f MiB", float64(value)/(1024*1024))
	case value >= 1024:
		return fmt.Sprintf("%.0f KiB", float64(value)/1024)
	default:
		return fmt.Sprintf("%d B", value)
	}
}

func formatGalleryExtent(value float64) string {
	if value <= 0 {
		return "unknown"
	}
	return fmt.Sprintf("%.2f", value)
}

func truncateGalleryLabel(label string, width, fontSize float32) string {
	maxRunes := max(1, int((width-imutils.S(12))/max(fontSize*0.55, float32(1))))
	runes := []rune(label)
	if utf8.RuneCountInString(label) > maxRunes {
		if maxRunes == 1 {
			return "…"
		}
		return string(runes[:maxRunes-1]) + "…"
	}
	return label
}

func (pv *UnitPreviewState) DrawCaptureFrame() ([]byte, bool, error) {
	return pv.drawCaptureFrame(false)
}

func (pv *UnitPreviewState) drawCaptureFrame(allowPending bool) ([]byte, bool, error) {
	if allowPending && !pv.HasLoadedUnit() {
		return nil, false, fmt.Errorf("no loadable meshes in this armor set")
	}
	pv.captureMode = true
	defer func() { pv.captureMode = false }()

	imgui.SetNextWindowPosV(imgui.MainViewport().Pos(), imgui.CondAlways, imgui.NewVec2(0, 0))
	imgui.SetNextWindowSize(imgui.NewVec2(540, 540))
	flags := imgui.WindowFlagsNoDecoration |
		imgui.WindowFlagsNoSavedSettings |
		imgui.WindowFlagsNoDocking |
		imgui.WindowFlagsNoBackground |
		imgui.WindowFlagsNoInputs
	imgui.BeginV("##UnitGalleryRenderer", nil, flags)
	clipPos := imgui.CursorScreenPos()
	drawList := imgui.WindowDrawList()
	drawList.PushClipRect(clipPos, clipPos)
	pv.Draw("##UnitGalleryRenderer")
	drawList.PopClipRect()
	imgui.End()

	settled := pv.animTime == -1 && !pv.doAutoZoomNextFrame && (allowPending || pv.MaterialsReady())
	if !settled {
		return nil, false, nil
	}
	pixels, err := pv.fb.ReadPixelsRGBA()
	return pixels, true, err
}
