package previews

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"path"
	"slices"
	"time"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/ebitengine/oto/v3"
	"github.com/xypwn/filediver/cmd/filediver-gui/imutils"
	datalib "github.com/xypwn/filediver/datalibrary"
	"github.com/xypwn/filediver/dds"
	"github.com/xypwn/filediver/exec"
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/animation"
	"github.com/xypwn/filediver/stingray/entity"
	"github.com/xypwn/filediver/stingray/shading_environment"
	stingray_strings "github.com/xypwn/filediver/stingray/strings"
	"github.com/xypwn/filediver/stingray/ttf"
	"github.com/xypwn/filediver/stingray/unit"
	"github.com/xypwn/filediver/stingray/unit/material"
	"github.com/xypwn/filediver/stingray/unit/texture"
	stingray_wwise "github.com/xypwn/filediver/stingray/wwise"
)

type AutoPreviewType int

const (
	AutoPreviewEmpty AutoPreviewType = iota
	AutoPreviewUnit
	AutoPreviewAnimation
	AutoPreviewTree
	AutoPreviewAudio
	AutoPreviewVideo
	AutoPreviewTexture
	AutoPreviewStrings
	AutoPreviewMaterial
	AutoPreviewXAML
	AutoPreviewFont
)

type AutoPreview struct {
	activeType AutoPreviewType
	activeID   stingray.FileID
	previews   struct {
		unit     *UnitPreviewState
		audio    *WwisePreview
		video    *BinkPreview
		texture  *ImagePreview
		strings  *StringsPreview
		material *MaterialPreview
		xaml     *XamlPreview
		font     *FontPreview
	}
	animationUnit       *UnitPreviewState
	animationUnitSource stingray.FileID
	entityInfo          *entity.Entity
	colorGradingID      stingray.FileID
	levelGallery        *UnitGallery
	animationData       *animation.Animation
	animationResources  []stingray.FileID
	pendingAnimation    *linkedAnimationPreviewRequest

	hashes               map[stingray.Hash]string
	thinhashes           map[stingray.ThinHash]string
	isHelldivers1        bool
	getResourceGenerator GetResourceGeneratorFunc
	getOverride          func(stingray.FileID) stingray.FileID
	resolveHD1Material   ResolveHD1MaterialFunc
	armorParams          ExtractorArmorParameters
	lookupHash           func(stingray.Hash) string
	lookupThinHash       func(stingray.ThinHash) string
	loadMs               uint32
	drawTime             float64
	debug                bool

	err error
}

type linkedAnimationPreviewRequest struct {
	unitID      stingray.FileID
	animationID stingray.Hash
	stateName   string
	preferBlend bool
}

// QueueLinkedAnimationPreview switches the Animation window to the requested
// unit clip after that unit's state-machine links have been loaded.
func (pv *AutoPreview) QueueLinkedAnimationPreview(unitID stingray.FileID, animationID stingray.Hash, stateName string, preferBlend bool) {
	pv.pendingAnimation = &linkedAnimationPreviewRequest{
		unitID: unitID, animationID: animationID, stateName: stateName, preferBlend: preferBlend,
	}
}

func (pv *AutoPreview) SetAnimationResources(ids []stingray.FileID) {
	pv.animationResources = append(pv.animationResources[:0], ids...)
	if pv.previews.unit != nil {
		pv.previews.unit.SetAnimationResources(ids)
	}
	if pv.animationUnit != nil {
		pv.animationUnit.SetAnimationResources(ids)
	}
}

type ExtractorPlanetParameters struct {
	Name                 *string
	UseCity              *bool
	UpdateAssetOverrides func(string, bool)
}

type ExtractorArmorParameters struct {
	ArmorSets        map[stingray.Hash]datalib.ArmorSet
	SelectedArchives func() []stingray.Hash
}

func NewAutoPreview(otoCtx *oto.Context, audioSampleRate int, hashes map[stingray.Hash]string, thinhashes map[stingray.ThinHash]string, isHelldivers1 bool, getResourceGenerator GetResourceGeneratorFunc, getOverride func(stingray.FileID) stingray.FileID, runner *exec.Runner, planetParams ExtractorPlanetParameters, armorParams ExtractorArmorParameters, resolveHD1Material ResolveHD1MaterialFunc) (*AutoPreview, error) {
	var err error
	pv := &AutoPreview{
		hashes:               hashes,
		thinhashes:           thinhashes,
		isHelldivers1:        isHelldivers1,
		getResourceGenerator: getResourceGenerator,
		getOverride:          getOverride,
		resolveHD1Material:   resolveHD1Material,
		armorParams:          armorParams,
	}
	lookupHash := func(h stingray.Hash) string {
		if val, contains := hashes[h]; contains {
			return val
		}
		return h.String()
	}

	lookupThinHash := func(h stingray.ThinHash) string {
		if val, contains := thinhashes[h]; contains {
			return val
		}
		return h.String()
	}
	pv.lookupHash = lookupHash
	pv.lookupThinHash = lookupThinHash
	pv.previews.unit, err = NewUnitPreview(getResourceGenerator(true), pv.getOverride, armorParams, lookupHash, lookupThinHash, isHelldivers1)
	if err != nil {
		return nil, err
	}
	pv.previews.unit.resolveHD1Material = resolveHD1Material
	pv.previews.audio = NewWwisePreview(otoCtx, audioSampleRate)
	pv.previews.video = NewBinkPreview(runner)
	pv.previews.texture = NewImagePreview()
	pv.previews.texture.Flags = LinearFilteringButton | IgnoreAlphaButton
	pv.previews.strings = NewStringsPreview()
	pv.previews.material = NewMaterialPreview()
	pv.previews.xaml = NewXamlPreview()
	pv.previews.font = NewFontPreview()
	return pv, nil
}

func (pv *AutoPreview) Delete() {
	pv.previews.unit.Delete()
	if pv.animationUnit != nil {
		pv.animationUnit.Delete()
	}
	pv.previews.audio.Delete()
	pv.previews.video.Delete()
	pv.previews.texture.Delete()
	pv.previews.xaml.Delete()
	pv.previews.font.Delete()
}

func (pv *AutoPreview) Err() error {
	return pv.err
}

func (pv *AutoPreview) ActiveID() stingray.FileID {
	return pv.activeID
}

func (pv *AutoPreview) ActiveType() AutoPreviewType {
	return pv.activeType
}

func (pv *AutoPreview) NeedCJKFont() bool {
	return pv.previews.strings.NeedCJKFont()
}

func (pv *AutoPreview) NewUnitGallery() (*UnitGallery, error) {
	return pv.newSceneGallery()
}

func (pv *AutoPreview) NewLevelGallery() (*UnitGallery, error) {
	gallery, err := pv.newSceneGallery()
	if err != nil {
		return nil, err
	}
	gallery.itemType = stingray.Sum("level")
	gallery.collectionName = "levels"
	gallery.levelPreviewAngle = DefaultUnitCamera()
	gallery.SetEntityInfo(pv.entityInfo)
	pv.levelGallery = gallery
	return gallery, nil
}

func (pv *AutoPreview) newSceneGallery() (*UnitGallery, error) {
	renderer, err := NewUnitPreview(pv.getResourceGenerator(true), pv.getOverride, pv.armorParams, pv.lookupHash, pv.lookupThinHash, pv.isHelldivers1)
	if err != nil {
		return nil, err
	}
	renderer.resolveHD1Material = pv.resolveHD1Material
	return NewUnitGallery(renderer, pv.getResourceGenerator(true), pv.thinhashes), nil
}

func (pv *AutoPreview) NewTextureGallery() *TextureGallery {
	return NewTextureGallery(pv.getResourceGenerator(false))
}

func (pv *AutoPreview) UnitCamera() (UnitCamera, bool) {
	if pv.activeType != AutoPreviewUnit {
		return UnitCamera{}, false
	}
	return pv.previews.unit.Camera(), true
}

func (pv *AutoPreview) SetUnitCamera(camera UnitCamera) {
	if pv.activeType == AutoPreviewUnit {
		pv.previews.unit.SetCamera(camera)
	}
}

// SetHideLevelGround applies the level gallery's ground visibility preference
// to the regular preview and any animation preview renderer.
func (pv *AutoPreview) SetHideLevelGround(hidden bool) {
	if pv.previews.unit != nil {
		pv.previews.unit.SetHideLevelGround(hidden)
	}
	if pv.animationUnit != nil {
		pv.animationUnit.SetHideLevelGround(hidden)
	}
}

func (pv *AutoPreview) HideLevelGround() bool {
	return pv.previews.unit != nil && pv.previews.unit.hideLevelGround
}

func (pv *AutoPreview) SceneOverlayVisible(kind SceneOverlayKind) bool {
	return pv.previews.unit == nil || pv.previews.unit.SceneOverlayVisible(kind)
}

func (pv *AutoPreview) SetSceneOverlayVisible(kind SceneOverlayKind, visible bool) {
	if pv.previews.unit != nil {
		pv.previews.unit.SetSceneOverlayVisible(kind, visible)
	}
	if pv.animationUnit != nil {
		pv.animationUnit.SetSceneOverlayVisible(kind, visible)
	}
}

func (pv *AutoPreview) AutoZoomUnitNow() {
	if pv.activeType == AutoPreviewUnit {
		pv.previews.unit.doAutoZoomNextFrame = true
		pv.previews.unit.animTime = 0
	}
}

func (pv *AutoPreview) LoadFile(ctx context.Context, fileID stingray.FileID, maxVideoVerticalResolution int, colorGrading stingray.FileID, entityVarMapping shading_environment.ShadingEnvironmentEntityToShaderMapping) {
	if fileID == (stingray.FileID{}) {
		pv.activeID.Name.Value = 0
		pv.activeID.Type.Value = 0
		pv.activeType = AutoPreviewEmpty
		return
	}

	pv.activeID = fileID
	pv.err = nil
	if pv.pendingAnimation != nil && pv.pendingAnimation.unitID != fileID {
		pv.pendingAnimation = nil
	}

	var data [3][]byte
	// Fills data with the files of the according
	// data types. If the requested type doesn't
	// exist, the data slice of the missing type
	// remains nil.
	loadFiles := func(types ...stingray.DataType) error {
		for _, typ := range types {
			if data[typ] != nil {
				panic("programmer error: duplicate data type")
			}
			b, exists, err := pv.getResourceGenerator(false)(fileID, typ)
			if err != nil {
				return fmt.Errorf("reading file: %w", err)
			}
			if exists {
				data[typ] = b
			}
		}
		return nil
	}

	var entityInfo *entity.Entity
	entityData, exists, err := pv.getResourceGenerator(false)(colorGrading, stingray.DataMain)
	if err == nil && exists {
		entityInfo, err = entity.LoadEntity(bytes.NewReader(entityData), entityVarMapping)
	}
	gradingChanged := pv.colorGradingID != colorGrading
	pv.colorGradingID = colorGrading
	pv.entityInfo = entityInfo
	if gradingChanged && pv.levelGallery != nil {
		pv.levelGallery.SetEntityInfo(entityInfo)
	}
	loadStart := time.Now()
	switch fileID.Type {
	case stingray.Sum("unit"):
		pv.activeType = AutoPreviewUnit
		if err := loadFiles(stingray.DataMain, stingray.DataGPU); err != nil {
			pv.err = err
			return
		}
		pv.previews.unit.Clear()
		pv.previews.unit.SetEntityInfo(entityInfo)
		if err := pv.previews.unit.LoadUnit(
			fileID.Name,
			data[stingray.DataMain],
			data[stingray.DataGPU],
			entityInfo,
		); err != nil {
			pv.err = fmt.Errorf("loading unit: %w", err)
			return
		}
	case stingray.Sum("animation"):
		pv.activeType = AutoPreviewAnimation
		if err := loadFiles(stingray.DataMain); err != nil {
			pv.err = err
			return
		}
		pv.animationData, err = animation.LoadAnimation(bytes.NewReader(data[stingray.DataMain]))
		if err != nil {
			pv.err = fmt.Errorf("loading animation: %w", err)
			return
		}
	case stingray.Sum("prefab"):
		pv.activeType = AutoPreviewUnit
		if err := loadFiles(stingray.DataMain); err != nil {
			pv.err = err
			return
		}
		pv.previews.unit.Clear()
		pv.previews.unit.SetEntityInfo(entityInfo)
		if err := pv.previews.unit.LoadPrefab(
			fileID.Name,
			data[stingray.DataMain],
			entityInfo,
		); err != nil {
			pv.err = fmt.Errorf("loading prefab: %w", err)
			return
		}
	case stingray.Sum("level"):
		pv.activeType = AutoPreviewUnit
		if err := loadFiles(stingray.DataMain); err != nil {
			pv.err = err
			return
		}
		pv.previews.unit.Clear()
		pv.previews.unit.SetEntityInfo(entityInfo)
		if err := pv.previews.unit.LoadLevel(
			fileID.Name,
			data[stingray.DataMain],
			entityInfo,
		); err != nil {
			pv.err = fmt.Errorf("loading prefab: %w", err)
			return
		}
	case stingray.Sum("speedtree"):
		pv.activeType = AutoPreviewUnit
		if err := loadFiles(stingray.DataMain, stingray.DataGPU); err != nil {
			pv.err = err
			return
		}
		pv.previews.unit.Clear()
		if err := pv.previews.unit.LoadSpeedtree(
			fileID.Name,
			data[stingray.DataMain],
			data[stingray.DataGPU],
			pv.thinhashes,
			entityInfo,
		); err != nil {
			pv.err = fmt.Errorf("loading speedtree: %w", err)
			return
		}
	case stingray.Sum("wwise_stream"):
		pv.previews.audio.ClearStreams()
		pv.activeType = AutoPreviewAudio
		if err := loadFiles(stingray.DataStream); err != nil {
			pv.err = err
			return
		}
		pv.previews.audio.Title = fileID.Name.String()
		pv.previews.audio.LoadStream(fileID.Name.String(), data[stingray.DataStream], nil, true)
	case stingray.Sum("wwise_bank"):
		pv.previews.audio.ClearStreams()
		pv.activeType = AutoPreviewAudio
		if err := loadFiles(stingray.DataMain); err != nil {
			pv.err = err
			return
		}
		bnkFile, ok := pv.hashes[fileID.Name]
		if !ok {
			pv.err = fmt.Errorf("expected wwise bank file %v.wwise_bank to have a known name", fileID.Name)
			return
		}
		pv.previews.audio.Title = bnkFile
		dir := path.Dir(bnkFile)
		streams, err := stingray_wwise.BnkGetAllReferencedStreamData(
			bytes.NewReader(data[stingray.DataMain]),
			func(id uint32) (data []byte, exists bool, err error) {
				fileID := stingray.FileID{
					Name: stingray.Sum(path.Join(dir, fmt.Sprint(id))),
					Type: stingray.Sum("wwise_stream"),
				}
				return pv.getResourceGenerator(false)(fileID, stingray.DataStream)
			},
		)
		if err != nil {
			pv.err = fmt.Errorf("loading wwise bank: %w", err)
			return
		}
		for _, id := range slices.Sorted(maps.Keys(streams)) {
			stream := streams[id]
			pv.previews.audio.LoadStream(fmt.Sprint(id), stream.Data, stream.Err, false)
		}
	case stingray.Sum("bik"), stingray.Sum("bk2"):
		pv.activeType = AutoPreviewVideo
		if err := loadFiles(stingray.DataMain, stingray.DataStream, stingray.DataGPU); err != nil {
			pv.err = err
			return
		}
		rs := []io.Reader{bytes.NewReader(data[stingray.DataMain])}
		if data[stingray.DataStream] != nil {
			rs = append(rs, bytes.NewReader(data[stingray.DataStream]))
		} else {
			rs = append(rs, bytes.NewReader(data[stingray.DataGPU]))
		}
		r := io.MultiReader(rs...)

		// Skip stingray header
		if _, err := io.ReadFull(r, make([]byte, 16)); err != nil {
			pv.err = err
			return
		}

		if err := pv.previews.video.Load(r, maxVideoVerticalResolution); err != nil {
			cmdNotRegisteredErr := &exec.CommandNotRegisteredError{}
			if errors.As(err, &cmdNotRegisteredErr) {
				pv.err = errors.New(`FFmpeg not found; go to Settings->Extensions to install FFmpeg`)
			} else {
				pv.err = err
			}
			return
		}
	case stingray.Sum("texture"):
		pv.activeType = AutoPreviewTexture
		if err := loadFiles(stingray.DataMain, stingray.DataStream, stingray.DataGPU); err != nil {
			pv.err = err
			return
		}
		if len(data[stingray.DataMain]) >= 4 && string(data[stingray.DataMain][:4]) == "DDS " {
			img, err := dds.Decode(bytes.NewReader(data[stingray.DataMain]), false)
			if err != nil {
				pv.err = fmt.Errorf("loading Helldivers 1 DDS image: %w", err)
				return
			}
			ImagePreviewLoadDDS(pv.previews.texture, img)
			return
		}
		r := io.MultiReader(
			bytes.NewReader(data[stingray.DataMain]),
			bytes.NewReader(data[stingray.DataStream]),
			bytes.NewReader(data[stingray.DataGPU]),
		)
		if _, err := texture.DecodeInfo(r); err != nil {
			pv.err = fmt.Errorf("loading stingray DDS info: %w", err)
			return
		}
		img, err := dds.Decode(r, false)
		if err != nil {
			pv.err = fmt.Errorf("loading DDS image: %w", err)
			return
		}
		ImagePreviewLoadDDS(pv.previews.texture, img)
	case stingray.Sum("strings"):
		pv.activeType = AutoPreviewStrings
		if err := loadFiles(stingray.DataMain); err != nil {
			pv.err = err
			return
		}
		data, err := stingray_strings.Load(
			bytes.NewReader(data[stingray.DataMain]),
		)
		if err != nil {
			pv.err = fmt.Errorf("loading DDS image: %w", err)
			return
		}
		pv.previews.strings.Load(data, pv.thinhashes)
	case stingray.Sum("material"):
		pv.activeType = AutoPreviewMaterial
		if err := loadFiles(stingray.DataMain); err != nil {
			pv.err = err
			return
		}
		var materialData *material.Material
		var err error
		if pv.isHelldivers1 {
			materialData, err = material.LoadHelldivers1Main(data[stingray.DataMain], stingray.ThinHash{}, nil, func(textureName stingray.Hash) bool {
				textureBytes, exists, textureErr := pv.getResourceGenerator(false)(stingray.NewFileID(textureName, stingray.Sum("texture")), stingray.DataMain)
				return textureErr == nil && exists && len(textureBytes) >= 4 && string(textureBytes[:4]) == "DDS "
			})
		} else {
			materialData, err = material.LoadMain(bytes.NewReader(data[stingray.DataMain]))
		}
		if err != nil {
			pv.err = fmt.Errorf("loading material: %w", err)
			return
		}
		err = pv.previews.material.LoadMaterial(materialData, pv.getResourceGenerator(false), pv.hashes, pv.thinhashes)
		if err != nil {
			pv.err = fmt.Errorf("loading material: %w", err)
			return
		}
	case stingray.Sum("xaml"):
		pv.activeType = AutoPreviewXAML
		if err := loadFiles(stingray.DataMain); err != nil {
			pv.err = err
			return
		}
		if err := pv.previews.xaml.LoadXaml(data[stingray.DataMain]); err != nil {
			pv.err = fmt.Errorf("loading xaml: %w", err)
			return
		}
	case stingray.Sum("ttf"), stingray.Sum("otf"):
		pv.activeType = AutoPreviewFont
		if err := loadFiles(stingray.DataMain); err != nil {
			pv.err = err
			return
		}
		ttf, err := ttf.LoadTTF(bytes.NewReader(data[stingray.DataMain]))
		if err != nil {
			pv.err = fmt.Errorf("loading ttf/otf: %w", err)
			return
		}
		if err := pv.previews.font.Load(ttf.Data); err != nil {
			pv.err = fmt.Errorf("loading ttf/otf into preview: %w", err)
			return
		}
	default:
		pv.activeType = AutoPreviewEmpty
	}
	pv.loadMs = uint32(time.Since(loadStart).Milliseconds())
}

var ArmorSetTypeHash = stingray.Sum("armor_set")

func (pv *AutoPreview) ShowArmorSet(kitID uint32, kitArchive stingray.Hash) bool {
	if pv.previews.unit == nil || !pv.previews.unit.LoadArmorSetByKit(kitID) {
		return false
	}
	pv.err = nil
	pv.activeType = AutoPreviewUnit
	pv.activeID = stingray.NewFileID(kitArchive, ArmorSetTypeHash)
	return true
}

func (pv *AutoPreview) UpdateAssetOverrides(entityInfo *entity.Entity) {
	pv.entityInfo = entityInfo
	if pv.levelGallery != nil {
		pv.levelGallery.SetEntityInfo(entityInfo)
	}
	if pv.activeType != AutoPreviewUnit {
		return
	}
	pv.previews.unit.UpdateAssetOverrides(entityInfo)
}

func (pv *AutoPreview) MaterialSettingsEmpty() bool {
	return pv.previews.material.SettingsEmpty()
}

func (pv *AutoPreview) MaterialSettingsVisible() bool {
	return pv.previews.material.SettingsVisible()
}

func (pv *AutoPreview) SetMaterialSettingsVisible(visible bool) {
	pv.previews.material.SetSettingsVisible(visible)
}

func (pv *AutoPreview) Debug() bool {
	return pv.debug
}

func (pv *AutoPreview) SetDebug(debug bool) {
	pv.debug = debug
}

func (pv *AutoPreview) Draw(name string) bool {
	if pv.err != nil {
		imutils.TextError(pv.err)
		return true
	}
	rootPos := imgui.CursorPos()
	drawStart := time.Now()
	switch pv.activeType {
	case AutoPreviewEmpty:
		return false
	case AutoPreviewUnit:
		pv.previews.unit.Draw(name)
	case AutoPreviewAnimation:
		pv.previews.unit.DrawAnimationInfo(pv.activeID.Name, pv.animationData)
	case AutoPreviewAudio:
		pv.previews.audio.Draw(name)
	case AutoPreviewVideo:
		pv.previews.video.Draw()
	case AutoPreviewTexture:
		pv.previews.texture.Draw(name)
	case AutoPreviewStrings:
		pv.previews.strings.Draw()
	case AutoPreviewMaterial:
		pv.previews.material.Draw(name)
	case AutoPreviewXAML:
		pv.previews.xaml.Draw(name)
	case AutoPreviewFont:
		pv.previews.font.Draw(name)
	default:
		panic("unhandled case")
	}
	if pv.debug {
		drawMs := time.Since(drawStart).Milliseconds()
		pv.drawTime = pv.drawTime - (pv.drawTime / 120.0) + (time.Since(drawStart).Seconds() / 120.0)
		text := fmt.Sprintf("Load took %vms\nDraw took %vms (%.2f fps)", pv.loadMs, drawMs, math.Min(1.0/pv.drawTime, 1000.0))
		size := imgui.CalcTextSize(text)
		rootPos.X = imgui.ContentRegionAvail().X - size.X - imgui.CurrentStyle().ItemInnerSpacing().X
		rootPos.Y += imgui.CurrentStyle().ItemInnerSpacing().Y
		imgui.SetCursorPos(rootPos)
		imgui.SetNextItemAllowOverlap()
		imutils.Textf(text)
	}
	return true
}

func (pv *AutoPreview) DrawSettings() bool {
	if pv.err != nil {
		return true
	}
	switch pv.activeType {
	case AutoPreviewEmpty:
		return false
	case AutoPreviewMaterial:
		pv.previews.material.DrawSettings()
	case AutoPreviewUnit:
		pv.previews.unit.DrawSettings()
	case AutoPreviewAnimation:
		return false
	}
	return true
}

func (pv *AutoPreview) DrawAnimation(name string) bool {
	if pv.err != nil {
		imutils.TextError(pv.err)
		return true
	}
	if pv.activeID == (stingray.FileID{}) {
		imgui.TextUnformatted("Select a unit or animation resource to play it here.")
		return true
	}
	if pv.animationUnit == nil {
		renderer, err := NewUnitPreview(pv.getResourceGenerator(true), pv.getOverride, pv.armorParams, pv.lookupHash, pv.lookupThinHash, pv.isHelldivers1)
		if err != nil {
			imutils.TextError(err)
			return true
		}
		renderer.resolveHD1Material = pv.resolveHD1Material
		renderer.SetAnimationEnabled(true)
		renderer.SetAnimationResources(pv.animationResources)
		renderer.SetEntityInfo(pv.entityInfo)
		pv.animationUnit = renderer
	}

	switch pv.activeID.Type {
	case ArmorSetTypeHash:
		if pv.animationUnitSource != pv.activeID {
			var kitID uint32
			kit, ok := pv.armorParams.ArmorSets[pv.activeID.Name]
			if !ok {
				imutils.TextError(fmt.Errorf("armor set is unavailable"))
				return true
			}
			kitID = kit.Id.Value
			renderer := pv.animationUnit
			renderer.armorAnimationMode = true
			if !renderer.LoadArmorSetByKit(kitID) {
				imutils.TextError(fmt.Errorf("could not assemble armor set"))
				return true
			}
			rigID := stingray.NewFileID(stingray.Sum("content/fac_helldivers/cha_avatar/avatar_helldiver"), stingray.Sum("unit"))
			data, exists, err := pv.getResourceGenerator(true)(rigID, stingray.DataMain)
			if err != nil || !exists {
				imutils.TextError(fmt.Errorf("Helldiver animation rig is unavailable"))
				return true
			}
			rig, err := unit.LoadInfo(bytes.NewReader(data))
			if err != nil {
				imutils.TextError(fmt.Errorf("loading Helldiver animation rig: %w", err))
				return true
			}
			root := renderer.rootHash
			renderer.rootHash = rigID
			renderer.loadLinkedAnimations(rig)
			renderer.rootHash = root
			renderer.armorBoneMap = make(map[stingray.FileID][]int)
			byName := make(map[stingray.ThinHash]int, len(rig.Bones))
			for i, b := range rig.Bones {
				byName[b.NameHash] = i
			}
			for id, info := range renderer.unitInfos {
				mapping := make([]int, len(info.Bones))
				for i, b := range info.Bones {
					mapping[i] = -1
					if j, ok := byName[b.NameHash]; ok {
						mapping[i] = j
					}
				}
				renderer.armorBoneMap[id] = mapping
			}
			pv.animationUnitSource = pv.activeID
		}
		pv.animationUnit.DrawAnimated(name)
	case stingray.Sum("unit"):
		pv.animationUnit.armorAnimationMode = false
		if pv.animationUnitSource != pv.activeID {
			mainData, exists, err := pv.getResourceGenerator(true)(pv.activeID, stingray.DataMain)
			if err != nil || !exists {
				if err == nil {
					err = fmt.Errorf("unit resource is missing")
				}
				imutils.TextError(fmt.Errorf("loading unit animation preview: %w", err))
				return true
			}
			gpuData, _, err := pv.getResourceGenerator(true)(pv.activeID, stingray.DataGPU)
			if err != nil {
				imutils.TextError(fmt.Errorf("loading unit animation mesh: %w", err))
				return true
			}
			pv.animationUnit.Clear()
			pv.animationUnit.SetEntityInfo(pv.entityInfo)
			if err := pv.animationUnit.LoadUnit(pv.activeID.Name, mainData, gpuData); err != nil {
				imutils.TextError(fmt.Errorf("loading unit animation preview: %w", err))
				return true
			}
			pv.animationUnitSource = pv.activeID
		}
		if request := pv.pendingAnimation; request != nil && request.unitID == pv.activeID {
			if err := pv.animationUnit.SelectLinkedAnimation(request.animationID, request.stateName, request.preferBlend); err != nil {
				pv.animationUnit.animationPlayer.loadError = err
			}
			pv.pendingAnimation = nil
		}
		pv.animationUnit.DrawAnimated(name)
	case stingray.Sum("animation"):
		if pv.animationUnitSource != pv.activeID && pv.animationData != nil {
			pv.animationUnit.Clear()
			animationData, exists, err := pv.getResourceGenerator(false)(pv.activeID, stingray.DataMain)
			if err != nil || !exists {
				if err == nil {
					err = fmt.Errorf("animation resource is missing")
				}
				imutils.TextError(fmt.Errorf("reading animation: %w", err))
				return true
			}
			if err := pv.animationUnit.SetStandaloneAnimation(pv.activeID.Name, animationData); err != nil {
				imutils.TextError(fmt.Errorf("loading animation: %w", err))
				return true
			}
			pv.animationUnit.animationPlayer.playing = true
			pv.animationUnitSource = pv.activeID
		}
		if pv.animationUnit.HasLoadedUnit() {
			pv.animationUnit.DrawAnimated(name)
		} else {
			pv.animationUnit.DrawStandaloneAnimation()
		}
	default:
		imgui.TextUnformatted("Select a unit or animation resource to play it here.")
	}
	return true
}
