package previews

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/go-gl/gl/v4.3-core/gl"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/x448/float16"
	fnt "github.com/xypwn/filediver/cmd/filediver-gui/fonts"
	"github.com/xypwn/filediver/cmd/filediver-gui/imutils"
	"github.com/xypwn/filediver/cmd/filediver-gui/widgets"
	preview_mats "github.com/xypwn/filediver/cmd/filediver-gui/widgets/previews/materials"
	datalib "github.com/xypwn/filediver/datalibrary"
	"github.com/xypwn/filediver/datalibrary/enum"
	"github.com/xypwn/filediver/dds"
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/entity"
	"github.com/xypwn/filediver/stingray/level"
	"github.com/xypwn/filediver/stingray/prefab"
	"github.com/xypwn/filediver/stingray/speedtree"
	"github.com/xypwn/filediver/stingray/unit"
	geometrygroup "github.com/xypwn/filediver/stingray/unit/geometry_group"
	"github.com/xypwn/filediver/stingray/unit/material"
	"github.com/xypwn/filediver/stingray/unit/texture"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// stingray coords to OpenGL coords
var stingrayToGLCoords = mgl32.Mat4FromRows(
	mgl32.Vec4{1, 0, 0, 0},
	mgl32.Vec4{0, 0, 1, 0},
	mgl32.Vec4{0, -1, 0, 0},
	mgl32.Vec4{0, 0, 0, 1},
)

var UNIT = stingray.Sum("unit")
var SPEEDTREE = stingray.Sum("speedtree")
var PREFAB = stingray.Sum("prefab")
var LEVEL = stingray.Sum("level")
var MATERIAL = stingray.Sum("material")

type unitPreviewObject struct {
	vao                uint32   // vertex array object
	ibos               []uint32 // index buffer objects
	vbo                uint32   // vertex buffer object
	imbo               uint32   // instance matrices buffer object
	materials          []preview_mats.Material
	wireframe          preview_mats.Material
	normalVis          preview_mats.Material
	matrix             mgl32.Mat4
	bindMatrix         mgl32.Mat4
	boneIndex          int
	skeletonMapIndex   int
	basePositions      [][3]float32
	baseNormals        [][3]float32
	baseBoneIndices    [][4]uint8
	baseBoneWeights    [][4]float32
	normalsOffset      int
	animAttribVBO      uint32
	animSkinBuffer     uint32
	animNormalBuffer   uint32
	animSkinMatrices   []mgl32.Mat4
	animNormalMatrices []mgl32.Mat4
	name               stingray.Hash

	numVertices        int32
	numIndices         []int32
	numInstances       int32
	indexType          []uint32
	hasVisibilityMasks int32
}

func (obj *unitPreviewObject) genObjects(textures bool, numIbos int32) {
	gl.GenVertexArrays(1, &obj.vao)
	gl.GenBuffers(1, &obj.vbo)
	if numIbos > 0 {
		obj.ibos = make([]uint32, numIbos)
		obj.numIndices = make([]int32, numIbos)
		gl.GenBuffers(numIbos, &obj.ibos[0])
	}
	gl.GenBuffers(1, &obj.imbo)
	obj.numInstances = -1

	gl.BindVertexArray(obj.vao)
	defer gl.BindVertexArray(0)

	gl.BindBuffer(gl.ARRAY_BUFFER, obj.vbo)
	defer gl.BindBuffer(gl.ARRAY_BUFFER, 0)

	gl.BindBuffer(gl.ARRAY_BUFFER, obj.imbo)
	defer gl.BindBuffer(gl.ARRAY_BUFFER, 0)
}

func (obj unitPreviewObject) deleteObjects(ml *preview_mats.MaterialManager) {
	gl.DeleteVertexArrays(1, &obj.vao)
	bufferIDs := []uint32{obj.vbo, obj.imbo, obj.animAttribVBO, obj.animSkinBuffer, obj.animNormalBuffer}
	gl.DeleteBuffers(int32(len(bufferIDs)), &bufferIDs[0])
	if len(obj.ibos) > 0 {
		gl.DeleteBuffers(int32(len(obj.ibos)), &obj.ibos[0])
	}
	for _, material := range obj.materials {
		ml.DeleteMaterial(material)
	}
	ml.DeleteMaterial(obj.wireframe)
	ml.DeleteMaterial(obj.normalVis)
}

type unitPreviewNode struct {
	name            string
	shown           bool
	overlayKind     SceneOverlayKind
	matrix          mgl32.Mat4
	children        []stingray.FileID
	parent          stingray.Hash
	mountParentUnit stingray.FileID
	mountBoneIndex  int
}

// SceneOverlayKind identifies level helper geometry that is useful to inspect
// but can obscure the placed environment when rendered as opaque solids.
type SceneOverlayKind uint8

const (
	SceneOverlayNone SceneOverlayKind = iota
	SceneOverlayVolumes
	SceneOverlayAudioVolumes
	SceneOverlayAvoidanceVolumes
	SceneOverlayRendererHelpers
	SceneOverlayDecalBoxes
)

var tutorialTriggerUnitHash = stingray.Sum("content/env_shared/assets/tutorial_trigger_area/tutorial_trigger_area")
var killVolumeUnitHash = stingray.Sum("content/env_shared/assets/kill_volume/kill_volume")
var landingZoneMaterialHash = stingray.Sum("content/markers/m_landing_zone")
var reverbVolumeMaterialHash = stingray.Sum("content/env_shared/assets/reverb_volume/m_reverb_volume")
var wwiseAudioVolumeUnitHash = stingray.Sum("content/wwise/wwise_audio_volume")
var wwiseAudioPortalUnitHash = stingray.Sum("content/wwise/wwise_audio_portal")

const rendererHelperUnitPrefix = "core/stingray_renderer/helper_units/"

var decalBoxUnitHash = stingray.Sum("core/stingray_renderer/helper_units/decals/decal_box")
var decalBoxLongRangeUnitHash = stingray.Sum("core/stingray_renderer/helper_units/decals/decal_box_long_range")

var armorCamos = []enum.CamouflageType{
	enum.CamouflageType_Default,
	enum.CamouflageType_Arctic,
	enum.CamouflageType_Desert,
	enum.CamouflageType_Forest,
	enum.CamouflageType_Crimson,
}

type UnitPreviewState struct {
	fb *widgets.GLViewState
	ml *preview_mats.MaterialManager

	captureMode     bool
	hideLevelGround bool
	isHelldivers1   bool

	getResource        GetResourceFunc
	resolveHD1Material ResolveHD1MaterialFunc

	nodes    map[stingray.Hash]unitPreviewNode
	root     unitPreviewNode
	rootHash stingray.FileID

	getOverride     func(stingray.FileID) stingray.FileID
	objectOverrides map[stingray.FileID]stingray.FileID

	objectDrawList      map[stingray.FileID]map[string][]float32
	objects             map[stingray.FileID]map[string]unitPreviewObject
	objectsShown        map[stingray.FileID]map[string]bool
	objectsShownDefault map[stingray.FileID]map[string]bool
	objectsSelected     map[stingray.FileID]map[string]bool
	sceneOverlayVisible map[SceneOverlayKind]bool
	sceneOverlayKinds   map[stingray.FileID]SceneOverlayKind

	treeViewShown bool
	treeViewDrawn bool

	boundingBoxMaterial preview_mats.Material

	skeletons       map[stingray.FileID]unitPreviewObject
	boundingBoxes   map[stingray.FileID]map[string]unitPreviewObject
	unitInfos       map[stingray.FileID]*unit.Info
	visibilityMasks map[stingray.FileID]*unitVisibilityMasks

	rootHealthComponent   *datalib.HealthComponent
	rootHealthLoaded      bool
	rootHealthLoadError   string
	damageModelSelection  damageModelMode
	damageOverlayBoxes    []damageOverlayBox
	damageOverlayVolumes  []damageOverlayVolume
	damageOverlayUnit     stingray.FileID
	damageOverlayLoaded   bool
	damageOverlayError    string
	showPhysicsHitboxes   bool
	showDamageBoxes       bool
	showDamageReadout     bool
	damageBoxRenderer     unitPreviewObject
	damageSphereRenderer  unitPreviewObject
	damageCapsuleRenderer unitPreviewObject
	damageBoxRenderReady  bool
	damageBoxScratch      []float32

	mountedAttachmentsEnabled bool
	mountedAttachmentsLoaded  bool
	mountedAttachmentNodes    []stingray.Hash
	mountedAttachmentWarnings []string

	vfov         float32
	modelPos     mgl32.Vec4
	model        mgl32.Mat4
	viewDistance float32
	viewRotation mgl32.Vec2 // {yaw, pitch}

	// Previous view distance and rotation (for view animation)
	animOrigViewDistance float32
	animOrigViewRotation mgl32.Vec2
	animOrigModelPos     mgl32.Vec4
	animTime             float32 // range [0;1], -1 when not animating

	// Axis-aligned bounding box. Don't forget
	// to multiply aabb's vertices with aabbMat first!
	aabb    map[stingray.FileID]map[string][2]mgl32.Vec3
	aabbMat map[stingray.FileID]map[string]mgl32.Mat4

	// For fitting mesh to screen and debug info
	meshPositions map[stingray.FileID]map[string][][3]float32
	meshNormals   map[stingray.FileID]map[string][][3]float32

	fibonacciNormalLut [][4]float32

	skeletonPositions    map[stingray.FileID][]mgl32.Vec3
	skeletonBoneIndices  map[stingray.FileID][]int
	rootUnitInfo         *unit.Info
	animationBoneMap     []int
	animationLocals      []mgl32.Mat4
	animationGlobals     []mgl32.Mat4
	animationRootBone    int
	animationPlayer      animationPlayback
	animationEnabled     bool
	animationResourceIDs []stingray.FileID

	maxViewDistance float32

	numUdims           uint32
	udimsShownDefault  [64]bool
	udimsSelected      [64]bool  // udims persistently selected
	udimsShown         [64]int32 // udims visually selected 1 (shown) or 0 (hidden)
	udimNames          [64]string
	udimsSettingsShown bool
	udimsSettingsDrawn bool

	// For dragging selection
	activeUDimListItem  int32
	hoveredUDimListItem int32

	activeMeshListItem  int32
	hoveredMeshListItem int32

	showWireframe             bool
	thumbnailFlat             bool
	wireframeColor            mgl32.Vec4
	showAABB                  bool
	aabbColor                 mgl32.Vec4
	showSkeleton              bool
	skeletonColor             mgl32.Vec4
	visualizeNormals          bool
	visualizeTangentBitangent int32 // 1 or 0
	autoZoomEnabled           bool
	doAutoZoomNextFrame       bool
	lodLevel                  int
	maxLODLevel               int

	armorSets                map[stingray.Hash]datalib.ArmorSet
	selectedCamo             enum.CamouflageType
	selectedSet              int64
	getSelectedArchives      func() []stingray.Hash
	previousSelectedArchives []stingray.Hash
	// Archives of armor kits chosen in the Armor Set window: their LUTs apply to the shown
	// pieces as if the archives were selected in the Archive Filter.
	armorContextArchives []stingray.Hash
	armorThumbnailOnly   bool
	armorAnimationMode   bool
	armorBoneMap         map[stingray.FileID][]int
	archivesModified     bool
	lookupThinHash       func(stingray.ThinHash) string
	lookupHash           func(stingray.Hash) string
}

func NewUnitPreview(
	getResource func(id stingray.FileID, typ stingray.DataType) (data []byte, exists bool, err error),
	getOverride func(stingray.FileID) stingray.FileID,
	ArmorParams ExtractorArmorParameters,
	lookupHash func(stingray.Hash) string,
	lookupThinHash func(stingray.ThinHash) string,
	isHelldivers1 bool,
) (*UnitPreviewState, error) {
	var err error

	pv := &UnitPreviewState{}
	pv.animationPlayer.reset()
	pv.isHelldivers1 = isHelldivers1
	pv.sceneOverlayVisible = map[SceneOverlayKind]bool{
		SceneOverlayVolumes:          true,
		SceneOverlayAudioVolumes:     true,
		SceneOverlayAvoidanceVolumes: false,
		SceneOverlayRendererHelpers:  false,
		SceneOverlayDecalBoxes:       false,
	}
	pv.sceneOverlayKinds = make(map[stingray.FileID]SceneOverlayKind)

	pv.fb, err = widgets.NewGLView()
	if err != nil {
		return nil, err
	}

	pv.ml = preview_mats.NewMaterialManager(getResource, getOverride, lookupThinHash, lookupHash)
	pv.ml.GenerateIBLLookup(512)

	pv.lookupHash = lookupHash
	pv.lookupThinHash = lookupThinHash
	pv.model = stingrayToGLCoords

	pv.armorSets = ArmorParams.ArmorSets
	pv.selectedCamo = enum.CamouflageType_None
	pv.getSelectedArchives = ArmorParams.SelectedArchives
	if pv.getSelectedArchives == nil {
		pv.getSelectedArchives = func() []stingray.Hash { return nil }
	}
	pv.getOverride = getOverride
	pv.getResource = getResource

	boundingBoxMaterial := preview_mats.NewDebugObjectMaterial("bounding box")
	err = boundingBoxMaterial.Generate()
	if err != nil {
		return nil, err
	}
	pv.boundingBoxMaterial = boundingBoxMaterial

	pv.vfov = mgl32.DegToRad(60)
	pv.viewDistance = 25
	pv.modelPos = mgl32.Vec4{0.0, 0.0, 0.0, 1.0}
	pv.viewRotation = mgl32.Vec2{math.Pi, 0.0}

	pv.wireframeColor = [4]float32{1.0, 1.0, 1.0, 0.5}
	pv.skeletonColor = [4]float32{1.0, 0.5, 0.0, 0.5}
	pv.aabbColor = [4]float32{0.3, 0.3, 0.8, 0.2}

	pv.activeUDimListItem = -1
	pv.hoveredUDimListItem = -1

	pv.activeMeshListItem = -1
	pv.hoveredMeshListItem = -1

	return pv, nil
}

func (pv *UnitPreviewState) Delete() {
	pv.fb.Delete()
	for hash := range pv.objects {
		for name := range pv.objects[hash] {
			pv.objects[hash][name].deleteObjects(pv.ml)
			pv.boundingBoxes[hash][name].deleteObjects(pv.ml)
		}
	}
	if pv.damageBoxRenderReady {
		pv.damageBoxRenderer.deleteObjects(pv.ml)
		pv.damageSphereRenderer.deleteObjects(pv.ml)
		pv.damageCapsuleRenderer.deleteObjects(pv.ml)
	}
	pv.ml.Delete()
}

type UnitCamera struct {
	Rotation mgl32.Vec2
	Distance float32
	ModelPos mgl32.Vec4
}

func DefaultUnitCamera() UnitCamera {
	return UnitCamera{
		Rotation: mgl32.Vec2{math.Pi, 0},
		Distance: 25,
		ModelPos: mgl32.Vec4{0, 0, 0, 1},
	}
}

func (pv *UnitPreviewState) Camera() UnitCamera {
	return UnitCamera{Rotation: pv.viewRotation, Distance: pv.viewDistance, ModelPos: pv.modelPos}
}

func (pv *UnitPreviewState) SetCamera(camera UnitCamera) {
	pv.viewRotation = camera.Rotation
	pv.viewDistance = camera.Distance
	pv.modelPos = camera.ModelPos
	pv.animOrigViewDistance = camera.Distance
	pv.animOrigViewRotation = camera.Rotation
	pv.animOrigModelPos = camera.ModelPos
	pv.animTime = -1
	pv.doAutoZoomNextFrame = false
}

func (pv *UnitPreviewState) HasLoadedUnit() bool {
	return len(pv.objects) > 0
}

func (pv *UnitPreviewState) udimsShownFor(fileID stingray.FileID) *int32 {
	if state := pv.visibilityMasks[fileID]; state != nil {
		return &state.shown[0]
	}
	return &pv.udimsShown[0]
}

func (pv *UnitPreviewState) MaterialsReady() bool {
	for _, objects := range pv.objects {
		for _, object := range objects {
			for _, material := range object.materials {
				if !pv.ml.MaterialTexturesReady(material) {
					return false
				}
			}
		}
	}
	return true
}

func (pv *UnitPreviewState) SetAnimationEnabled(enabled bool) {
	pv.animationEnabled = enabled
}

func (pv *UnitPreviewState) SetAnimationResources(ids []stingray.FileID) {
	pv.animationResourceIDs = append(pv.animationResourceIDs[:0], ids...)
}

func (pv *UnitPreviewState) ReleaseLoadedUnit() {
	for fileID, objects := range pv.objects {
		for name, object := range objects {
			object.deleteObjects(pv.ml)
			if boundingBox, ok := pv.boundingBoxes[fileID][name]; ok {
				boundingBox.deleteObjects(pv.ml)
			}
		}
		if skeleton, ok := pv.skeletons[fileID]; ok {
			skeleton.deleteObjects(pv.ml)
		}
	}
	clear(pv.nodes)
	clear(pv.objects)
	clear(pv.objectsShown)
	clear(pv.objectsShownDefault)
	clear(pv.objectsSelected)
	clear(pv.objectOverrides)
	clear(pv.boundingBoxes)
	clear(pv.skeletons)
	clear(pv.meshPositions)
	clear(pv.meshNormals)
	clear(pv.aabb)
	clear(pv.aabbMat)
	clear(pv.skeletonPositions)
	clear(pv.skeletonBoneIndices)
	clear(pv.unitInfos)
	clear(pv.visibilityMasks)
	clear(pv.sceneOverlayKinds)
	pv.rootHealthComponent = nil
	pv.rootHealthLoaded = false
	pv.rootHealthLoadError = ""
	pv.damageModelSelection = damageModelIntact
	pv.damageOverlayBoxes = nil
	pv.damageOverlayVolumes = nil
	pv.damageOverlayUnit = stingray.FileID{}
	pv.damageOverlayLoaded = false
	pv.damageOverlayError = ""
	pv.damageBoxScratch = pv.damageBoxScratch[:0]
	pv.mountedAttachmentsLoaded = false
	pv.mountedAttachmentNodes = nil
	pv.mountedAttachmentWarnings = nil
	pv.rootUnitInfo = nil
	pv.animationBoneMap = nil
	pv.animationLocals = nil
	pv.animationGlobals = nil
	pv.animationRootBone = -1
	pv.animationPlayer.reset()
	pv.root = unitPreviewNode{}
	pv.rootHash = stingray.FileID{}
	pv.maxViewDistance = 0
	pv.doAutoZoomNextFrame = false
	pv.animTime = -1
	pv.ml.ClearTextureData()
	pv.ml.Sweep()
}

// SetHideLevelGround controls the generated terrain mesh in loaded and future
// level previews. It leaves every other scene object untouched.
func (pv *UnitPreviewState) SetHideLevelGround(hidden bool) {
	if pv.hideLevelGround == hidden {
		return
	}
	pv.hideLevelGround = hidden
	for fileID, info := range pv.unitInfos {
		if len(info.TerrainInfos) == 0 {
			continue
		}
		for name := range pv.objectsShownDefault[fileID] {
			if name != "terrain" {
				continue
			}
			shown := !hidden
			pv.objectsShownDefault[fileID][name] = shown
			pv.objectsShown[fileID][name] = shown
			pv.objectsSelected[fileID][name] = shown
		}
	}
}

func (pv *UnitPreviewState) SceneOverlayVisible(kind SceneOverlayKind) bool {
	if pv.sceneOverlayVisible == nil {
		return true
	}
	return pv.sceneOverlayVisible[kind]
}

func (pv *UnitPreviewState) SetSceneOverlayVisible(kind SceneOverlayKind, visible bool) {
	if kind == SceneOverlayNone {
		return
	}
	if pv.sceneOverlayVisible == nil {
		pv.sceneOverlayVisible = make(map[SceneOverlayKind]bool)
	}
	pv.sceneOverlayVisible[kind] = visible
}

func (pv *UnitPreviewState) setNodeSceneOverlay(nodeHash stingray.Hash, unitID stingray.FileID) {
	node, ok := pv.nodes[nodeHash]
	if !ok {
		return
	}
	node.overlayKind = pv.sceneOverlayKind(unitID, node.name)
	pv.nodes[nodeHash] = node
	if node.overlayKind == SceneOverlayNone {
		return
	}
	pv.sceneOverlayKinds[unitID] = node.overlayKind
	// Several placed helper instances use unnamed UUID nodes after the first
	// named occurrence. Cache the kind by resource and update those nodes too.
	for hash, sibling := range pv.nodes {
		for _, child := range sibling.children {
			if child == unitID {
				sibling.overlayKind = node.overlayKind
				pv.nodes[hash] = sibling
				break
			}
		}
	}
}

func (pv *UnitPreviewState) sceneOverlayKind(unitID stingray.FileID, placedName string) SceneOverlayKind {
	if unitID.Type != UNIT {
		return SceneOverlayNone
	}
	if kind, ok := pv.sceneOverlayKinds[unitID]; ok {
		return kind
	}
	placedName = strings.ToLower(strings.ReplaceAll(placedName, "\\", "/"))
	if strings.Contains(placedName, "wwise_audio_volume") || strings.Contains(placedName, "wwise_audio_portal") || strings.Contains(placedName, "reverb_volume") {
		return SceneOverlayAudioVolumes
	}
	if strings.Contains(placedName, "hellpod_avoidance_volume") || strings.Contains(placedName, "avoidance_volume") {
		return SceneOverlayAvoidanceVolumes
	}
	if strings.Contains(placedName, "decal_box") {
		return SceneOverlayDecalBoxes
	}
	if strings.Contains(placedName, "trigger") || strings.Contains(placedName, "volume") {
		return SceneOverlayVolumes
	}
	// Use resource hashes as well as names: game hash dictionaries vary between
	// builds, and unresolved helper names previously let these boxes render.
	if unitID.Name == decalBoxUnitHash || unitID.Name == decalBoxLongRangeUnitHash {
		return SceneOverlayDecalBoxes
	}
	if unitID.Name == wwiseAudioVolumeUnitHash || unitID.Name == wwiseAudioPortalUnitHash {
		return SceneOverlayAudioVolumes
	}
	if pv.lookupHash != nil {
		name := strings.ToLower(strings.ReplaceAll(pv.lookupHash(unitID.Name), "\\", "/"))
		if strings.Contains(name, "hellpod_avoidance_volume") || strings.Contains(name, "avoidance_volume") {
			return SceneOverlayAvoidanceVolumes
		}
		if strings.HasPrefix(name, "core/stingray_renderer/helper_units/decals/decal_box") {
			return SceneOverlayDecalBoxes
		}
		if strings.Contains(name, "content/wwise/wwise_audio_volume") || strings.Contains(name, "content/wwise/wwise_audio_portal") || strings.Contains(name, "reverb_volume") {
			return SceneOverlayAudioVolumes
		}
		if strings.HasPrefix(name, rendererHelperUnitPrefix) {
			return SceneOverlayRendererHelpers
		}
		if strings.Contains(name, "trigger") || strings.Contains(name, "volume") {
			return SceneOverlayVolumes
		}
	}
	if unitID.Name == tutorialTriggerUnitHash || unitID.Name == killVolumeUnitHash {
		return SceneOverlayVolumes
	}
	// Placed instances can use aliases whose resource name does not match the
	// source unit. The shared marker materials identify those volume helpers.
	for _, object := range pv.objects[unitID] {
		for _, objectMaterial := range object.materials {
			materialID := objectMaterial.ID()
			if materialID.Name == reverbVolumeMaterialHash {
				return SceneOverlayAudioVolumes
			}
			if materialID.Name == landingZoneMaterialHash {
				return SceneOverlayVolumes
			}
			if pv.lookupHash != nil {
				materialName := strings.ToLower(pv.lookupHash(materialID.Name))
				if strings.Contains(materialName, "m_reverb_volume") {
					return SceneOverlayAudioVolumes
				}
				if strings.Contains(materialName, "m_landing_zone") {
					return SceneOverlayVolumes
				}
			}
		}
	}
	return SceneOverlayNone
}

func sceneOverlayOpacity(kind SceneOverlayKind) float32 {
	if kind == SceneOverlayVolumes || kind == SceneOverlayAudioVolumes || kind == SceneOverlayAvoidanceVolumes {
		return 0.30
	}
	if kind == SceneOverlayRendererHelpers {
		return 0.25
	}
	return 1
}

func (pv *UnitPreviewState) UpdateAssetOverrides(entityInfo *entity.Entity) {
	entityChanged := pv.ml.SetEntity(entityInfo)
	pv.ml.ClearTextureData()
	for fileID := range pv.objects {
		if fileID.Type.Value == 0x0 {
			// not actually a file
			continue
		}
		overrideID := pv.getOverride(fileID)
		if overrideID != pv.objectOverrides[fileID] && pv.rootHash.Name != fileID.Name {
			switch fileID.Type {
			case UNIT:
				unitMainData, _, _ := pv.getResource(fileID, stingray.DataMain)
				unitGpuData, _, _ := pv.getResource(fileID, stingray.DataGPU)
				err := pv.loadUnit(fileID, unitMainData, unitGpuData)
				if err != nil {
					fmt.Printf("Failed to update override for %v.%v: %v\n", pv.lookupHash(fileID.Name), pv.lookupHash(fileID.Type), err)
					break
				}
				pv.objectOverrides[fileID] = overrideID
			case SPEEDTREE:
				speedtreeMainData, _, _ := pv.getResource(fileID, stingray.DataMain)
				speedtreeGpuData, _, _ := pv.getResource(fileID, stingray.DataGPU)
				err := pv.loadSpeedtree(fileID, speedtreeMainData, speedtreeGpuData)
				if err != nil {
					fmt.Printf("Failed to update override for %v.%v: %v\n", pv.lookupHash(fileID.Name), pv.lookupHash(fileID.Type), err)
					break
				}
				pv.objectOverrides[fileID] = overrideID
			case PREFAB:
				prefabMainData, _, _ := pv.getResource(fileID, stingray.DataMain)
				node, contains := pv.nodes[fileID.Name]
				if !contains {
					fmt.Printf("%v.%v is not present in the scene? Why are we updating overrides for it?\n", pv.lookupHash(fileID.Name), pv.lookupHash(fileID.Type))
					break
				}

				children, err := pv.loadPrefab(fileID, prefabMainData)
				if err != nil {
					fmt.Printf("Failed to update override for %v.%v: %v\n", pv.lookupHash(fileID.Name), pv.lookupHash(fileID.Type), err)
					break
				}
				node.children = children
				pv.nodes[fileID.Name] = node
				pv.objectOverrides[fileID] = overrideID
			}
			continue
		}

		armorInfo := pv.getArmorInfo(fileID.Name)
		for mesh := range pv.objects[fileID] {
			for group := range pv.objects[fileID][mesh].materials {
				var mat *material.Material
				materialID := pv.objects[fileID][mesh].materials[group].ID()
				materialOverrideID := pv.getOverride(materialID)
				if materialOverrideID != pv.objects[fileID][mesh].materials[group].GetOverride() {
					mat, _ = loadMaterialInfo(pv.getResource, materialID)
					overrideLUTMaterial(mat, armorInfo, pv.selectedCamo)
					name := pv.objects[fileID][mesh].materials[group].Name()
					shown := pv.objects[fileID][mesh].materials[group].Shown()

					material := pv.ml.LoadMaterial(materialID, name, shown, mat, pv.objects[fileID][mesh].materials[group], fileID)
					override := materialID
					if pv.getOverride != nil {
						override = pv.getOverride(materialID)
					}
					material.SetOverride(override)
					pv.objects[fileID][mesh].materials[group] = material
				} else {
					mat, _ = loadMaterialInfo(pv.getResource, materialOverrideID)
					pv.ml.ReloadTextures(pv.objects[fileID][mesh].materials[group], mat)
				}
			}
		}
	}

	pv.ml.UpdateAssetGradingLut()
	if entityChanged {
		pv.refreshRockAssetGrading()
	}

	if err := pv.finalizeObjectMaterials(); err != nil {
		fmt.Printf("finalizing override materials: %v\n", err)
	}
}

// SetEntityInfo supplies the game color-grading context before a scene is
// loaded, so materials such as rocks can use the same biome palette as the game.
func (pv *UnitPreviewState) SetEntityInfo(entityInfo *entity.Entity) {
	if pv.ml == nil || !pv.ml.SetEntity(entityInfo) || len(pv.objects) == 0 {
		return
	}
	if err := pv.ml.UpdateAssetGradingLut(); err != nil {
		fmt.Printf("updating preview color grading LUT: %v\n", err)
		return
	}
	pv.refreshRockAssetGrading()
}

func (pv *UnitPreviewState) refreshRockAssetGrading() {
	for _, meshes := range pv.objects {
		for _, object := range meshes {
			for _, objectMaterial := range object.materials {
				pv.ml.RefreshAssetGrading(objectMaterial)
			}
		}
	}
}

// Keep numeric suffixes in object names: Hive Lord's body0/body1 are separate
// visible segments, not LOD numbers. Only explicit LODn suffixes are LODs.
var objectRegex = regexp.MustCompile("^(g_)?(\\w*?)_?(cull|shadow|rubble|rubble_shadow|debris|shadowmesh|c)?_?(LOD\\d)?$")

// These hashes aren't actually known, so I'm shimming them so previews work
var vistaMeshes = map[stingray.ThinHash]string{
	stingray.ThinHash{Value: 0xe199ef6a}: "g_slice_nne",
	stingray.ThinHash{Value: 0xc3a99d14}: "g_slice_ne",
	stingray.ThinHash{Value: 0x2673e969}: "g_slice_e",
	stingray.ThinHash{Value: 0x3eb222e4}: "g_slice_se",
	stingray.ThinHash{Value: 0x20996a53}: "g_slice_sse",
	stingray.ThinHash{Value: 0x19885480}: "g_slice_ssw",
	stingray.ThinHash{Value: 0x386d808c}: "g_slice_sw",
	stingray.ThinHash{Value: 0xec717099}: "g_slice_w",
	stingray.ThinHash{Value: 0x0157da28}: "g_slice_nw",
	stingray.ThinHash{Value: 0x4101fd70}: "g_slice_nnw",
}

func (pv *UnitPreviewState) loadMeshes(meshInfos []unit.MeshInfo, meshLayouts []unit.MeshLayout, gpuData []byte) (map[string]unit.Mesh, map[string]bool, error) {
	minLods := make(map[string]int)
	objects := make(map[string]map[string]int)
	for idx, info := range meshInfos {
		meshName := pv.lookupThinHash(info.Header.MeshName)
		vistaName, contains := vistaMeshes[info.Header.MeshName]
		if contains {
			meshName = vistaName
		}
		if strings.HasPrefix(meshName, "0x") {
			continue
		}
		object := objectRegex.FindStringSubmatch(meshName)
		if object == nil || object[2] == "" {
			continue
		}
		fmt.Printf("Found object:\n    name: %v\n    shadow/rubble: %v\n    index: %v\n    LOD: %v\n", object[2], object[3], object[4], object[4])
		// rubble/shadow/rubble_shadow
		objectName := object[2]
		if object[3] != "" {
			objectName += "_" + object[3]
		}
		if lod := strings.TrimPrefix(object[4], "LOD"); lod != "" {
			if level, err := strconv.Atoi(lod); err == nil {
				pv.maxLODLevel = max(pv.maxLODLevel, level)
			}
		}
		foundObject, ok := objects[objectName]
		if !ok {
			foundObject = make(map[string]int)
		}
		foundObject[object[4]] = idx
		objects[objectName] = foundObject
	}

	for object, lods := range objects {
		chosenLod, chosenIdx := chooseMeshLOD(lods, pv.lodLevel)
		if chosenLod == "" {
			chosenLod = "<base-mesh>"
		}
		fmt.Printf("choosing %v as min lod for object %v\n", chosenLod, object)
		minLods[object] = chosenIdx
	}

	meshesToLoad := make([]uint32, 0)
	shownDefault := make(map[string]bool)
	// if we don't have any idea what the lods are, just fall back to original behavior
	if len(minLods) == 0 {
		highestDetailIdx := -1
		highestDetailCount := -1
		for i, info := range meshInfos {
			for _, group := range info.Groups {
				if int(group.NumIndices) > highestDetailCount && info.Header.MeshType != unit.MeshTypeUnknown00 {
					highestDetailIdx = i
					highestDetailCount = int(group.NumIndices)
				}
			}
		}
		if highestDetailIdx == -1 {
			return nil, nil, fmt.Errorf("unable to find mesh to load")
		}
		meshesToLoad = append(meshesToLoad, uint32(highestDetailIdx))
	} else {
		for object, idx := range minLods {
			isVisibleRubble := strings.HasPrefix(object, "rubble") &&
				!strings.Contains(object, "shadow") &&
				!strings.Contains(object, "cull") &&
				!strings.Contains(object, "coll")
			if !strings.HasPrefix(object, "0x") &&
				(strings.Contains(object, "shadow") ||
					strings.HasPrefix(object, "c_") ||
					(!isVisibleRubble && strings.HasSuffix(object, "_c")) ||
					strings.Contains(object, "cull") ||
					strings.Contains(object, "coll") ||
					(!isVisibleRubble && strings.HasSuffix(object, "rubble") && !strings.Contains(object, "rock")) ||
					(!isVisibleRubble && strings.HasPrefix(object, "rubble") && !strings.Contains(object, "rock")) ||
					strings.HasSuffix(object, "debris") ||
					object == "ai_blocker" ||
					object == "v" ||
					object == "navigation") {
				shownDefault[object] = false
			}
			meshesToLoad = append(meshesToLoad, uint32(idx))
		}
		// A resolved collision or shadow name alone does not identify the
		// visible mesh. Some armor pieces have unnamed LODs and a named
		// culling mesh; select the largest unresolved mesh in that case.
		visible := false
		for name := range minLods {
			if shown, ok := shownDefault[name]; !ok || shown {
				visible = true
				break
			}
		}
		if !visible {
			best, bestCount := -1, -1
			for i, info := range meshInfos {
				if !strings.HasPrefix(pv.lookupThinHash(info.Header.MeshName), "0x") || info.Header.MeshType == unit.MeshTypeUnknown00 {
					continue
				}
				count := 0
				for _, group := range info.Groups {
					count += int(group.NumIndices)
				}
				if count > bestCount {
					best, bestCount = i, count
				}
			}
			if best >= 0 {
				minLods["unresolved highest-detail"] = best
				meshesToLoad = append(meshesToLoad, uint32(best))
			}
		}
	}

	meshes := make(map[string]unit.Mesh)
	{
		var err error
		meshMap, err := unit.LoadMeshes(bytes.NewReader(gpuData), meshInfos, meshLayouts, meshesToLoad)
		if err != nil {
			return nil, nil, err
		}
		if len(minLods) == 0 {
			meshes["highest-detail"] = meshMap[meshesToLoad[0]]
			shownDefault["highest-detail"] = true
		} else {
			for object, idx := range minLods {
				if _, contains := shownDefault[object]; !contains {
					shownDefault[object] = true
				}
				meshes[object] = meshMap[uint32(idx)]
			}
		}
	}
	return meshes, shownDefault, nil
}

func chooseMeshLOD(lods map[string]int, requested int) (string, int) {
	key := fmt.Sprintf("LOD%d", requested)
	if idx, ok := lods[key]; ok {
		return key, idx
	}
	bestDistance := int(^uint(0) >> 1)
	bestLevel := int(^uint(0) >> 1)
	bestKey := ""
	bestIdx := -1
	for candidate, idx := range lods {
		level := 0
		if candidate != "" {
			parsedLevel, err := strconv.Atoi(strings.TrimPrefix(candidate, "LOD"))
			if err != nil {
				continue
			}
			level = parsedLevel
		}
		distance := level - requested
		if distance < 0 {
			distance = -distance
		}
		preferExplicit := distance == bestDistance && level == bestLevel && candidate != "" && bestKey == ""
		if distance < bestDistance || distance == bestDistance && level < bestLevel || preferExplicit {
			bestDistance, bestLevel, bestKey, bestIdx = distance, level, candidate, idx
		}
	}
	return bestKey, bestIdx
}

func overrideLUTMaterial(mat *material.Material, armorInfo *datalib.UnitData, camo enum.CamouflageType) {
	if mat == nil || mat.Textures == nil || armorInfo == nil {
		return
	}
	material_lut := stingray.Sum("material_lut").Thin()
	pattern_lut := stingray.Sum("pattern_lut").Thin()
	cape_lut := stingray.Sum("cape_lut").Thin()
	cape_gradient := stingray.Sum("cape_gradient").Thin()
	cape_nac := stingray.Sum("cape_nac").Thin()
	id_masks_array := stingray.Sum("id_masks_array").Thin()
	decal_scalar_fields := stingray.Sum("decal_scalar_fields").Thin()
	base_data := stingray.Sum("base_data").Thin()
	decal_sheet := stingray.Sum("decal_sheet").Thin()
	if _, contains := mat.Textures[material_lut]; contains && armorInfo.MaterialLut.Value != 0 {
		mat.Textures[material_lut] = armorInfo.MaterialLut
		for _, camoflagePiece := range armorInfo.CamoflagePieces {
			if camo == enum.CamouflageType_None || camo == enum.CamouflageType_Default {
				break
			}
			if camo == camoflagePiece.CamoflageType {
				mat.Textures[material_lut] = camoflagePiece.MaterialLut
				break
			}
		}
	}
	if _, contains := mat.Textures[pattern_lut]; contains && armorInfo.PatternLut.Value != 0 {
		mat.Textures[pattern_lut] = armorInfo.PatternLut
	}
	if _, contains := mat.Textures[cape_lut]; contains && armorInfo.CapeLut.Value != 0 {
		mat.Textures[cape_lut] = armorInfo.CapeLut
	}
	if _, contains := mat.Textures[cape_gradient]; contains && armorInfo.CapeGradient.Value != 0 {
		mat.Textures[cape_gradient] = armorInfo.CapeGradient
	}
	if _, contains := mat.Textures[cape_nac]; contains && armorInfo.CapeNac.Value != 0 {
		mat.Textures[cape_nac] = armorInfo.CapeNac
	}
	if _, contains := mat.Textures[base_data]; contains && armorInfo.BaseData.Value != 0 {
		mat.Textures[base_data] = armorInfo.BaseData
	}
	if _, contains := mat.Textures[decal_sheet]; contains && armorInfo.DecalSheet.Value != 0 {
		mat.Textures[decal_sheet] = armorInfo.DecalSheet
	}

	// id masks and decal scalar fields are the same texture, just named differently for armors and capes
	if _, contains := mat.Textures[id_masks_array]; contains && armorInfo.DecalScalarFields.Value != 0 {
		mat.Textures[id_masks_array] = armorInfo.DecalScalarFields
	}
	if _, contains := mat.Textures[decal_scalar_fields]; contains && armorInfo.DecalScalarFields.Value != 0 {
		mat.Textures[decal_scalar_fields] = armorInfo.DecalScalarFields
	}
}

func (pv *UnitPreviewState) finalizeObjectMaterials() error {
	remainingUploads := 4
	fileIDs := slices.SortedFunc(maps.Keys(pv.objects), func(a, b stingray.FileID) int {
		return stingray.Hash.Cmp(a.Name, b.Name)
	})
	for _, objectHash := range fileIDs {
		for _, mesh := range slices.Sorted(maps.Keys(pv.objects[objectHash])) {
			for group := range pv.objects[objectHash][mesh].materials {
				if remainingUploads <= 0 {
					return nil
				}
				material := pv.objects[objectHash][mesh].materials[group]
				if err := pv.ml.FinalizeMaterialTexturesWithBudget(material, &remainingUploads); err != nil {
					return err
				}
				pv.objects[objectHash][mesh].materials[group] = material
			}
		}
	}
	return nil
}

func loadMaterialInfo(getResource GetResourceFunc, materialID stingray.FileID) (*material.Material, error) {
	if materialID.Name.Value == 0x0 {
		return nil, fmt.Errorf("nil material")
	}
	matData, ok, err := getResource(materialID, stingray.DataMain)
	if err != nil {
		return nil, fmt.Errorf("load material %v.material: %w", materialID.Name, err)
	}
	if !ok {
		return nil, fmt.Errorf("load material %v.material does not exist", materialID.Name)
	}
	return material.LoadMain(bytes.NewReader(matData))
}

func (pv *UnitPreviewState) getArmorInfo(name stingray.Hash) (armorInfo *datalib.UnitData) {
	selected := pv.getSelectedArchives()
	// The explicitly assembled kit must take precedence over archive filters,
	// especially when several capes share the same generic mesh.
	if name == stingray.Sum("content/fac_helldivers/capes/medium_cape") {
		for _, archive := range pv.armorContextArchives {
			set, ok := pv.armorSets[archive]
			if !ok || set.Type != datalib.KitCape {
				continue
			}
			if value, ok := set.UnitMetadata[name]; ok {
				return &value
			}
			if value, ok := set.UnitMetadata[stingray.Hash{}]; ok {
				return &value
			}
		}
	}
	for _, archive := range pv.armorContextArchives {
		set, ok := pv.armorSets[archive]
		if !ok {
			continue
		}
		value, contains := set.UnitMetadata[name]
		if !contains && set.Type == datalib.KitCape && name == stingray.Sum("content/fac_helldivers/capes/medium_cape") {
			value, contains = set.UnitMetadata[stingray.Hash{}]
		}
		if contains {
			return &value
		}
	}
	for _, set := range pv.armorSets {
		if !slices.Contains(selected, set.Archive) {
			continue
		}
		value, contains := set.UnitMetadata[name]
		if !contains && set.Type == datalib.KitCape && name == stingray.Sum("content/fac_helldivers/capes/medium_cape") {
			value, contains = set.UnitMetadata[stingray.Hash{}]
		}
		if !contains {
			continue
		}

		armorInfo = &value
	}
	return
}

// relatedArmorKits selects capes only when the game table gives a matching
// nonzero set ID. A shared ID may list multiple capes, so use the selected
// cape when applicable, otherwise pick the first consistently by archive.
func relatedArmorKits(all map[stingray.Hash]datalib.ArmorSet, selected datalib.ArmorSet) []datalib.ArmorSet {
	sets := make([]datalib.ArmorSet, 0)
	capes := make([]datalib.ArmorSet, 0)
	for _, set := range all {
		if set.SetId != selected.SetId || (selected.SetId == 0 && set.Id != selected.Id) {
			continue
		}
		if set.Type == datalib.KitCape {
			capes = append(capes, set)
		} else {
			sets = append(sets, set)
		}
	}
	slices.SortFunc(capes, func(a, b datalib.ArmorSet) int { return a.Archive.Cmp(b.Archive) })
	if len(capes) > 0 {
		chosen := capes[0]
		if selected.Type == datalib.KitCape {
			for _, cape := range capes {
				if cape.Id == selected.Id {
					chosen = cape
					break
				}
			}
		}
		sets = append(sets, chosen)
	}
	slices.SortFunc(sets, func(a, b datalib.ArmorSet) int { return a.Archive.Cmp(b.Archive) })
	return sets
}

func (pv *UnitPreviewState) removeRenderedObject(nodeId stingray.Hash) {
	node, contains := pv.nodes[nodeId]
	if !contains {
		return
	}
	delete(pv.nodes, nodeId)

	for _, hash := range node.children {
		if _, contains := pv.nodes[hash.Name]; contains {
			pv.removeRenderedObject(hash.Name)
		}
		// several rendered objects may map to the same unit resource, so check if its already been removed
		if _, contains := pv.objects[hash]; !contains {
			return
		}
		for name := range pv.objects[hash] {
			pv.objects[hash][name].deleteObjects(pv.ml)
			pv.boundingBoxes[hash][name].deleteObjects(pv.ml)
		}
		pv.skeletons[hash].deleteObjects(pv.ml)
		delete(pv.objects, hash)
		delete(pv.boundingBoxes, hash)
		delete(pv.skeletons, hash)
		delete(pv.objectsShown, hash)
		delete(pv.objectsSelected, hash)
		delete(pv.objectsShownDefault, hash)
		delete(pv.aabb, hash)
		delete(pv.aabbMat, hash)
		delete(pv.meshPositions, hash)
		delete(pv.meshNormals, hash)
		delete(pv.skeletonPositions, hash)
	}
}

func (pv *UnitPreviewState) Clear() {
	for hash := range pv.nodes {
		pv.removeRenderedObject(hash)
	}
	clear(pv.nodes)
	clear(pv.unitInfos)
	pv.mountedAttachmentsLoaded = false
	pv.mountedAttachmentNodes = nil
	pv.mountedAttachmentWarnings = nil
	pv.ml.DeleteAssetGradingLut()
}

// Calculate max zoom out distance
func (pv *UnitPreviewState) getMaxZoom(node unitPreviewNode, nodeId stingray.FileID, useSkeleton bool) (maxViewDistance float32) {
	// Get origin sphere around mesh
	var maxDistSqrFromOrigin float32
	modelTranslation := mgl32.Translate3D(pv.modelPos[0], pv.modelPos[1], pv.modelPos[2])
	var calcDistance func(unitPreviewNode, stingray.FileID, mgl32.Mat4, bool)
	calcDistance = func(curr unitPreviewNode, nodeId stingray.FileID, matrix mgl32.Mat4, useSkeleton bool) {
		if !curr.shown {
			return
		}
		sceneMatrix := pv.model.Mul4(modelTranslation).Mul4(matrix)
		for _, childHash := range curr.children {
			if child, contains := pv.nodes[childHash.Name]; contains && nodeId != childHash {
				calcDistance(child, childHash, matrix.Mul4(child.matrix), useSkeleton)
			}
			if _, contains := pv.objects[childHash]; contains {
				for name := range pv.objects[childHash] {
					if shown, exists := pv.objectsShown[childHash][name]; exists && !shown {
						continue
					}
					boundsMatrix := sceneMatrix.Mul4(pv.aabbMat[childHash][name])
					for _, p := range pv.getAABBVertices(childHash, name) {
						position := boundsMatrix.Mul4x1(p.Vec4(1.0))
						if position.W() != 0 {
							position = position.Mul(1 / position.W())
						}
						distanceSqr := position.Vec3().LenSqr()
						if !math.IsNaN(float64(distanceSqr)) && !math.IsInf(float64(distanceSqr), 0) {
							maxDistSqrFromOrigin = max(maxDistSqrFromOrigin, distanceSqr)
						}
					}
				}
				if skeletonVertices, contains := pv.skeletonPositions[childHash]; contains && useSkeleton {
					for _, sk := range skeletonVertices {
						position := sceneMatrix.Mul4x1(sk.Vec4(1.0))
						if position.W() != 0 {
							position = position.Mul(1 / position.W())
						}
						distanceSqr := position.Vec3().LenSqr()
						if !math.IsNaN(float64(distanceSqr)) && !math.IsInf(float64(distanceSqr), 0) {
							maxDistSqrFromOrigin = max(maxDistSqrFromOrigin, distanceSqr)
						}
					}
				}
			}
		}
	}
	calcDistance(node, nodeId, node.matrix, useSkeleton)

	maxDistFromOrigin := float32(math.Sqrt(float64(maxDistSqrFromOrigin)))

	// Calculate camera distance to fit vertical frustum into disk orthogonal
	// to view direction with radius of the sphere. Ideally, we'd want
	// to fit the sphere into the frustum, but the disk should be close
	// enough.
	// tan(vfov/2) = maxDistFromOrigin/viewDistance
	maxViewDistance = float32(float64(maxDistFromOrigin) / math.Tan(float64(pv.vfov/2)))

	// We want to be able to zoom out a bit further.
	maxViewDistance = max(0.001, maxViewDistance*2)
	return
}

func (pv *UnitPreviewState) getNodeUnitFileID(nodeId stingray.Hash) (stingray.FileID, bool) {
	node, contains := pv.nodes[nodeId]
	if !contains {
		return stingray.FileID{}, contains
	}
	// this is not a node that just has one unit
	if len(node.children) != 1 {
		return stingray.FileID{}, false
	}
	hash := node.children[0]
	_, contains = pv.objects[hash]
	for !contains {
		node, contains = pv.nodes[hash.Name]
		if !contains {
			break
		}
		// this is not a node that just has one unit
		if len(node.children) != 1 {
			return stingray.FileID{}, false
		}
		hash = node.children[0]
		_, contains = pv.objects[hash]
	}
	return hash, contains
}

func (pv *UnitPreviewState) LoadLevel(fileID stingray.Hash, mainData []byte, entityContext ...*entity.Entity) error {
	if len(entityContext) > 0 {
		pv.SetEntityInfo(entityContext[0])
	}
	if pv.nodes == nil {
		pv.nodes = make(map[stingray.Hash]unitPreviewNode)
	}

	pv.maxViewDistance = 0.001

	children := make([]stingray.FileID, 0)

	levelInfo, err := level.LoadLevel(bytes.NewReader(mainData), nil)
	if err != nil {
		return err
	}

	childrenRange := make(map[stingray.ThinHash][]stingray.FileID)
	for idx, unit := range levelInfo.Units {
		var collection *level.HashIndexRange
		for _, hashRange := range levelInfo.UnitHashIndexRange {
			if idx >= int(hashRange.End) || idx < int(hashRange.Start) || hashRange.Hash.Value == 0x0 {
				continue
			}
			collection = &hashRange
			if _, contains := childrenRange[collection.Hash]; !contains {
				childrenRange[collection.Hash] = make([]stingray.FileID, 0)
			}
		}
		childId := stingray.NewFileID(unit.UUID, stingray.Hash{})
		if collection == nil {
			children = append(children, childId)
		} else {
			childrenRange[collection.Hash] = append(childrenRange[collection.Hash], childId)
		}
		unitID := stingray.NewFileID(unit.Hash, UNIT)
		pv.nodes[unit.UUID] = unitPreviewNode{
			name:     pv.lookupHash(unit.Name),
			shown:    true,
			matrix:   unit.Matrix(),
			children: []stingray.FileID{unitID},
			parent:   fileID,
		}
		if _, contains := pv.objects[unitID]; contains {
			pv.setNodeSceneOverlay(unit.UUID, unitID)
			continue
		}
		unitMainData, exists, err := pv.getResource(unitID, stingray.DataMain)
		if err != nil {
			return fmt.Errorf("loading %v.unit data in %v.level: %v", pv.lookupHash(unit.Hash), pv.lookupHash(fileID), err)
		}
		if !exists {
			return fmt.Errorf("%v.unit in %v.level does not exist", pv.lookupHash(unit.Hash), pv.lookupHash(fileID))
		}

		// Some units won't have GPU data but will have terrain or geometry group info
		unitGpuData, _, _ := pv.getResource(unitID, stingray.DataGPU)

		err = pv.loadUnit(unitID, unitMainData, unitGpuData)
		if err != nil {
			return fmt.Errorf("loading %v.unit in %v.level for rendering: %v", pv.lookupHash(unit.Hash), pv.lookupHash(fileID), err)
		}
		pv.setNodeSceneOverlay(unit.UUID, unitID)
	}

	for _, speedtree := range levelInfo.Speedtrees {
		speedtreeID := stingray.NewFileID(speedtree.Hash, SPEEDTREE)
		if _, contains := pv.objects[speedtreeID]; !contains {
			speedtreeMainData, exists, err := pv.getResource(speedtreeID, stingray.DataMain)
			if err != nil {
				return fmt.Errorf("loading %v.speedtree data in %v.level: %v", pv.lookupHash(speedtree.Hash), pv.lookupHash(fileID), err)
			}
			if !exists {
				return fmt.Errorf("%v.speedtree in %v.level does not exist", pv.lookupHash(speedtree.Hash), pv.lookupHash(fileID))
			}

			// Some units won't have GPU data but will have terrain or geometry group info
			speedtreeGpuData, _, _ := pv.getResource(speedtreeID, stingray.DataGPU)

			err = pv.loadSpeedtree(speedtreeID, speedtreeMainData, speedtreeGpuData)
			if err != nil {
				return fmt.Errorf("loading %v.speedtree in %v.level for rendering: %v", pv.lookupHash(speedtree.Hash), pv.lookupHash(fileID), err)
			}
		}
		for idx, transform := range speedtree.Transforms {
			var collection *level.HashIndexRange
			for _, hashRange := range speedtree.Layers {
				if idx >= int(hashRange.End) || idx < int(hashRange.Start) || hashRange.Hash.Value == 0x0 {
					continue
				}
				collection = &hashRange
				if _, contains := childrenRange[collection.Hash]; !contains {
					childrenRange[collection.Hash] = make([]stingray.FileID, 0)
				}
			}
			nodeHash := stingray.Sum(pv.lookupHash(speedtree.Hash) + fmt.Sprintf(" instance %v", idx))
			childId := stingray.NewFileID(nodeHash, stingray.Hash{})
			if collection == nil {
				children = append(children, childId)
			} else {
				childrenRange[collection.Hash] = append(childrenRange[collection.Hash], childId)
			}
			quat := mgl32.QuatSlerp(transform.MinRotation.Quat(), transform.MaxRotation.Quat(), 0.5)
			speedtreeTransform := stingray.Transform{
				PositionVec: transform.Position.Vec3(),
				RotationVec: quat.V.Vec4(quat.W),
				ScaleVec:    mgl32.Vec3{1.0, 1.0, 1.0},
			}
			pv.nodes[childId.Name] = unitPreviewNode{
				name:     pv.lookupHash(speedtree.Hash) + fmt.Sprintf(" instance %v", idx),
				shown:    true,
				matrix:   speedtreeTransform.Matrix(),
				children: []stingray.FileID{speedtreeID},
				parent:   fileID,
			}
		}
	}

	for idx, prefab := range levelInfo.Prefabs {
		var collection *level.HashIndexRange
		for _, hashRange := range levelInfo.PrefabHashIndexRange {
			if idx >= int(hashRange.End) || idx < int(hashRange.Start) || hashRange.Hash.Value == 0x0 {
				continue
			}
			collection = &hashRange
			if _, contains := childrenRange[collection.Hash]; !contains {
				childrenRange[collection.Hash] = make([]stingray.FileID, 0)
			}
		}
		childId := stingray.NewFileID(prefab.Name, stingray.Hash{})
		if collection == nil {
			children = append(children, childId)
		} else {
			childrenRange[collection.Hash] = append(childrenRange[collection.Hash], childId)
		}
		if _, contains := pv.nodes[prefab.Name]; contains {
			continue
		}
		prefabMainData, exists, err := pv.getResource(stingray.NewFileID(prefab.Path, stingray.Sum("prefab")), stingray.DataMain)
		if err != nil {
			return fmt.Errorf("loading %v.prefab data in %v.level: %v", pv.lookupHash(prefab.Path), pv.lookupHash(fileID), err)
		}
		if !exists {
			return fmt.Errorf("%v.prefab in %v.level does not exist", pv.lookupHash(prefab.Path), pv.lookupHash(fileID))
		}

		prefabId := stingray.NewFileID(prefab.Path, PREFAB)
		nestedChildren, err := pv.loadPrefab(prefabId, prefabMainData)
		if err != nil {
			return fmt.Errorf("loading %v.prefab in %v.level for rendering: %v", pv.lookupHash(prefab.Path), pv.lookupHash(fileID), err)
		}
		pv.nodes[prefab.Name] = unitPreviewNode{
			name:     pv.lookupHash(prefab.Name),
			shown:    true,
			matrix:   prefab.Matrix(),
			children: nestedChildren,
			parent:   fileID,
		}
	}

	for idx, prefab := range levelInfo.EmbeddedPrefabs {
		var collection *level.HashIndexRange
		for _, hashRange := range levelInfo.PrefabHashIndexRange {
			if idx >= int(hashRange.End) || idx < int(hashRange.Start) || hashRange.Hash.Value == 0x0 {
				continue
			}
			collection = &hashRange
			if _, contains := childrenRange[collection.Hash]; !contains {
				childrenRange[collection.Hash] = make([]stingray.FileID, 0)
			}
		}
		childId := stingray.NewFileID(prefab.EmbeddedPrefabTransform.Hash, stingray.Hash{})
		if collection == nil {
			children = append(children, childId)
		} else {
			childrenRange[collection.Hash] = append(childrenRange[collection.Hash], childId)
		}
		if _, contains := pv.nodes[prefab.EmbeddedPrefabTransform.Hash]; contains {
			continue
		}
		prefabId := stingray.NewFileID(prefab.EmbeddedPrefabTransform.Hash, PREFAB)
		nestedChildren, err := pv.loadPrefabInfo(prefabId, &prefab.Prefab)
		if err != nil {
			return fmt.Errorf("loading embedded %v.prefab in %v.level for rendering: %v", pv.lookupHash(prefab.EmbeddedPrefabTransform.Hash), pv.lookupHash(fileID), err)
		}
		pv.nodes[prefab.EmbeddedPrefabTransform.Hash] = unitPreviewNode{
			name:     pv.lookupHash(prefab.EmbeddedPrefabTransform.Hash),
			shown:    true,
			matrix:   prefab.Matrix(),
			children: nestedChildren,
			parent:   fileID,
		}
	}

	for collectionHash, collectionChildren := range childrenRange {
		name := pv.lookupThinHash(collectionHash)
		collectionId := stingray.NewFileID(stingray.Sum(name), stingray.Sum("collection"))
		if strings.HasPrefix(name, "0x") {
			hash := stingray.Hash{
				Value: uint64(collectionHash.Value) << 32,
			}
			collectionId = stingray.NewFileID(hash, stingray.Sum("collection"))
		}
		pv.nodes[collectionId.Name] = unitPreviewNode{
			name:     name,
			shown:    true,
			matrix:   mgl32.Ident4(),
			children: collectionChildren,
			parent:   fileID,
		}
		children = append(children, collectionId)
	}

	pv.root = unitPreviewNode{
		name:     pv.lookupHash(fileID),
		shown:    true,
		matrix:   mgl32.Ident4(),
		children: children,
		parent:   stingray.Hash{},
	}
	pv.rootHash = stingray.NewFileID(fileID, LEVEL)
	pv.maxViewDistance = pv.getMaxZoom(pv.root, pv.rootHash, false)

	if err != nil {
		return err
	}
	return pv.finalizeObjectMaterials()
}

func (pv *UnitPreviewState) LoadPrefab(fileID stingray.Hash, mainData []byte, entityInfo *entity.Entity) error {
	if pv.nodes == nil {
		pv.nodes = make(map[stingray.Hash]unitPreviewNode)
	}
	pv.ml.SetEntity(entityInfo)

	pv.maxViewDistance = 0.001

	prefabID := stingray.NewFileID(fileID, PREFAB)

	children, err := pv.loadPrefab(prefabID, mainData)
	pv.root = unitPreviewNode{
		name:     pv.lookupHash(fileID),
		shown:    true,
		matrix:   mgl32.Ident4(),
		children: children,
		parent:   stingray.Hash{},
	}
	pv.rootHash = stingray.NewFileID(fileID, PREFAB)
	pv.maxViewDistance = pv.getMaxZoom(pv.root, pv.rootHash, false)

	if err != nil {
		return err
	}
	return pv.finalizeObjectMaterials()
}

func (pv *UnitPreviewState) loadPrefab(fileID stingray.FileID, mainData []byte) ([]stingray.FileID, error) {
	info, err := prefab.Load(bytes.NewReader(mainData))
	if err != nil {
		return []stingray.FileID{}, err
	}
	return pv.loadPrefabInfo(fileID, info)
}

func (pv *UnitPreviewState) loadPrefabInfo(fileID stingray.FileID, info *prefab.Prefab) ([]stingray.FileID, error) {
	contents := make([]stingray.FileID, 0)
	for _, unit := range info.Units {
		var err error
		unitID := stingray.NewFileID(unit.Hash, UNIT)
		nodeID := stingray.NewFileID(unit.UUID, stingray.Hash{})
		if _, contains := pv.objects[unitID]; contains {
			contents = append(contents, nodeID)
			pv.nodes[unit.UUID] = unitPreviewNode{
				name:     pv.lookupHash(unit.Name),
				shown:    true,
				matrix:   unit.Matrix(),
				children: []stingray.FileID{unitID},
				parent:   fileID.Name,
			}
			pv.setNodeSceneOverlay(unit.UUID, unitID)
			continue
		}
		unitMainData, exists, err := pv.getResource(unitID, stingray.DataMain)
		if err != nil {
			return contents, fmt.Errorf("loading %v.unit data in %v.prefab: %v", pv.lookupHash(unit.Hash), pv.lookupHash(fileID.Name), err)
		}
		if !exists {
			return contents, fmt.Errorf("%v.unit in %v.prefab does not exist", pv.lookupHash(unit.Hash), pv.lookupHash(fileID.Name))
		}

		// Some units won't have GPU data but will have terrain or geometry group info
		unitGpuData, _, _ := pv.getResource(unitID, stingray.DataGPU)

		err = pv.loadUnit(unitID, unitMainData, unitGpuData)
		if err != nil {
			return contents, fmt.Errorf("loading %v.unit in %v.prefab for rendering: %v", pv.lookupHash(unit.Hash), pv.lookupHash(fileID.Name), err)
		}
		contents = append(contents, nodeID)
		pv.nodes[unit.UUID] = unitPreviewNode{
			name:     pv.lookupHash(unit.Name),
			shown:    true,
			matrix:   unit.Matrix(),
			children: []stingray.FileID{unitID},
			parent:   fileID.Name,
		}
		pv.setNodeSceneOverlay(unit.UUID, unitID)
	}

	for _, prefab := range info.NestedPrefabs {
		var err error
		var nestedChildren []stingray.FileID
		nodeID := stingray.NewFileID(prefab.Name, stingray.Hash{})
		prefabID := stingray.NewFileID(prefab.Path, PREFAB)
		if _, contains := pv.nodes[prefab.Name]; contains {
			contents = append(contents, nodeID)
			continue
		}
		prefabMainData, exists, err := pv.getResource(prefabID, stingray.DataMain)
		if err != nil {
			return contents, fmt.Errorf("loading %v.prefab data in %v.prefab: %v", pv.lookupHash(prefab.Path), pv.lookupHash(fileID.Name), err)
		}
		if !exists {
			return contents, fmt.Errorf("%v.prefab in %v.prefab does not exist", pv.lookupHash(prefab.Path), pv.lookupHash(fileID.Name))
		}

		nestedChildren, err = pv.loadPrefab(prefabID, prefabMainData)
		if err != nil {
			return contents, fmt.Errorf("loading %v.unit in %v.prefab for rendering: %v", pv.lookupHash(prefab.Path), pv.lookupHash(fileID.Name), err)
		}
		contents = append(contents, nodeID)
		pv.nodes[prefab.Name] = unitPreviewNode{
			name:     pv.lookupHash(prefab.Name),
			shown:    true,
			matrix:   prefab.Matrix(),
			children: nestedChildren,
			parent:   fileID.Name,
		}
	}

	return contents, nil
}

func (pv *UnitPreviewState) LoadUnit(fileID stingray.Hash, mainData, gpuData []byte, entityContext ...*entity.Entity) error {
	if len(entityContext) > 0 {
		pv.SetEntityInfo(entityContext[0])
	}
	pv.lodLevel = 0
	pv.maxLODLevel = 0
	unitID := stingray.NewFileID(fileID, UNIT)
	if pv.damageOverlayUnit != unitID {
		pv.damageOverlayBoxes = nil
		pv.damageOverlayVolumes = nil
		pv.damageOverlayUnit = stingray.FileID{}
		pv.damageOverlayLoaded = false
		pv.damageOverlayError = ""
		pv.rootHealthComponent = nil
		pv.rootHealthLoaded = false
		pv.rootHealthLoadError = ""
	}
	pv.mountedAttachmentsLoaded = false
	pv.mountedAttachmentNodes = nil
	pv.mountedAttachmentWarnings = nil
	if pv.nodes == nil {
		pv.nodes = make(map[stingray.Hash]unitPreviewNode)
	}

	pv.root = unitPreviewNode{
		name:     pv.lookupHash(fileID),
		shown:    true,
		matrix:   mgl32.Ident4(),
		children: []stingray.FileID{unitID},
		parent:   stingray.Hash{},
	}
	pv.rootHash = unitID
	pv.nodes[fileID] = pv.root

	if err := pv.loadUnit(unitID, mainData, gpuData); err != nil {
		return err
	}
	if pv.showPhysicsHitboxes || pv.showDamageBoxes || pv.showDamageReadout {
		if err := pv.loadDamageOverlayData(); err != nil {
			pv.damageOverlayError = err.Error()
		}
	}
	if pv.mountedAttachmentsEnabled {
		if err := pv.loadMountedAttachments(); err != nil {
			pv.mountedAttachmentWarnings = append(pv.mountedAttachmentWarnings, err.Error())
			fmt.Printf("[warning] loading mounted attachments for %v: %v\n", pv.lookupHash(fileID), err)
		}
		pv.mountedAttachmentsLoaded = true
	}

	if err := pv.finalizeObjectMaterials(); err != nil {
		return err
	}

	pv.maxViewDistance = pv.getMaxZoom(pv.root, pv.rootHash, len(pv.aabb[unitID]) == 0)

	return nil
}

func (pv *UnitPreviewState) SetLOD(level int) error {
	level = min(max(level, 0), pv.maxLODLevel)
	if level == pv.lodLevel {
		return nil
	}
	pv.lodLevel = level
	for fileID := range pv.objects {
		if fileID.Type != UNIT {
			continue
		}
		mainData, exists, err := pv.getResource(fileID, stingray.DataMain)
		if err != nil {
			return fmt.Errorf("read unit for LOD change: %w", err)
		}
		if !exists {
			return fmt.Errorf("unit %v disappeared while changing LOD", fileID)
		}
		gpuData, _, err := pv.getResource(fileID, stingray.DataGPU)
		if err != nil {
			return fmt.Errorf("read unit GPU data for LOD change: %w", err)
		}
		if err := pv.loadUnit(fileID, mainData, gpuData); err != nil {
			return fmt.Errorf("reload unit at LOD %d: %w", level, err)
		}
	}
	if err := pv.finalizeObjectMaterials(); err != nil {
		return fmt.Errorf("finalize materials after LOD change: %w", err)
	}
	pv.maxViewDistance = pv.getMaxZoom(pv.root, pv.rootHash, len(pv.aabb[pv.rootHash]) == 0)
	return nil
}

func materialShown(slot string) bool {
	normalized := strings.ToLower(slot)
	if strings.Contains(normalized, "gibs") || normalized == "transparent" {
		return false
	}
	// These are auxiliary surfaces used by the game for shadows, culling, or
	// collision. They are useful in metadata but are not part of the visible model.
	for _, part := range strings.FieldsFunc(normalized, func(r rune) bool {
		return r == '_' || r == '-' || r == '/' || r == '.' || r == ' '
	}) {
		if part == "shadow" || part == "collision" || part == "culling" {
			return false
		}
	}
	return true
}

func (pv *UnitPreviewState) loadUnit(fileID stingray.FileID, mainData, gpuData []byte) error {
	var err error
	var info *unit.Info
	var meshes map[string]unit.Mesh
	var defaultShown map[string]bool
	if pv.isHelldivers1 {
		info, meshes, err = unit.LoadHelldivers1(mainData)
		if err != nil {
			return err
		}
		defaultShown = make(map[string]bool, len(meshes))
		for name := range meshes {
			defaultShown[name] = true
		}
	} else {
		info, err = unit.LoadInfo(bytes.NewReader(mainData))
		if err != nil {
			return err
		}
		if pv.unitInfos == nil {
			pv.unitInfos = make(map[stingray.FileID]*unit.Info)
		}
		pv.unitInfos[fileID] = info
		if fileID == pv.rootHash && fileID.Type == UNIT {
			pv.rootUnitInfo = info
		}
		if fileID == pv.rootHash && pv.animationEnabled {
			pv.loadLinkedAnimations(info)
		}
	}

	setModelPos := len(pv.objects) == 0
	if setModelPos {
		pv.modelPos = mgl32.Vec4{0, 0, 0, 1}
	}

	if pv.objects == nil {
		pv.objects = make(map[stingray.FileID]map[string]unitPreviewObject)
		pv.objectOverrides = make(map[stingray.FileID]stingray.FileID)
		pv.boundingBoxes = make(map[stingray.FileID]map[string]unitPreviewObject)
		pv.objectsShown = make(map[stingray.FileID]map[string]bool)
		pv.objectsSelected = make(map[stingray.FileID]map[string]bool)
		pv.objectsShownDefault = make(map[stingray.FileID]map[string]bool)
		pv.aabb = make(map[stingray.FileID]map[string][2]mgl32.Vec3)
		pv.aabbMat = make(map[stingray.FileID]map[string]mgl32.Mat4, 0)
		pv.meshPositions = make(map[stingray.FileID]map[string][][3]float32)
		pv.meshNormals = make(map[stingray.FileID]map[string][][3]float32)
		pv.skeletonPositions = make(map[stingray.FileID][]mgl32.Vec3)
		pv.skeletonBoneIndices = make(map[stingray.FileID][]int)
		pv.visibilityMasks = make(map[stingray.FileID]*unitVisibilityMasks)
	}
	if pv.visibilityMasks == nil {
		pv.visibilityMasks = make(map[stingray.FileID]*unitVisibilityMasks)
	}

	if pv.isHelldivers1 {
		// HD1 unit records contain their own vertex and index buffers.
	} else if len(info.MeshInfos) > 0 && info.GeometryGroup.Value == 0x0 {
		meshes, defaultShown, err = pv.loadMeshes(info.MeshInfos, info.MeshLayouts, gpuData)
		if err != nil {
			return err
		}
	} else if len(info.TerrainInfos) > 0 {
		mesh, err := unit.LoadTerrain(info.TerrainInfos[0])
		if err != nil {
			return err
		}
		terrainConversionMatrix := mgl32.Rotate3DX(math.Pi).Mat4().Mul4(stingray.ToGLTFMatrix)
		for i := range mesh.Positions {
			mesh.Positions[i] = terrainConversionMatrix.Mul4x1(mgl32.Vec3(mesh.Positions[i]).Vec4(1)).Vec3()
			mesh.Normals[i] = terrainConversionMatrix.Mul4x1(mgl32.Vec3(mesh.Normals[i]).Vec4(1)).Vec3()
			mesh.Tangents[i] = terrainConversionMatrix.Mul4x1(mgl32.Vec4(mesh.Tangents[i]))
			mesh.Bitangents[i] = terrainConversionMatrix.Mul4x1(mgl32.Vec3(mesh.Bitangents[i]).Vec4(1)).Vec3()
		}
		meshes = make(map[string]unit.Mesh)
		meshes["terrain"] = mesh
		info.Materials[stingray.Sum("terrain").Thin()] = fileID.Name
		defaultShown = map[string]bool{"terrain": !pv.hideLevelGround}
	} else if info.GeometryGroup.Value != 0x0 && info.GeometryGroup.Value != 0xfce38c71ca9c2166 {
		// 0xfce38c71ca9c2166 seems to be a "dev assets" geometry group that isn't included in the
		// prod release of the game, so in our POV its the same as a null geometry group
		geoID := stingray.NewFileID(info.GeometryGroup, stingray.Sum("geometry_group"))
		geoMain, exists, err := pv.getResource(geoID, stingray.DataMain)
		if !exists {
			return fmt.Errorf("%v.geometry_group does not exist", info.GeometryGroup.String())
		} else if err != nil {
			return fmt.Errorf("failed to load %v.geometry_group: %v", info.GeometryGroup.String(), err)
		}
		geoGroup, err := geometrygroup.LoadGeometryGroup(bytes.NewReader(geoMain))
		if err != nil {
			return fmt.Errorf("failed to parse %v.geometry_group: %v", info.GeometryGroup.String(), err)
		}
		geoInfo, ok := geoGroup.MeshInfos[fileID.Name]
		if !ok {
			return fmt.Errorf("%v.geometry_group does not contain %v.unit", info.GeometryGroup.String(), fileID)
		}
		meshInfos := make([]unit.MeshInfo, 0)
		for idx, header := range geoInfo.MeshHeaders {
			meshName := geoInfo.MeshNames[idx]
			var aabb unit.AABB
			var transformIdx uint32
			for _, meshInfo := range info.MeshInfos {
				if meshInfo.Header.MeshName == meshName {
					aabb = meshInfo.Header.AABB
					transformIdx = meshInfo.Header.AABBTransformIndex
					break
				}
			}
			meshInfos = append(meshInfos, unit.MeshInfo{
				Groups: header.Groups,
				Header: unit.MeshHeader{
					MeshName:           meshName,
					AABB:               aabb,
					AABBTransformIndex: transformIdx,
					MeshType:           unit.MeshTypeUnknown01,
					LayoutIdx:          int32(header.MeshLayoutIndex),
				},
				Materials: header.Materials,
			})
		}
		geoGPU, exists, err := pv.getResource(geoID, stingray.DataGPU)
		if !exists {
			return fmt.Errorf("%v.geometry_group missing gpu data", info.GeometryGroup.String())
		} else if err != nil {
			return fmt.Errorf("failed to load %v.geometry_group gpu data: %v", info.GeometryGroup.String(), err)
		}
		meshes, defaultShown, err = pv.loadMeshes(meshInfos, geoGroup.MeshLayouts, geoGPU)
		if err != nil {
			return err
		}
	}

	var contains bool
	if _, contains = pv.objects[fileID]; contains {
		for name := range pv.objects[fileID] {
			pv.objects[fileID][name].deleteObjects(pv.ml)
			pv.boundingBoxes[fileID][name].deleteObjects(pv.ml)
		}
		pv.skeletons[fileID].deleteObjects(pv.ml)
	}

	if !contains {
		pv.objectsShownDefault[fileID] = defaultShown
		pv.objectsShown[fileID] = maps.Clone(defaultShown)
		pv.objectsSelected[fileID] = maps.Clone(defaultShown)
		pv.objectOverrides[fileID] = fileID
	} else {
		// If a user has hidden a mesh, don't unhide it when we update the asset overrides
		// but also make sure the correct mesh names are being used
		for name := range pv.objectsShown[fileID] {
			if _, contains := defaultShown[name]; !contains {
				delete(pv.objectsShownDefault[fileID], name)
				delete(pv.objectsShown[fileID], name)
				delete(pv.objectsSelected[fileID], name)
				continue
			}
		}
		for name := range defaultShown {
			if _, contains := pv.objectsShown[fileID][name]; !contains {
				pv.objectsShownDefault[fileID][name] = defaultShown[name]
				pv.objectsShown[fileID][name] = defaultShown[name]
				pv.objectsSelected[fileID][name] = defaultShown[name]
			}
		}
	}
	pv.objects[fileID] = make(map[string]unitPreviewObject)
	pv.meshPositions[fileID] = make(map[string][][3]float32)
	pv.meshNormals[fileID] = make(map[string][][3]float32)
	pv.aabb[fileID] = make(map[string][2]mgl32.Vec3)
	pv.aabbMat[fileID] = make(map[string]mgl32.Mat4, 0)
	pv.boundingBoxes[fileID] = make(map[string]unitPreviewObject)
	for name, mesh := range meshes {
		pv.aabb[fileID][name] = [2]mgl32.Vec3{mesh.Info.Header.AABB.Min, mesh.Info.Header.AABB.Max}
		pv.aabbMat[fileID][name] = info.Bones[mesh.Info.Header.AABBTransformIndex].Matrix

		if len(mesh.Positions) == 0 {
			// gui logger warning probably
			fmt.Printf("mesh %v contains no positions\n", name)
			continue
		}
		if len(mesh.Normals) == 0 {
			// gui logger warning probably
			fmt.Printf("mesh %v contains no normals\n", name)
			continue
		}
		if len(mesh.UVCoords) == 0 || len(mesh.UVCoords[0]) == 0 {
			// gui logger warning probably
			fmt.Printf("mesh %v contains no UV coordinates\n", name)
			continue
		}

		object := unitPreviewObject{boneIndex: int(mesh.Info.Header.TransformIdx), skeletonMapIndex: -1}
		object.matrix = info.Bones[mesh.Info.Header.TransformIdx].Matrix
		object.bindMatrix = object.matrix
		if pv.animationEnabled && (fileID == pv.rootHash || pv.armorAnimationMode) && mesh.Info.Header.SkeletonMapIdx >= 0 &&
			int(mesh.Info.Header.SkeletonMapIdx) < len(info.SkeletonMaps) && len(mesh.BoneIndices) > 0 &&
			len(mesh.BoneIndices[0]) == len(mesh.Positions) && len(mesh.BoneWeights) == len(mesh.Positions) {
			object.skeletonMapIndex = int(mesh.Info.Header.SkeletonMapIdx)
			object.basePositions = mesh.Positions
			object.baseNormals = mesh.Normals
			object.baseBoneIndices = remapMeshBoneIndices(mesh.BoneIndices[0], mesh.Info.Groups, &info.SkeletonMaps[object.skeletonMapIndex])
			object.baseBoneWeights = mesh.BoneWeights
			object.normalsOffset = len(mesh.Positions) * 3 * 4
		}
		object.name = fileID.Name
		wireframe := preview_mats.NewWireframeMaterial()
		err = wireframe.Generate()
		if err != nil {
			return err
		}
		object.wireframe = wireframe

		normalVis := preview_mats.NewNormalVisMaterial()
		err = normalVis.Generate()
		if err != nil {
			return err
		}
		object.normalVis = normalVis

		// Create index buffers
		{
			object.genObjects(true, 0)
			object.ibos = make([]uint32, len(mesh.Indices))
			if len(object.ibos) > 0 {
				gl.GenBuffers(int32(len(object.ibos)), &object.ibos[0])
			}

			for idx := range object.materials {
				// release textures and delete shaders from old materials
				pv.ml.DeleteMaterial(object.materials[idx])
			}
			armorInfo := pv.getArmorInfo(object.name)
			object.materials = make([]preview_mats.Material, 0, len(mesh.Indices))
			for group := range mesh.Info.Groups {
				idx := mesh.Info.Groups[group].MaterialIdx
				matID := mesh.Info.Materials[idx]
				matFileName, ok := info.Materials[matID]
				if !ok {
					continue
					//return fmt.Errorf("load material: id %v not found", matID.String())
				}
				name := pv.lookupThinHash(matID)
				if materialName, ok := info.MaterialNames[matID]; ok {
					name = materialName
				}
				materialID := stingray.NewFileID(matFileName, MATERIAL)

				var mat *material.Material
				var err error
				if pv.isHelldivers1 {
					if pv.resolveHD1Material != nil {
						resolvedID, materialData, exists, resolveErr := pv.resolveHD1Material(fileID, matID)
						if resolveErr != nil {
							err = resolveErr
						} else if exists {
							materialID = resolvedID
							aliases := make([]stingray.ThinHash, 0, len(info.MaterialNames))
							for alias := range info.MaterialNames {
								aliases = append(aliases, alias)
							}
							mat, err = material.LoadHelldivers1Main(materialData, matID, aliases, func(textureName stingray.Hash) bool {
								textureData, textureExists, textureErr := pv.getResource(stingray.NewFileID(textureName, stingray.Sum("texture")), stingray.DataMain)
								return textureErr == nil && textureExists && len(textureData) >= 4 && string(textureData[:4]) == "DDS "
							})
						}
					}
				} else {
					mat, err = loadMaterialInfo(pv.getResource, materialID)
				}
				if err != nil {
					fmt.Printf("[warning] loading material %v: %v\n", pv.lookupHash(materialID.Name), err)
				}
				// does not change any state if the material or armor info is nil
				//     or the material doesn't contain the correct textures
				overrideLUTMaterial(mat, armorInfo, pv.selectedCamo)

				material := pv.ml.LoadMaterial(materialID, name, materialShown(name), mat, nil, fileID)
				override := materialID
				if pv.getOverride != nil {
					override = pv.getOverride(materialID)
				}
				material.SetOverride(override)
				object.materials = append(object.materials, material)
			}
		}

		if len(mesh.Positions) != len(mesh.UVCoords[0]) {
			return errors.New("expected positions and UVs to have the same length")
		}

		object.numVertices = int32(len(mesh.Positions))

		// Upload object data
		{
			gl.BindVertexArray(object.vao)

			positionsSize := len(mesh.Positions) * 3 * 4
			normalsSize := len(mesh.Normals) * 3 * 4
			uvsSize := len(mesh.UVCoords[0]) * 2 * 4
			tangentsSize := len(mesh.Tangents) * 4 * 4
			bitangentsSize := len(mesh.Bitangents) * 3 * 4

			gl.BindBuffer(gl.ARRAY_BUFFER, object.vbo)
			gl.BufferData(gl.ARRAY_BUFFER, positionsSize+normalsSize+uvsSize*min(len(mesh.UVCoords), 3)+tangentsSize+bitangentsSize, nil, gl.STATIC_DRAW)
			offset := 0
			//
			gl.BufferSubData(gl.ARRAY_BUFFER, offset, positionsSize, gl.Ptr(mesh.Positions))
			gl.VertexAttribPointerWithOffset(0, 3, gl.FLOAT, false, 3*4, uintptr(offset))
			gl.EnableVertexAttribArray(0)
			offset += positionsSize
			//
			gl.BufferSubData(gl.ARRAY_BUFFER, offset, normalsSize, gl.Ptr(mesh.Normals))
			gl.VertexAttribPointerWithOffset(1, 3, gl.FLOAT, true, 3*4, uintptr(offset))
			gl.EnableVertexAttribArray(1)
			offset += normalsSize
			//
			gl.BufferSubData(gl.ARRAY_BUFFER, offset, tangentsSize, gl.Ptr(mesh.Tangents))
			gl.VertexAttribPointerWithOffset(2, 3, gl.FLOAT, true, 4*4, uintptr(offset))
			gl.EnableVertexAttribArray(2)
			offset += tangentsSize
			//
			gl.BufferSubData(gl.ARRAY_BUFFER, offset, bitangentsSize, gl.Ptr(mesh.Bitangents))
			gl.VertexAttribPointerWithOffset(3, 3, gl.FLOAT, true, 3*4, uintptr(offset))
			gl.EnableVertexAttribArray(3)
			offset += bitangentsSize
			//
			for layer, uvcoords := range mesh.UVCoords[:min(len(mesh.UVCoords), 3)] {
				index := uint32(4 + layer)
				uvsSize := len(uvcoords) * 2 * 4
				fmt.Printf("size %v offset %v index %v\n", uvsSize, offset, index)
				gl.BufferSubData(gl.ARRAY_BUFFER, offset, uvsSize, gl.Ptr(uvcoords))
				gl.VertexAttribPointerWithOffset(index, 2, gl.FLOAT, false, 2*4, uintptr(offset))
				gl.EnableVertexAttribArray(index)
				offset += uvsSize
			}
			if object.skeletonMapIndex >= 0 {
				// Bone data is static per mesh; leave per-frame skinning to the
				// vertex shader so large creatures do not stall the UI thread.
				skinMap := &info.SkeletonMaps[object.skeletonMapIndex]
				boneAttributes := make([][8]float32, len(object.baseBoneIndices))
				for vertex, indices := range object.baseBoneIndices {
					for influence := range 4 {
						localBone := int(indices[influence])
						boneAttributes[vertex][influence] = float32(localBone)
						weight := object.baseBoneWeights[vertex][influence]
						if localBone >= len(skinMap.BoneIndices) {
							weight = 0
						}
						boneAttributes[vertex][influence+4] = weight
					}
				}
				gl.GenBuffers(1, &object.animAttribVBO)
				gl.BindBuffer(gl.ARRAY_BUFFER, object.animAttribVBO)
				gl.BufferData(gl.ARRAY_BUFFER, len(boneAttributes)*32, gl.Ptr(boneAttributes), gl.STATIC_DRAW)
				gl.VertexAttribPointer(14, 4, gl.FLOAT, false, 8*4, gl.PtrOffset(0))
				gl.EnableVertexAttribArray(14)
				gl.VertexAttribPointer(15, 4, gl.FLOAT, false, 8*4, gl.PtrOffset(4*4))
				gl.EnableVertexAttribArray(15)

				object.animSkinMatrices = make([]mgl32.Mat4, len(skinMap.BoneIndices))
				object.animNormalMatrices = make([]mgl32.Mat4, len(skinMap.BoneIndices))
				for i := range object.animSkinMatrices {
					object.animSkinMatrices[i] = mgl32.Ident4()
					object.animNormalMatrices[i] = mgl32.Ident4()
				}
				gl.GenBuffers(1, &object.animSkinBuffer)
				gl.BindBuffer(gl.SHADER_STORAGE_BUFFER, object.animSkinBuffer)
				gl.BufferData(gl.SHADER_STORAGE_BUFFER, len(object.animSkinMatrices)*64, gl.Ptr(object.animSkinMatrices), gl.DYNAMIC_DRAW)
				gl.GenBuffers(1, &object.animNormalBuffer)
				gl.BindBuffer(gl.SHADER_STORAGE_BUFFER, object.animNormalBuffer)
				gl.BufferData(gl.SHADER_STORAGE_BUFFER, len(object.animNormalMatrices)*64, gl.Ptr(object.animNormalMatrices), gl.DYNAMIC_DRAW)
				gl.BindBuffer(gl.SHADER_STORAGE_BUFFER, 0)
			}

			// Setup instance model/normal matrix attributes
			gl.BindBuffer(gl.ARRAY_BUFFER, object.imbo)
			gl.EnableVertexAttribArray(7)
			gl.VertexAttribPointerWithOffset(7, 4, gl.FLOAT, false, 7*16, uintptr(0*16))
			gl.VertexAttribDivisor(7, 1)

			gl.EnableVertexAttribArray(8)
			gl.VertexAttribPointerWithOffset(8, 4, gl.FLOAT, false, 7*16, uintptr(1*16))
			gl.VertexAttribDivisor(8, 1)

			gl.EnableVertexAttribArray(9)
			gl.VertexAttribPointerWithOffset(9, 4, gl.FLOAT, false, 7*16, uintptr(2*16))
			gl.VertexAttribDivisor(9, 1)

			gl.EnableVertexAttribArray(10)
			gl.VertexAttribPointerWithOffset(10, 4, gl.FLOAT, false, 7*16, uintptr(3*16))
			gl.VertexAttribDivisor(10, 1)

			gl.EnableVertexAttribArray(11)
			gl.VertexAttribPointerWithOffset(11, 3, gl.FLOAT, false, 7*16, uintptr(4*16))
			gl.VertexAttribDivisor(11, 1)

			gl.EnableVertexAttribArray(12)
			gl.VertexAttribPointerWithOffset(12, 3, gl.FLOAT, false, 7*16, uintptr(5*16))
			gl.VertexAttribDivisor(12, 1)

			gl.EnableVertexAttribArray(13)
			gl.VertexAttribPointerWithOffset(13, 3, gl.FLOAT, false, 7*16, uintptr(6*16))
			gl.VertexAttribDivisor(13, 1)

			object.numIndices = make([]int32, len(mesh.Indices))
			object.indexType = make([]uint32, len(mesh.Indices))
			for group, indices := range mesh.Indices {
				gl.BindBuffer(gl.ELEMENT_ARRAY_BUFFER, object.ibos[group])
				if len(indices) > 0 {
					gl.BufferData(gl.ELEMENT_ARRAY_BUFFER, len(indices)*4, gl.Ptr(indices), gl.STATIC_DRAW)
				}
				object.numIndices[group] = int32(len(indices))
				object.indexType[group] = gl.UNSIGNED_INT
			}

			gl.BindBuffer(gl.ELEMENT_ARRAY_BUFFER, 0)
			gl.BindBuffer(gl.ARRAY_BUFFER, 0)
		}
		pv.meshPositions[fileID][name] = mesh.Positions
		pv.meshNormals[fileID][name] = mesh.Normals

		pv.objects[fileID][name] = object

		// Upload bounding box data
		{
			boundingBox := unitPreviewObject{}
			boundingBox.genObjects(false, 1)
			gl.BindVertexArray(boundingBox.vao)

			verts := pv.getAABBVertices(fileID, name)
			gl.BindBuffer(gl.ARRAY_BUFFER, boundingBox.vbo)
			gl.BufferData(gl.ARRAY_BUFFER, len(verts)*3*4, gl.Ptr(verts[:]), gl.STATIC_DRAW)

			boundingBox.numIndices[0] = int32(len(aabbIndices))
			boundingBox.numVertices = int32(len(verts))
			gl.BindBuffer(gl.ELEMENT_ARRAY_BUFFER, boundingBox.ibos[0])
			gl.BufferData(gl.ELEMENT_ARRAY_BUFFER, int(boundingBox.numIndices[0]*4), gl.Ptr(aabbIndices[:]), gl.STATIC_DRAW)

			gl.VertexAttribPointer(0, 3, gl.FLOAT, false, 3*4, nil)
			gl.EnableVertexAttribArray(0)

			// Setup instance model/normal matrix attributes
			gl.BindBuffer(gl.ARRAY_BUFFER, boundingBox.imbo)
			gl.EnableVertexAttribArray(7)
			gl.VertexAttribPointerWithOffset(7, 4, gl.FLOAT, false, 7*16, uintptr(0*16))
			gl.VertexAttribDivisor(7, 1)

			gl.EnableVertexAttribArray(8)
			gl.VertexAttribPointerWithOffset(8, 4, gl.FLOAT, false, 7*16, uintptr(1*16))
			gl.VertexAttribDivisor(8, 1)

			gl.EnableVertexAttribArray(9)
			gl.VertexAttribPointerWithOffset(9, 4, gl.FLOAT, false, 7*16, uintptr(2*16))
			gl.VertexAttribDivisor(9, 1)

			gl.EnableVertexAttribArray(10)
			gl.VertexAttribPointerWithOffset(10, 4, gl.FLOAT, false, 7*16, uintptr(3*16))
			gl.VertexAttribDivisor(10, 1)

			gl.EnableVertexAttribArray(11)
			gl.VertexAttribPointerWithOffset(11, 3, gl.FLOAT, false, 7*16, uintptr(4*16))
			gl.VertexAttribDivisor(11, 1)

			gl.EnableVertexAttribArray(12)
			gl.VertexAttribPointerWithOffset(12, 3, gl.FLOAT, false, 7*16, uintptr(5*16))
			gl.VertexAttribDivisor(12, 1)

			gl.EnableVertexAttribArray(13)
			gl.VertexAttribPointerWithOffset(13, 3, gl.FLOAT, false, 7*16, uintptr(6*16))
			gl.VertexAttribDivisor(13, 1)

			gl.BindBuffer(gl.ARRAY_BUFFER, 0)

			pv.boundingBoxes[fileID][name] = boundingBox
		}
	}

	skeletonVertices := make([]mgl32.Vec3, 0)
	skeletonBoneIndices := make([]int, 0)
	skeletonIndices := make([]uint32, 0)
	root := info.Bones[0]
	var recurseSkeleton func(uint32) uint32
	recurseSkeleton = func(boneIndex uint32) uint32 {
		curr := info.Bones[boneIndex]
		currentVertex := uint32(len(skeletonVertices))
		skeletonBoneIndices = append(skeletonBoneIndices, int(boneIndex))
		skeletonVertices = append(skeletonVertices, curr.Matrix.Mul4x1(mgl32.Vec4{0.0, 0.0, 0.0, 1.0}).Vec3())
		for _, idx := range curr.Children {
			childVertexIdx := recurseSkeleton(idx)
			skeletonIndices = append(skeletonIndices, currentVertex, childVertexIdx)
		}
		return currentVertex
	}
	recurseSkeleton(root.Index)

	if len(skeletonIndices) > 0 {
		if pv.skeletons == nil {
			pv.skeletons = make(map[stingray.FileID]unitPreviewObject)
		}
		skeleton := unitPreviewObject{}
		skeleton.genObjects(false, 1)
		gl.BindVertexArray(skeleton.vao)
		gl.BindBuffer(gl.ARRAY_BUFFER, skeleton.vbo)
		gl.BufferData(gl.ARRAY_BUFFER, len(skeletonVertices)*3*4, gl.Ptr(skeletonVertices), gl.STATIC_DRAW)

		gl.BindBuffer(gl.ELEMENT_ARRAY_BUFFER, skeleton.ibos[0])
		gl.BufferData(gl.ELEMENT_ARRAY_BUFFER, len(skeletonIndices)*4, gl.Ptr(skeletonIndices), gl.STATIC_DRAW)

		gl.VertexAttribPointer(0, 3, gl.FLOAT, false, 3*4, nil)
		gl.EnableVertexAttribArray(0)

		// Setup instance model/normal matrix attributes
		gl.BindBuffer(gl.ARRAY_BUFFER, skeleton.imbo)
		gl.EnableVertexAttribArray(7)
		gl.VertexAttribPointerWithOffset(7, 4, gl.FLOAT, false, 7*16, uintptr(0*16))
		gl.VertexAttribDivisor(7, 1)

		gl.EnableVertexAttribArray(8)
		gl.VertexAttribPointerWithOffset(8, 4, gl.FLOAT, false, 7*16, uintptr(1*16))
		gl.VertexAttribDivisor(8, 1)

		gl.EnableVertexAttribArray(9)
		gl.VertexAttribPointerWithOffset(9, 4, gl.FLOAT, false, 7*16, uintptr(2*16))
		gl.VertexAttribDivisor(9, 1)

		gl.EnableVertexAttribArray(10)
		gl.VertexAttribPointerWithOffset(10, 4, gl.FLOAT, false, 7*16, uintptr(3*16))
		gl.VertexAttribDivisor(10, 1)

		gl.EnableVertexAttribArray(11)
		gl.VertexAttribPointerWithOffset(11, 3, gl.FLOAT, false, 7*16, uintptr(4*16))
		gl.VertexAttribDivisor(11, 1)

		gl.EnableVertexAttribArray(12)
		gl.VertexAttribPointerWithOffset(12, 3, gl.FLOAT, false, 7*16, uintptr(5*16))
		gl.VertexAttribDivisor(12, 1)

		gl.EnableVertexAttribArray(13)
		gl.VertexAttribPointerWithOffset(13, 3, gl.FLOAT, false, 7*16, uintptr(6*16))
		gl.VertexAttribDivisor(13, 1)
		skeleton.numVertices = int32(len(skeletonVertices))
		skeleton.numIndices[0] = int32(len(skeletonIndices))
		if _, contains = pv.skeletons[fileID]; contains {
			pv.skeletons[fileID].deleteObjects(pv.ml)
		}
		pv.skeletonPositions[fileID] = skeletonVertices
		pv.skeletonBoneIndices[fileID] = skeletonBoneIndices
		pv.skeletons[fileID] = skeleton
	}

	gl.BindBuffer(gl.ARRAY_BUFFER, 0)
	gl.BindBuffer(gl.ELEMENT_ARRAY_BUFFER, 0)
	gl.BindVertexArray(0)

	if pv.autoZoomEnabled {
		pv.doAutoZoomNextFrame = true
	}

	visibilityState := newUnitVisibilityMasks()
	visibilityMasks, err := datalib.ParseVisibilityMasks()
	if err != nil {
		return err
	}
	visibilityMask, ok := visibilityMasks[fileID.Name]
	if !ok {
		entityHash := datalib.UnitsToEntities(fileID.Name)
		visibilityMask, ok = visibilityMasks[entityHash]
	}
	if ok {
		for name := range pv.objects[fileID] {
			object := pv.objects[fileID][name]
			object.hasVisibilityMasks = 1
			pv.objects[fileID][name] = object
			for _, material := range pv.objects[fileID][name].materials {
				material.SetUniform("hasVisibilityMasks", 1, &object.hasVisibilityMasks)
			}
		}
		for _, info := range visibilityMask.MaskInfos {
			if int(info.Index) >= len(visibilityState.defaults) {
				// No support for udims with index > 64 at the moment
				continue
			}
			if info.Name.Value == 0 {
				continue
			}
			visibilityState.num = max(uint32(info.Index)+1, visibilityState.num)
			visibilityState.defaults[info.Index] = info.StartHidden == 0
			name := pv.lookupThinHash(info.Name)
			visibilityState.names[info.Index] = name
			visibilityState.hashes[info.Index] = info.Name
		}
	}
	visibilityState.revealIntactBodySegments(meshes)
	visibilityState.selected = visibilityState.defaults
	visibilityState.syncShown()
	pv.visibilityMasks[fileID] = visibilityState
	if fileID == pv.rootHash {
		pv.numUdims = visibilityState.num
		pv.udimsShownDefault = visibilityState.defaults
		pv.udimsSelected = visibilityState.selected
		pv.udimsShown = visibilityState.shown
		pv.udimNames = visibilityState.names
		pv.rootHealthComponent = nil
		pv.rootHealthLoaded = false
		pv.rootHealthLoadError = ""
		pv.damageModelSelection = damageModelIntact
	}

	pv.updateUnitBoundsFromVertices(fileID)
	if setModelPos {
		if !pv.centerUnitOnVisibleBounds(fileID) {
			for name := range pv.objects[fileID] {
				if shown, contains := pv.objectsShown[fileID][name]; contains && !shown {
					continue
				}
				pv.modelPos = pv.objects[fileID][name].matrix.Inv().Mul4x1(mgl32.Vec4{0, 0, 0, 1})
				break
			}
		}
	}

	return nil
}

func (pv *UnitPreviewState) LoadSpeedtree(fileID stingray.Hash, mainData, gpuData []byte, thinhashes map[stingray.ThinHash]string, entityInfo *entity.Entity) error {
	pv.lookupThinHash = func(hash stingray.ThinHash) string {
		if name, ok := thinhashes[hash]; ok {
			return name
		}
		return hash.String()
	}
	pv.ml.SetEntity(entityInfo)
	speedtreeID := stingray.NewFileID(fileID, SPEEDTREE)

	pv.loadSpeedtree(speedtreeID, mainData, gpuData)

	if pv.nodes == nil {
		pv.nodes = make(map[stingray.Hash]unitPreviewNode)
	}

	pv.nodes[fileID] = unitPreviewNode{
		name:     pv.lookupHash(fileID),
		shown:    true,
		matrix:   mgl32.Ident4(),
		children: []stingray.FileID{speedtreeID},
		parent:   stingray.Hash{},
	}

	pv.root = pv.nodes[fileID]
	pv.rootHash = speedtreeID

	if err := pv.finalizeObjectMaterials(); err != nil {
		return err
	}

	pv.maxViewDistance = pv.getMaxZoom(pv.root, pv.rootHash, len(pv.aabb[speedtreeID]) == 0)

	return nil
}

func (pv *UnitPreviewState) loadSpeedtree(speedtreeID stingray.FileID, mainData, gpuData []byte) error {
	info, err := speedtree.LoadSpeedTree(bytes.NewReader(mainData))
	if err != nil {
		return err
	}

	setModelPos := len(pv.objects) == 0

	if pv.objects == nil {
		pv.objects = make(map[stingray.FileID]map[string]unitPreviewObject)
		pv.objectOverrides = make(map[stingray.FileID]stingray.FileID)
		pv.boundingBoxes = make(map[stingray.FileID]map[string]unitPreviewObject)
		pv.objectsShown = make(map[stingray.FileID]map[string]bool)
		pv.objectsSelected = make(map[stingray.FileID]map[string]bool)
		pv.objectsShownDefault = make(map[stingray.FileID]map[string]bool)
		pv.aabb = make(map[stingray.FileID]map[string][2]mgl32.Vec3)
		pv.aabbMat = make(map[stingray.FileID]map[string]mgl32.Mat4, 0)
		pv.meshPositions = make(map[stingray.FileID]map[string][][3]float32)
		pv.meshNormals = make(map[stingray.FileID]map[string][][3]float32)
		pv.skeletonPositions = make(map[stingray.FileID][]mgl32.Vec3)
		pv.skeletons = make(map[stingray.FileID]unitPreviewObject)
		pv.fibonacciNormalLut = make([][4]float32, 0)
	}

	if len(pv.fibonacciNormalLut) == 0 {
		fileID := stingray.NewFileID(stingray.Sum("content/art_shared/textures/fibonacci_normal_lut"), stingray.Sum("texture"))
		texMain, _, _ := pv.getResource(fileID, stingray.DataMain)
		texGPU, _, _ := pv.getResource(fileID, stingray.DataGPU)

		dataR := io.MultiReader(bytes.NewReader(texMain), bytes.NewReader(texGPU))

		_, err := texture.DecodeInfo(dataR)
		if err != nil {
			fmt.Println(err)
		}
		ddsImg, err := dds.Decode(dataR, false)
		pv.fibonacciNormalLut = make([][4]float32, ddsImg.Bounds().Dx()*ddsImg.Bounds().Dy())
		binary.Decode(ddsImg.Images[0].MipMaps[0].Raw, binary.LittleEndian, pv.fibonacciNormalLut)
	}

	name := "lod0"
	defaultShown := map[string]bool{name: true}

	var contains bool
	if _, contains = pv.objects[speedtreeID]; contains {
		for name := range pv.objects[speedtreeID] {
			pv.objects[speedtreeID][name].deleteObjects(pv.ml)
			pv.boundingBoxes[speedtreeID][name].deleteObjects(pv.ml)
		}
	}

	if !contains {
		pv.objectsShownDefault[speedtreeID] = defaultShown
		pv.objectsShown[speedtreeID] = maps.Clone(defaultShown)
		pv.objectsSelected[speedtreeID] = maps.Clone(defaultShown)
		pv.objectOverrides[speedtreeID] = speedtreeID
	} else {
		// If a user has hidden a mesh, don't unhide it when we update the asset overrides
		// but also make sure the correct mesh names are being used
		for name := range pv.objectsShown[speedtreeID] {
			if _, contains := defaultShown[name]; !contains {
				delete(pv.objectsShownDefault[speedtreeID], name)
				delete(pv.objectsShown[speedtreeID], name)
				delete(pv.objectsSelected[speedtreeID], name)
				continue
			}
		}
		for name := range defaultShown {
			if _, contains := pv.objectsShown[speedtreeID][name]; !contains {
				pv.objectsShownDefault[speedtreeID][name] = defaultShown[name]
				pv.objectsShown[speedtreeID][name] = defaultShown[name]
				pv.objectsSelected[speedtreeID][name] = defaultShown[name]
			}
		}
	}

	pv.aabb[speedtreeID] = make(map[string][2]mgl32.Vec3)
	pv.aabbMat[speedtreeID] = make(map[string]mgl32.Mat4)
	pv.aabb[speedtreeID][name] = [2]mgl32.Vec3{
		info.Extents[0],
		info.Extents[1],
	}
	pv.aabbMat[speedtreeID][name] = mgl32.Ident4()
	pv.objectsShown[speedtreeID] = maps.Clone(pv.objectsShownDefault[speedtreeID])
	pv.objectsSelected[speedtreeID] = maps.Clone(pv.objectsShownDefault[speedtreeID])
	pv.objects[speedtreeID] = make(map[string]unitPreviewObject)
	pv.boundingBoxes[speedtreeID] = make(map[string]unitPreviewObject)
	pv.meshPositions[speedtreeID] = make(map[string][][3]float32)
	pv.meshNormals[speedtreeID] = make(map[string][][3]float32)
	lod0 := info.IndexDefinitions[0]

	object := unitPreviewObject{}
	object.name = speedtreeID.Name
	object.matrix = mgl32.Ident4()
	wireframe := preview_mats.NewSpeedtreeWireframeMaterial()
	err = wireframe.Generate()
	if err != nil {
		return err
	}
	object.wireframe = wireframe

	normalVis := preview_mats.NewSpeedtreeNormalVisMaterial()
	err = normalVis.Generate()
	if err != nil {
		return err
	}
	pv.ml.UpdateSpeedtreeNormalVisMaterial(normalVis)
	object.normalVis = normalVis

	// Create index buffers
	{
		object.genObjects(true, 0)
		object.ibos = make([]uint32, lod0.MeshCount)
		gl.GenBuffers(int32(len(object.ibos)), &object.ibos[0])

		for idx := range object.materials {
			// release textures and delete shaders from old materials
			pv.ml.DeleteMaterial(object.materials[idx])
		}

		object.materials = make([]preview_mats.Material, 0, lod0.MeshCount)
		for group := range lod0.MeshCount {
			var treeMaterial speedtree.MaterialDefinition
			for _, matDef := range info.StingrayMaterials {
				if matDef.Index == uint64(info.MeshDefinitions[lod0.MeshOffset+group].Material) {
					treeMaterial = matDef
					break
				}
			}

			name := fmt.Sprintf("mat%v", treeMaterial.Index)
			materialID := stingray.NewFileID(treeMaterial.Path, MATERIAL)
			var override stingray.FileID
			if pv.getOverride != nil {
				override = pv.getOverride(materialID)
			} else {
				override = materialID
			}

			mat, err := loadMaterialInfo(pv.getResource, materialID)
			if err != nil {
				// gui logger warning
				fmt.Printf("failed to load speedtree material: %v", err)
				continue
			}
			material := pv.ml.LoadMaterial(materialID, name, true, mat, nil, speedtreeID)
			material.SetOverride(override)
			object.materials = append(object.materials, material)
		}
	}

	// Upload object data
	{
		gl.BindVertexArray(object.vao)

		vertexDef := info.VertexDefinitions[lod0.VertexDef]

		gl.BindBuffer(gl.ARRAY_BUFFER, object.vbo)
		gl.BufferData(gl.ARRAY_BUFFER, int(vertexDef.Count*vertexDef.Stride), gl.Ptr(&gpuData[vertexDef.Offset]), gl.STATIC_DRAW)

		offset := uintptr(0)
		for idx, attr := range info.VertexXML.Stream.Attribute {
			var gltype uint32
			var size uint32
			switch attr.Type {
			case "byte":
				gltype = gl.BYTE
				size = 1
			case "ubyte":
				gltype = gl.UNSIGNED_BYTE
				size = 1
			case "short":
				gltype = gl.SHORT
				size = 2
			case "ushort":
				gltype = gl.UNSIGNED_SHORT
				size = 2
			case "int":
				gltype = gl.INT
				size = 4
			case "uint":
				gltype = gl.UNSIGNED_INT
				size = 4
			case "half":
				gltype = gl.HALF_FLOAT
				size = 2
			case "float":
				gltype = gl.FLOAT
				size = 4
			case "double":
				gltype = gl.DOUBLE
				size = 8
			default:
				gltype = gl.FLOAT
				size = 4
			}

			if strings.HasPrefix(attr.Description, "position") {
				pv.meshPositions[speedtreeID] = make(map[string][][3]float32)
				positions := make([][3]float32, 0)
				for i := range vertexDef.Count {
					switch attr.Type {
					case "half":
						var pos []float16.Float16 = make([]float16.Float16, 3)
						binary.Decode(gpuData[offset+uintptr(i*vertexDef.Stride):], binary.LittleEndian, pos)
						positions = append(positions, mgl32.Vec3{pos[0].Float32(), pos[1].Float32(), pos[2].Float32()})
					case "float":
						var pos []float32 = make([]float32, 3)
						binary.Decode(gpuData[offset+uintptr(i*vertexDef.Stride):], binary.LittleEndian, pos)
						positions = append(positions, mgl32.Vec3{pos[0], pos[1], pos[2]})
					}
				}
				pv.meshPositions[speedtreeID][name] = positions

			}
			if strings.HasPrefix(attr.Description, "normal") {
				pv.meshNormals[speedtreeID] = make(map[string][][3]float32)
				normals := make([][3]float32, 0)
				for i := range vertexDef.Count {
					switch attr.Type {
					case "ubyte":
						packedCoords := gpuData[offset+uintptr(i*vertexDef.Stride)]
						x := packedCoords & 0xf
						y := packedCoords >> 4
						normals = append(normals, [3]float32(pv.fibonacciNormalLut[x+y*16][:3]))
					}
				}
				pv.meshNormals[speedtreeID][name] = normals
			}
			//fmt.Println(pv.meshPositions[speedtreeID][name])

			normalize := false
			if attr.Normalize != nil {
				normalize = *attr.Normalize
			}
			switch gltype {
			case gl.BYTE, gl.UNSIGNED_BYTE, gl.SHORT, gl.UNSIGNED_SHORT, gl.INT, gl.UNSIGNED_INT:
				if !normalize {
					gl.VertexAttribIPointerWithOffset(uint32(idx), int32(attr.Count), gltype, int32(vertexDef.Stride), offset)
				} else {
					gl.VertexAttribPointerWithOffset(uint32(idx), int32(attr.Count), gltype, normalize, int32(vertexDef.Stride), offset)
				}
			default:
				gl.VertexAttribPointerWithOffset(uint32(idx), int32(attr.Count), gltype, normalize, int32(vertexDef.Stride), offset)
			}
			gl.EnableVertexAttribArray(uint32(idx))
			offset += uintptr(size * uint32(attr.Count))
		}
		object.numVertices = int32(vertexDef.Count)

		// Setup instance model/normal matrix attributes
		gl.BindBuffer(gl.ARRAY_BUFFER, object.imbo)
		gl.EnableVertexAttribArray(7)
		gl.VertexAttribPointerWithOffset(7, 4, gl.FLOAT, false, 7*16, uintptr(0*16))
		gl.VertexAttribDivisor(7, 1)

		gl.EnableVertexAttribArray(8)
		gl.VertexAttribPointerWithOffset(8, 4, gl.FLOAT, false, 7*16, uintptr(1*16))
		gl.VertexAttribDivisor(8, 1)

		gl.EnableVertexAttribArray(9)
		gl.VertexAttribPointerWithOffset(9, 4, gl.FLOAT, false, 7*16, uintptr(2*16))
		gl.VertexAttribDivisor(9, 1)

		gl.EnableVertexAttribArray(10)
		gl.VertexAttribPointerWithOffset(10, 4, gl.FLOAT, false, 7*16, uintptr(3*16))
		gl.VertexAttribDivisor(10, 1)

		gl.EnableVertexAttribArray(11)
		gl.VertexAttribPointerWithOffset(11, 3, gl.FLOAT, false, 7*16, uintptr(4*16))
		gl.VertexAttribDivisor(11, 1)

		gl.EnableVertexAttribArray(12)
		gl.VertexAttribPointerWithOffset(12, 3, gl.FLOAT, false, 7*16, uintptr(5*16))
		gl.VertexAttribDivisor(12, 1)

		gl.EnableVertexAttribArray(13)
		gl.VertexAttribPointerWithOffset(13, 3, gl.FLOAT, false, 7*16, uintptr(6*16))
		gl.VertexAttribDivisor(13, 1)

		object.numIndices = make([]int32, lod0.MeshCount)
		object.indexType = make([]uint32, lod0.MeshCount)
		for group, meshDef := range info.MeshDefinitions[lod0.MeshOffset : lod0.MeshOffset+lod0.MeshCount] {
			gl.BindBuffer(gl.ELEMENT_ARRAY_BUFFER, object.ibos[group])
			gl.BufferData(gl.ELEMENT_ARRAY_BUFFER, int(meshDef.IndexCount*lod0.Stride), gl.Ptr(&gpuData[lod0.Offset+meshDef.IndexOffset*lod0.Stride]), gl.STATIC_DRAW)
			object.numIndices[group] = int32(meshDef.IndexCount)

			switch lod0.Stride {
			case 2:
				object.indexType[group] = gl.UNSIGNED_SHORT
			case 4:
				object.indexType[group] = gl.UNSIGNED_INT
			}
			gl.BindBuffer(gl.ELEMENT_ARRAY_BUFFER, 0)
		}

		gl.BindBuffer(gl.ARRAY_BUFFER, 0)
		gl.BindVertexArray(0)
	}

	// Upload bounding box data
	{
		boundingBox := unitPreviewObject{}
		boundingBox.genObjects(false, 1)
		gl.BindVertexArray(boundingBox.vao)

		verts := pv.getAABBVertices(speedtreeID, name)
		gl.BindBuffer(gl.ARRAY_BUFFER, boundingBox.vbo)
		defer gl.BindBuffer(gl.ARRAY_BUFFER, 0)
		gl.BufferData(gl.ARRAY_BUFFER, len(verts)*3*4, gl.Ptr(verts[:]), gl.STATIC_DRAW)

		boundingBox.numIndices[0] = int32(len(aabbIndices))
		boundingBox.numVertices = int32(len(verts))
		gl.BindBuffer(gl.ELEMENT_ARRAY_BUFFER, boundingBox.ibos[0])
		defer gl.BindBuffer(gl.ELEMENT_ARRAY_BUFFER, 0)
		gl.BufferData(gl.ELEMENT_ARRAY_BUFFER, int(boundingBox.numIndices[0]*4), gl.Ptr(aabbIndices[:]), gl.STATIC_DRAW)

		gl.VertexAttribPointer(0, 3, gl.FLOAT, false, 3*4, nil)
		gl.EnableVertexAttribArray(0)

		// Setup instance model/normal matrix attributes
		gl.BindBuffer(gl.ARRAY_BUFFER, boundingBox.imbo)
		gl.EnableVertexAttribArray(7)
		gl.VertexAttribPointerWithOffset(7, 4, gl.FLOAT, false, 7*16, uintptr(0*16))
		gl.VertexAttribDivisor(7, 1)

		gl.EnableVertexAttribArray(8)
		gl.VertexAttribPointerWithOffset(8, 4, gl.FLOAT, false, 7*16, uintptr(1*16))
		gl.VertexAttribDivisor(8, 1)

		gl.EnableVertexAttribArray(9)
		gl.VertexAttribPointerWithOffset(9, 4, gl.FLOAT, false, 7*16, uintptr(2*16))
		gl.VertexAttribDivisor(9, 1)

		gl.EnableVertexAttribArray(10)
		gl.VertexAttribPointerWithOffset(10, 4, gl.FLOAT, false, 7*16, uintptr(3*16))
		gl.VertexAttribDivisor(10, 1)

		gl.EnableVertexAttribArray(11)
		gl.VertexAttribPointerWithOffset(11, 3, gl.FLOAT, false, 7*16, uintptr(4*16))
		gl.VertexAttribDivisor(11, 1)

		gl.EnableVertexAttribArray(12)
		gl.VertexAttribPointerWithOffset(12, 3, gl.FLOAT, false, 7*16, uintptr(5*16))
		gl.VertexAttribDivisor(12, 1)

		gl.EnableVertexAttribArray(13)
		gl.VertexAttribPointerWithOffset(13, 3, gl.FLOAT, false, 7*16, uintptr(6*16))
		gl.VertexAttribDivisor(13, 1)

		pv.boundingBoxes[speedtreeID][name] = boundingBox
	}

	// Create dummy skeleton since speedtrees don't seem to have one
	{
		skeletonVertices := []mgl32.Vec3{{0.0, 0.0, 0.0}, {0.0, 1.0, 0.0}}
		skeletonIndices := []uint32{0, 1}
		skeleton := unitPreviewObject{}
		skeleton.genObjects(false, 1)
		gl.BindVertexArray(skeleton.vao)
		gl.BindBuffer(gl.ARRAY_BUFFER, skeleton.vbo)
		gl.BufferData(gl.ARRAY_BUFFER, len(skeletonVertices)*3*4, gl.Ptr(skeletonVertices), gl.STATIC_DRAW)

		gl.BindBuffer(gl.ELEMENT_ARRAY_BUFFER, skeleton.ibos[0])
		gl.BufferData(gl.ELEMENT_ARRAY_BUFFER, len(skeletonIndices)*4, gl.Ptr(skeletonIndices), gl.STATIC_DRAW)

		gl.VertexAttribPointer(0, 3, gl.FLOAT, false, 3*4, nil)
		gl.EnableVertexAttribArray(0)

		// Setup instance model/normal matrix attributes
		gl.BindBuffer(gl.ARRAY_BUFFER, skeleton.imbo)
		gl.EnableVertexAttribArray(7)
		gl.VertexAttribPointerWithOffset(7, 4, gl.FLOAT, false, 7*16, uintptr(0*16))
		gl.VertexAttribDivisor(7, 1)

		gl.EnableVertexAttribArray(8)
		gl.VertexAttribPointerWithOffset(8, 4, gl.FLOAT, false, 7*16, uintptr(1*16))
		gl.VertexAttribDivisor(8, 1)

		gl.EnableVertexAttribArray(9)
		gl.VertexAttribPointerWithOffset(9, 4, gl.FLOAT, false, 7*16, uintptr(2*16))
		gl.VertexAttribDivisor(9, 1)

		gl.EnableVertexAttribArray(10)
		gl.VertexAttribPointerWithOffset(10, 4, gl.FLOAT, false, 7*16, uintptr(3*16))
		gl.VertexAttribDivisor(10, 1)

		gl.EnableVertexAttribArray(11)
		gl.VertexAttribPointerWithOffset(11, 3, gl.FLOAT, false, 7*16, uintptr(4*16))
		gl.VertexAttribDivisor(11, 1)

		gl.EnableVertexAttribArray(12)
		gl.VertexAttribPointerWithOffset(12, 3, gl.FLOAT, false, 7*16, uintptr(5*16))
		gl.VertexAttribDivisor(12, 1)

		gl.EnableVertexAttribArray(13)
		gl.VertexAttribPointerWithOffset(13, 3, gl.FLOAT, false, 7*16, uintptr(6*16))
		gl.VertexAttribDivisor(13, 1)
		skeleton.numVertices = int32(len(skeletonVertices))
		skeleton.numIndices[0] = int32(len(skeletonIndices))
		if _, contains = pv.skeletons[speedtreeID]; contains {
			pv.skeletons[speedtreeID].deleteObjects(pv.ml)
		}
		pv.skeletonPositions[speedtreeID] = skeletonVertices
		pv.skeletons[speedtreeID] = skeleton
	}

	pv.objects[speedtreeID][name] = object

	if setModelPos {
		pv.modelPos = mgl32.Vec4{0.0, 0.0, 0.0, 1.0}
	}

	return nil
}

// SetArmorContext makes the LUTs of these kit archives apply to the pieces shown (Armor Set window).
func (pv *UnitPreviewState) SetArmorContext(archives []stingray.Hash) {
	if slices.Equal(pv.armorContextArchives, archives) {
		return
	}
	pv.armorContextArchives = archives
	pv.reapplyArmorMaterials()
}

// LoadArmorSetByKit assembles the whole set (armor + helmet) the kit with this id belongs to,
// whatever the Archive Filter selection is. Returns false if the kit is unknown.
func (pv *UnitPreviewState) LoadArmorSetByKit(kitID uint32) bool {
	var found *datalib.ArmorSet
	for _, set := range pv.armorSets {
		if set.Id.Value == kitID {
			s := set
			found = &s
			break
		}
	}
	if found == nil {
		return false
	}
	sets := relatedArmorKits(pv.armorSets, *found)
	ctx := make([]stingray.Hash, 0)
	for _, set := range sets {
		ctx = append(ctx, set.Archive)
	}
	slices.SortFunc(ctx, stingray.Hash.Cmp)
	pv.armorContextArchives = ctx
	pv.selectedSet = int64(kitID)
	pv.loadArmorSet(sets)
	return pv.HasLoadedUnit()
}

// Loads the specified armor set and any others in the armor set list that share the armor set id
// Therefore this can load the armor and helmet at the same time
func (pv *UnitPreviewState) loadArmorSet(armorSets []datalib.ArmorSet) {
	pv.Clear()
	if pv.nodes == nil {
		pv.nodes = make(map[stingray.Hash]unitPreviewNode)
	}
	var selectedSet int
	for idx := range armorSets {
		if armorSets[idx].Id.Value == uint32(pv.selectedSet) {
			selectedSet = idx
			break
		}
	}
	setId := armorSets[selectedSet].SetId
	hasStocky := false
	for _, set := range armorSets {
		for _, data := range set.UnitMetadata {
			if data.BodyType == datalib.BodyTypeStocky {
				hasStocky = true
				break
			}
		}
	}
	pv.nodes[stingray.Sum("body_any")] = unitPreviewNode{
		name:     "Any",
		shown:    true,
		matrix:   mgl32.Ident4(),
		children: []stingray.FileID{},
		parent:   armorSets[selectedSet].Archive,
	}
	pv.nodes[stingray.Sum("body_male")] = unitPreviewNode{
		name:     "Stocky",
		shown:    true,
		matrix:   mgl32.Ident4(),
		children: []stingray.FileID{},
		parent:   armorSets[selectedSet].Archive,
	}
	pv.nodes[stingray.Sum("body_female")] = unitPreviewNode{
		name:     "Slim",
		shown:    !hasStocky,
		matrix:   mgl32.Ident4(),
		children: []stingray.FileID{},
		parent:   armorSets[selectedSet].Archive,
	}
	for _, set := range armorSets {
		if set.SetId != setId {
			continue
		}
		for childHash, piece := range set.UnitMetadata {
			if childHash.Value == 0 {
				if set.Type != datalib.KitCape {
					continue
				}
				childHash = stingray.Sum("content/fac_helldivers/capes/medium_cape")
			}
			if pv.armorThumbnailOnly && hasStocky && piece.BodyType == datalib.BodyTypeSlim {
				continue
			}
			fileId := stingray.NewFileID(childHash, UNIT)
			if _, loaded := pv.objects[fileId]; !loaded {
				unitMain, exists, err := pv.getResource(fileId, stingray.DataMain)
				if err != nil || !exists {
					continue
				}
				unitGpu, _, _ := pv.getResource(fileId, stingray.DataGPU)
				if err := pv.loadUnit(fileId, unitMain, unitGpu); err != nil {
					continue
				}
			}
			// A standalone cape kit uses the shared medium-cape unit, whose
			// other meshes are alternate and backpack variants. Select only the
			// main mesh by default while keeping the variants in the tree.
			if childHash == stingray.Sum("content/fac_helldivers/capes/medium_cape") {
				if _, ok := pv.objects[fileId]["medium_cape"]; ok {
					for name := range pv.objects[fileId] {
						shown := name == "medium_cape"
						pv.objectsShown[fileId][name] = shown
						pv.objectsShownDefault[fileId][name] = shown
					}
				}
			}
			switch piece.BodyType {
			case datalib.BodyTypeAny:
				node := pv.nodes[stingray.Sum("body_any")]
				node.children = append(node.children, fileId)
				pv.nodes[stingray.Sum("body_any")] = node
			case datalib.BodyTypeSlim:
				node := pv.nodes[stingray.Sum("body_female")]
				node.children = append(node.children, fileId)
				pv.nodes[stingray.Sum("body_female")] = node
			case datalib.BodyTypeStocky:
				node := pv.nodes[stingray.Sum("body_male")]
				node.children = append(node.children, fileId)
				pv.nodes[stingray.Sum("body_male")] = node
			}
		}
	}
	pv.root = unitPreviewNode{
		name:   armorSets[selectedSet].Name,
		shown:  true,
		matrix: mgl32.Ident4(),
		children: []stingray.FileID{
			stingray.NewFileID(stingray.Sum("body_any"), stingray.Hash{}),
			stingray.NewFileID(stingray.Sum("body_male"), stingray.Hash{}),
			stingray.NewFileID(stingray.Sum("body_female"), stingray.Hash{}),
		},
		parent: stingray.Hash{},
	}
	pv.rootHash = stingray.NewFileID(armorSets[selectedSet].Archive, stingray.Hash{})
	pv.nodes[pv.rootHash.Name] = pv.root

	pv.centerArmorOnVisibleBounds()
	pv.maxViewDistance = pv.getMaxZoom(pv.root, pv.rootHash, false)
	pv.doAutoZoomNextFrame = true

	pv.finalizeObjectMaterials()
}

func (pv *UnitPreviewState) computeMVP(aspectRatio float32, animate bool) (
	modelPos mgl32.Vec4,
	viewPosition mgl32.Vec3,
	view mgl32.Mat4,
	projection mgl32.Mat4,
) {
	var viewDistance float32
	var viewRotation mgl32.Vec2

	if animate && pv.animTime >= 0 && pv.animTime <= 1 {
		// Animate -> lerp original to current by animTime
		viewDistance = pv.animOrigViewDistance*(1-pv.animTime) + pv.viewDistance*pv.animTime
		viewRotation = pv.animOrigViewRotation.Mul(1 - pv.animTime).Add(pv.viewRotation.Mul(pv.animTime))
		modelPos = pv.animOrigModelPos.Mul(1 - pv.animTime).Add(pv.modelPos.Mul(pv.animTime))
	} else {
		viewDistance = pv.viewDistance
		viewRotation = pv.viewRotation
		modelPos = pv.modelPos
	}

	{
		mat := mgl32.Ident3()
		mat = mat.Mul3(mgl32.Rotate3DY(viewRotation[0]))
		mat = mat.Mul3(mgl32.Rotate3DX(viewRotation[1]))
		viewPosition = mat.Mul3x1(mgl32.Vec3{0, 0, viewDistance})
	}
	view = mgl32.LookAt(
		viewPosition[0], viewPosition[1], viewPosition[2],
		0, 0, 0,
		0, 1, 0,
	)
	projection = mgl32.Perspective(
		pv.vfov,
		aspectRatio,
		max(0.001, 0.001*viewDistance),
		32768,
	)
	return
}

// previewSceneModelMatrix stabilizes the displayed model and its mounted parts
// against root translation in locomotion clips.
func (pv *UnitPreviewState) previewSceneModelMatrix(modelPos mgl32.Vec4) mgl32.Mat4 {
	correction := pv.animationRootMotionCorrection()
	return pv.previewBaseSceneModelMatrix(modelPos).
		Mul4(mgl32.Translate3D(correction[0], correction[1], correction[2]))
}

// previewBaseSceneModelMatrix is used for damage and hitbox overlays. Those
// boxes follow the animation's actual bone transforms without the preview-only
// root-motion correction applied to the displayed model.
func (pv *UnitPreviewState) previewBaseSceneModelMatrix(modelPos mgl32.Vec4) mgl32.Mat4 {
	return pv.model.Mul4(mgl32.Translate3D(modelPos[0], modelPos[1], modelPos[2]))
}

var aabbIndices = [12 * 3]uint32{
	1, 2, 0,
	1, 3, 2,
	0, 6, 4,
	0, 2, 6,
	4, 7, 5,
	4, 6, 7,
	5, 3, 1,
	5, 7, 3,
	2, 3, 7,
	2, 7, 6,
	0, 4, 5,
	0, 5, 1,
}

func (pv *UnitPreviewState) getAABBVertices(fileId stingray.FileID, name string) [8]mgl32.Vec3 {
	return [8]mgl32.Vec3{
		{pv.aabb[fileId][name][0][0], pv.aabb[fileId][name][0][1], pv.aabb[fileId][name][0][2]},
		{pv.aabb[fileId][name][0][0], pv.aabb[fileId][name][0][1], pv.aabb[fileId][name][1][2]},
		{pv.aabb[fileId][name][0][0], pv.aabb[fileId][name][1][1], pv.aabb[fileId][name][0][2]},
		{pv.aabb[fileId][name][0][0], pv.aabb[fileId][name][1][1], pv.aabb[fileId][name][1][2]},
		{pv.aabb[fileId][name][1][0], pv.aabb[fileId][name][0][1], pv.aabb[fileId][name][0][2]},
		{pv.aabb[fileId][name][1][0], pv.aabb[fileId][name][0][1], pv.aabb[fileId][name][1][2]},
		{pv.aabb[fileId][name][1][0], pv.aabb[fileId][name][1][1], pv.aabb[fileId][name][0][2]},
		{pv.aabb[fileId][name][1][0], pv.aabb[fileId][name][1][1], pv.aabb[fileId][name][1][2]},
	}
}

func (pv *UnitPreviewState) centerUnitOnVisibleBounds(fileID stingray.FileID) bool {
	minBounds, maxBounds, found := pv.visibleUnitBounds(fileID)
	if !found {
		return false
	}
	center := minBounds.Add(maxBounds).Mul(0.5)
	pv.modelPos = mgl32.Vec4{-center[0], -center[1], -center[2], 1}
	return true
}

// Armor sets are scene roots containing several separate unit resources.
// Their root has no mesh bounds, so include visible child units and transforms.
func (pv *UnitPreviewState) centerArmorOnVisibleBounds() bool {
	minBounds := mgl32.Vec3{float32(math.Inf(1)), float32(math.Inf(1)), float32(math.Inf(1))}
	maxBounds := mgl32.Vec3{float32(math.Inf(-1)), float32(math.Inf(-1)), float32(math.Inf(-1))}
	found := false
	var visit func(unitPreviewNode, mgl32.Mat4)
	visit = func(node unitPreviewNode, transform mgl32.Mat4) {
		if !node.shown {
			return
		}
		for _, id := range node.children {
			if child, ok := pv.nodes[id.Name]; ok {
				visit(child, transform.Mul4(child.matrix))
			}
			low, high, ok := pv.visibleUnitBounds(id)
			if !ok {
				continue
			}
			for mask := 0; mask < 8; mask++ {
				point := mgl32.Vec3{low[0], low[1], low[2]}
				for axis := range 3 {
					if mask&(1<<axis) != 0 {
						point[axis] = high[axis]
					}
				}
				p := transform.Mul4x1(point.Vec4(1))
				if p.W() == 0 {
					continue
				}
				p = p.Mul(1 / p.W())
				if math.IsNaN(float64(p[0])) || math.IsNaN(float64(p[1])) || math.IsNaN(float64(p[2])) || math.IsInf(float64(p[0]), 0) || math.IsInf(float64(p[1]), 0) || math.IsInf(float64(p[2]), 0) {
					continue
				}
				for axis := range 3 {
					minBounds[axis] = min(minBounds[axis], p[axis])
					maxBounds[axis] = max(maxBounds[axis], p[axis])
				}
				found = true
			}
		}
	}
	visit(pv.root, pv.root.matrix)
	if found {
		center := minBounds.Add(maxBounds).Mul(0.5)
		pv.modelPos = mgl32.Vec4{-center[0], -center[1], -center[2], 1}
	}
	return found
}

func (pv *UnitPreviewState) updateUnitBoundsFromVertices(fileID stingray.FileID) {
	for name, object := range pv.objects[fileID] {
		positions := pv.meshPositions[fileID][name]
		if len(positions) == 0 {
			continue
		}
		minBounds := mgl32.Vec3{float32(math.Inf(1)), float32(math.Inf(1)), float32(math.Inf(1))}
		maxBounds := mgl32.Vec3{float32(math.Inf(-1)), float32(math.Inf(-1)), float32(math.Inf(-1))}
		found := false
		for _, position := range positions {
			point := object.matrix.Mul4x1(mgl32.Vec3(position).Vec4(1))
			if point.W() == 0 || math.IsNaN(float64(point.W())) || math.IsInf(float64(point.W()), 0) {
				continue
			}
			point = point.Mul(1 / point.W())
			valid := true
			for axis := range 3 {
				if math.IsNaN(float64(point[axis])) || math.IsInf(float64(point[axis]), 0) {
					valid = false
					break
				}
			}
			if !valid {
				continue
			}
			for axis := range 3 {
				minBounds[axis] = min(minBounds[axis], point[axis])
				maxBounds[axis] = max(maxBounds[axis], point[axis])
			}
			found = true
		}
		if found {
			pv.aabb[fileID][name] = [2]mgl32.Vec3{minBounds, maxBounds}
			pv.aabbMat[fileID][name] = mgl32.Ident4()
		}
	}
}

func (pv *UnitPreviewState) visibleUnitBounds(fileID stingray.FileID) (minBounds, maxBounds mgl32.Vec3, found bool) {
	minBounds = mgl32.Vec3{float32(math.Inf(1)), float32(math.Inf(1)), float32(math.Inf(1))}
	maxBounds = mgl32.Vec3{float32(math.Inf(-1)), float32(math.Inf(-1)), float32(math.Inf(-1))}
	for name := range pv.objects[fileID] {
		if shown, exists := pv.objectsShown[fileID][name]; exists && !shown {
			continue
		}
		_, hasBounds := pv.aabb[fileID][name]
		boundsMatrix, hasMatrix := pv.aabbMat[fileID][name]
		if !hasBounds || !hasMatrix {
			continue
		}
		for _, corner := range pv.getAABBVertices(fileID, name) {
			point := boundsMatrix.Mul4x1(corner.Vec4(1))
			if point.W() == 0 || math.IsNaN(float64(point.W())) || math.IsInf(float64(point.W()), 0) {
				continue
			}
			point = point.Mul(1 / point.W())
			valid := true
			for axis := range 3 {
				coordinate := point[axis]
				if math.IsNaN(float64(coordinate)) || math.IsInf(float64(coordinate), 0) {
					valid = false
					break
				}
			}
			if !valid {
				continue
			}
			for axis := range 3 {
				coordinate := point[axis]
				minBounds[axis] = min(minBounds[axis], coordinate)
				maxBounds[axis] = max(maxBounds[axis], coordinate)
			}
			found = true
		}
	}
	if !found {
		return minBounds, maxBounds, false
	}
	return minBounds, maxBounds, found
}

func (pv *UnitPreviewState) VisibleModelExtent(fileID stingray.FileID) float64 {
	minBounds, maxBounds, found := pv.visibleUnitBounds(fileID)
	if !found {
		return 0
	}
	dimensions := maxBounds.Sub(minBounds)
	return float64(max(dimensions[0], dimensions[1], dimensions[2]))
}

func sum(s []int32) (result int32) {
	result = 0
	for _, val := range s {
		result += val
	}
	return
}

func (pv *UnitPreviewState) initDrawList(hash stingray.FileID, name string) {
	if pv.objectDrawList == nil {
		pv.objectDrawList = make(map[stingray.FileID]map[string][]float32)
	}
	if _, contains := pv.objectDrawList[hash]; !contains {
		pv.objectDrawList[hash] = make(map[string][]float32)
	}
	if _, contains := pv.objectDrawList[hash][name]; !contains {
		pv.objectDrawList[hash][name] = make([]float32, 0)
	}
}

func (pv *UnitPreviewState) drawLists(size imgui.Vec2, model, view, projection mgl32.Mat4, viewPosition mgl32.Vec3) {
	gl.Enable(gl.DEPTH_TEST)
	blendingWasEnabled := gl.IsEnabled(gl.BLEND)
	gl.Enable(gl.BLEND)
	gl.BlendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA)
	cullingEnabled := false
	if pv.thumbnailFlat {
		cullingEnabled = gl.IsEnabled(gl.CULL_FACE)
		gl.Disable(gl.CULL_FACE)
	}
	sortedFileIds := slices.SortedFunc(maps.Keys(pv.objects), func(a, b stingray.FileID) int {
		return stingray.Hash.Cmp(a.Name, b.Name)
	})
	passes := 1
	if !pv.thumbnailFlat && !pv.showWireframe {
		passes = 2
	}
	for pass := range passes {
		drawingOverlays := pass == 1
		gl.DepthMask(!drawingOverlays)
		for _, fileId := range sortedFileIds {
			overlayKind := pv.sceneOverlayKind(fileId, "")
			if pv.rootHash.Type == LEVEL && overlayKind != SceneOverlayNone && !pv.SceneOverlayVisible(overlayKind) {
				continue
			}
			if passes == 2 && (overlayKind != SceneOverlayNone) != drawingOverlays {
				continue
			}
			sortedMeshes := slices.Sorted(maps.Keys(pv.objects[fileId]))
			for _, mesh := range sortedMeshes {
				list := pv.objectDrawList[fileId][mesh]
				instanceCount := int32(len(list)) / (7 * 4)
				object := pv.objects[fileId][mesh]

				gl.BindVertexArray(object.vao)
				if instanceCount > 0 && instanceCount != object.numInstances {
					gl.BindBuffer(gl.ARRAY_BUFFER, object.imbo)
					gl.BufferData(gl.ARRAY_BUFFER, len(list)*4, gl.Ptr(list), gl.STATIC_DRAW)
					object.numInstances = instanceCount
				} else if instanceCount != object.numInstances {
					object.numInstances = instanceCount
				}
				pv.objects[fileId][mesh] = object
				if object.numInstances == 0 {
					continue
				}
				animationSkinning := int32(0)
				if pv.animationEnabled && object.animSkinBuffer != 0 && object.animNormalBuffer != 0 {
					animationSkinning = 1
					gl.BindBufferBase(gl.SHADER_STORAGE_BUFFER, 5, object.animSkinBuffer)
					gl.BindBufferBase(gl.SHADER_STORAGE_BUFFER, 6, object.animNormalBuffer)
				} else {
					gl.BindBufferBase(gl.SHADER_STORAGE_BUFFER, 5, 0)
					gl.BindBufferBase(gl.SHADER_STORAGE_BUFFER, 6, 0)
				}

				if pv.thumbnailFlat {
					if !pv.boundingBoxMaterial.Use() {
						continue
					}
					if err := pv.boundingBoxMaterial.SetUniform("model", 1, model); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
						panic(err)
					}
					if err := pv.boundingBoxMaterial.SetUniform("view", 1, view); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
						panic(err)
					}
					if err := pv.boundingBoxMaterial.SetUniform("projection", 1, projection); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
						panic(err)
					}
					if err := pv.boundingBoxMaterial.SetUniform("color", 1, mgl32.Vec4{0.78, 0.81, 0.86, 1}); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
						panic(err)
					}
				} else if pv.showWireframe {
					if !object.wireframe.Use() {
						continue
					}
					if err := object.wireframe.SetUniform("model", 1, model); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
						panic(err)
					}
					if err := object.wireframe.SetUniform("view", 1, view); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
						panic(err)
					}
					if err := object.wireframe.SetUniform("projection", 1, projection); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
						panic(err)
					}
					if err := object.wireframe.SetUniform("useAnimationSkinning", 1, &animationSkinning); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
						panic(err)
					}
					if err := object.wireframe.SetUniform("color", 1, pv.wireframeColor); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
						panic(err)
					}
					if err := object.wireframe.SetUniform("hasVisibilityMasks", 1, &object.hasVisibilityMasks); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
						panic(err)
					}
					if err := object.wireframe.SetUniform("udimShown[0]", 64, pv.udimsShownFor(fileId)); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
						panic(err)
					}
				}
				for group, ibo := range object.ibos {
					if pv.thumbnailFlat && (group >= len(object.materials) || !object.materials[group].Shown()) {
						continue
					}
					if !pv.showWireframe && !pv.thumbnailFlat {
						if group >= len(object.materials) {
							continue
						}
						if !object.materials[group].Use() {
							continue
						}
						overlayOpacity := sceneOverlayOpacity(overlayKind)
						if err := object.materials[group].SetUniform("previewOpacity", 1, &overlayOpacity); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
							panic(err)
						}
						if err := object.materials[group].SetUniform("model", 1, model); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
							panic(err)
						}
						if err := object.materials[group].SetUniform("view", 1, view); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
							panic(err)
						}
						if err := object.materials[group].SetUniform("projection", 1, projection); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
							panic(err)
						}
						if err := object.materials[group].SetUniform("viewPosition", 1, viewPosition); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
							panic(err)
						}
						if err := object.materials[group].SetUniform("useAnimationSkinning", 1, &animationSkinning); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
							panic(err)
						}
						if err := object.materials[group].SetUniform("udimShown[0]", 64, pv.udimsShownFor(fileId)); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
							panic(err)
						}
					}
					gl.BindBuffer(gl.ELEMENT_ARRAY_BUFFER, ibo)
					gl.DrawElementsInstanced(gl.TRIANGLES, object.numIndices[group], object.indexType[group], nil, instanceCount)
				}
			}
		}
	}
	gl.DepthMask(true)
	if pv.thumbnailFlat {
		if cullingEnabled {
			gl.Enable(gl.CULL_FACE)
		} else {
			gl.Disable(gl.CULL_FACE)
		}
	}
	gl.ActiveTexture(gl.TEXTURE0)
	gl.BindTexture(gl.TEXTURE_2D, 0)
	gl.BindTexture(gl.TEXTURE_2D_ARRAY, 0)
	gl.BindTexture(gl.TEXTURE_BUFFER, 0)
	gl.BindVertexArray(0)
	gl.UseProgram(0)
	gl.PolygonMode(gl.FRONT_AND_BACK, gl.FILL)
	if !blendingWasEnabled {
		gl.Disable(gl.BLEND)
	}
}

func (pv *UnitPreviewState) drawListsNormalVis(size imgui.Vec2, model, view, projection mgl32.Mat4) {
	gl.Enable(gl.DEPTH_TEST)
	sortedFileIds := slices.SortedFunc(maps.Keys(pv.objects), func(a, b stingray.FileID) int {
		return stingray.Hash.Cmp(a.Name, b.Name)
	})
	for _, fileId := range sortedFileIds {
		sortedMeshes := slices.Sorted(maps.Keys(pv.objects[fileId]))
		for _, mesh := range sortedMeshes {
			list := pv.objectDrawList[fileId][mesh]
			instanceCount := int32(len(list)) / (7 * 4)
			object := pv.objects[fileId][mesh]

			gl.BindVertexArray(object.vao)
			if instanceCount > 0 && instanceCount != object.numInstances {
				gl.BindBuffer(gl.ARRAY_BUFFER, object.imbo)
				gl.BufferData(gl.ARRAY_BUFFER, len(list)*4, gl.Ptr(list), gl.STATIC_DRAW)
				object.numInstances = instanceCount
			} else if instanceCount != object.numInstances {
				object.numInstances = instanceCount
			}
			pv.objects[fileId][mesh] = object
			if object.numInstances == 0 {
				continue
			}
			animationSkinning := int32(0)
			if pv.animationEnabled && object.animSkinBuffer != 0 && object.animNormalBuffer != 0 {
				animationSkinning = 1
				gl.BindBufferBase(gl.SHADER_STORAGE_BUFFER, 5, object.animSkinBuffer)
				gl.BindBufferBase(gl.SHADER_STORAGE_BUFFER, 6, object.animNormalBuffer)
			} else {
				gl.BindBufferBase(gl.SHADER_STORAGE_BUFFER, 5, 0)
				gl.BindBufferBase(gl.SHADER_STORAGE_BUFFER, 6, 0)
			}

			if !object.normalVis.Use() {
				continue
			}
			if err := object.normalVis.SetUniform("model", 1, model); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
				// these should work, programmer error if they don't
				panic(err)
			}
			if err := object.normalVis.SetUniform("view", 1, view); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
				panic(err)
			}
			if err := object.normalVis.SetUniform("projection", 1, projection); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
				panic(err)
			}
			if err := object.normalVis.SetUniform("useAnimationSkinning", 1, &animationSkinning); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
				panic(err)
			}
			normalLength := pv.viewDistance * 0.02
			if err := object.normalVis.SetUniform("len", 1, &normalLength); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
				panic(err)
			}
			if err := object.normalVis.SetUniform("showTangentBitangent", 1, &pv.visualizeTangentBitangent); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
				panic(err)
			}
			if err := object.normalVis.SetUniform("hasVisibilityMasks", 1, &object.hasVisibilityMasks); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
				panic(err)
			}
			if err := object.normalVis.SetUniform("udimShown[0]", 64, pv.udimsShownFor(fileId)); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
				panic(err)
			}
			for group, ibo := range object.ibos {
				gl.BindBuffer(gl.ELEMENT_ARRAY_BUFFER, ibo)
				gl.DrawElementsInstanced(gl.POINTS, object.numIndices[group], object.indexType[group], nil, instanceCount)
			}
		}
	}
	gl.ActiveTexture(gl.TEXTURE0)
	gl.BindTexture(gl.TEXTURE_2D, 0)
	gl.BindTexture(gl.TEXTURE_2D_ARRAY, 0)
	gl.BindTexture(gl.TEXTURE_BUFFER, 0)
	gl.BindVertexArray(0)
	gl.UseProgram(0)
	gl.PolygonMode(gl.FRONT_AND_BACK, gl.FILL)
}

func (pv *UnitPreviewState) drawListsBoundingBoxes(size imgui.Vec2, model, view, projection mgl32.Mat4) {
	if !pv.boundingBoxMaterial.Use() {
		return
	}
	gl.Disable(gl.DEPTH_TEST)
	if err := pv.boundingBoxMaterial.SetUniform("color", 1, pv.aabbColor); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
		// these should work, programmer error if they don't
		panic(err)
	}
	if err := pv.boundingBoxMaterial.SetUniform("model", 1, model); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
		panic(err)
	}
	if err := pv.boundingBoxMaterial.SetUniform("view", 1, view); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
		panic(err)
	}
	if err := pv.boundingBoxMaterial.SetUniform("projection", 1, projection); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
		panic(err)
	}
	sortedFileIds := slices.SortedFunc(maps.Keys(pv.objects), func(a, b stingray.FileID) int {
		return stingray.Hash.Cmp(a.Name, b.Name)
	})
	for _, fileId := range sortedFileIds {
		sortedMeshes := slices.Sorted(maps.Keys(pv.objects[fileId]))
		for _, mesh := range sortedMeshes {
			list := pv.objectDrawList[fileId][mesh]
			instanceCount := int32(len(list)) / (7 * 4)
			object := pv.boundingBoxes[fileId][mesh]

			gl.BindVertexArray(object.vao)
			if instanceCount > 0 && instanceCount != object.numInstances {
				gl.BindBuffer(gl.ARRAY_BUFFER, object.imbo)
				gl.BufferData(gl.ARRAY_BUFFER, len(list)*4, gl.Ptr(list), gl.STATIC_DRAW)
				object.numInstances = instanceCount
			} else if instanceCount != object.numInstances {
				object.numInstances = instanceCount
			}
			pv.boundingBoxes[fileId][mesh] = object
			if object.numInstances == 0 {
				continue
			}

			gl.BindVertexArray(object.vao)
			gl.DrawElementsInstanced(gl.TRIANGLES, object.numIndices[0], gl.UNSIGNED_INT, nil, object.numInstances)
		}
	}
	gl.BindVertexArray(0)
	gl.UseProgram(0)
	gl.PolygonMode(gl.FRONT_AND_BACK, gl.FILL)
}

const filediverSkeleton = "filediver_skeleton"

func (pv *UnitPreviewState) drawListsSkeletons(size imgui.Vec2, model, view, projection mgl32.Mat4) {
	if !pv.boundingBoxMaterial.Use() {
		return
	}
	gl.Disable(gl.DEPTH_TEST)
	if err := pv.boundingBoxMaterial.SetUniform("color", 1, pv.skeletonColor); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
		// these should work, programmer error if they don't
		panic(err)
	}
	if err := pv.boundingBoxMaterial.SetUniform("model", 1, model); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
		panic(err)
	}
	if err := pv.boundingBoxMaterial.SetUniform("view", 1, view); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
		panic(err)
	}
	if err := pv.boundingBoxMaterial.SetUniform("projection", 1, projection); err != nil && err != preview_mats.UNIFORM_NOT_FOUND {
		panic(err)
	}
	sortedFileIds := slices.SortedFunc(maps.Keys(pv.objects), func(a, b stingray.FileID) int {
		return stingray.Hash.Cmp(a.Name, b.Name)
	})
	for _, fileId := range sortedFileIds {
		list := pv.objectDrawList[fileId][filediverSkeleton]
		instanceCount := int32(len(list)) / (7 * 4)
		object := pv.skeletons[fileId]

		gl.BindVertexArray(object.vao)
		if instanceCount > 0 && instanceCount != object.numInstances {
			gl.BindBuffer(gl.ARRAY_BUFFER, object.imbo)
			gl.BufferData(gl.ARRAY_BUFFER, len(list)*4, gl.Ptr(list), gl.STATIC_DRAW)
			object.numInstances = instanceCount
		} else if instanceCount != object.numInstances {
			object.numInstances = instanceCount
		}
		pv.skeletons[fileId] = object
		if object.numInstances == 0 {
			continue
		}

		gl.BindVertexArray(object.vao)
		gl.BindBuffer(gl.ELEMENT_ARRAY_BUFFER, object.ibos[0])
		gl.DrawElementsInstanced(gl.LINES, object.numIndices[0], gl.UNSIGNED_INT, nil, object.numInstances)
	}
	gl.BindVertexArray(0)
	gl.UseProgram(0)
	gl.PolygonMode(gl.FRONT_AND_BACK, gl.FILL)
}

func (pv *UnitPreviewState) drawObject(hash stingray.FileID, matrix, sceneModel mgl32.Mat4) {
	for name := range pv.objects[hash] {
		if shown, contains := pv.objectsShown[hash][name]; contains && !shown {
			continue
		}
		pv.initDrawList(hash, name)
		model := matrix.Mul4(pv.objects[hash][name].matrix)
		normal := sceneModel.Mul4(model).Inv().Transpose()
		pv.objectDrawList[hash][name] = append(pv.objectDrawList[hash][name], model[:]...)
		pv.objectDrawList[hash][name] = append(pv.objectDrawList[hash][name], normal[:12]...)
	}
}

func (pv *UnitPreviewState) drawBoundingBox(hash stingray.FileID, matrix, _ mgl32.Mat4) {
	for name := range pv.boundingBoxes[hash] {
		if shown, contains := pv.objectsShown[hash][name]; contains && !shown {
			continue
		}
		pv.initDrawList(hash, name)
		model := matrix.Mul4(pv.aabbMat[hash][name])
		pv.objectDrawList[hash][name] = append(pv.objectDrawList[hash][name], model[:]...)
		pv.objectDrawList[hash][name] = append(pv.objectDrawList[hash][name], make([]float32, 12)...)
	}
}

func (pv *UnitPreviewState) drawSkeleton(hash stingray.FileID, matrix, _ mgl32.Mat4) {
	pv.initDrawList(hash, filediverSkeleton)
	pv.objectDrawList[hash][filediverSkeleton] = append(pv.objectDrawList[hash][filediverSkeleton], matrix[:]...)
	pv.objectDrawList[hash][filediverSkeleton] = append(pv.objectDrawList[hash][filediverSkeleton], make([]float32, 12)...)
}

func (pv *UnitPreviewState) getDrawNodeRecursiveFunc(drawCallback func(stingray.FileID, mgl32.Mat4, mgl32.Mat4), sceneModel mgl32.Mat4) (drawNode func(unitPreviewNode, stingray.FileID, mgl32.Mat4)) {
	drawNode = func(curr unitPreviewNode, nodeId stingray.FileID, matrix mgl32.Mat4) {
		if !curr.shown {
			return
		}
		if pv.rootHash.Type == LEVEL && curr.overlayKind != SceneOverlayNone && !pv.SceneOverlayVisible(curr.overlayKind) {
			return
		}
		for _, childHash := range curr.children {
			if child, contains := pv.nodes[childHash.Name]; contains && nodeId != childHash {
				drawNode(child, childHash, matrix.Mul4(child.matrix))
			} else {
				// add to the draw list
				drawCallback(childHash, matrix, sceneModel)
			}
		}
	}
	return
}

func (pv *UnitPreviewState) Draw(previewId string) {
	if len(pv.objects) == 0 {
		return
	}

	imgui.PushIDStr(previewId)
	defer imgui.PopID()

	viewSize := imgui.ContentRegionAvail()
	if pv.captureMode {
		viewSize = imgui.NewVec2(512, 512)
	} else {
		viewSize.Y -= imutils.CheckboxHeight()
	}

	if pv.animTime == -1 || pv.animTime >= 1 {
		pv.animOrigViewDistance = pv.viewDistance
		pv.animOrigViewRotation = pv.viewRotation
		pv.animOrigModelPos = pv.modelPos
		pv.animTime = -1
	}

	widgets.GLView(previewId, pv.fb, viewSize,
		func() {
			if pv.captureMode {
				return
			}
			io := imgui.CurrentIO()

			if imgui.IsItemActive() {
				md := io.MouseDelta()
				md4 := mgl32.Vec4{md.X, -md.Y, 0.0, 1.0}
				if io.KeyShift() && md4.Vec2().LenSqr() > 0 {
					modelPos, _, view, projection := pv.computeMVP(viewSize.X/viewSize.Y, false)
					modelViewProj := projection.Mul4(view).Mul4(pv.model)
					invModelViewProjection := modelViewProj.Inv()

					projected := modelViewProj.Mul4x1(modelPos.Vec3().Vec4(1.0))
					// Set depth to current model position
					md4[2] = projected.Z() / projected.W()

					positionDelta := invModelViewProjection.Mul4x1(md4)
					positionDelta = positionDelta.Mul(1 / positionDelta.W())
					pv.modelPos = modelPos.Add(positionDelta.Mul(io.DeltaTime() / 2).Vec3().Vec4(0.0))
				} else {
					pv.viewRotation = pv.viewRotation.Add(mgl32.Vec2{md.X, md.Y}.Mul(-0.01))
					pv.viewRotation[1] = mgl32.Clamp(pv.viewRotation[1], -1.55, 1.55)
				}
			}
			if imgui.IsItemDeactivated() && pv.autoZoomEnabled {
				pv.doAutoZoomNextFrame = true
			}
			if imgui.IsItemHovered() {
				scroll := io.MouseWheel()
				pv.viewDistance -= 0.1 * pv.viewDistance * scroll
				if scroll != 0 {
					pv.autoZoomEnabled = false
				}
			}
			pv.viewDistance = mgl32.Clamp(
				pv.viewDistance,
				0.001,
				pv.maxViewDistance,
			)
		},
		func(pos, size imgui.Vec2) {
			gl.ClearColor(0.2, 0.2, 0.2, 1)
			gl.Clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT)
			if err := pv.finalizeObjectMaterials(); err != nil {
				fmt.Printf("[warning] finalizing preview textures: %v\n", err)
			}
			if pv.animationEnabled {
				pv.updateAnimationPose()
			}

			modelPos, viewPosition, view, projection := pv.computeMVP(size.X/size.Y, true)
			sceneModel := pv.previewSceneModelMatrix(modelPos)

			drawNode := pv.getDrawNodeRecursiveFunc(pv.drawObject, sceneModel)
			drawNodeBoundingBox := pv.getDrawNodeRecursiveFunc(pv.drawBoundingBox, mgl32.Mat4{})
			drawNodeSkeletons := pv.getDrawNodeRecursiveFunc(pv.drawSkeleton, mgl32.Mat4{})
			drawNode(pv.root, pv.rootHash, pv.root.matrix)
			pv.drawLists(size, sceneModel, view, projection, viewPosition)
			if pv.visualizeNormals {
				pv.drawListsNormalVis(size, sceneModel, view, projection)
			}
			pv.objectDrawList = nil
			if pv.showAABB {
				drawNodeBoundingBox(pv.root, pv.rootHash, pv.root.matrix)
				pv.drawListsBoundingBoxes(size, sceneModel, view, projection)
				pv.objectDrawList = nil
			}
			if !pv.captureMode && pv.rootHash.Type == UNIT && (pv.showPhysicsHitboxes || pv.showDamageBoxes) && pv.damageOverlayLoaded {
				pv.drawDamageOverlayBoxes(pv.previewBaseSceneModelMatrix(modelPos), view, projection)
			}
			if pv.showSkeleton {
				drawNodeSkeletons(pv.root, pv.rootHash, pv.root.matrix)
				pv.drawListsSkeletons(size, sceneModel, view, projection)
				pv.objectDrawList = nil
			}

			gl.BindVertexArray(0)
			gl.UseProgram(0)

			if pv.doAutoZoomNextFrame {
				pv.viewDistance = pv.maxViewDistance

				_, viewPosition, view, projection := pv.computeMVP(size.X/size.Y, false)
				sceneModel := pv.previewSceneModelMatrix(pv.modelPos)

				fitVertexCamDistDelta := func(vertex mgl32.Vec3) (float32, bool) {
					v := sceneModel.Mul4x1(vertex.Vec4(1.0))
					if v.W() == 0 {
						return 0, false
					}
					v = v.Mul(1 / v.W())
					projV := projection.Mul4x1(view.Mul4x1(v))
					if projV.W() <= 0 || math.IsNaN(float64(projV.W())) || math.IsInf(float64(projV.W()), 0) {
						return 0, false
					}
					projV = projV.Mul(1 / projV.W())

					// Component with maximum distance from screen center
					maxDist := max(mgl32.Abs(projV.X()), mgl32.Abs(projV.Y()))

					// Calculate orthogonal distance from camera to selected vertex
					var od float32
					{
						viewDir := mgl32.Vec3{}.Sub(viewPosition).Normalize()
						vert := v
						vert = vert.Mul(1 / vert.W())
						camToVert := vert.Vec3().Sub(viewPosition)
						od = viewDir.Dot(camToVert)
					}
					if od <= 0 || math.IsNaN(float64(od)) || math.IsInf(float64(od), 0) || math.IsNaN(float64(maxDist)) || math.IsInf(float64(maxDist), 0) {
						return 0, false
					}

					// Fit model to screen
					return od * (maxDist - 1), true
				}

				var maxCamDistDelta float32
				hasFitVertex := false
				var fitVisibleNode func(unitPreviewNode, stingray.FileID, mgl32.Mat4)
				fitVisibleNode = func(curr unitPreviewNode, nodeId stingray.FileID, matrix mgl32.Mat4) {
					if !curr.shown {
						return
					}
					for _, childHash := range curr.children {
						if child, contains := pv.nodes[childHash.Name]; contains && nodeId != childHash {
							fitVisibleNode(child, childHash, matrix.Mul4(child.matrix))
							continue
						}
						for name := range pv.objects[childHash] {
							if shown, exists := pv.objectsShown[childHash][name]; exists && !shown {
								continue
							}
							boundsMatrix := matrix.Mul4(pv.aabbMat[childHash][name])
							for _, vert := range pv.getAABBVertices(childHash, name) {
								delta, ok := fitVertexCamDistDelta(boundsMatrix.Mul4x1(vert.Vec4(1.0)).Vec3())
								if !ok {
									continue
								}
								if !hasFitVertex || delta > maxCamDistDelta {
									maxCamDistDelta = delta
									hasFitVertex = true
								}
							}
						}
					}
				}
				fitVisibleNode(pv.root, pv.rootHash, pv.root.matrix)
				if hasFitVertex {
					pv.viewDistance += maxCamDistDelta
					pv.viewDistance *= 1.02
				}

				pv.doAutoZoomNextFrame = false

				pv.animTime = 0
			}
		},
		func(pos, size imgui.Vec2) {
			if pv.captureMode {
				return
			}
			dl := imgui.WindowDrawList()
			if pv.showDamageReadout && pv.rootHash.Type == UNIT && pv.damageOverlayLoaded {
				pv.drawDamageOverlayReadouts(dl, pos, size)
			}

			// Scale indicator
			{
				// Screen size in world here refers to how large the screen
				// content rectangle would be if it intersected
				// the origin.
				// tan(vfov/2) = screenHeightInWorld/camDist
				screenHeightInWorld := float32(math.Tan(float64(pv.vfov/2)) * float64(pv.viewDistance))
				screenWidthInWorld := screenHeightInWorld / size.Y * size.X
				indicatorWidthInWorld := screenWidthInWorld / 2
				{
					order := float32(
						math.Pow(
							10,
							math.Floor(math.Log10(float64(indicatorWidthInWorld)))-1,
						),
					)
					indicatorWidthInWorld = order * float32(math.Floor(float64(indicatorWidthInWorld/order)))
				}
				indicatorColor := imgui.ColorU32Col(imgui.ColText)

				indicatorWidth := size.X * indicatorWidthInWorld / screenWidthInWorld
				indicatorPos := pos.Add(imutils.SVec2(10, 10))
				dl.AddRectFilled(
					indicatorPos.Add(imutils.SVec2(0, 0)),
					indicatorPos.Add(imutils.SVec2(2, 10)),
					indicatorColor,
				)
				dl.AddRectFilled(
					indicatorPos.Add(imutils.SVec2(0, 4)),
					indicatorPos.Add(imgui.NewVec2(indicatorWidth, 0).Add(imutils.SVec2(0, 6))),
					indicatorColor,
				)
				dl.AddRectFilled(
					indicatorPos.Add(imgui.NewVec2(indicatorWidth, 0).Add(imutils.SVec2(-2, 0))),
					indicatorPos.Add(imgui.NewVec2(indicatorWidth, 0).Add(imutils.SVec2(0, 10))),
					indicatorColor,
				)

				var dimPrefix string
				var dim float32
				if indicatorWidthInWorld >= 1e3 {
					dimPrefix = "k"
					dim = 1e3
				} else if indicatorWidthInWorld >= 1 {
					dimPrefix = ""
					dim = 1
				} else if indicatorWidthInWorld >= 1e-2 {
					dimPrefix = "c"
					dim = 1e-2
				} else if indicatorWidthInWorld >= 1e-3 {
					dimPrefix = "m"
					dim = 1e-3
				} else {
					dimPrefix = "µ"
					dim = 1e-6
				}
				text := fmt.Sprintf(
					"%v%vm",
					strings.TrimRight(strings.TrimRight(
						fmt.Sprintf("%.3f", indicatorWidthInWorld/dim),
						"0"), "."),
					dimPrefix,
				)
				textSize := imgui.CalcTextSize(text)
				textPos := indicatorPos.Add(imgui.NewVec2(indicatorWidth/2-textSize.X/2, 0)).Add(imutils.SVec2(0, 12))
				dl.AddRectFilled(
					textPos.Add(imutils.SVec2(-4, 0)),
					textPos.Add(textSize).Add(imutils.SVec2(4, 0)),
					imgui.ColorU32Vec4(imgui.NewVec4(0, 0, 0, 0.5)),
				)
				dl.AddTextVec2(
					textPos,
					indicatorColor,
					text,
				)

			}

			modelPos, _, view, projection := pv.computeMVP(size.X/size.Y, false)
			// Show axes
			{
				// Mirror across the x axis
				mirror := mgl32.Mat4FromRows(
					mgl32.Vec4{1.0, 0.0, 0.0, 0.0},
					mgl32.Vec4{0.0, -1.0, 0.0, 0.0},
					mgl32.Vec4{0.0, 0.0, 1.0, 0.0},
					mgl32.Vec4{0.0, 0.0, 0.0, 1.0},
				)
				xPoint := mirror.Mul4x1(view.Mul4x1(mgl32.Vec4{1.0, 0.0, 0.0, 1.0}))
				yPoint := mirror.Mul4x1(view.Mul4x1(mgl32.Vec4{0.0, 1.0, 0.0, 1.0}))
				zPoint := mirror.Mul4x1(view.Mul4x1(mgl32.Vec4{0.0, 0.0, 1.0, 1.0}))
				crossHatching := make([][4]mgl32.Vec4, 11)
				for i := range crossHatching {
					x := float32(2*i+1)/float32(len(crossHatching)) - 1.0
					y := float32(math.Sqrt(1.0 - float64(x*x)))
					crossHatching[i][0] = mirror.Mul4x1(view.Mul4x1(mgl32.Vec4{-y, 0.0, x, 1.0}))
					crossHatching[i][1] = mirror.Mul4x1(view.Mul4x1(mgl32.Vec4{y, 0.0, x, 1.0}))
					crossHatching[i][2] = mirror.Mul4x1(view.Mul4x1(mgl32.Vec4{x, 0.0, -y, 1.0}))
					crossHatching[i][3] = mirror.Mul4x1(view.Mul4x1(mgl32.Vec4{x, 0.0, y, 1.0}))
				}

				origin := pos.Add(size.Sub(imutils.SVec2(2.0, 1.5).Mul(50.0)))
				x := imutils.SVec2(xPoint.Vec2().Elem()).Mul(50.0).Add(origin)
				y := imutils.SVec2(yPoint.Vec2().Elem()).Mul(50.0).Add(origin)
				z := imutils.SVec2(zPoint.Vec2().Elem()).Mul(50.0).Add(origin)
				minorRadius := math.Sqrt(1.0 - float64(yPoint.Vec2().LenSqr()))
				radius := imutils.SVec2(1.0, minorRadius).Mul(50.0)
				red := imgui.NewVec4(1.0, 0.0, 0.0, 1.0)
				green := imgui.NewVec4(0.0, 1.0, 0.0, 1.0)
				blue := imgui.NewVec4(0.0, 0.0, 1.0, 1.0)
				dl.AddCircleFilled(
					origin,
					imutils.S(50.0)+imgui.CurrentStyle().ItemInnerSpacing().X,
					imgui.ColorConvertFloat4ToU32(imgui.NewVec4(0, 0, 0, 0.5)),
				)
				for i := range crossHatching {
					a := imutils.SVec2(crossHatching[i][0].Vec2().Elem()).Mul(50.0).Add(origin)
					b := imutils.SVec2(crossHatching[i][1].Vec2().Elem()).Mul(50.0).Add(origin)
					c := imutils.SVec2(crossHatching[i][2].Vec2().Elem()).Mul(50.0).Add(origin)
					d := imutils.SVec2(crossHatching[i][3].Vec2().Elem()).Mul(50.0).Add(origin)
					dl.AddLineArgs(a, b, imgui.ColorConvertFloat4ToU32(imgui.NewVec4(0.5, 0.5, 0.5, 0.5)), 1.0)
					dl.AddLineArgs(c, d, imgui.ColorConvertFloat4ToU32(imgui.NewVec4(0.5, 0.5, 0.5, 0.5)), 1.0)
				}
				dl.AddEllipse(origin, radius, imgui.ColorConvertFloat4ToU32(imgui.NewVec4(0.5, 0.5, 0.5, 0.5)))
				dl.AddLineArgs(origin, x, imgui.ColorConvertFloat4ToU32(red), 1.5)
				dl.AddLineArgs(origin, z, imgui.ColorConvertFloat4ToU32(blue), 1.5)
				dl.AddLineArgs(origin, y, imgui.ColorConvertFloat4ToU32(green), 1.5)
			}
			sceneModelForNormals := pv.previewSceneModelMatrix(modelPos)

			// Show hovered vertex info
			if pv.visualizeNormals {
				igRelMousePos := imgui.MousePos().Sub(pos)
				mousePos := mgl32.Vec2{
					igRelMousePos.X,
					igRelMousePos.Y,
				}
				var closestPos mgl32.Vec2
				closestDist := float32(math.MaxFloat32)
				var closestVertex, closestNormal [3]float32
				var findNearestNormal func(unitPreviewNode, stingray.FileID)
				findNearestNormal = func(curr unitPreviewNode, nodeId stingray.FileID) {
					if !curr.shown {
						return
					}
					for _, childHash := range curr.children {
						if child, contains := pv.nodes[childHash.Name]; contains && nodeId != childHash {
							child.matrix = curr.matrix.Mul4(child.matrix)
							findNearestNormal(child, childHash)
						} else {
							for name := range pv.meshPositions[childHash] {
								if shown, contains := pv.objectsShown[childHash][name]; contains && !shown {
									continue
								}
								if len(pv.meshPositions[childHash][name]) != len(pv.meshNormals[childHash][name]) {
									continue
								}
								model := sceneModelForNormals.Mul4(curr.matrix.Mul4(pv.objects[childHash][name].matrix))
								normalMat := model.Inv().Transpose().Mat3()
								mvp := projection.Mul4(view).Mul4(model)
								for i, vtx := range pv.meshPositions[childHash][name] {
									v := mvp.Mul4x1(mgl32.Vec3(vtx).Vec4(1.0))
									v = v.Mul(1 / v.W())
									v[0] = (v[0] + 1) * size.X * 0.5
									v[1] = (-v[1] + 1) * size.Y * 0.5
									dist := mousePos.Sub(v.Vec2()).LenSqr()
									if dist < closestDist {
										closestPos = v.Vec2()
										closestDist = dist
										closestVertex = model.Mul4x1(mgl32.Vec3(vtx).Vec4(1.0)).Vec3()
										normal := pv.meshNormals[childHash][name][i]
										closestNormal = normalMat.Mul3x1(normal)
									}
								}
							}
						}
					}
				}
				findNearestNormal(pv.root, pv.rootHash)
				markerPos := pos.Add(imgui.NewVec2(closestPos.X(), closestPos.Y()))
				dl.AddCircleFilled(
					markerPos,
					imutils.S(2),
					imgui.ColorU32Vec4(imgui.NewVec4(1, 0, 0, 1)),
				)
				dl.AddTextVec2(
					markerPos,
					imgui.ColorU32Vec4(imgui.NewVec4(1, 1, 0, 1)),
					fmt.Sprintf("Pos: %v\nNormal: %v", closestVertex, closestNormal),
				)
			}
		},
	)

	if pv.captureMode {
		if pv.animTime != -1 {
			pv.animTime += 20 * imgui.CurrentIO().DeltaTime()
		}
		return
	}

	if imgui.Button(fnt.I.Home) {
		pv.viewRotation = mgl32.Vec2{math.Pi, 0.0}
		pv.modelPos = mgl32.Vec4{0, 0, 0, 1}
		for hash := range pv.objects {
			for name := range pv.objects[hash] {
				if shown, contains := pv.objectsShown[hash][name]; contains && !shown {
					continue
				}
				pv.modelPos = pv.objects[hash][name].matrix.Inv().Mul4x1(mgl32.Vec4{0, 0, 0, 1})
				break
			}
		}
		pv.doAutoZoomNextFrame = true
		pv.animTime = 0
	}
	imgui.SetItemTooltip("Reset view")
	imgui.SameLine()
	if imgui.Button(fnt.I.DataObject + " Debug info") {
		imgui.OpenPopupStr("Debug info")
	}
	imgui.SetItemTooltip("Mesh information, display settings, and damage-state controls")
	if imgui.BeginPopup("Debug info") {
		imgui.TextUnformatted("Mesh info")
		imgui.Indent()
		var numVertices, numIndices, numTriangles int = 0, 0, 0
		for hash := range pv.objects {
			for name := range pv.objects[hash] {
				if shown, ok := pv.objectsShown[hash][name]; ok && !shown {
					continue
				}
				numVertices += int(pv.objects[hash][name].numVertices)
				for _, indexCount := range pv.objects[hash][name].numIndices {
					numIndices += int(indexCount)
					numTriangles += int(indexCount / 3)
				}
			}
		}
		imutils.Textf("Vertices: %v", numVertices)
		imutils.Textf("Triangles: %v", numTriangles)
		imutils.Textf("Indices: %v", numIndices)
		imgui.Unindent()

		imgui.Separator()

		const colorPickerFlags = imgui.ColorEditFlagsNoInputs | imgui.ColorEditFlagsAlphaBar | imgui.ColorEditFlagsNoLabel
		imgui.TextUnformatted("Display")
		imgui.Indent()
		imgui.Checkbox("Wireframe mode", &pv.showWireframe)
		imgui.SameLineV(imutils.S(170), -1)
		imgui.ColorEdit4V("Wireframe color", (*[4]float32)(&pv.wireframeColor), colorPickerFlags)

		imgui.Checkbox("Show Skeleton", &pv.showSkeleton)
		imgui.SameLineV(imutils.S(170), -1)
		imgui.ColorEdit4V("Skeleton color", (*[4]float32)(&pv.skeletonColor), colorPickerFlags)

		imgui.Checkbox("Bounding box", &pv.showAABB)
		imgui.SameLine()
		imgui.TextUnformatted(fnt.I.Warning)
		imgui.SetItemTooltip("Bounding boxes are known to sometimes be wrong")
		imgui.SameLineV(imutils.S(170), -1)
		imgui.ColorEdit4V("Bounding box color", (*[4]float32)(&pv.aabbColor), colorPickerFlags)

		imgui.Checkbox("Vertex normals", &pv.visualizeNormals)
		imgui.SetItemTooltip("Normal is blue")
		{
			imgui.BeginDisabledV(!pv.visualizeNormals)
			check := pv.visualizeTangentBitangent != 0
			imgui.Checkbox("Vertex tangent and bitangent", &check)
			if pv.visualizeNormals {
				imgui.SetItemTooltip("Tangent is red, bitangent is green")
			} else {
				imgui.SetItemTooltip("Requires normals to be shown")
			}
			pv.visualizeTangentBitangent = 0
			if check {
				pv.visualizeTangentBitangent = 1
			}
			imgui.EndDisabled()
		}
		imgui.Unindent()

		imgui.Separator()
		imgui.TextUnformatted("Damage state")
		pv.drawDamageModelControl()

		imgui.EndPopup()
	}
	imgui.SameLine()
	pv.drawDamageOverlayControls()
	imgui.SameLine()
	if imgui.Checkbox(fnt.I.AllOut+" Auto-zoom", &pv.autoZoomEnabled) && pv.autoZoomEnabled {
		pv.doAutoZoomNextFrame = true
	}
	imgui.SameLine()
	imgui.BeginDisabledV(pv.isHelldivers1 || pv.rootHash.Type != UNIT)
	showMountedAttachments := pv.mountedAttachmentsEnabled
	if imgui.Checkbox("Mounted models", &showMountedAttachments) {
		if err := pv.SetMountedAttachmentsEnabled(showMountedAttachments); err != nil {
			fmt.Printf("showing mounted models: %v\n", err)
		}
	}
	imgui.EndDisabled()
	tooltip := "Load models referenced by this unit's mount records and place them on their named skeleton bones."
	if pv.isHelldivers1 {
		tooltip = "Mounted-model records are only available for Helldivers 2 units."
	} else if pv.rootHash.Type != UNIT {
		tooltip = "Select a unit file to show its mounted models."
	} else if warnings := pv.mountedAttachmentWarningsText(); warnings != "" {
		tooltip += "\n\n" + warnings
	}
	imgui.SetItemTooltip(tooltip)
	if pv.maxLODLevel > 0 {
		imgui.SameLine()
		imgui.SetNextItemWidth(imutils.S(130))
		selectedLOD := int32(pv.lodLevel)
		if imgui.SliderInt("LOD", &selectedLOD, 0, int32(pv.maxLODLevel)) {
			if err := pv.SetLOD(int(selectedLOD)); err != nil {
				fmt.Printf("changing preview LOD: %v\n", err)
			} else {
				pv.doAutoZoomNextFrame = true
				pv.animTime = 0
			}
		}
	}

	imgui.SameLine()
	imgui.BeginDisabledV(pv.numUdims <= 1)
	label := "Visibility Masks Selection"
	if pv.numUdims <= 1 || !pv.udimsSettingsShown {
		label = "Show " + label
	} else {
		label = "Hide " + label
	}
	if imgui.Button(label) {
		pv.udimsSettingsShown = !pv.udimsSettingsShown
	}
	if pv.numUdims <= 1 {
		imgui.SetItemTooltip("Unit has no visibility masks")
	}
	imgui.EndDisabled()
	if !pv.udimsSettingsShown || !pv.udimsSettingsDrawn {
		for i := range pv.udimsShown {
			if pv.udimsSelected[i] {
				pv.udimsShown[i] = 1
			} else {
				pv.udimsShown[i] = 0
			}
		}
	}
	pv.syncRootVisibilityMasks()

	imgui.SameLine()
	label = "Tree View"
	if !pv.treeViewShown {
		label = "Show " + label
	} else {
		label = "Hide " + label
	}
	if imgui.Button(label) {
		pv.treeViewShown = !pv.treeViewShown
	}
	if pv.rootHash.Type == LEVEL {
		imgui.SameLine()
		if imgui.Button("Visibility") {
			imgui.OpenPopupStr("##LevelVisibility")
		}
		imgui.SetItemTooltip("Level visibility options")
		if imgui.BeginPopup("##LevelVisibility") {
			hideGround := pv.hideLevelGround
			if imgui.Checkbox("Hide ground patch", &hideGround) {
				pv.SetHideLevelGround(hideGround)
			}
			imgui.SetItemTooltip("Hide the generated terrain patch used as the level's ground preview.")

			volumeVisible := pv.SceneOverlayVisible(SceneOverlayVolumes)
			if imgui.Checkbox("Level volume helpers", &volumeVisible) {
				pv.SetSceneOverlayVisible(SceneOverlayVolumes, volumeVisible)
			}
			imgui.SetItemTooltip("Show or hide tutorial triggers, kill zones, and landing-zone markers.")

			audioVolumesVisible := pv.SceneOverlayVisible(SceneOverlayAudioVolumes)
			if imgui.Checkbox("Audio volumes and portals", &audioVolumesVisible) {
				pv.SetSceneOverlayVisible(SceneOverlayAudioVolumes, audioVolumesVisible)
			}
			imgui.SetItemTooltip("Show or hide Wwise audio volumes, portals, and reverb regions. These colored boxes mark spatial audio areas, not solid map structures.")

			avoidanceVolumesVisible := pv.SceneOverlayVisible(SceneOverlayAvoidanceVolumes)
			if imgui.Checkbox("Hellpod avoidance boxes", &avoidanceVolumesVisible) {
				pv.SetSceneOverlayVisible(SceneOverlayAvoidanceVolumes, avoidanceVolumesVisible)
			}
			imgui.SetItemTooltip("Show or hide the primitive boxes that mark areas where hellpods cannot land. These are gameplay placement helpers, not solid map structures.")

			rendererHelpersVisible := pv.SceneOverlayVisible(SceneOverlayRendererHelpers)
			if imgui.Checkbox("Renderer helper volumes", &rendererHelpersVisible) {
				pv.SetSceneOverlayVisible(SceneOverlayRendererHelpers, rendererHelpersVisible)
			}
			imgui.SetItemTooltip("Show or hide Stingray editor helper meshes such as occluders and reflection probes.")

			decalBoxesVisible := pv.SceneOverlayVisible(SceneOverlayDecalBoxes)
			if imgui.Checkbox("Decal projection boxes", &decalBoxesVisible) {
				pv.SetSceneOverlayVisible(SceneOverlayDecalBoxes, decalBoxesVisible)
			}
			imgui.SetItemTooltip("Show or hide the decal_box and decal_box_long_range meshes. The game uses these as projection bounds rather than solid geometry.")
			imgui.EndPopup()
		}
	}

	if pv.animTime != -1 {
		pv.animTime += 20 * imgui.CurrentIO().DeltaTime()
	}

	// We sweep the textures here so we aren't modifying opengl state during a draw call
	pv.ml.Sweep()

	currentArchives := pv.getSelectedArchives()

	var armorSets []datalib.ArmorSet
	if len(currentArchives) > 0 {
		armorSets = make([]datalib.ArmorSet, 0)
		maxStr := ""
		for idx := range currentArchives {
			var set datalib.ArmorSet
			var contains bool
			if set, contains = pv.armorSets[currentArchives[idx]]; !contains {
				continue
			}
			if len(set.Name) > len(maxStr) {
				maxStr = set.Name
			}
			armorSets = append(armorSets, set)
		}
		armorSets := slices.SortedFunc(slices.Values(armorSets), func(a, b datalib.ArmorSet) int {
			return strings.Compare(a.Name, b.Name)
		})
		if len(armorSets) > 0 {
			imgui.SameLine()
			var selectedSet int64 = -1
			var selectedCamo enum.CamouflageType = enum.CamouflageType_None
			width := imgui.CalcTextSize(maxStr).X + 2*imgui.CurrentStyle().FramePadding().X
			imgui.SetNextItemWidth(width + imgui.TextLineHeightWithSpacing())
			seenNames := make(map[string]any)
			hasCamo := false
			for _, set := range armorSets {
				for _, metadata := range set.UnitMetadata {
					if hasCamo {
						break
					}
					if len(metadata.CamoflagePieces) > 0 {
						hasCamo = true
					}
				}
				if hasCamo {
					break
				}
			}
			armorSetName := ""
			if pv.selectedSet != -1 {
				for _, set := range armorSets {
					if pv.selectedSet == int64(set.Id.Value) {
						armorSetName = set.Name
						break
					}
				}
			}
			if imgui.BeginComboV("Load Armor Set...", armorSetName, imgui.ComboFlagsNone) {
				for _, set := range armorSets {
					if _, contains := seenNames[set.Name]; contains {
						continue
					}
					imgui.SetNextItemWidth(width)
					if imgui.SelectableBoolV(set.Name, int64(set.Id.Value) == selectedSet, imgui.SelectableFlagsNone, imgui.NewVec2(width, 0)) {
						selectedSet = int64(set.Id.Value)
					}
					seenNames[set.Name] = true
				}
				imgui.EndCombo()
			}
			if hasCamo {
				imgui.SameLine()
				camoStr := "None"
				camoWidth := float32(0)
				for _, camo := range armorCamos {
					titleName := cases.Title(language.English).String(camo.Name())
					camoWidth = max(imgui.CalcTextSize(titleName).X+2*imgui.CurrentStyle().FramePadding().X, camoWidth)
				}
				if pv.selectedCamo != enum.CamouflageType_None {
					camoStr = cases.Title(language.English).String(pv.selectedCamo.Name())
				}
				imgui.SetNextItemWidth(camoWidth + imgui.TextLineHeightWithSpacing())
				if imgui.BeginComboV("Camoflage", camoStr, imgui.ComboFlagsNone) {
					for _, camo := range armorCamos {
						imgui.SetNextItemWidth(camoWidth)
						if imgui.SelectableBoolV(camo.Name(), camo == pv.selectedCamo, imgui.SelectableFlagsNone, imgui.NewVec2(width, 0)) {
							selectedCamo = camo
						}
					}
					imgui.EndCombo()
				}
			}
			if selectedSet != -1 || selectedCamo != enum.CamouflageType_None {
				if selectedSet != -1 {
					pv.selectedSet = selectedSet
				}
				if selectedCamo != enum.CamouflageType_None {
					pv.selectedCamo = selectedCamo
				}
				pv.loadArmorSet(armorSets)
			}
		}
	}

	if len(pv.previousSelectedArchives) != len(currentArchives) {
		pv.reapplyArmorMaterials()
		pv.previousSelectedArchives = currentArchives
	}
}

// reapplyArmorMaterials reloads every shown material with the LUT override that the current
// archive selection / armor context gives.
func (pv *UnitPreviewState) reapplyArmorMaterials() {
	for hash := range pv.objects {
		for name := range pv.objects[hash] {
			armorInfo := pv.getArmorInfo(pv.objects[hash][name].name)
			for group := range pv.objects[hash][name].materials {
				material := pv.objects[hash][name].materials[group]
				mat, _ := loadMaterialInfo(pv.getResource, material.ID())
				overrideLUTMaterial(mat, armorInfo, pv.selectedCamo)
				material = pv.ml.LoadMaterial(material.ID(), material.Name(), material.Shown(), mat, material, hash)
				pv.objects[hash][name].materials[group] = material
			}
		}
	}
	pv.finalizeObjectMaterials()
}

func (pv *UnitPreviewState) DrawSettings() {
	if pv.udimsSettingsShown && pv.numUdims > 1 {
		pv.drawVisibilityMaskSelector()
	}
	if pv.treeViewShown {
		pv.drawTreeViewEditor()
	}
}

func cmpHashStringsGenerator(lookupHash func(stingray.Hash) string, nodes map[stingray.Hash]unitPreviewNode) func(a, b stingray.FileID) int {
	return func(a, b stingray.FileID) int {
		aString := lookupHash(a.Name)
		bString := lookupHash(b.Name)
		if nodes != nil {
			aString = nodes[a.Name].name
			bString = nodes[b.Name].name
		}
		return strings.Compare(aString, bString)
	}
}

func (pv *UnitPreviewState) drawUnitMaterialSettings(unit stingray.FileID, object map[string]unitPreviewObject, flags imgui.TreeNodeFlags) {
	unitMeshNames := slices.Sorted(maps.Keys(object))
	for _, mesh := range unitMeshNames {
		shown := pv.objectsShown[unit][mesh]
		color := imgui.StyleColorVec4(imgui.ColText)
		if !shown {
			color = imgui.StyleColorVec4(imgui.ColTextDisabled)
		}
		if !imutils.TreeNodeExcf(*color, flags, "%s %s", fnt.I.DeployedCode, mesh) {
			continue
		}

		var icon string
		if shown {
			icon = fnt.I.Visibility
		} else {
			icon = fnt.I.VisibilityOff
		}
		rootPos := imgui.CursorScreenPos()
		size := imgui.NewVec2(imgui.ContentRegionAvail().X, imgui.FontSize())
		imutils.Textcf(*color, "%v Toggle Visibility", icon)
		imgui.SetCursorScreenPos(rootPos)
		imgui.SetNextItemAllowOverlap()
		if imgui.InvisibleButton("btnMesh", size) {
			pv.objectsShown[unit][mesh] = !shown
		}
		for idx := range pv.objects[unit][mesh].materials {
			pv.objects[unit][mesh].materials[idx].DrawTree(flags, color, shown)
		}
		imgui.TreePop()
	}
}

func (pv *UnitPreviewState) drawNodeTree(curr unitPreviewNode, nodeId stingray.FileID, matrix mgl32.Mat4) {
	flags := imgui.TreeNodeFlagsOpenOnArrow | imgui.TreeNodeFlagsDrawLinesToNodes
	if nodeId == pv.rootHash {
		flags |= imgui.TreeNodeFlagsDefaultOpen
	}
	if imgui.TreeNodeExStrV(curr.name, flags) {
		shown := curr.shown
		var icon string
		if shown {
			icon = fnt.I.Visibility
		} else {
			icon = fnt.I.VisibilityOff
		}
		rootPos := imgui.CursorScreenPos()
		size := imgui.NewVec2(imgui.ContentRegionAvail().X, imgui.FontSize())
		color := imgui.StyleColorVec4(imgui.ColText)
		if !shown {
			color = imgui.StyleColorVec4(imgui.ColTextDisabled)
		}
		imutils.Textcf(*color, "%v Toggle Visibility", icon)
		imgui.SetCursorScreenPos(rootPos)
		imgui.SetNextItemAllowOverlap()
		if imgui.InvisibleButton("btnNode", size) {
			curr.shown = !shown
			pv.nodes[nodeId.Name] = curr
			if pv.rootHash == nodeId {
				pv.root = curr
			}
		}

		if nodeId == pv.rootHash {
			pv.ml.DrawGlobalBlocks(imgui.TreeNodeFlagsOpenOnArrow|imgui.TreeNodeFlagsDrawLinesToNodes, color)
		}

		children := slices.SortedFunc(slices.Values(curr.children), cmpHashStringsGenerator(pv.lookupHash, pv.nodes))
		units := make([]stingray.FileID, 0)
		for _, childHash := range children {
			if child, contains := pv.nodes[childHash.Name]; contains && childHash != nodeId {
				childMatrix := matrix.Mul4(child.matrix)
				pv.drawNodeTree(child, childHash, childMatrix)
			} else {
				units = append(units, childHash)
			}
		}

		unitHashesSorted := slices.SortedFunc(slices.Values(units), cmpHashStringsGenerator(pv.lookupHash, nil))
		for _, unit := range unitHashesSorted {
			if _, contains := pv.objects[unit]; !contains {
				continue
			}
			object := pv.objects[unit]
			if !imgui.TreeNodeExStrV(fmt.Sprintf("%v %v", fnt.I.Box, filepath.Base(pv.lookupHash(unit.Name))), flags) {
				continue
			}
			pv.drawUnitMaterialSettings(unit, object, flags)

			imgui.TreePop()
		}
		imgui.TreePop()
	}
}

func (pv *UnitPreviewState) drawTreeViewEditor() {
	defer imgui.End()
	if pv.treeViewDrawn = imgui.BeginV(fnt.I.Settings+" Tree View", &pv.treeViewShown, 0); !pv.treeViewDrawn {
		return
	}
	pv.drawNodeTree(pv.root, pv.rootHash, pv.root.matrix)
}

func (pv *UnitPreviewState) drawVisibilityMaskSelector() {
	if pv.udimsSettingsDrawn = imgui.BeginV(fnt.I.FolderEye+" Visibility Mask Selection", &pv.udimsSettingsShown, imgui.WindowFlagsNoFocusOnAppearing); pv.udimsSettingsDrawn {
		nextActiveUDimListItem := int32(-1)
		nextHoveredUDimListItem := int32(-1)
		if imgui.Button("Reset") {
			pv.udimsSelected = pv.udimsShownDefault
			pv.damageModelSelection = damageModelIntact
		}
		imgui.Separator()
		imgui.PushStyleVarVec2(imgui.StyleVarItemSpacing,
			imgui.NewVec2(imgui.CurrentStyle().ItemSpacing().X, 0))
		dragging := pv.activeUDimListItem != -1 && pv.hoveredUDimListItem != -1
		var draggingMin, draggingMax int32
		if dragging {
			draggingMin = min(pv.activeUDimListItem, pv.hoveredUDimListItem)
			draggingMax = max(pv.activeUDimListItem, pv.hoveredUDimListItem)
		}
		var draggingMinPos, draggingMaxPos imgui.Vec2
		for i := range int32(pv.numUdims) {
			selected := pv.udimsSelected[i]
			if dragging {
				if i >= draggingMin && i <= draggingMax {
					selected = !selected
				}
				if imgui.IsMouseClickedBool(imgui.MouseButtonRight) {
					imgui.CurrentContext().SetActiveId(0)
				}
			}
			if selected {
				pv.udimsShown[i] = 1
			} else {
				pv.udimsShown[i] = 0
			}
			if imgui.IsMouseReleased(imgui.MouseButtonLeft) {
				if pv.udimsSelected[i] != selected {
					pv.damageModelSelection = damageModelCustom
				}
				pv.udimsSelected[i] = selected
			}
			var icon string
			if selected {
				icon = fnt.I.Visibility
			} else {
				icon = fnt.I.VisibilityOff
			}
			imgui.PushIDInt(i)
			pos := imgui.CursorScreenPos()
			size := imgui.NewVec2(imgui.ContentRegionAvail().X, imgui.FontSize())
			if dragging {
				if i == draggingMin {
					draggingMinPos = pos
				}
				if i == draggingMax {
					draggingMaxPos = pos.Add(size)
				}
			}
			if selected {
				imgui.WindowDrawList().AddRectFilled(pos, pos.Add(size), imgui.ColorU32Col(imgui.ColButton))
			}
			imutils.Textf(fmt.Sprintf("%s %02d: %s", icon, i, cmp.Or(pv.udimNames[i], "unknown")))
			imgui.SetCursorScreenPos(pos)
			imgui.SetNextItemAllowOverlap()
			imgui.InvisibleButton("btn", size)
			if imgui.IsItemActive() {
				nextActiveUDimListItem = i
			}
			hovered := imgui.ItemStatusFlags(imgui.CurrentContext().LastItemData().CData.StatusFlags)&imgui.ItemStatusFlagsHoveredRect != 0
			if hovered {
				nextHoveredUDimListItem = i
			}
			imgui.SetItemTooltip(`Click to toggle item visibility
Drag to toggle multiple items (right-click to cancel)`)
			imgui.PopID()
		}
		imgui.PopStyleVar()
		if dragging {
			imgui.WindowDrawList().AddRectV(draggingMinPos, draggingMaxPos, imgui.ColorU32Col(imgui.ColButtonActive), 0, 0, imgui.DrawFlagsNone)
		}
		pv.activeUDimListItem = nextActiveUDimListItem
		pv.hoveredUDimListItem = nextHoveredUDimListItem
	}
	imgui.End()
}
