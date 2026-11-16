package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/text/cases"
	language_lib "golang.org/x/text/language"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/gobwas/glob"
	"github.com/qmuntal/gltf"
	"github.com/xypwn/filediver/app/appconfig"
	datalib "github.com/xypwn/filediver/datalibrary"
	"github.com/xypwn/filediver/datalibrary/enum"
	"github.com/xypwn/filediver/dds"
	"github.com/xypwn/filediver/exec"
	"github.com/xypwn/filediver/extractor"
	extr_ah_bin "github.com/xypwn/filediver/extractor/ah_bin"
	extr_animation "github.com/xypwn/filediver/extractor/animation"
	extr_bik "github.com/xypwn/filediver/extractor/bink"
	extr_bones "github.com/xypwn/filediver/extractor/bones"
	extr_entity "github.com/xypwn/filediver/extractor/entity"
	extr_geogroup "github.com/xypwn/filediver/extractor/geometry_group"
	extr_level "github.com/xypwn/filediver/extractor/level"
	extr_material "github.com/xypwn/filediver/extractor/material"
	extr_package "github.com/xypwn/filediver/extractor/package"
	extr_prefab "github.com/xypwn/filediver/extractor/prefab"
	extr_shading_environment "github.com/xypwn/filediver/extractor/shading_environment"
	extr_speedtree "github.com/xypwn/filediver/extractor/speedtree"
	extr_state_machine "github.com/xypwn/filediver/extractor/state_machine"
	extr_strings "github.com/xypwn/filediver/extractor/strings"
	extr_texture "github.com/xypwn/filediver/extractor/texture"
	extr_ttf "github.com/xypwn/filediver/extractor/ttf"
	extr_unit "github.com/xypwn/filediver/extractor/unit"
	extr_wwise "github.com/xypwn/filediver/extractor/wwise"
	extr_xaml "github.com/xypwn/filediver/extractor/xaml"
	"github.com/xypwn/filediver/steampath"
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/ah_bin"
	stingray_package "github.com/xypwn/filediver/stingray/package"
	"github.com/xypwn/filediver/stingray/shading_environment"
	stingray_strings "github.com/xypwn/filediver/stingray/strings"
	"github.com/xypwn/filediver/stingray/unit"
	geometrygroup "github.com/xypwn/filediver/stingray/unit/geometry_group"
	stingray_material "github.com/xypwn/filediver/stingray/unit/material"
	"github.com/xypwn/filediver/util"
)

// Returns error if steam path couldn't be found.
func DetectGameDir() (string, error) {
	return steampath.GetAppPath("553850", "Helldivers 2")
}

func VerifyGameDir(path string) error {
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return fmt.Errorf("invalid game directory: %v: not a directory", path)
	}
	dataDir := gameDataDirectory(path)
	if info, err := os.Stat(filepath.Join(dataDir, "settings.ini")); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("invalid game directory: %v: valid data directory not found", path)
	}
	return nil
}

func gameDataDirectory(gameDir string) string {
	gameDir = filepath.Clean(gameDir)
	for _, candidate := range []string{
		gameDir,
		filepath.Join(gameDir, "contents"),
		filepath.Join(gameDir, "data"),
	} {
		info, err := os.Stat(filepath.Join(candidate, "settings.ini"))
		if err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	return filepath.Join(gameDir, "data")
}

type App struct {
	Hashes             map[stingray.Hash]string
	ThinHashes         map[stingray.ThinHash]string
	ArmorSets          map[stingray.Hash]datalib.ArmorSet
	ArmorSetsSource    string // where ArmorSets came from (this build's tables, or embedded live copy)
	SkinOverrideGroups []datalib.UnitSkinOverrideGroup
	WeaponPaintSchemes []datalib.WeaponCustomizableItem
	AttachmentSlots    map[stingray.Hash]enum.WeaponCustomizationSlot
	EntityVarMapping   shading_environment.ShadingEnvironmentEntityToShaderMapping
	Planets            map[string]datalib.PlanetData
	PlanetOverrides    datalib.PlanetOverridesMap
	PlanetRegionsMap   map[stingray.ThinHash]datalib.GenerationRegionVariantList
	EnvironmentMap     map[enum.PlanetType]datalib.EnvironmentSettings
	AssetOverrides     *map[stingray.FileID]stingray.FileID
	DataDir            *stingray.DataDir
	Language           stingray.ThinHash
	LanguageMap        map[uint32]string
	Metadata           map[stingray.FileID]FileMetadata
	GameBuildInfo      *ah_bin.BuildInfo
}

func getLowerCaser(language stingray.ThinHash) cases.Caser {
	languageTag, contains := stingray_strings.LanguageHashToLanguageTag[language]
	if !contains {
		languageTag = language_lib.English
	}
	return cases.Lower(languageTag)
}

func getFileMetadata(dataDir *stingray.DataDir, thinHashes map[stingray.ThinHash]string) map[stingray.FileID]FileMetadata {
	metadata := make(map[stingray.FileID]FileMetadata, len(dataDir.Files))
	geometryGroups := make(map[stingray.Hash]*geometrygroup.GeometryGroup)
	for fileID := range dataDir.Files {
		meta := FileMetadata{
			AvailableFields: make(map[string]bool),
			Name:            fileID.Name,
			Type:            fileID.Type,
		}
		for _, info := range dataDir.Files[fileID] {
			meta.Archives = append(meta.Archives, info.ArchiveID)
		}
		meta.addAvailableFields("Name", "Type", "Archives")
		switch fileID.Type {
		case stingray.Sum("texture"):
			const stingrayHeaderSize = 0xc0
			const textureHeaderSize = stingrayHeaderSize + 0x04 /*DDS magic*/ + 0x7c /*DDS header*/ + 0x14 /*DXT10 header*/
			b, err := dataDir.ReadAtMost(fileID, stingray.DataMain, textureHeaderSize)
			if err != nil {
				// ignore for now
				continue
			}
			bR := bytes.NewReader(b)
			if _, err := bR.Seek(stingrayHeaderSize, io.SeekCurrent); err != nil {
				// ignore for now
				continue
			}
			info, err := dds.DecodeInfo(bR)
			if err != nil {
				// ignore for now
				continue
			}
			meta.Width = int(info.Header.Width)
			meta.Height = int(info.Header.Height)
			meta.Format = info.DXT10Header.DXGIFormat.String()
			meta.addAvailableFields("Width", "Height", "Format")
		case stingray.Sum("strings"):
			b, err := dataDir.ReadAtMost(fileID, stingray.DataMain, 0x10)
			if err != nil {
				// ignore for now
				continue
			}
			hdr, err := stingray_strings.LoadHeader(bytes.NewReader(b))
			if err != nil {
				// ignore for now
				continue
			}
			meta.Language = hdr.Language
			meta.addAvailableFields("Language")
		case stingray.Sum("material"):
			b, err := dataDir.Read(fileID, stingray.DataMain)
			if err != nil {
				// ignore for now
				continue
			}
			mat, err := stingray_material.LoadMain(bytes.NewReader(b))
			if err != nil {
				// ignore for now
				continue
			}
			slots := make([]stingray.ThinHash, 0)
			textures := make([]stingray.Hash, 0)
			if mat.Textures != nil {
				slots = slices.Collect(maps.Keys(mat.Textures))
				textures = slices.Collect(maps.Values(mat.Textures))
			}
			meta.BaseMaterial = mat.BaseMaterial
			meta.TextureSlots = slots
			meta.TextureNames = textures
			meta.addAvailableFields("BaseMaterial")
			meta.addAvailableFields("TextureSlots")
			meta.addAvailableFields("TextureNames")
		case stingray.Sum("unit"):
			b, err := dataDir.Read(fileID, stingray.DataMain)
			if err != nil {
				// ignore for now
				continue
			}
			unitInfo, err := unit.LoadInfo(bytes.NewReader(b))
			if err != nil {
				continue
			}
			meta.MeshMaterials = make([]stingray.Hash, 0)
			meta.MaterialSlots = make([]stingray.ThinHash, 0)
			for key, value := range unitInfo.Materials {
				meta.MaterialSlots = append(meta.MaterialSlots, key)
				meta.MeshMaterials = append(meta.MeshMaterials, value)
			}
			meta.VertexCount, meta.TriangleCount, meta.AllLODTriangles, meta.ModelExtent = unitMeshStats(dataDir, fileID, unitInfo, geometryGroups, thinHashes)
			meta.ModelSize = unitResourceSize(dataDir, fileID, unitInfo)
			meta.addAvailableFields("MeshMaterials", "MaterialSlots", "VertexCount", "TriangleCount", "AllLODTriangles", "ModelSize", "ModelExtent")
		}
		metadata[fileID] = meta
	}
	return metadata
}

var unitMeshObjectRegex = regexp.MustCompile(`^(g_)?(\w*?)_?(cull|shadow|rubble|rubble_shadow|debris|shadowmesh|c)?_?\d*?_?(LOD\d)?$`)

func unitMeshStats(dataDir *stingray.DataDir, fileID stingray.FileID, unitInfo *unit.Info, cache map[stingray.Hash]*geometrygroup.GeometryGroup, thinHashes map[stingray.ThinHash]string) (uint64, uint64, uint64, float64) {
	var meshInfos []unit.MeshInfo
	if unitInfo.GeometryGroup.Value != 0 {
		geometryGroup, cached := cache[unitInfo.GeometryGroup]
		if !cached {
			geometryGroupID := stingray.NewFileID(unitInfo.GeometryGroup, stingray.Sum("geometry_group"))
			geometryGroupData, err := dataDir.Read(geometryGroupID, stingray.DataMain)
			if err == nil {
				geometryGroup, err = geometrygroup.LoadGeometryGroup(bytes.NewReader(geometryGroupData))
			}
			if err != nil {
				geometryGroup = nil
			}
			cache[unitInfo.GeometryGroup] = geometryGroup
		}
		if geometryGroup == nil {
			return 0, 0, 0, 0
		}
		geometryGroupInfo, ok := geometryGroup.MeshInfos[fileID.Name]
		if !ok {
			return 0, 0, 0, 0
		}
		for i, meshHeader := range geometryGroupInfo.MeshHeaders {
			meshName := stingray.ThinHash{}
			if i < len(geometryGroupInfo.MeshNames) {
				meshName = geometryGroupInfo.MeshNames[i]
			}
			var bounds unit.AABB
			var transformIndex uint32
			for _, unitMesh := range unitInfo.MeshInfos {
				if unitMesh.Header.MeshName == meshName {
					bounds = unitMesh.Header.AABB
					transformIndex = unitMesh.Header.AABBTransformIndex
					break
				}
			}
			meshInfos = append(meshInfos, unit.MeshInfo{
				Header: unit.MeshHeader{
					MeshName:           meshName,
					AABB:               bounds,
					AABBTransformIndex: transformIndex,
					MeshType:           unit.MeshTypeUnknown01,
				},
				Groups: meshHeader.Groups,
			})
		}
	} else {
		meshInfos = unitInfo.MeshInfos
	}

	selected := selectedUnitMeshInfos(meshInfos, thinHashes)
	var vertices, triangles, allLODTriangles uint64
	for _, meshInfo := range meshInfos {
		for _, group := range meshInfo.Groups {
			allLODTriangles += uint64(group.NumIndices / 3)
		}
	}
	for _, meshInfo := range selected {
		for _, group := range meshInfo.Groups {
			vertices += uint64(group.NumVertices)
			triangles += uint64(group.NumIndices / 3)
		}
	}
	return vertices, triangles, allLODTriangles, unitModelExtent(selected, unitInfo.Bones, thinHashes)
}

func unitModelExtent(meshInfos []unit.MeshInfo, bones []unit.Bone, thinHashes map[stingray.ThinHash]string) float64 {
	minBounds := mgl32.Vec3{float32(math.Inf(1)), float32(math.Inf(1)), float32(math.Inf(1))}
	maxBounds := mgl32.Vec3{float32(math.Inf(-1)), float32(math.Inf(-1)), float32(math.Inf(-1))}
	found := false
	for _, meshInfo := range meshInfos {
		meshName := thinHashes[meshInfo.Header.MeshName]
		match := unitMeshObjectRegex.FindStringSubmatch(meshName)
		if match != nil && match[2] != "" {
			objectName := match[2]
			if match[3] != "" {
				objectName += "_" + match[3]
			}
			if unitMeshHiddenByDefault(objectName) {
				continue
			}
		}
		if int(meshInfo.Header.AABBTransformIndex) >= len(bones) {
			continue
		}
		matrix := bones[meshInfo.Header.AABBTransformIndex].Matrix
		for cornerIdx := range 8 {
			var corner mgl32.Vec3
			for axis := range 3 {
				if cornerIdx&(1<<axis) == 0 {
					corner[axis] = meshInfo.Header.AABB.Min[axis]
				} else {
					corner[axis] = meshInfo.Header.AABB.Max[axis]
				}
			}
			point := matrix.Mul4x1(corner.Vec4(1))
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
	}
	if !found {
		return 0
	}
	dimensions := maxBounds.Sub(minBounds)
	return float64(max(dimensions[0], dimensions[1], dimensions[2]))
}

func unitMeshHiddenByDefault(object string) bool {
	visibleRubble := strings.HasPrefix(object, "rubble") &&
		!strings.Contains(object, "shadow") &&
		!strings.Contains(object, "cull") &&
		!strings.Contains(object, "coll")
	return strings.Contains(object, "shadow") ||
		strings.HasPrefix(object, "c_") ||
		(!visibleRubble && strings.HasSuffix(object, "_c")) ||
		strings.Contains(object, "cull") ||
		strings.Contains(object, "coll") ||
		(!visibleRubble && strings.HasSuffix(object, "rubble") && !strings.Contains(object, "rock")) ||
		(!visibleRubble && strings.HasPrefix(object, "rubble") && !strings.Contains(object, "rock")) ||
		strings.HasSuffix(object, "debris") ||
		object == "ai_blocker" || object == "v" || object == "navigation"
}

func selectedUnitMeshInfos(meshInfos []unit.MeshInfo, thinHashes map[stingray.ThinHash]string) []unit.MeshInfo {
	type candidate struct {
		info  unit.MeshInfo
		index int
	}
	objects := make(map[string]map[string]candidate)
	for i, info := range meshInfos {
		meshName := thinHashes[info.Header.MeshName]
		match := unitMeshObjectRegex.FindStringSubmatch(meshName)
		if match == nil || match[2] == "" {
			continue
		}
		objectName := match[2]
		if match[3] != "" {
			objectName += "_" + match[3]
		}
		if objects[objectName] == nil {
			objects[objectName] = make(map[string]candidate)
		}
		objects[objectName][match[4]] = candidate{info: info, index: i}
	}
	if len(objects) == 0 {
		bestIndex, bestCount := -1, -1
		for i, info := range meshInfos {
			if info.Header.MeshType == unit.MeshTypeUnknown00 {
				continue
			}
			for _, group := range info.Groups {
				if int(group.NumIndices) > bestCount {
					bestIndex, bestCount = i, int(group.NumIndices)
				}
			}
		}
		if bestIndex >= 0 {
			return []unit.MeshInfo{meshInfos[bestIndex]}
		}
		return nil
	}

	selected := make([]unit.MeshInfo, 0, len(objects))
	for _, lods := range objects {
		if best, ok := lods["LOD0"]; ok {
			selected = append(selected, best.info)
			continue
		}
		if base, ok := lods[""]; ok {
			selected = append(selected, base.info)
			continue
		}
		bestLevel, best := int(^uint(0)>>1), candidate{}
		for key, option := range lods {
			level, err := strconv.Atoi(strings.TrimPrefix(key, "LOD"))
			if err == nil && level < bestLevel {
				bestLevel, best = level, option
			}
		}
		if best.index >= 0 {
			selected = append(selected, best.info)
		}
	}
	return selected
}

func unitResourceSize(dataDir *stingray.DataDir, fileID stingray.FileID, unitInfo *unit.Info) uint64 {
	resourceSize := func(id stingray.FileID) uint64 {
		fileInfos := dataDir.Files[id]
		if len(fileInfos) == 0 {
			return 0
		}
		var size uint64
		for _, typ := range [...]stingray.DataType{stingray.DataMain, stingray.DataStream, stingray.DataGPU} {
			size += uint64(fileInfos[0].Files[typ].Size)
		}
		return size
	}
	size := resourceSize(fileID)
	if unitInfo.GeometryGroup.Value != 0 {
		size += resourceSize(stingray.NewFileID(unitInfo.GeometryGroup, stingray.Sum("geometry_group")))
	}
	return size
}

func LoadSkinOverrides(dataDir *stingray.DataDir, languageMap map[uint32]string) ([]datalib.UnitSkinOverrideGroup, error) {
	var getResource datalib.GetResourceFunc = func(id stingray.FileID, typ stingray.DataType) (data []byte, exists bool, err error) {
		fileInfo, ok := dataDir.Files[id]
		if !ok || !fileInfo[0].Exists(typ) {
			return nil, false, nil
		}
		exists = true
		data, err = dataDir.Read(id, typ)
		return
	}

	customizationSettings, err := datalib.ParseUnitCustomizationSettings(getResource, languageMap)
	if err != nil {
		return nil, fmt.Errorf("error parsing unit customization settings: %v\n", err)
	}

	var hellpodIdx int = -1
	var hellpodRackIdx int = -1
	for i := range customizationSettings {
		if customizationSettings[i].CollectionType == datalib.CollectionHellpod {
			hellpodIdx = i
		} else if customizationSettings[i].CollectionType == datalib.CollectionHellpodRack {
			hellpodRackIdx = i
		}
	}
	if hellpodIdx != -1 && hellpodRackIdx != -1 {
		for i := range customizationSettings[hellpodRackIdx].Skins {
			customizationSettings[hellpodRackIdx].Skins[i].Name = customizationSettings[hellpodIdx].Skins[i].Name
			for j, ammoRack := range customizationSettings[hellpodRackIdx].Skins[i].Customization.MaterialsTexturesOverrides {
				if ammoRack.MaterialID == stingray.Sum("m_ammo_rack").Thin() || ammoRack.MaterialID.Value == 0xefd45abb {
					// Rattlesnake overrides the wrong material ids, fix it so they use the correct ones
					customizationSettings[hellpodRackIdx].Skins[i].Customization.MaterialsTexturesOverrides[j].MaterialID = stingray.Sum("m_rack").Thin()
				}
			}
		}
	}

	skinOverrideGroups := make([]datalib.UnitSkinOverrideGroup, 0)
	for _, setting := range customizationSettings {
		skinOverrideGroups = append(skinOverrideGroups, setting.GetSkinOverrideGroup())
	}

	return skinOverrideGroups, nil
}

func LoadPaintSchemes(dataDir *stingray.DataDir, languageMap map[uint32]string) ([]datalib.WeaponCustomizableItem, error) {
	var getResource datalib.GetResourceFunc = func(id stingray.FileID, typ stingray.DataType) (data []byte, exists bool, err error) {
		fileInfo, ok := dataDir.Files[id]
		if !ok || !fileInfo[0].Exists(typ) {
			return nil, false, nil
		}
		exists = true
		data, err = dataDir.Read(id, typ)
		return

	}

	weaponCustomizations, err := datalib.ParseWeaponCustomizationSettings(getResource, languageMap)
	if err != nil {
		return nil, fmt.Errorf("error parsing weapon customization settings: %v", err)
	}

	for _, customization := range weaponCustomizations {
		if len(customization.Items) == 0 || len(customization.Items[0].Slots) == 0 {
			continue
		}
		if customization.Items[0].Slots[0] == enum.WeaponCustomizationSlot_PaintScheme {
			return customization.Items, nil
		}
	}

	return nil, fmt.Errorf("could not find any weapon customization settings?")
}

func LoadAttachmentSlots(dataDir *stingray.DataDir, languageMap map[uint32]string, lookupHash datalib.HashLookup) (map[stingray.Hash]enum.WeaponCustomizationSlot, error) {
	var getResource datalib.GetResourceFunc = func(id stingray.FileID, typ stingray.DataType) (data []byte, exists bool, err error) {
		fileInfo, ok := dataDir.Files[id]
		if !ok || !fileInfo[0].Exists(typ) {
			return nil, false, nil
		}
		exists = true
		data, err = dataDir.Read(id, typ)
		return

	}

	weaponCustomizations, err := datalib.ParseWeaponCustomizationSettings(getResource, languageMap)
	if err != nil {
		return nil, fmt.Errorf("error parsing weapon customization settings: %v", err)
	}

	result := make(map[stingray.Hash]enum.WeaponCustomizationSlot)
	for _, customization := range weaponCustomizations {
		if len(customization.Items) == 0 || len(customization.Items[0].Slots) == 0 {
			continue
		}
		if customization.Items[0].Slots[0] == enum.WeaponCustomizationSlot_AmmoType ||
			customization.Items[0].Slots[0] == enum.WeaponCustomizationSlot_AmmoTypeAlternate ||
			customization.Items[0].Slots[0] == enum.WeaponCustomizationSlot_Internals ||
			customization.Items[0].Slots[0] == enum.WeaponCustomizationSlot_Triggers ||
			customization.Items[0].Slots[0] == enum.WeaponCustomizationSlot_PaintScheme {
			// These slots don't have units that will be overwritten
			continue
		}
		for _, item := range customization.Items {
			if item.AddPath.Value == 0x0 || item.Archive.Value == 0x0 {
				continue
			}
			_, ok := dataDir.Files[stingray.NewFileID(item.AddPath, stingray.Sum("unit"))]
			// Some items in the settings just use the unit path as the add path, others are indirect and use the package
			unitHashes := []stingray.Hash{item.AddPath}
			if !ok {
				archiveData, exists, err := getResource(stingray.NewFileID(item.Archive, stingray.Sum("package")), stingray.DataMain)
				if !exists || err != nil {
					return nil, fmt.Errorf("failed to load package file %v: %v", lookupHash(item.Archive), err)
				}
				archive, err := stingray_package.LoadPackage(bytes.NewReader(archiveData))
				if err != nil {
					return nil, fmt.Errorf("failed to parse package file %v: %v", lookupHash(item.Archive), err)
				}
				clear(unitHashes)
				for _, item := range archive.Items {
					if item.Type == stingray.Sum("unit") {
						unitHashes = append(unitHashes, item.Name)
					}
				}
			}
			//fmt.Printf("Adding %v: %v to attachment slots\n", lookupHash(unitHash), item.Slots[0].String())

			if len(unitHashes) == 0 {
				return nil, fmt.Errorf("expected each attachment to have at least some unit hash: %v", item.AddPath.String())
			}

			for _, unitHash := range unitHashes {
				if _, contains := result[unitHash]; contains {
					continue
					//return nil, fmt.Errorf("expected each attachment to appear only once in weapon customization settings: %v", item.AddPath.String())
				}
				// smg underbarrel flamerthrower canister doesn't have its own weapon customization setting, so we have to set it manually
				if unitHash == stingray.Sum("content/fac_helldivers/equipment/attachment/magazine/smg_flamer_cannister") {
					result[unitHash] = enum.WeaponCustomizationSlot_Magazine
					continue
				}
				result[unitHash] = item.Slots[0]
			}
		}
	}

	return result, nil
}

func LoadEntityVariableMappings(dataDir *stingray.DataDir) (entityVarMapping shading_environment.ShadingEnvironmentEntityToShaderMapping) {
	shadingEnvironmentMappings, err := shading_environment.LoadMappingsFromDataDir(dataDir)
	if err == nil {
		entityVarMapping = make(shading_environment.ShadingEnvironmentEntityToShaderMapping)
		for _, mapping := range shadingEnvironmentMappings {
			maps.Copy(entityVarMapping, mapping.ToEntityMap())
		}
	}
	return
}

// Open game dir and read metadata.
func OpenGameDir(ctx context.Context, gameDir string, hashStrings []string, thinhashes []string, language stingray.ThinHash, onProgress func(curr, total int)) (*App, error) {
	dataDir, err := stingray.OpenDataDir(ctx, gameDataDirectory(gameDir), onProgress)
	if err != nil {
		return nil, err
	}

	hashesMap := make(map[stingray.Hash]string)
	for _, h := range hashStrings {
		hashesMap[stingray.Sum(h)] = h
	}
	if dataDir.IsHelldivers1 {
		for h, name := range recoverHelldivers1Names(dataDir, hashStrings) {
			hashesMap[h] = name
		}
	}
	thinHashesMap := make(map[stingray.ThinHash]string)
	for _, h := range thinhashes {
		thinHashesMap[stingray.Sum(h).Thin()] = h
	}
	if dataDir.IsHelldivers1 {
		metadata := helldivers1Metadata(dataDir)
		assetOverrides := make(map[stingray.FileID]stingray.FileID)
		return &App{
			Hashes:             hashesMap,
			ThinHashes:         thinHashesMap,
			ArmorSets:          make(map[stingray.Hash]datalib.ArmorSet),
			SkinOverrideGroups: []datalib.UnitSkinOverrideGroup{},
			WeaponPaintSchemes: []datalib.WeaponCustomizableItem{},
			AttachmentSlots:    make(map[stingray.Hash]enum.WeaponCustomizationSlot),
			EntityVarMapping:   make(shading_environment.ShadingEnvironmentEntityToShaderMapping),
			Planets:            make(map[string]datalib.PlanetData),
			PlanetOverrides:    make(datalib.PlanetOverridesMap),
			PlanetRegionsMap:   make(map[stingray.ThinHash]datalib.GenerationRegionVariantList),
			EnvironmentMap:     make(map[enum.PlanetType]datalib.EnvironmentSettings),
			AssetOverrides:     &assetOverrides,
			DataDir:            dataDir,
			Language:           language,
			LanguageMap:        make(map[uint32]string),
			Metadata:           metadata,
		}, nil
	}

	mapping := stingray_strings.LoadLanguageMap(dataDir, language)

	buildInfo, err := ah_bin.LoadFromDataDir(dataDir)
	if err != nil && err != ah_bin.NotFound {
		return nil, fmt.Errorf("error loading game build info: %v", err)
	}

	lookupHash := func(hash stingray.Hash) string {
		if name, ok := hashesMap[hash]; ok {
			return name
		}
		return hash.String()
	}

	lookupThinHash := func(hash stingray.ThinHash) string {
		if name, ok := thinHashesMap[hash]; ok {
			return name
		}
		return hash.String()
	}

	lookupString := func(stringId uint32) string {
		if stringId == 0 {
			return ""
		}
		if name, ok := mapping[stringId]; ok {
			return name
		}
		return strconv.FormatUint(uint64(stringId), 10)
	}

	// Armor sets: the opened build's own tables when they can be read (plain in release
	// builds, sealed later: see game_tables.go), else filediver's embedded live copies.
	var passives map[uint32]datalib.HelldiverCustomizationPassiveBonusSettings
	var armorSets map[stingray.Hash]datalib.ArmorSet
	armorSetsSource := "embedded (live)"
	{
		keys, kerr := loadGameKeys()
		if kerr != nil {
			fmt.Fprintf(os.Stderr, "[WARNING] key file: %v\n", kerr)
		}
		pData, perr := readGameTable(gameDir, "generated_customization_passive_bonuses.dl_bin", keys)
		aData, aerr := readGameTable(gameDir, "generated_customization_armor_sets.dl_bin", keys)
		pieceSize, passiveSize := datalib.PieceInfoSize, datalib.PassiveBonusSettingsSize
		if tl, terr := readGameTable(gameDir, "dl_library.dl_typelib", keys); terr == nil {
			if n, ok := typeSizeFromTypelib(tl, uint32(datalib.Sum("HelldiverCustomizationPieceInfo"))); ok {
				pieceSize = n
			}
			if n, ok := typeSizeFromTypelib(tl, uint32(datalib.Sum("HelldiverCustomizationPassiveBonusSettings"))); ok {
				passiveSize = n
			}
		}
		if perr == nil && aerr == nil {
			p, err1 := datalib.LoadPassiveBonusDefinitionsFromData(pData, lookupHash, lookupThinHash, lookupString, passiveSize)
			if err1 == nil {
				a, err2 := datalib.LoadArmorSetDefinitionsFromData(aData, mapping, p, pieceSize)
				if err2 == nil {
					passives, armorSets, armorSetsSource = p, a, "this game build's tables"
				} else {
					fmt.Fprintf(os.Stderr, "[WARNING] this build's armor table: %v\n", err2)
				}
			} else {
				fmt.Fprintf(os.Stderr, "[WARNING] this build's passive table: %v\n", err1)
			}
		} else if !errors.Is(perr, errSealedNoKey) && !errors.Is(aerr, errSealedNoKey) {
			fmt.Fprintf(os.Stderr, "[WARNING] this build's customization tables: %v / %v\n", perr, aerr)
		}
	}
	if armorSets == nil {
		passives, err = datalib.LoadPassiveBonusDefinitions(lookupHash, lookupThinHash, lookupString)
		if err != nil {
			return nil, fmt.Errorf("error loading passive bonus definitions: %v", err)
		}
		armorSets, err = datalib.LoadArmorSetDefinitions(mapping, passives)
		if err != nil {
			return nil, fmt.Errorf("error loading armor set definitions: %v", err)
		}
	}
	fmt.Fprintf(os.Stderr, "[INFO] armor sets: %v kits from %v\n", len(armorSets), armorSetsSource)

	skinOverrideGroups, err := LoadSkinOverrides(dataDir, mapping)
	if err != nil {
		// Older game builds use a different hash_lookup layout for skin
		// customization data. Skin overrides are optional metadata; keep the
		// rest of the game data usable when this table cannot be decoded.
		skinOverrideGroups = []datalib.UnitSkinOverrideGroup{}
	}

	weaponPaintSchemes, err := LoadPaintSchemes(dataDir, mapping)
	if err != nil {
		// Paint schemes are optional metadata and older game builds use a
		// different customization hash_lookup layout.
		weaponPaintSchemes = []datalib.WeaponCustomizableItem{}
	}
	attachmentSlots, err := LoadAttachmentSlots(dataDir, mapping, lookupHash)
	if err != nil {
		// The same versioned customization resources back attachment labels.
		// Preserve the core file browser when they cannot be decoded.
		attachmentSlots = map[stingray.Hash]enum.WeaponCustomizationSlot{}
	}

	entityVarMapping := LoadEntityVariableMappings(dataDir)

	planetData, err := datalib.LoadPlanetData(lookupHash, lookupThinHash, lookupString)
	if err != nil {
		return nil, fmt.Errorf("error loading planet data: %v", err)
	}
	planetMap := make(map[string]datalib.PlanetData)
	caser := getLowerCaser(language)
	for _, planet := range planetData {
		if planet.PlanetNameLoc == "" {
			continue
		}
		planetNameLower := caser.String(planet.PlanetNameLoc)
		planetMap[planetNameLower] = planet
	}
	planetOverrides, err := datalib.LoadPlanetOverrideSettings(lookupHash, lookupThinHash, lookupString)
	if err != nil {
		return nil, fmt.Errorf("error loading planet overrides data: %v", err)
	}
	planetOverridesMap := make(datalib.PlanetOverridesMap)
	for _, override := range planetOverrides {
		maps.Copy(planetOverridesMap, override.ToMap())
	}

	environmentSettings, err := datalib.LoadEnvironmentSettings()
	if err != nil {
		return nil, fmt.Errorf("error loading environment settings: %v", err)
	}
	environmentMap := make(map[enum.PlanetType]datalib.EnvironmentSettings)
	for _, setting := range environmentSettings {
		environmentMap[setting.PlanetType] = setting
	}

	planetRegionMappings := make(map[stingray.ThinHash]datalib.GenerationRegionVariantList)
	regionGroups, err := datalib.LoadRegionGroups(lookupHash, lookupThinHash, lookupString)
	if err == nil {
		for _, group := range regionGroups {
			maps.Copy(planetRegionMappings, group.RegionsMap())
		}
	}

	var assetOverrides map[stingray.FileID]stingray.FileID

	return &App{
		Hashes:             hashesMap,
		ThinHashes:         thinHashesMap,
		ArmorSets:          armorSets,
		ArmorSetsSource:    armorSetsSource,
		SkinOverrideGroups: skinOverrideGroups,
		WeaponPaintSchemes: weaponPaintSchemes,
		AttachmentSlots:    attachmentSlots,
		EntityVarMapping:   entityVarMapping,
		Planets:            planetMap,
		PlanetOverrides:    planetOverridesMap,
		PlanetRegionsMap:   planetRegionMappings,
		EnvironmentMap:     environmentMap,
		AssetOverrides:     &assetOverrides,
		DataDir:            dataDir,
		Language:           language,
		LanguageMap:        mapping,
		Metadata:           getFileMetadata(dataDir, thinHashesMap),
		GameBuildInfo:      buildInfo,
	}, nil
}

func (a *App) hashNameVariationsForMatch(h stingray.Hash) []string {
	res := []string{
		h.StringEndian(binary.LittleEndian),
		h.StringEndian(binary.BigEndian),
		"0x" + h.StringEndian(binary.BigEndian),
		"0x" + strings.TrimLeft(h.StringEndian(binary.BigEndian), "0"),
		strings.ToUpper(h.StringEndian(binary.LittleEndian)),
		strings.ToUpper(h.StringEndian(binary.BigEndian)),
		"0x" + strings.ToUpper(h.StringEndian(binary.BigEndian)),
		"0x" + strings.ToUpper(strings.TrimLeft(h.StringEndian(binary.BigEndian), "0")),
	}
	if name, ok := a.Hashes[h]; ok {
		res = append(res, name)
	}
	return res
}

func (a *App) matchFileID(id stingray.FileID, glb glob.Glob, nameOnly bool) bool {
	nameVariations := a.hashNameVariationsForMatch(id.Name)

	var typeVariations []string
	if !nameOnly {
		typeVariations = a.hashNameVariationsForMatch(id.Type)
	}

	for _, name := range nameVariations {
		if nameOnly {
			if glb.Match(name) {
				return true
			}
		} else {
			for _, typ := range typeVariations {
				if glb.Match(name + "." + typ) {
					return true
				}
			}
		}
	}

	return false
}

func (a *App) MatchingFiles(
	includeGlob string,
	excludeGlob string,
	includeOnlyTypes []string,
	includeArchiveIDs []stingray.Hash,
	metadataFilter string,
	infof func(format string, args ...any),
) (
	map[stingray.FileID]struct{},
	error,
) {
	var inclGlob glob.Glob
	inclGlobNameOnly := !strings.Contains(includeGlob, ".")
	if includeGlob != "" {
		var err error
		inclGlob, err = glob.Compile(includeGlob)
		if err != nil {
			return nil, err
		}
	}
	var exclGlob glob.Glob
	exclGlobNameOnly := !strings.Contains(excludeGlob, ".")
	if excludeGlob != "" {
		var err error
		exclGlob, err = glob.Compile(excludeGlob)
		if err != nil {
			return nil, err
		}
	}
	var metadataFilterProg *FilterExprProgram
	if metadataFilter != "" {
		var err error
		metadataFilterProg, err = CompileMetadataFilterExpr(metadataFilter)
		if err != nil {
			return nil, err
		}
	}

	var includeArchiveFiles map[stingray.FileID]struct{} = make(map[stingray.FileID]struct{})
	for _, includeArchiveID := range includeArchiveIDs {
		files, ok := a.DataDir.Archives[includeArchiveID]
		if !ok {
			return nil, fmt.Errorf("archive %v does not exist", includeArchiveID.String())
		}
		for _, f := range files {
			includeArchiveFiles[f] = struct{}{}
		}
		if _, contains := a.ArmorSets[includeArchiveID]; !contains {
			continue
		}
		if a.ArmorSets[includeArchiveID].Type == datalib.KitCape {
			typeVariations := a.hashNameVariationsForMatch(stingray.Sum("unit"))
			if len(includeOnlyTypes) == 0 || slices.ContainsFunc(includeOnlyTypes, func(includedType string) bool {
				return slices.Contains(typeVariations, includedType)
			}) {
				infof("Archive of cape '%v' detected, adding cape units to export", util.PrettyTitleCase(a.ArmorSets[includeArchiveID].Name))
				includeArchiveFiles[stingray.NewFileID(stingray.Sum("content/fac_helldivers/capes/medium_cape"), stingray.Sum("unit"))] = struct{}{}
				includeArchiveFiles[stingray.NewFileID(stingray.Sum("content/fac_helldivers/capes/shock_trooper_cape"), stingray.Sum("unit"))] = struct{}{}
			}
		}
	}

	res := make(map[stingray.FileID]struct{})
	for id := range a.DataDir.Files {
		shouldIncl := true
		if len(includeArchiveIDs) != 0 {
			if _, ok := includeArchiveFiles[id]; !ok {
				shouldIncl = false
			}
		}
		if len(includeOnlyTypes) != 0 {
			typeVariations := a.hashNameVariationsForMatch(id.Type)
			if slices.ContainsFunc(includeOnlyTypes, func(includedType string) bool {
				return !slices.Contains(typeVariations, includedType)
			}) {
				continue
			}
		}
		if includeGlob != "" {
			// Include all files in archive even if they don't match the includeGlob - includeGlob will only add files to read
			shouldIncl = (len(includeArchiveIDs) != 0 && shouldIncl) || a.matchFileID(id, inclGlob, inclGlobNameOnly)
		}
		if excludeGlob != "" {
			if a.matchFileID(id, exclGlob, exclGlobNameOnly) {
				shouldIncl = false
			}
		}
		if metadataFilterProg != nil && shouldIncl {
			matches, err := MetadataFilterExprMatches(metadataFilterProg, a.Metadata[id])
			if err != nil {
				return nil, err
			}
			if !matches {
				shouldIncl = false
			}
		}
		if !shouldIncl {
			continue
		}

		res[id] = struct{}{}
	}

	return res, nil
}

// Prints hash if human-readable name is unknown.
func (a *App) LookupHash(hash stingray.Hash) string {
	if name, ok := a.Hashes[hash]; ok {
		return name
	}
	return hash.String()
}

// Prints hash if human-readable name is unknown.
func (a *App) LookupThinHash(hash stingray.ThinHash) string {
	if name, ok := a.ThinHashes[hash]; ok {
		return name
	}
	return hash.String()
}

// Prints string if string id is known.
func (a *App) LookupString(stringId uint32) string {
	if name, ok := a.LanguageMap[stringId]; ok {
		return name
	}
	if stringId == 0 {
		return ""
	}
	return strconv.FormatUint(uint64(stringId), 10)
}

func (a *App) GetPlanet(planetName string, city bool) *datalib.PlanetData {
	caser := getLowerCaser(a.Language)
	planetName = caser.String(planetName)
	var planet datalib.PlanetData
	var contains bool
	if planet, contains = a.Planets[planetName]; !contains {
		return nil
	}
	return &planet
}

func (a *App) GetAssetOverrides(planetName string, city bool) map[stingray.FileID]stingray.FileID {
	planet := a.GetPlanet(planetName, city)
	if planet == nil {
		return nil
	}
	assetOverrides := make(map[stingray.FileID]stingray.FileID)
	for _, region := range planet.ResourceRegionOverrides {
		if !city && region.RegionFlag == enum.RegionFlag_City {
			continue
		}
		regionOverrides, contains := a.PlanetOverrides[region.ID]
		if !contains {
			continue
		}
		maps.Copy(assetOverrides, regionOverrides)
	}
	return assetOverrides
}

func (a *App) UpdateAssetOverrides(planetName string, city bool) {
	assetOverrides := a.GetAssetOverrides(planetName, city)
	*a.AssetOverrides = assetOverrides
}

func getSourceExtractFunc(extrCfg appconfig.Config, typ string) (extr extractor.ExtractFunc) {
	switch extrCfg.Raw.Format {
	case "main":
		extr = extractor.ExtractFuncRawSingleType(typ, stingray.DataMain)
	case "stream":
		extr = extractor.ExtractFuncRawSingleType(typ, stingray.DataStream)
	case "gpu":
		extr = extractor.ExtractFuncRawSingleType(typ, stingray.DataGPU)
	case "combined":
		extr = extractor.ExtractFuncRawCombined(typ)
	default:
		extr = extractor.ExtractFuncRaw(typ)
	}
	return
}

// Returns path to extracted file/directory.
func (a *App) ExtractFile(ctx context.Context, id stingray.FileID, outDir string, extrCfg appconfig.Config, runner *exec.Runner, gltfDoc *gltf.Document, archiveIDs []stingray.Hash, printer Printer, statusf func(format string, args ...any)) ([]string, error) {
	if ctxErr := ctx.Err(); errors.Is(ctxErr, context.Canceled) {
		return nil, ctxErr
	}

	name, typ := a.LookupHash(id.Name), a.LookupHash(id.Type)

	typeFormats := appconfig.GetTypeFormats(extrCfg)
	extrFormat := typeFormats[typ]

	var extr extractor.ExtractFunc
	if extrFormat == "raw" {
		extr = getSourceExtractFunc(extrCfg, typ)
	} else {
		switch typ {
		case "animation":
			extr = extr_animation.ExtractAnimationJson
		case "bik", "bk2":
			if extrFormat == "bik" || extrFormat == "bk2" {
				extr = extr_bik.ExtractBink(typ)
			} else {
				extr = extr_bik.ConvertBinkToMP4(typ)
			}
		case "wwise_stream":
			if extrFormat == "wwise" {
				extr = extr_wwise.ExtractWem
			} else {
				extr = extr_wwise.ConvertWem
			}
		case "wwise_bank":
			if extrFormat == "wwise" {
				extr = extr_wwise.ExtractBnk
			} else {
				extr = extr_wwise.ConvertBnk
			}
		case "material":
			if extrFormat == "folder" {
				extr = extr_material.ConvertToFolder
			} else {
				extr = extr_material.Convert(gltfDoc)
			}
		case "unit":
			extr = extr_unit.Convert(gltfDoc)
		case "geometry_group":
			extr = extr_geogroup.Convert(gltfDoc)
		case "prefab":
			if extrFormat == "model" && typeFormats["unit"] == "raw" {
				extr = getSourceExtractFunc(extrCfg, typ)
			} else {
				extr = extr_prefab.Convert(gltfDoc)
			}
		case "level":
			if extrFormat == "model" && typeFormats["unit"] == "raw" {
				extr = getSourceExtractFunc(extrCfg, typ)
			} else {
				extr = extr_level.Convert(gltfDoc)
			}
		case "speedtree":
			if extrFormat == "model" && typeFormats["unit"] == "raw" {
				extr = getSourceExtractFunc(extrCfg, typ)
			} else {
				extr = extr_speedtree.Convert(gltfDoc)
			}
		case "texture":
			if extrFormat == "dds" {
				extr = extr_texture.ExtractDDS
			} else {
				extr = extr_texture.ConvertToPNG
			}
		case "state_machine":
			extr = extr_state_machine.ExtractStateMachineJson
		case "strings":
			extr = extr_strings.ExtractStringsJSON
		case "package":
			extr = extr_package.ExtractPackageJSON
		case "bones":
			extr = extr_bones.ExtractBonesJSON
		case "ah_bin":
			extr = extr_ah_bin.ExtractAhBinJSON
		case "entity":
			extr = extr_entity.ExtractEntityJSON
		case "shading_environment":
			extr = extr_shading_environment.ExtractShadingEnvironmentJSON
		case "shading_environment_mapping":
			extr = extr_shading_environment.ExtractShadingEnvironmentMappingJSON
		case "xaml":
			if extrFormat == "svg" {
				extr = extr_xaml.ExtractSVG
			} else {
				extr = extr_xaml.ExtractXAML
			}
		case "ttf":
			extr = extr_ttf.ExtractTTF
		case "otf":
			extr = extr_ttf.ExtractOTF
		default:
			extr = getSourceExtractFunc(extrCfg, typ)
		}
	}

	outPath := filepath.Join(outDir, name)
	if err := os.MkdirAll(filepath.Dir(outPath), os.ModePerm); err != nil {
		return nil, err
	}
	extrCtx, getOutFiles := extractor.NewContext(
		ctx,
		id,
		a.Hashes,
		a.ThinHashes,
		a.ArmorSets,
		a.SkinOverrideGroups,
		a.WeaponPaintSchemes,
		a.AttachmentSlots,
		a.EntityVarMapping,
		a.Planets,
		a.PlanetRegionsMap,
		a.EnvironmentMap,
		a.GameBuildInfo,
		a.LanguageMap,
		a.DataDir,
		runner,
		extrCfg,
		outPath,
		archiveIDs,
		func(format string, args ...any) {
			name, typ := a.LookupHash(id.Name), a.LookupHash(id.Type)
			printer.Warnf("extract %v.%v: %v", name, typ, fmt.Sprintf(format, args...))
		},
		statusf,
	)

	caser := getLowerCaser(a.Language)
	planetName := caser.String(extrCfg.Planet.Name)
	if planet, contains := a.Planets[planetName]; contains {
		a.UpdateAssetOverrides(planetName, extrCfg.Planet.City)
		extrCtx = extrCtx.WithAssetOverrides(*a.AssetOverrides).WithColorGrading(planet.PaletteGroupLowland.AssetGrading)
	}

	extrCtx = (func() *extractor.Context {
		if !extrCfg.Unit.EntityOverrideDefault {
			return extrCtx
		}
		if extrCfg.Unit.EntityName == "" {
			return extrCtx
		}
		entityHash, err := stingray.ParseOrSum(extrCfg.Unit.EntityName)
		if err != nil {
			return extrCtx
		}

		materialSwapData, err := datalib.GetMaterialSwapComponentDataForHash(entityHash)
		if err != nil {
			return extrCtx
		}

		var materialSwap datalib.MaterialSwapComponent
		if _, err := binary.Decode(materialSwapData, binary.LittleEndian, &materialSwap); err != nil {
			return extrCtx
		}

		materialOverrides := make(map[stingray.ThinHash]stingray.Hash)
		for _, materialSlot := range materialSwap.MaterialSlots {
			if materialSlot.MaterialSlotName.Value == 0x0 {
				continue
			}
			materialOverrides[materialSlot.MaterialSlotName] = materialSlot.SwapSettings[0].Material
		}

		return extrCtx.WithMaterialOverrides(materialOverrides)
	})()

	err := extr(extrCtx)
	outFiles := getOutFiles()
	if err != nil {
		{
			var err error
			var errPath string
			for _, path := range outFiles {
				if e := os.Remove(path); e != nil && !errors.Is(e, os.ErrNotExist) && err == nil {
					err = e
					errPath = path
				}
			}
			if err != nil {
				return nil, fmt.Errorf("cleanup %v: %w", errPath, err)
			}
		}
		return nil, fmt.Errorf("extract %v.%v: %w", name, typ, err)
	}

	return outFiles, nil
}
