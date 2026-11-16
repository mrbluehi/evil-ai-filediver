package previews

import (
	"bytes"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/go-gl/gl/v4.3-core/gl"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/xypwn/filediver/cmd/filediver-gui/imutils"
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/animation"
	"github.com/xypwn/filediver/stingray/bones"
	"github.com/xypwn/filediver/stingray/state_machine"
	"github.com/xypwn/filediver/stingray/unit"
)

type animationClipRef struct {
	id         stingray.Hash
	label      string
	additive   bool
	blendIDs   []stingray.Hash
	weights    []float32
	blendFuncs []state_machine.CustomBlendFunction
	varNames   []string
	varValues  []float32
	varRanges  [][2]float32
	varUsed    []bool
}

type animationBlendLayer struct {
	clip   *animation.Animation
	tracks []animationBoneTrack
	weight float32
}

type animationChannelKey[T any] struct {
	time  float32
	value T
}

type animationBoneTrack struct {
	positions []animationChannelKey[mgl32.Vec3]
	rotations []animationChannelKey[mgl32.Quat]
	scales    []animationChannelKey[mgl32.Vec3]
}

type animationPlayback struct {
	clips             []animationClipRef
	selected          int
	clip              *animation.Animation
	tracks            []animationBoneTrack
	clipName          string
	time              float32
	speed             float32
	playing           bool
	loop              bool
	loadError         error
	standalone        bool
	additive          bool
	blend             []animationBlendLayer
	duration          float32
	blendFuncs        []state_machine.CustomBlendFunction
	varNames          []string
	varValues         []float32
	varRanges         [][2]float32
	varUsed           []bool
	blendControlsOpen bool
}

func (p *animationPlayback) reset() {
	*p = animationPlayback{speed: 1, loop: true}
}

func (p *animationPlayback) setAnimation(name string, clip *animation.Animation, additive bool) {
	p.clipName = name
	p.clip = clip
	p.tracks = makeAnimationTracks(clip)
	p.additive = additive
	p.blend = nil
	p.duration = 0
	p.blendFuncs = nil
	p.varNames = nil
	p.varValues = nil
	p.varRanges = nil
	p.varUsed = nil
	p.blendControlsOpen = false
	if clip != nil {
		p.duration = clip.Header.AnimationLength
	}
	p.time = 0
	p.playing = false
	p.loadError = nil
}

func makeAnimationTracks(clip *animation.Animation) []animationBoneTrack {
	if clip == nil {
		return nil
	}
	tracks := make([]animationBoneTrack, clip.Header.BoneCount)
	for _, entry := range clip.Entries {
		if entry.Header == nil {
			continue
		}
		bone := int(entry.Header.Bone())
		if bone < 0 || bone >= len(tracks) {
			continue
		}
		keyTime := float32(entry.Header.TimeMS()) / 1000
		track := &tracks[bone]
		switch entry.Header.Type() {
		case animation.EntryTypePosition:
			if value, err := entry.Position(); err == nil {
				track.positions = append(track.positions, animationChannelKey[mgl32.Vec3]{keyTime, value})
			}
		case animation.EntryTypeRotation:
			if value, err := entry.Rotation(); err == nil {
				track.rotations = append(track.rotations, animationChannelKey[mgl32.Quat]{keyTime, value})
			}
		case animation.EntryTypeScale:
			if value, err := entry.Scale(); err == nil {
				track.scales = append(track.scales, animationChannelKey[mgl32.Vec3]{keyTime, value})
			}
		case animation.EntryTypeExtended:
			switch entry.Header.Subtype() {
			case animation.EntrySubtypePosition:
				if value, err := entry.Position(); err == nil {
					track.positions = append(track.positions, animationChannelKey[mgl32.Vec3]{keyTime, value})
				}
			case animation.EntrySubtypeRotation:
				if value, err := entry.Rotation(); err == nil {
					track.rotations = append(track.rotations, animationChannelKey[mgl32.Quat]{keyTime, value})
				}
			case animation.EntrySubtypeScale:
				if value, err := entry.Scale(); err == nil {
					track.scales = append(track.scales, animationChannelKey[mgl32.Vec3]{keyTime, value})
				}
			}
		}
	}
	for i := range tracks {
		sort.SliceStable(tracks[i].positions, func(a, b int) bool { return tracks[i].positions[a].time < tracks[i].positions[b].time })
		sort.SliceStable(tracks[i].rotations, func(a, b int) bool { return tracks[i].rotations[a].time < tracks[i].rotations[b].time })
		sort.SliceStable(tracks[i].scales, func(a, b int) bool { return tracks[i].scales[a].time < tracks[i].scales[b].time })
	}
	return tracks
}

func sampleChannel[T any](keys []animationChannelKey[T], time float32, initial T, lerp func(T, T, float32) T) T {
	if len(keys) == 0 {
		return initial
	}
	idx := sort.Search(len(keys), func(i int) bool { return keys[i].time >= time })
	if idx == 0 {
		return keys[0].value
	}
	if idx >= len(keys) {
		return keys[len(keys)-1].value
	}
	prev, next := keys[idx-1], keys[idx]
	if next.time <= prev.time {
		return next.value
	}
	factor := (time - prev.time) / (next.time - prev.time)
	return lerp(prev.value, next.value, mgl32.Clamp(factor, 0, 1))
}

func lerpVec3(a, b mgl32.Vec3, t float32) mgl32.Vec3 {
	return a.Add(b.Sub(a).Mul(t))
}

func lerpQuat(a, b mgl32.Quat, t float32) mgl32.Quat {
	if a.Dot(b) < 0 {
		b = mgl32.Quat{V: b.V.Mul(-1), W: -b.W}
	}
	q := mgl32.Quat{V: lerpVec3(a.V, b.V, t), W: a.W + (b.W-a.W)*t}
	length := float32(math.Sqrt(float64(q.V.LenSqr() + q.W*q.W)))
	if length <= 1e-8 {
		return a
	}
	return mgl32.Quat{V: q.V.Mul(1 / length), W: q.W / length}
}

func (p *animationPlayback) sample() ([]mgl32.Vec3, []mgl32.Quat, []mgl32.Vec3) {
	if p.clip == nil {
		return nil, nil, nil
	}
	if len(p.blend) == 0 {
		return sampleAnimation(p.clip, p.tracks, p.time)
	}
	count := len(p.clip.Header.InitialTransforms)
	positions := make([]mgl32.Vec3, count)
	rotations := make([]mgl32.Quat, count)
	scales := make([]mgl32.Vec3, count)
	weights := make([]float32, len(p.blend))
	influences := make([]state_machine.CustomBlendFunction, 0, len(p.blendFuncs))
	for _, driver := range p.blendFuncs {
		if driver.DriverType == state_machine.DriverType_Influence {
			influences = append(influences, driver)
		}
	}
	totalWeight := float32(0)
	for i, layer := range p.blend {
		weights[i] = max(layer.weight, 0)
		if i < len(influences) {
			if value, err := influences[i].Evaluate(p.varValues); err == nil && !float32IsNaNOrInf(value) {
				weights[i] = max(value, 0)
			}
		}
		totalWeight += weights[i]
	}
	if totalWeight <= 1e-8 {
		for i := range weights {
			weights[i] = 1 / float32(len(weights))
		}
		totalWeight = 1
	}
	for i := range weights {
		weights[i] /= totalWeight
	}
	accumulatedWeight := float32(0)
	for layerIndex, layer := range p.blend {
		weight := weights[layerIndex]
		if layer.clip == nil || weight <= 0 || len(layer.clip.Header.InitialTransforms) != count {
			continue
		}
		clipLength := layer.clip.Header.AnimationLength
		clipTime := float32(0)
		if p.duration > 0 && clipLength > 0 {
			clipTime = p.time / p.duration * clipLength
		}
		lp, lr, ls := sampleAnimation(layer.clip, layer.tracks, clipTime)
		if accumulatedWeight == 0 {
			for i := 0; i < count; i++ {
				positions[i] = lp[i]
				rotations[i] = lr[i]
				scales[i] = ls[i]
			}
			accumulatedWeight = weight
			continue
		}
		factor := weight / (accumulatedWeight + weight)
		for i := 0; i < count; i++ {
			positions[i] = lerpVec3(positions[i], lp[i], factor)
			rotations[i] = lerpQuat(rotations[i], lr[i], factor)
			scales[i] = lerpVec3(scales[i], ls[i], factor)
		}
		accumulatedWeight += weight
	}
	return positions, rotations, scales
}

func sampleAnimation(clip *animation.Animation, tracks []animationBoneTrack, time float32) ([]mgl32.Vec3, []mgl32.Quat, []mgl32.Vec3) {
	if clip == nil {
		return nil, nil, nil
	}
	count := len(clip.Header.InitialTransforms)
	positions := make([]mgl32.Vec3, count)
	rotations := make([]mgl32.Quat, count)
	scales := make([]mgl32.Vec3, count)
	for i := 0; i < count; i++ {
		initial := &clip.Header.InitialTransforms[i]
		positions[i] = initial.Position()
		rotations[i] = initial.Rotation()
		scales[i] = initial.Scale()
		if i >= len(tracks) {
			continue
		}
		positions[i] = sampleChannel(tracks[i].positions, time, positions[i], lerpVec3)
		rotations[i] = sampleChannel(tracks[i].rotations, time, rotations[i], lerpQuat)
		scales[i] = sampleChannel(tracks[i].scales, time, scales[i], lerpVec3)
	}
	return positions, rotations, scales
}

func (p *animationPlayback) update(delta float32) {
	if !p.playing || p.clip == nil || p.duration <= 0 {
		return
	}
	p.time += delta * p.speed * p.controllerPlaybackSpeed()
	length := p.duration
	if p.time >= length {
		if p.loop {
			p.time = float32(math.Mod(float64(p.time), float64(length)))
		} else {
			p.time = length
			p.playing = false
		}
	}
}

func (p *animationPlayback) controllerPlaybackSpeed() float32 {
	for _, driver := range p.blendFuncs {
		if driver.DriverType != state_machine.DriverType_PlaybackSpeed {
			continue
		}
		value, err := driver.Evaluate(p.varValues)
		if err == nil && isFiniteFloat32(value) {
			return max(value, 0)
		}
	}
	return 1
}

func (p *animationPlayback) drawControls(load func(int) error) (changed bool) {
	if p.clip == nil {
		return false
	}
	if len(p.clips) > 0 {
		preview := p.clipName
		if imgui.BeginCombo("Animation", preview) {
			for i, clip := range p.clips {
				selected := i == p.selected
				if imgui.SelectableBoolPtr(clip.label, &selected) && i != p.selected {
					if err := load(i); err != nil {
						p.loadError = err
					} else {
						changed = true
					}
				}
				if selected {
					imgui.SetItemDefaultFocus()
				}
			}
			imgui.EndCombo()
		}
	}
	if p.playing {
		if imgui.Button("Pause") {
			p.playing = false
		}
	} else if imgui.Button("Play") {
		if p.duration > 0 && p.time >= p.duration {
			p.time = 0
		}
		p.playing = true
	}
	imgui.SameLine()
	if imgui.Button("Stop") {
		p.playing = false
		p.time = 0
	}
	imgui.SameLine()
	imgui.Checkbox("Loop", &p.loop)
	imgui.SameLine()
	imgui.SetNextItemWidth(90)
	imgui.SliderFloatV("Speed", &p.speed, 0.1, 3, "%.1fx", imgui.SliderFlagsAlwaysClamp)
	length := p.duration
	if length <= 0 {
		length = 0.001
	}
	imgui.SetNextItemWidth(-1)
	if imgui.SliderFloatV("##AnimationTime", &p.time, 0, length, "%.2fs", imgui.SliderFlagsAlwaysClamp) {
		p.playing = false
	}
	if len(p.blend) > 0 {
		imutils.Textf("%s · %d animation clips blended · %d bones · %.2f seconds", p.clipName, len(p.blend), p.clip.Header.BoneCount, p.duration)
		if imgui.CollapsingHeaderBoolPtr("Blend variables", &p.blendControlsOpen) {
			imgui.TextDisabled("Adjust the controller inputs used to blend these clips.")
			shown := false
			for i, variable := range p.varNames {
				if i >= len(p.varValues) {
					break
				}
				if i < len(p.varUsed) && !p.varUsed[i] {
					continue
				}
				shown = true
				low, high := blendVariableRange(i, p.varValues[i], p.varRanges)
				imgui.SetNextItemWidth(220)
				imgui.SliderFloatV(variable, &p.varValues[i], low, high, "%.2f", imgui.SliderFlagsAlwaysClamp)
			}
			if !shown {
				imgui.TextDisabled("This blend state has no exposed controller variables.")
			}
		}
	} else {
		imutils.Textf("%s · %d bones · %.2f seconds", p.clipName, p.clip.Header.BoneCount, p.duration)
	}
	return changed
}

func (pv *UnitPreviewState) loadLinkedAnimations(info *unit.Info) {
	pv.animationPlayer.reset()
	pv.animationBoneMap = nil
	pv.animationRootBone = -1
	pv.rootUnitInfo = info
	if info == nil || pv.isHelldivers1 {
		return
	}
	type clipKey struct {
		id       stingray.Hash
		additive bool
	}
	seen := make(map[clipKey]struct{})
	addClip := func(id stingray.Hash, label string, additive bool) {
		key := clipKey{id: id, additive: additive}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		pv.animationPlayer.clips = append(pv.animationPlayer.clips, animationClipRef{id: id, label: label, additive: additive})
	}
	if info.StateMachine.Value != 0 {
		stateID := stingray.NewFileID(info.StateMachine, stingray.Sum("state_machine"))
		stateData, exists, err := pv.getResource(stateID, stingray.DataMain)
		if err != nil {
			pv.animationPlayer.loadError = fmt.Errorf("read state machine: %w", err)
		} else if exists {
			sm, err := state_machine.LoadStateMachine(bytes.NewReader(stateData))
			if err != nil {
				pv.animationPlayer.loadError = fmt.Errorf("parse state machine: %w", err)
			} else {
				for layerIdx, layer := range sm.Layers {
					for stateIdx, state := range layer.States {
						stateName := state.ResolvedName
						if stateName == "" {
							stateName = pv.lookupHash(state.Name)
						}
						stateLabel := stateName
						if stateLabel == "" || stateLabel == state.Name.String() {
							stateLabel = fmt.Sprintf("Layer %d / State %d", layerIdx+1, stateIdx+1)
						}
						var stateAnimationIDs []stingray.Hash
						for _, id := range state.AnimationHashes {
							if _, exists, err := pv.getResource(stingray.NewFileID(id, stingray.Sum("animation")), stingray.DataMain); err != nil || !exists {
								continue
							}
							stateAnimationIDs = append(stateAnimationIDs, id)
							label := fmt.Sprintf("%s · %s", stateLabel, pv.lookupHash(id))
							addClip(id, label, state.Additive)
						}
						if state.Type == state_machine.StateType_Blend && len(stateAnimationIDs) > 1 {
							weights := stateBlendWeights(state, sm.AnimationVariableValues, len(stateAnimationIDs))
							variableNames := make([]string, len(sm.AnimationVariableNames))
							for i, hash := range sm.AnimationVariableNames {
								variableNames[i] = pv.lookupThinHash(hash)
							}
							pv.animationPlayer.clips = append(pv.animationPlayer.clips, animationClipRef{
								id: stateAnimationIDs[0], label: fmt.Sprintf("%s · blended state (%d clips)", stateLabel, len(stateAnimationIDs)),
								additive: state.Additive, blendIDs: stateAnimationIDs, weights: weights,
								blendFuncs: append([]state_machine.CustomBlendFunction(nil), state.CustomBlendFuncDefinition...),
								varNames:   variableNames, varValues: append([]float32(nil), sm.AnimationVariableValues...),
								varRanges: makeBlendVariableRanges(state.CustomBlendFuncDefinition, variableNames, sm.AnimationVariableValues),
								varUsed:   makeBlendVariableMask(state.CustomBlendFuncDefinition, variableNames),
							})
						}
					}
				}
			}
		}
	}
	// Some units have clips under their own animations folder but omit the
	// state-machine link (or only link a subset). Include those local clips too.
	unitPath := strings.Trim(strings.ReplaceAll(pv.lookupHash(pv.rootHash.Name), `\`, "/"), "/")
	animationIDs := make([]stingray.Hash, 0)
	for _, id := range pv.animationResourceIDs {
		candidate := strings.Trim(strings.ReplaceAll(pv.lookupHash(id.Name), `\`, "/"), "/")
		if strings.HasPrefix(strings.ToLower(candidate), strings.ToLower(unitPath+"/animations/")) {
			animationIDs = append(animationIDs, id.Name)
		}
	}
	sort.Slice(animationIDs, func(i, j int) bool {
		return pv.lookupHash(animationIDs[i]) < pv.lookupHash(animationIDs[j])
	})
	for _, id := range animationIDs {
		addClip(id, pv.lookupHash(id), false)
	}
	if len(pv.animationPlayer.clips) > 0 {
		if err := pv.loadLinkedAnimation(0); err != nil {
			pv.animationPlayer.loadError = err
		}
	}
}

func stateBlendWeights(state state_machine.State, variables []float32, count int) []float32 {
	weights := make([]float32, count)
	if len(state.AnimationWeights) == count {
		copy(weights, state.AnimationWeights)
	} else {
		influences := make([]state_machine.CustomBlendFunction, 0, len(state.CustomBlendFuncDefinition))
		for _, driver := range state.CustomBlendFuncDefinition {
			if driver.DriverType == state_machine.DriverType_Influence {
				influences = append(influences, driver)
			}
		}
		if len(influences) == count {
			for i, driver := range influences {
				value, err := driver.Evaluate(variables)
				if err == nil && !float32IsNaNOrInf(value) {
					weights[i] = max(value, 0)
				}
			}
		}
	}
	total := float32(0)
	for _, weight := range weights {
		total += max(weight, 0)
	}
	if total <= 1e-8 {
		for i := range weights {
			weights[i] = 1 / float32(count)
		}
		return weights
	}
	for i := range weights {
		weights[i] = max(weights[i], 0) / total
	}
	return weights
}

func float32IsNaNOrInf(value float32) bool {
	return math.IsNaN(float64(value)) || math.IsInf(float64(value), 0)
}

func (pv *UnitPreviewState) loadLinkedAnimation(index int) error {
	if index < 0 || index >= len(pv.animationPlayer.clips) {
		return fmt.Errorf("animation selection %d is out of range", index)
	}
	ref := pv.animationPlayer.clips[index]
	data, exists, err := pv.getResource(stingray.NewFileID(ref.id, stingray.Sum("animation")), stingray.DataMain)
	if err != nil {
		return fmt.Errorf("read animation %s: %w", pv.lookupHash(ref.id), err)
	}
	if !exists {
		return fmt.Errorf("animation %s is missing", pv.lookupHash(ref.id))
	}
	clip, err := animation.LoadAnimation(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("parse animation %s: %w", pv.lookupHash(ref.id), err)
	}
	pv.animationPlayer.selected = index
	pv.animationPlayer.setAnimation(ref.label, clip, ref.additive)
	if len(ref.blendIDs) > 1 {
		pv.animationPlayer.blend = make([]animationBlendLayer, 0, len(ref.blendIDs))
		pv.animationPlayer.duration = 0
		pv.animationPlayer.blendFuncs = append([]state_machine.CustomBlendFunction(nil), ref.blendFuncs...)
		pv.animationPlayer.varNames = append([]string(nil), ref.varNames...)
		pv.animationPlayer.varValues = append([]float32(nil), ref.varValues...)
		pv.animationPlayer.varRanges = append([][2]float32(nil), ref.varRanges...)
		pv.animationPlayer.varUsed = append([]bool(nil), ref.varUsed...)
		for i, id := range ref.blendIDs {
			layerData, exists, err := pv.getResource(stingray.NewFileID(id, stingray.Sum("animation")), stingray.DataMain)
			if err != nil || !exists {
				if err == nil {
					err = fmt.Errorf("animation resource is missing")
				}
				return fmt.Errorf("read blend-state clip %s: %w", pv.lookupHash(id), err)
			}
			layerClip, err := animation.LoadAnimation(bytes.NewReader(layerData))
			if err != nil {
				return fmt.Errorf("parse blend-state clip %s: %w", pv.lookupHash(id), err)
			}
			weight := float32(1) / float32(len(ref.blendIDs))
			if i < len(ref.weights) {
				weight = ref.weights[i]
			}
			pv.animationPlayer.blend = append(pv.animationPlayer.blend, animationBlendLayer{clip: layerClip, tracks: makeAnimationTracks(layerClip), weight: weight})
			pv.animationPlayer.duration = max(pv.animationPlayer.duration, layerClip.Header.AnimationLength)
		}
	}
	pv.animationBoneMap = pv.mapAnimationBones(clip)
	return nil
}

func makeBlendVariableRanges(functions []state_machine.CustomBlendFunction, names []string, defaults []float32) [][2]float32 {
	ranges := make([][2]float32, len(names))
	for i := range ranges {
		value := float32(0)
		if i < len(defaults) {
			value = defaults[i]
		}
		padding := max(float32(1), float32(math.Abs(float64(value))*0.5))
		ranges[i] = [2]float32{min(-1, value-padding), max(1, value+padding)}
	}
	indexByName := make(map[string]int, len(names))
	for i, name := range names {
		indexByName[name] = i
	}
	for _, function := range functions {
		driver, err := function.ToDriver(names)
		if err != nil {
			continue
		}
		for i, name := range driver.Variables {
			index, ok := indexByName[name]
			if !ok || i >= len(driver.Limits) {
				continue
			}
			low, high := driver.Limits[i][0], driver.Limits[i][1]
			if !isFiniteFloat32(low) || !isFiniteFloat32(high) {
				continue
			}
			if low == high {
				padding := max(float32(1), float32(math.Abs(float64(low))*0.25))
				low -= padding
				high += padding
			}
			ranges[index][0] = min(ranges[index][0], low)
			ranges[index][1] = max(ranges[index][1], high)
		}
	}
	return ranges
}

func makeBlendVariableMask(functions []state_machine.CustomBlendFunction, names []string) []bool {
	used := make([]bool, len(names))
	indexByName := make(map[string]int, len(names))
	for i, name := range names {
		indexByName[name] = i
	}
	for _, function := range functions {
		driver, err := function.ToDriver(names)
		if err != nil {
			continue
		}
		for _, name := range driver.Variables {
			if i, ok := indexByName[name]; ok {
				used[i] = true
			}
		}
	}
	return used
}

func blendVariableRange(index int, value float32, ranges [][2]float32) (float32, float32) {
	if index < len(ranges) {
		low, high := ranges[index][0], ranges[index][1]
		if isFiniteFloat32(low) && isFiniteFloat32(high) && high > low {
			return low, high
		}
	}
	padding := max(float32(1), float32(math.Abs(float64(value))*0.5))
	return min(-1, value-padding), max(1, value+padding)
}

func isFiniteFloat32(value float32) bool {
	return !math.IsNaN(float64(value)) && !math.IsInf(float64(value), 0)
}

func (pv *UnitPreviewState) SelectLinkedAnimation(id stingray.Hash, stateName string, preferBlend bool) error {
	player := &pv.animationPlayer
	selected := -1
	for i, ref := range player.clips {
		if preferBlend && len(ref.blendIDs) > 1 && slices.Contains(ref.blendIDs, id) && (stateName == "" || strings.HasPrefix(ref.label, stateName+" ·")) {
			selected = i
			break
		}
	}
	if selected < 0 && preferBlend {
		for i, ref := range player.clips {
			if len(ref.blendIDs) > 1 && slices.Contains(ref.blendIDs, id) {
				selected = i
				break
			}
		}
	}
	if selected < 0 {
		for i, ref := range player.clips {
			if ref.id == id && len(ref.blendIDs) == 0 && (stateName == "" || strings.HasPrefix(ref.label, stateName+" ·")) {
				selected = i
				break
			}
		}
	}
	if selected < 0 {
		for i, ref := range player.clips {
			if ref.id == id || slices.Contains(ref.blendIDs, id) {
				selected = i
				break
			}
		}
	}
	if selected < 0 {
		return fmt.Errorf("unit has no linked animation for %s", id)
	}
	if err := pv.loadLinkedAnimation(selected); err != nil {
		return err
	}
	player.time = 0
	player.playing = true
	return nil
}

func (pv *UnitPreviewState) SetStandaloneAnimation(id stingray.Hash, data []byte) error {
	clip, err := animation.LoadAnimation(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("parse animation: %w", err)
	}
	pv.tryLoadAnimationModel(id)
	name := pv.lookupHash(id)
	pv.animationPlayer.reset()
	pv.animationPlayer.standalone = true
	pv.animationPlayer.clips = []animationClipRef{{id: id, label: name}}
	pv.animationPlayer.selected = 0
	pv.animationPlayer.setAnimation(name, clip, false)
	pv.animationBoneMap = pv.mapAnimationBones(clip)
	return nil
}

func (pv *UnitPreviewState) tryLoadAnimationModel(id stingray.Hash) {
	resourcePath := strings.ReplaceAll(pv.lookupHash(id), `\`, "/")
	marker := "/animations/"
	cut := strings.Index(strings.ToLower(resourcePath), marker)
	if cut <= 0 {
		return
	}
	unitDir := strings.TrimRight(resourcePath[:cut], "/")
	separator := strings.LastIndex(unitDir, "/")
	if separator < 0 || separator == len(unitDir)-1 {
		return
	}
	unitName := unitDir[separator+1:]
	unitID := stingray.NewFileID(stingray.Sum(unitDir+"/"+unitName), stingray.Sum("unit"))
	if pv.rootHash.Name == unitID.Name && len(pv.objects) > 0 {
		return
	}
	mainData, exists, err := pv.getResource(unitID, stingray.DataMain)
	if err != nil || !exists {
		return
	}
	gpuData, _, err := pv.getResource(unitID, stingray.DataGPU)
	if err != nil {
		return
	}
	if _, err := unit.LoadInfo(bytes.NewReader(mainData)); err != nil {
		return
	}
	pv.ReleaseLoadedUnit()
	if err := pv.LoadUnit(unitID.Name, mainData, gpuData); err != nil {
		pv.ReleaseLoadedUnit()
		return
	}
	pv.doAutoZoomNextFrame = true
	pv.animTime = 0
}

func (pv *UnitPreviewState) DrawStandaloneAnimation() {
	pv.drawAnimationControls(func(index int) error {
		if index != 0 || pv.animationPlayer.clip == nil {
			return fmt.Errorf("animation selection is invalid")
		}
		return nil
	})
	pv.drawAnimationSummary()
}

func (pv *UnitPreviewState) DrawAnimated(previewId string) {
	if !pv.HasLoadedUnit() {
		pv.DrawStandaloneAnimation()
		return
	}
	pv.drawAnimationControls(func(index int) error { return pv.loadLinkedAnimation(index) })
	pv.Draw(previewId)
}

func (pv *UnitPreviewState) DrawAnimationInfo(id stingray.Hash, clip *animation.Animation) {
	if clip == nil {
		imgui.TextUnformatted("Select an animation resource to inspect it.")
		return
	}
	imutils.Textf("Animation: %s", pv.lookupHash(id))
	imutils.Textf("Duration: %.2f seconds", clip.Header.AnimationLength)
	imutils.Textf("Bones: %d · transform entries: %d · beats: %d", clip.Header.BoneCount, len(clip.Entries), len(clip.Header.ResolvedBeats))
	imgui.TextDisabled("Open the Animation tab to play this clip or try it on its matching unit.")
}

func (pv *UnitPreviewState) drawAnimationControls(load func(int) error) {
	if pv.animationPlayer.clip == nil {
		if pv.animationPlayer.loadError != nil {
			imutils.TextError(pv.animationPlayer.loadError)
		} else {
			imgui.TextDisabled("This unit has no linked animations.")
		}
		return
	}
	pv.animationPlayer.drawControls(load)
	if pv.animationPlayer.loadError != nil {
		imutils.TextError(pv.animationPlayer.loadError)
	}
	if pv.rootUnitInfo != nil && len(pv.animationBoneMap) == 0 {
		imgui.TextDisabled("No matching model skeleton was found; playback is showing track data only.")
	}
	pv.animationPlayer.update(imgui.CurrentIO().DeltaTime())
}

func (pv *UnitPreviewState) drawAnimationSummary() {
	if pv.animationPlayer.clip == nil {
		return
	}
	positions, rotations, scales := pv.animationPlayer.sample()
	imgui.Separator()
	imutils.Textf("Tracks at %.2fs", pv.animationPlayer.time)
	for i := 0; i < min(len(positions), 8); i++ {
		imutils.Textf("Bone %d  pos %.2f %.2f %.2f  scale %.2f %.2f %.2f", i,
			positions[i][0], positions[i][1], positions[i][2], scales[i][0], scales[i][1], scales[i][2])
		_ = rotations[i]
	}
	if len(positions) > 8 {
		imgui.TextDisabled(fmt.Sprintf("Showing the first 8 of %d animation tracks.", len(positions)))
	}
}

func (pv *UnitPreviewState) mapAnimationBones(clip *animation.Animation) []int {
	// Root-motion reference belongs to one clip/state selection. Capture a fresh
	// in-place origin from its first sampled pose instead of assuming the rig's
	// bind pose and the clip's first frame have identical root translations.
	pv.animationRootReference = mgl32.Vec3{}
	pv.animationRootReferenceValid = false
	info := pv.rootUnitInfo
	if info == nil || clip == nil || clip.Header.BoneCount == 0 {
		pv.animationRootBone = -1
		return nil
	}
	var result []int
	// Animation tracks use the ordered names from the unit's .bones resource.
	// Match those names directly to the joint table; spatial matching can map
	// symmetric limbs to the wrong side and silently leave most bones unmapped.
	if info.BonesHash.Value != 0 {
		if data, exists, err := pv.getResource(stingray.NewFileID(info.BonesHash, stingray.Sum("bones")), stingray.DataMain); err == nil && exists {
			if boneInfo, err := bones.LoadBones(bytes.NewReader(data)); err == nil && len(boneInfo.Hashes) > 0 {
				unitBones := make(map[stingray.ThinHash]int, len(info.Bones))
				for i, bone := range info.Bones {
					unitBones[bone.NameHash] = i
				}
				result := make([]int, int(clip.Header.BoneCount))
				for i := range result {
					result[i] = -1
					if i < len(boneInfo.Hashes) {
						if bone, ok := unitBones[boneInfo.Hashes[i]]; ok {
							result[i] = bone
						}
					}
				}
				matched := 0
				for _, bone := range result {
					if bone >= 0 {
						matched++
					}
				}
				if matched > 0 {
					pv.animationRootBone = findAnimationRootBone(info, result, pv.lookupThinHash)
					return result
				}
			}
		}
	}
	result = mapAnimationBonesByBindPose(info, clip, pv.lookupThinHash)
	pv.animationRootBone = findAnimationRootBone(info, result, pv.lookupThinHash)
	return result
}

// findAnimationRootBone selects the highest-level animated transform that
// carries locomotion. Stingray rigs normally call it "root"; some assets use
// explicit motion/trajectory names, while a few omit the conventional name.
func findAnimationRootBone(info *unit.Info, mapping []int, lookupThinHash func(stingray.ThinHash) string) int {
	if info == nil || len(mapping) == 0 {
		return -1
	}
	mapped := make([]bool, len(info.Bones))
	for _, boneIndex := range mapping {
		if boneIndex >= 0 && boneIndex < len(mapped) {
			mapped[boneIndex] = true
		}
	}
	depthOf := func(index int) int {
		depth := 0
		seen := make(map[int]bool)
		for index >= 0 && index < len(info.Bones) && !seen[index] {
			seen[index] = true
			parent := int(info.Bones[index].ParentIndex)
			if parent == index || parent < 0 || parent >= len(info.Bones) {
				break
			}
			index = parent
			depth++
		}
		return depth
	}
	bestIndex, bestPriority, bestDepth := -1, int(^uint(0)>>1), int(^uint(0)>>1)
	for index, bone := range info.Bones {
		if !mapped[index] {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(lookupThinHash(bone.NameHash)))
		normalized := strings.NewReplacer("_", "", "-", "", " ", "").Replace(name)
		priority := 5
		switch normalized {
		case "root":
			priority = 0
		case "rootmotion", "motionroot", "roottrajectory", "trajectoryroot":
			priority = 1
		case "motion", "trajectory":
			priority = 2
		case "hips", "pelvis":
			priority = 3
		case "skeleton", "stingrayentityroot":
			// These are structural parents rather than the moving rig root.
			continue
		default:
			if strings.Contains(name, "shadow") || strings.Contains(name, "cull") || strings.Contains(name, "collision") || strings.Contains(name, "hitbox") {
				continue
			}
		}
		depth := depthOf(index)
		if priority < bestPriority || (priority == bestPriority && depth < bestDepth) {
			bestIndex, bestPriority, bestDepth = index, priority, depth
		}
	}
	return bestIndex
}

// animationRootMotionCorrection cancels horizontal root translation relative
// to the clip's first sampled pose. Damage and hitbox overlays intentionally
// use the uncorrected base scene matrix so their authored transforms stay tied
// to the animation rather than the in-place display adjustment.
func (pv *UnitPreviewState) animationRootMotionCorrection() mgl32.Vec3 {
	if !pv.animationEnabled || pv.rootUnitInfo == nil || pv.animationRootBone < 0 ||
		pv.animationRootBone >= len(pv.rootUnitInfo.Bones) || pv.animationRootBone >= len(pv.animationGlobals) ||
		!pv.animationRootReferenceValid {
		return mgl32.Vec3{}
	}
	animatedPosition := pv.animationGlobals[pv.animationRootBone].Col(3).Vec3()
	correction := pv.animationRootReference.Sub(animatedPosition)
	// Y is up in the preview coordinate system. Cancel locomotion on the ground
	// plane while retaining root-bone bobbing and other vertical body motion.
	correction[1] = 0
	for axis := range 3 {
		if float32IsNaNOrInf(correction[axis]) {
			return mgl32.Vec3{}
		}
	}
	return correction
}

func mapAnimationBonesByBindPose(info *unit.Info, clip *animation.Animation, lookupThinHash func(stingray.ThinHash) string) []int {
	if info == nil || clip == nil || clip.Header.BoneCount == 0 {
		return nil
	}
	count := int(clip.Header.BoneCount)
	var candidates []int
	seen := make(map[int]bool)
	for _, bone := range info.Bones {
		if strings.EqualFold(lookupThinHash(bone.NameHash), "skeleton") && len(bone.Children) > 0 {
			candidates = append(candidates, int(bone.Index))
			seen[int(bone.Index)] = true
			var visit func(uint32)
			visit = func(index uint32) {
				if int(index) >= len(info.Bones) {
					return
				}
				if !seen[int(index)] {
					candidates = append(candidates, int(index))
					seen[int(index)] = true
				}
				for _, child := range info.Bones[index].Children {
					visit(child)
				}
			}
			for _, child := range bone.Children {
				visit(child)
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	type match struct {
		track int
		bone  int
		score float64
	}
	matches := make([]match, 0, count*len(candidates))
	for track := 0; track < min(count, len(clip.Header.InitialTransforms)); track++ {
		initial := &clip.Header.InitialTransforms[track]
		animationMatrix := animationTransformMatrix(initial.Position(), initial.Rotation(), initial.Scale())
		for _, boneIndex := range candidates {
			bone := info.Bones[boneIndex]
			local := bone.Matrix
			if int(bone.ParentIndex) < len(info.Bones) && int(bone.ParentIndex) != boneIndex {
				local = info.Bones[bone.ParentIndex].Matrix.Inv().Mul4(bone.Matrix)
			}
			positionError := local.Col(3).Vec3().Sub(initial.Position()).Len()
			rotationScaleError := 0.0
			for row := 0; row < 3; row++ {
				for col := 0; col < 3; col++ {
					delta := float64(local.At(row, col) - animationMatrix.At(row, col))
					rotationScaleError += delta * delta
				}
			}
			matches = append(matches, match{track: track, bone: boneIndex, score: float64(positionError) + rotationScaleError*0.5})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score == matches[j].score {
			if matches[i].track == matches[j].track {
				return matches[i].bone < matches[j].bone
			}
			return matches[i].track < matches[j].track
		}
		return matches[i].score < matches[j].score
	})
	result := make([]int, count)
	for i := range result {
		result[i] = -1
	}
	used := make(map[int]bool)
	for _, candidate := range matches {
		if candidate.score > 1.25 {
			break
		}
		if result[candidate.track] != -1 || used[candidate.bone] {
			continue
		}
		result[candidate.track] = candidate.bone
		used[candidate.bone] = true
	}
	return result
}

func animationTransformMatrix(position mgl32.Vec3, rotation mgl32.Quat, scale mgl32.Vec3) mgl32.Mat4 {
	return mgl32.Translate3D(position[0], position[1], position[2]).Mul4(rotation.Mat4()).Mul4(
		mgl32.Scale3D(scale[0], scale[1], scale[2]),
	)
}

func remapMeshBoneIndices(indices [][4]uint8, groups []unit.MeshGroup, skeletonMap *unit.SkeletonMap) [][4]uint8 {
	remapped := append([][4]uint8(nil), indices...)
	if skeletonMap == nil {
		return remapped
	}
	vertexOffset := 0
	for groupIndex, group := range groups {
		end := min(vertexOffset+int(group.NumVertices), len(remapped))
		if groupIndex < len(skeletonMap.RemapList) {
			remap := skeletonMap.RemapList[groupIndex]
			for vertex := vertexOffset; vertex < end; vertex++ {
				for influence, sourceIndex := range indices[vertex] {
					remapIndex := int(sourceIndex)
					if remapIndex >= len(remap) {
						remapped[vertex][influence] = 0
						continue
					}
					localIndex := remap[remapIndex]
					if int(localIndex) >= len(skeletonMap.BoneIndices) || localIndex > 255 {
						remapped[vertex][influence] = 0
						continue
					}
					remapped[vertex][influence] = uint8(localIndex)
				}
			}
		}
		vertexOffset = end
	}
	return remapped
}

func decomposeAnimationBase(matrix mgl32.Mat4) (mgl32.Vec3, mgl32.Quat, mgl32.Vec3) {
	linear := matrix.Mat3()
	scale := mgl32.Vec3{linear.Row(0).Len(), linear.Row(1).Len(), linear.Row(2).Len()}
	invScale := mgl32.Vec3{1, 1, 1}
	for i := range 3 {
		if scale[i] > 1e-8 {
			invScale[i] = 1 / scale[i]
		}
	}
	rotation := linear.Mul3(mgl32.Diag3(invScale)).Mat4()
	return matrix.Col(3).Vec3(), mgl32.Mat4ToQuat(rotation), scale
}

func (pv *UnitPreviewState) updateAnimationPose() {
	player := &pv.animationPlayer
	info := pv.rootUnitInfo
	if player.clip == nil || info == nil || len(pv.animationBoneMap) != int(player.clip.Header.BoneCount) {
		return
	}
	positions, rotations, scales := player.sample()
	if len(positions) == 0 {
		return
	}
	if cap(pv.animationLocals) < len(info.Bones) {
		pv.animationLocals = make([]mgl32.Mat4, len(info.Bones))
	} else {
		pv.animationLocals = pv.animationLocals[:len(info.Bones)]
	}
	if cap(pv.animationGlobals) < len(info.Bones) {
		pv.animationGlobals = make([]mgl32.Mat4, len(info.Bones))
	} else {
		pv.animationGlobals = pv.animationGlobals[:len(info.Bones)]
	}
	locals, globals := pv.animationLocals, pv.animationGlobals
	for i, bone := range info.Bones {
		if int(bone.ParentIndex) < len(info.Bones) && int(bone.ParentIndex) != i {
			locals[i] = info.Bones[bone.ParentIndex].Matrix.Inv().Mul4(bone.Matrix)
		} else {
			locals[i] = bone.Matrix
		}
	}
	for animBone, unitBone := range pv.animationBoneMap {
		if unitBone < 0 || unitBone >= len(locals) || animBone >= len(positions) {
			continue
		}
		currentMatrix := animationTransformMatrix(positions[animBone], rotations[animBone], scales[animBone])
		if player.additive || player.clip.Header.InitialTransforms[animBone].IsAdditive() {
			basePosition, baseRotation, _ := decomposeAnimationBase(locals[unitBone])
			locals[unitBone] = animationTransformMatrix(
				basePosition.Add(positions[animBone]),
				baseRotation.Mul(rotations[animBone]),
				scales[animBone],
			)
		} else {
			// Non-additive tracks are local transforms, as used by Filediver's
			// animation exporter. Replacing the local pose avoids accumulating
			// an incorrect bind-pose delta on rigs with different joint bases.
			locals[unitBone] = currentMatrix
		}
	}
	// Bone arrays are not guaranteed to be parent-first. Resolve the hierarchy
	// recursively so animated globals (and the mesh transforms and damage zones
	// attached to them) use the current parent pose even when a parent is stored
	// after its child in the unit's bone table.
	globalState := make([]uint8, len(info.Bones))
	var resolveGlobal func(int) mgl32.Mat4
	resolveGlobal = func(index int) mgl32.Mat4 {
		if globalState[index] == 2 {
			return globals[index]
		}
		bone := info.Bones[index]
		parent := int(bone.ParentIndex)
		if globalState[index] == 1 {
			// Malformed/cyclic skeletons fall back to this bone's local pose
			// instead of recursing forever.
			globals[index] = locals[index]
			globalState[index] = 2
			return globals[index]
		}
		globalState[index] = 1
		if parent >= 0 && parent < len(info.Bones) && parent != index {
			globals[index] = resolveGlobal(parent).Mul4(locals[index])
		} else {
			globals[index] = locals[index]
		}
		globalState[index] = 2
		return globals[index]
	}
	for i := range info.Bones {
		resolveGlobal(i)
	}
	if !pv.animationRootReferenceValid && pv.animationRootBone >= 0 && pv.animationRootBone < len(globals) {
		pv.animationRootReference = globals[pv.animationRootBone].Col(3).Vec3()
		pv.animationRootReferenceValid = true
	}
	pv.refreshMountedAttachmentTransforms(pv.rootHash, globals)
	for fileID, objects := range pv.objects {
		pieceInfo := info
		pieceGlobals := globals
		if pv.armorAnimationMode {
			pieceInfo = pv.unitInfos[fileID]
			mapping := pv.armorBoneMap[fileID]
			if pieceInfo == nil || len(mapping) != len(pieceInfo.Bones) {
				continue
			}
			pieceGlobals = make([]mgl32.Mat4, len(mapping))
			for i, idx := range mapping {
				pieceGlobals[i] = pieceInfo.Bones[i].Matrix
				if idx >= 0 && idx < len(globals) {
					pieceGlobals[i] = globals[idx]
				}
			}
		} else if fileID != pv.rootHash {
			continue
		}
		for meshName, object := range objects {
			if object.boneIndex >= 0 && object.boneIndex < len(pieceGlobals) {
				object.matrix = pieceGlobals[object.boneIndex]
				if object.skeletonMapIndex >= 0 && object.skeletonMapIndex < len(pieceInfo.SkeletonMaps) {
					skeletonMap := &pieceInfo.SkeletonMaps[object.skeletonMapIndex]
					if len(object.animSkinMatrices) == len(skeletonMap.BoneIndices) &&
						len(object.animNormalMatrices) == len(skeletonMap.BoneIndices) {
						worldToMesh := object.matrix.Inv()
						for localBone, unitBone := range skeletonMap.BoneIndices {
							if int(unitBone) >= len(pieceGlobals) || localBone >= len(skeletonMap.Matrices) {
								object.animSkinMatrices[localBone] = mgl32.Ident4()
								object.animNormalMatrices[localBone] = mgl32.Ident4()
								continue
							}
							packed := skeletonMap.Matrices[localBone]
							inverseBind := mgl32.Mat4FromRows(
								mgl32.Vec4(packed[0]), mgl32.Vec4(packed[1]), mgl32.Vec4(packed[2]), mgl32.Vec4(packed[3]),
							).Transpose()
							object.animSkinMatrices[localBone] = worldToMesh.Mul4(pieceGlobals[unitBone]).Mul4(inverseBind)
							object.animNormalMatrices[localBone] = object.animSkinMatrices[localBone].Inv().Transpose()
						}
						gl.BindBuffer(gl.SHADER_STORAGE_BUFFER, object.animSkinBuffer)
						gl.BufferSubData(gl.SHADER_STORAGE_BUFFER, 0, len(object.animSkinMatrices)*64, gl.Ptr(object.animSkinMatrices))
						gl.BindBuffer(gl.SHADER_STORAGE_BUFFER, object.animNormalBuffer)
						gl.BufferSubData(gl.SHADER_STORAGE_BUFFER, 0, len(object.animNormalMatrices)*64, gl.Ptr(object.animNormalMatrices))
						gl.BindBuffer(gl.SHADER_STORAGE_BUFFER, 0)
					}
				}
				pv.objects[fileID][meshName] = object
			}
		}
		if skeleton, ok := pv.skeletons[fileID]; ok {
			boneIndices := pv.skeletonBoneIndices[fileID]
			vertices := pv.skeletonPositions[fileID][:0]
			if cap(vertices) < len(boneIndices) {
				vertices = make([]mgl32.Vec3, 0, len(boneIndices))
			}
			for _, idx := range boneIndices {
				if idx < 0 || idx >= len(pieceGlobals) {
					vertices = append(vertices, mgl32.Vec3{})
					continue
				}
				vertices = append(vertices, pieceGlobals[idx].Mul4x1(mgl32.Vec4{0, 0, 0, 1}).Vec3())
			}
			if len(vertices) > 0 {
				gl.BindBuffer(gl.ARRAY_BUFFER, skeleton.vbo)
				gl.BufferSubData(gl.ARRAY_BUFFER, 0, len(vertices)*12, gl.Ptr(vertices))
				gl.BindBuffer(gl.ARRAY_BUFFER, 0)
				pv.skeletonPositions[fileID] = vertices
			}
		}
	}
}
