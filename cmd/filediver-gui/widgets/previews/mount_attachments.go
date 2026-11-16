package previews

import (
	"fmt"
	"slices"
	"sync"

	"github.com/go-gl/mathgl/mgl32"
	datalib "github.com/xypwn/filediver/datalibrary"
	"github.com/xypwn/filediver/stingray"
)

const (
	maxMountedAttachmentDepth = 4
	maxMountedAttachmentCount = 64
)

type mountEntityIndex struct {
	entities    map[stingray.Hash]datalib.Entity
	unitsByPath map[stingray.Hash][]stingray.Hash
}

var (
	mountEntityIndexOnce sync.Once
	mountEntityIndexData *mountEntityIndex
	mountEntityIndexErr  error
)

func getMountEntityIndex() (*mountEntityIndex, error) {
	mountEntityIndexOnce.Do(func() {
		entities, err := datalib.ParseEntityComponentSettings()
		if err != nil {
			mountEntityIndexErr = err
			return
		}

		index := &mountEntityIndex{
			entities:    entities,
			unitsByPath: make(map[stingray.Hash][]stingray.Hash),
		}
		for entityPath, entityData := range entities {
			unitComponent, ok := entityData.Components[datalib.Sum("UnitComponentData")].(datalib.UnitComponent)
			if !ok || unitComponent.UnitPath.Value == 0 {
				continue
			}
			index.unitsByPath[unitComponent.UnitPath] = append(index.unitsByPath[unitComponent.UnitPath], entityPath)
		}
		for unitPath := range index.unitsByPath {
			slices.SortFunc(index.unitsByPath[unitPath], stingray.Hash.Cmp)
		}
		mountEntityIndexData = index
	})
	return mountEntityIndexData, mountEntityIndexErr
}

func (index *mountEntityIndex) mountEntitiesForUnit(unitPath stingray.Hash) []stingray.Hash {
	if entityData, ok := index.entities[unitPath]; ok {
		if component, ok := entityData.Components[datalib.Sum("UnitComponentData")].(datalib.UnitComponent); ok && component.UnitPath == unitPath {
			if hasMountRecords(entityData) {
				return []stingray.Hash{unitPath}
			}
		}
	}

	var result []stingray.Hash
	for _, entityPath := range index.unitsByPath[unitPath] {
		if hasMountRecords(index.entities[entityPath]) {
			result = append(result, entityPath)
		}
	}
	return result
}

func (index *mountEntityIndex) healthComponentForUnit(unitPath stingray.Hash) (datalib.HealthComponent, bool) {
	if entityData, ok := index.entities[unitPath]; ok {
		if health, ok := entityData.Components[datalib.Sum("HealthComponentData")].(datalib.HealthComponent); ok {
			return health, true
		}
	}
	for _, entityPath := range index.unitsByPath[unitPath] {
		if entityPath == unitPath {
			continue
		}
		if entityData, ok := index.entities[entityPath]; ok {
			if health, ok := entityData.Components[datalib.Sum("HealthComponentData")].(datalib.HealthComponent); ok {
				return health, true
			}
		}
	}
	return datalib.HealthComponent{}, false
}

func (index *mountEntityIndex) damageDealerZonesForUnit(unitPath stingray.Hash) []datalib.DamageDealerZoneInfo {
	seen := make(map[datalib.DamageDealerZoneInfo]struct{})
	var zones []datalib.DamageDealerZoneInfo
	appendEntityZones := func(entityPath stingray.Hash) {
		entityData, ok := index.entities[entityPath]
		if !ok {
			return
		}
		component, ok := entityData.Components[datalib.Sum("OverlapDamageComponentData")].(datalib.OverlapDamageComponent)
		if !ok {
			return
		}
		for _, zone := range component.Zones {
			if zone.ZoneName.Value == 0 {
				break
			}
			if zone.Shape < datalib.DamageDealerZoneShapeSphere || zone.Shape > datalib.DamageDealerZoneShapeCapsule || zone.Bone.Value == 0 {
				continue
			}
			if _, exists := seen[zone]; exists {
				continue
			}
			seen[zone] = struct{}{}
			zones = append(zones, zone)
		}
	}

	appendEntityZones(unitPath)
	for _, entityPath := range index.unitsByPath[unitPath] {
		if entityPath != unitPath {
			appendEntityZones(entityPath)
		}
	}
	return zones
}

func hasMountRecords(entityData datalib.Entity) bool {
	component, ok := entityData.Components[datalib.Sum("MountComponentData")].(datalib.MountComponent)
	if !ok {
		return false
	}
	for _, info := range component.Infos {
		if info.Path.Value != 0 && info.AttachNode.Value != 0 {
			return true
		}
	}
	return false
}

func (pv *UnitPreviewState) SetMountedAttachmentsEnabled(enabled bool) error {
	if pv.isHelldivers1 && enabled {
		return fmt.Errorf("mounted-attachment metadata is only available for Helldivers 2")
	}
	if pv.rootHash.Type != UNIT {
		return fmt.Errorf("select a unit preview to show mounted attachments")
	}
	if pv.mountedAttachmentsEnabled == enabled {
		return nil
	}
	pv.mountedAttachmentsEnabled = enabled

	if enabled && !pv.mountedAttachmentsLoaded {
		if err := pv.loadMountedAttachments(); err != nil {
			pv.mountedAttachmentWarnings = append(pv.mountedAttachmentWarnings, err.Error())
			pv.mountedAttachmentsLoaded = true
			return err
		}
		pv.mountedAttachmentsLoaded = true
		if err := pv.finalizeObjectMaterials(); err != nil {
			pv.mountedAttachmentWarnings = append(pv.mountedAttachmentWarnings, "Some mounted textures could not be finalized: "+err.Error())
		}
	}

	for _, nodeHash := range pv.mountedAttachmentNodes {
		node, ok := pv.nodes[nodeHash]
		if !ok {
			continue
		}
		node.shown = enabled
		pv.nodes[nodeHash] = node
	}
	if root, ok := pv.nodes[pv.rootHash.Name]; ok {
		pv.root = root
	}
	pv.maxViewDistance = pv.getMaxZoom(pv.root, pv.rootHash, false)
	if enabled && pv.autoZoomEnabled {
		pv.doAutoZoomNextFrame = true
		pv.animTime = 0
	}
	return nil
}

func (pv *UnitPreviewState) loadMountedAttachments() error {
	index, err := getMountEntityIndex()
	if err != nil {
		return fmt.Errorf("reading game attachment metadata: %w", err)
	}
	rootInfo := pv.unitInfos[pv.rootHash]
	if rootInfo == nil {
		return fmt.Errorf("unit skeleton data is unavailable for attachment lookup")
	}
	rootNode, ok := pv.nodes[pv.rootHash.Name]
	if !ok {
		return fmt.Errorf("unit scene root is unavailable for attachment lookup")
	}

	ancestry := map[stingray.Hash]bool{pv.rootHash.Name: true}
	pv.expandMountedAttachments(index, pv.rootHash, pv.rootHash.Name, index.mountEntitiesForUnit(pv.rootHash.Name), 0, ancestry)
	if len(pv.mountedAttachmentNodes) == 0 {
		return fmt.Errorf("no mounted models with matching attachment bones were found for this unit")
	}
	if updatedRoot, exists := pv.nodes[pv.rootHash.Name]; exists {
		pv.root = updatedRoot
	} else {
		pv.root = rootNode
	}
	pv.maxViewDistance = pv.getMaxZoom(pv.root, pv.rootHash, false)
	return nil
}

func (pv *UnitPreviewState) expandMountedAttachments(
	index *mountEntityIndex,
	parentUnit stingray.FileID,
	parentNode stingray.Hash,
	entityPaths []stingray.Hash,
	depth int,
	ancestry map[stingray.Hash]bool,
) {
	if depth >= maxMountedAttachmentDepth || len(pv.mountedAttachmentNodes) >= maxMountedAttachmentCount {
		return
	}
	parentInfo := pv.unitInfos[parentUnit]
	if parentInfo == nil {
		return
	}

	seenMounts := make(map[string]bool)
	for _, entityPath := range entityPaths {
		entityData, ok := index.entities[entityPath]
		if !ok {
			continue
		}
		component, ok := entityData.Components[datalib.Sum("MountComponentData")].(datalib.MountComponent)
		if !ok {
			continue
		}
		for mountIndex, mount := range component.Infos {
			if mount.Path.Value == 0 || mount.AttachNode.Value == 0 {
				continue
			}
			mountKey := fmt.Sprintf("%016x/%08x/%08x", mount.Path.Value, mount.AttachNode.Value, mount.Name.Value)
			if seenMounts[mountKey] {
				continue
			}
			seenMounts[mountKey] = true

			boneIndex := -1
			for i, bone := range parentInfo.Bones {
				if bone.NameHash == mount.AttachNode {
					boneIndex = i
					break
				}
			}
			if boneIndex < 0 {
				pv.addMountedAttachmentWarning(fmt.Sprintf("mount %s references missing bone %s", pv.lookupThinHash(mount.Name), pv.lookupThinHash(mount.AttachNode)))
				continue
			}

			childEntity, exists := index.entities[mount.Path]
			if !exists {
				pv.addMountedAttachmentWarning(fmt.Sprintf("mount %s points to unknown entity %s", pv.lookupThinHash(mount.Name), pv.lookupHash(mount.Path)))
				continue
			}
			childUnit, ok := childEntity.Components[datalib.Sum("UnitComponentData")].(datalib.UnitComponent)
			if !ok || childUnit.UnitPath.Value == 0 {
				pv.addMountedAttachmentWarning(fmt.Sprintf("mounted entity %s has no unit model", pv.lookupHash(mount.Path)))
				continue
			}
			childID := stingray.NewFileID(childUnit.UnitPath, UNIT)
			if ancestry[childID.Name] {
				pv.addMountedAttachmentWarning(fmt.Sprintf("skipped cyclic mount %s", pv.lookupHash(childID.Name)))
				continue
			}
			if _, loaded := pv.objects[childID]; !loaded {
				mainData, exists, err := pv.getResource(childID, stingray.DataMain)
				if err != nil || !exists {
					pv.addMountedAttachmentWarning(fmt.Sprintf("could not read mounted model %s", pv.lookupHash(childID.Name)))
					continue
				}
				gpuData, _, gpuErr := pv.getResource(childID, stingray.DataGPU)
				if gpuErr != nil {
					pv.addMountedAttachmentWarning(fmt.Sprintf("could not read GPU data for mounted model %s", pv.lookupHash(childID.Name)))
					continue
				}
				if err := pv.loadUnit(childID, mainData, gpuData); err != nil {
					pv.addMountedAttachmentWarning(fmt.Sprintf("could not load mounted model %s: %v", pv.lookupHash(childID.Name), err))
					continue
				}
			}

			if len(pv.mountedAttachmentNodes) >= maxMountedAttachmentCount {
				pv.addMountedAttachmentWarning("stopped after reaching the 64 mounted-model safety limit")
				return
			}
			mountNodeHash := pv.newMountedAttachmentNodeHash(parentNode, entityPath, mountIndex, mount.Path)
			mountName := pv.lookupThinHash(mount.Name)
			if mount.Name.Value == 0 || mountName == mount.Name.String() {
				mountName = pv.lookupHash(childID.Name)
			}
			mountNode := unitPreviewNode{
				name:            fmt.Sprintf("%s · %s", mountName, pv.lookupHash(childID.Name)),
				shown:           true,
				matrix:          parentInfo.Bones[boneIndex].Matrix,
				children:        []stingray.FileID{childID},
				parent:          parentNode,
				mountParentUnit: parentUnit,
				mountBoneIndex:  boneIndex,
			}
			pv.nodes[mountNodeHash] = mountNode
			pv.mountedAttachmentNodes = append(pv.mountedAttachmentNodes, mountNodeHash)

			parent, exists := pv.nodes[parentNode]
			if exists {
				parent.children = append(parent.children, stingray.FileID{Name: mountNodeHash})
				pv.nodes[parentNode] = parent
			}

			ancestry[childID.Name] = true
			pv.expandMountedAttachments(index, childID, mountNodeHash, []stingray.Hash{mount.Path}, depth+1, ancestry)
			delete(ancestry, childID.Name)
		}
	}
}

func (pv *UnitPreviewState) addMountedAttachmentWarning(warning string) {
	if len(pv.mountedAttachmentWarnings) < 12 {
		pv.mountedAttachmentWarnings = append(pv.mountedAttachmentWarnings, warning)
	}
}

func (pv *UnitPreviewState) newMountedAttachmentNodeHash(parentNode, entityPath stingray.Hash, mountIndex int, childEntity stingray.Hash) stingray.Hash {
	base := fmt.Sprintf("filediver/mount/%016x/%016x/%d/%016x", parentNode.Value, entityPath.Value, mountIndex, childEntity.Value)
	for salt := 0; ; salt++ {
		candidate := stingray.Sum(fmt.Sprintf("%s/%d", base, salt))
		if _, exists := pv.nodes[candidate]; !exists {
			return candidate
		}
	}
}

func (pv *UnitPreviewState) mountedAttachmentWarningsText() string {
	if len(pv.mountedAttachmentWarnings) == 0 {
		return ""
	}
	text := "Mounted models were partially loaded:"
	for _, warning := range pv.mountedAttachmentWarnings {
		text += "\n• " + warning
	}
	return text
}

func (pv *UnitPreviewState) refreshMountedAttachmentTransforms(parentUnit stingray.FileID, globals []mgl32.Mat4) {
	for _, nodeHash := range pv.mountedAttachmentNodes {
		node, ok := pv.nodes[nodeHash]
		if !ok || node.mountParentUnit != parentUnit || node.mountBoneIndex < 0 || node.mountBoneIndex >= len(globals) {
			continue
		}
		node.matrix = globals[node.mountBoneIndex]
		pv.nodes[nodeHash] = node
	}
}
