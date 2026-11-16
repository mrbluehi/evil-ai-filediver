package previews

import (
	"fmt"
	"math"
	"strings"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/xypwn/filediver/cmd/filediver-gui/imutils"
	datalib "github.com/xypwn/filediver/datalibrary"
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/unit"
)

type unitVisibilityMasks struct {
	num      uint32
	defaults [64]bool
	selected [64]bool
	shown    [64]int32
	names    [64]string
	hashes   [64]stingray.ThinHash
}

type damageModelMode uint8

const (
	damageModelIntact damageModelMode = iota
	damageModelDamaged
	damageModelDowned
	damageModelDestroyed
	damageModelCustom damageModelMode = 0xff
)

func newUnitVisibilityMasks() *unitVisibilityMasks {
	state := &unitVisibilityMasks{num: 1}
	for i := range state.defaults {
		state.defaults[i] = true
		state.selected[i] = true
		state.shown[i] = 1
	}
	return state
}

func (state *unitVisibilityMasks) reset() {
	state.selected = state.defaults
	state.syncShown()
}

func (state *unitVisibilityMasks) syncShown() {
	for i, shown := range state.selected {
		state.shown[i] = 0
		if shown {
			state.shown[i] = 1
		}
	}
}

// revealIntactBodySegments repairs units whose authored default hides their
// ordinary upper/lower limb meshes. The renderer uses UV0's U tile as the
// visibility-mask index, so only correct a mask when the currently loaded LOD
// actually contains triangles assigned to it. Gib and damaged alternatives
// remain hidden. If a complete "*_whole" limb mesh is present, keep the
// authored segmented defaults to avoid drawing both representations.
func (state *unitVisibilityMasks) revealIntactBodySegments(meshes map[string]unit.Mesh) {
	if state == nil || len(meshes) == 0 {
		return
	}

	used := [64]bool{}
	for _, mesh := range meshes {
		if len(mesh.UVCoords) == 0 || len(mesh.UVCoords[0]) == 0 {
			continue
		}
		uvs := mesh.UVCoords[0]
		for _, indices := range mesh.Indices {
			for i := 0; i+2 < len(indices); i += 3 {
				tile := 64
				for j := 0; j < 3; j++ {
					index := indices[i+j]
					if int(index) >= len(uvs) {
						tile = 64
						break
					}
					vertexTile := visibilityMaskTile(uvs[index])
					if vertexTile < tile {
						tile = vertexTile
					}
				}
				if tile < len(used) {
					used[tile] = true
				}
			}
		}
	}

	indicesByName := make(map[string]uint32, state.num)
	for i := uint32(0); i < state.num; i++ {
		if state.names[i] != "" {
			indicesByName[strings.ToLower(state.names[i])] = i
		}
	}
	for i := uint32(0); i < state.num && i < uint32(len(used)); i++ {
		if !used[i] || state.defaults[i] || !isIntactBodySegmentMask(state.names[i]) {
			continue
		}
		name := strings.ToLower(state.names[i])
		wholeName := strings.TrimSuffix(name, "_upper") + "_whole"
		if strings.HasSuffix(name, "_lower") {
			wholeName = strings.TrimSuffix(name, "_lower") + "_whole"
		}
		if wholeIndex, ok := indicesByName[wholeName]; ok && wholeIndex < uint32(len(used)) && used[wholeIndex] && state.defaults[wholeIndex] {
			continue
		}
		state.defaults[i] = true
	}
}

func visibilityMaskTile(uv [2]float32) int {
	u := float64(uv[0])
	if math.IsNaN(u) || math.IsInf(u, 0) {
		return 0
	}
	return int(math.Floor(math.Max(0, math.Min(31.999, u))))
}

func isIntactBodySegmentMask(name string) bool {
	parts := strings.Split(strings.ToLower(name), "_")
	if len(parts) != 3 || (parts[0] != "arm" && parts[0] != "leg") {
		return false
	}
	if parts[1] != "left" && parts[1] != "right" && parts[1] != "l" && parts[1] != "r" {
		return false
	}
	return parts[2] == "upper" || parts[2] == "lower"
}

func (state *unitVisibilityMasks) setMask(hash stingray.ThinHash, shown bool) bool {
	if hash.Value == 0 {
		return false
	}
	changed := false
	for i := uint32(0); i < state.num; i++ {
		if state.hashes[i] == hash {
			state.selected[i] = shown
			changed = true
		}
	}
	return changed
}

// applyDamagePairs switches paired whole/damaged groups without showing both
// geometries at the same time. Names are game-authored visibility mask names.
func (state *unitVisibilityMasks) applyDamagePairs() bool {
	indices := make(map[string]uint32, state.num)
	for i := uint32(0); i < state.num; i++ {
		indices[strings.ToLower(state.names[i])] = i
	}
	changed := false
	for i := uint32(0); i < state.num; i++ {
		name := strings.ToLower(state.names[i])
		if !strings.HasSuffix(name, "_whole") {
			continue
		}
		base := strings.TrimSuffix(name, "_whole")
		damagedIndex, ok := indices[base+"_damaged"]
		if !ok || name == "_whole" {
			continue
		}
		state.selected[i] = false
		state.selected[damagedIndex] = true
		changed = true
	}
	return changed
}

func healthStageHasActions(state *unitVisibilityMasks, health datalib.HealthComponent, includeDead bool) bool {
	copy := *state
	return applyHealthVisibilityStage(&copy, health, includeDead)
}

// applyHealthVisibilityStage interprets the same hide/show mask fields used by
// the game's health component. A destroyed state composes downed then dead.
func applyHealthVisibilityStage(state *unitVisibilityMasks, health datalib.HealthComponent, includeDead bool) bool {
	changed := false
	apply := func(hide, show stingray.ThinHash) {
		changed = state.setMask(hide, false) || changed
		changed = state.setMask(show, true) || changed
	}
	apply(health.OnDownedHideVisibilityGroup, health.OnDownedShowVisibilityGroup)
	for _, zone := range health.DamageableZones {
		if zone.Info.ZoneName.Value == 0 {
			break
		}
		apply(zone.Info.OnDownedHideVisiblityMask, zone.Info.OnDownedShowVisiblityMask)
	}
	if !includeDead {
		return changed
	}
	apply(health.OnDeadHideVisibilityGroup, health.OnDeadShowVisibilityGroup)
	for _, zone := range health.DamageableZones {
		if zone.Info.ZoneName.Value == 0 {
			break
		}
		apply(zone.Info.OnDeadHideVisiblityGroup, zone.Info.OnDeadShowVisiblityGroup)
		apply(zone.Info.OnDeadHideVisiblityMask, zone.Info.OnDeadShowVisiblityMask)
	}
	return changed
}

func (state *unitVisibilityMasks) applyDamageMode(mode damageModelMode, health *datalib.HealthComponent) {
	state.reset()
	switch mode {
	case damageModelDamaged:
		state.applyDamagePairs()
	case damageModelDowned:
		if health != nil && !applyHealthVisibilityStage(state, *health, false) {
			state.applyDamagePairs()
		}
	case damageModelDestroyed:
		if health != nil && !applyHealthVisibilityStage(state, *health, true) {
			state.applyDamagePairs()
		}
	}
	state.syncShown()
}

func damageModelModes(state *unitVisibilityMasks, health *datalib.HealthComponent) []damageModelMode {
	modes := []damageModelMode{damageModelIntact}
	if state != nil && hasDamageMaskPairs(state) {
		modes = append(modes, damageModelDamaged)
	}
	if state != nil && health != nil {
		if healthStageHasActions(state, *health, false) {
			modes = append(modes, damageModelDowned)
		}
		if healthStageHasActions(state, *health, true) {
			modes = append(modes, damageModelDestroyed)
		}
	}
	return modes
}

func (pv *UnitPreviewState) loadRootDamageMetadata() {
	if pv.rootHealthLoaded || pv.rootHash.Type != UNIT || pv.isHelldivers1 {
		return
	}
	pv.rootHealthLoaded = true
	index, err := getMountEntityIndex()
	if err != nil {
		pv.rootHealthLoadError = err.Error()
		return
	}
	health, ok := index.healthComponentForUnit(pv.rootHash.Name)
	if !ok {
		// Some units keep their gameplay components on a separate entity path.
		entityHash := datalib.UnitsToEntities(pv.rootHash.Name)
		if entityHash != pv.rootHash.Name {
			health, ok = index.healthComponentForUnit(entityHash)
		}
	}
	if ok {
		pv.rootHealthComponent = &health
		return
	}
	pv.rootHealthLoadError = fmt.Sprintf("No health component is linked to unit %s.", pv.lookupHash(pv.rootHash.Name))
}

func (pv *UnitPreviewState) applyRootDamageModel(mode damageModelMode) {
	state := pv.visibilityMasks[pv.rootHash]
	if state == nil {
		return
	}
	state.applyDamageMode(mode, pv.rootHealthComponent)
	pv.damageModelSelection = mode
	pv.udimsSelected = state.selected
	pv.udimsShown = state.shown
}

func (pv *UnitPreviewState) syncRootVisibilityMasks() {
	if state := pv.visibilityMasks[pv.rootHash]; state != nil {
		state.selected = pv.udimsSelected
		state.shown = pv.udimsShown
	}
}

func (pv *UnitPreviewState) drawDamageModelControl() {
	imgui.BeginDisabledV(pv.isHelldivers1 || pv.rootHash.Type != UNIT)
	imgui.SetNextItemWidth(imutils.S(130))
	preview := pv.damageModelSelection.label()
	if imgui.BeginCombo("Damage model", preview) {
		pv.loadRootDamageMetadata()
		state := pv.visibilityMasks[pv.rootHash]
		modes := damageModelModes(state, pv.rootHealthComponent)
		for _, mode := range modes {
			if imgui.SelectableBool(mode.label()) && mode != pv.damageModelSelection {
				pv.applyRootDamageModel(mode)
			}
			if mode == pv.damageModelSelection {
				imgui.SetItemDefaultFocus()
			}
		}
		if len(modes) == 1 {
			imgui.TextDisabled("No damage variants found")
		}
		if pv.rootHealthLoadError != "" {
			imutils.TextError(fmt.Errorf("could not read damage metadata: %s", pv.rootHealthLoadError))
		}
		imgui.EndCombo()
	}
	imgui.SetItemTooltip("Switch the unit's visibility masks between intact and damage stages. Uses authored downed/dead mask rules when available, plus paired *_whole / *_damaged masks.")
	imgui.EndDisabled()
}

func hasDamageMaskPairs(state *unitVisibilityMasks) bool {
	if state == nil {
		return false
	}
	copy := *state
	return copy.applyDamagePairs()
}

func (mode damageModelMode) label() string {
	switch mode {
	case damageModelDamaged:
		return "Damaged"
	case damageModelDowned:
		return "Downed"
	case damageModelDestroyed:
		return "Destroyed"
	case damageModelCustom:
		return "Custom"
	default:
		return "Intact"
	}
}
