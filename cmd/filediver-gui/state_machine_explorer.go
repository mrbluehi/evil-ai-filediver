package main

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/AllenDang/cimgui-go/imgui"
	fnt "github.com/xypwn/filediver/cmd/filediver-gui/fonts"
	"github.com/xypwn/filediver/cmd/filediver-gui/imutils"
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/animation"
	"github.com/xypwn/filediver/stingray/state_machine"
	"github.com/xypwn/filediver/stingray/unit"
)

type stateMachineUsageScan struct {
	sync.Mutex
	dir        *stingray.DataDir
	generation uint64
	cancel     context.CancelFunc
	progress   int
	total      int
	done       bool
	refs       map[stingray.FileID][]stingray.FileID
	err        error
}

type stateMachineAnimationPreviewRequest struct {
	machineID   stingray.FileID
	animationID stingray.Hash
	stateName   string
	preferBlend bool
}

type stateMachineExplorerState struct {
	dir             *stingray.DataDir
	machines        []stingray.FileID
	selectedMachine stingray.FileID
	query           string
	parsedMachineID stingray.FileID
	parsedMachine   *state_machine.StateMachine
	parseErr        error
	selectedState   int
	selectedLayer   int
	leftWidth       float32
	selectedAnim    stingray.FileID
	pendingPreview  *stateMachineAnimationPreviewRequest
	parsedAnimID    stingray.FileID
	parsedAnim      *animation.Animation
	animParseErr    error
	usage           stateMachineUsageScan
}

func (s *stateMachineExplorerState) cancelScan() {
	s.usage.Lock()
	if s.usage.cancel != nil {
		s.usage.cancel()
		s.usage.cancel = nil
	}
	s.usage.Unlock()
}

func (s *stateMachineExplorerState) syncBuild(ctx context.Context, gd *GameData) {
	if gd == nil || gd.DataDir == nil || s.dir == gd.DataDir {
		return
	}
	s.cancelScan()
	s.dir = gd.DataDir
	s.machines = s.machines[:0]
	for id := range gd.DataDir.Files {
		if id.Type == stingray.Sum("state_machine") {
			s.machines = append(s.machines, id)
		}
	}
	slices.SortFunc(s.machines, func(a, b stingray.FileID) int {
		if c := strings.Compare(gd.KnownFileNames[a], gd.KnownFileNames[b]); c != 0 {
			return c
		}
		return a.Name.Cmp(b.Name)
	})
	s.selectedMachine = stingray.FileID{}
	if len(s.machines) > 0 {
		s.selectedMachine = s.machines[0]
	}
	s.parsedMachineID = stingray.FileID{}
	s.parsedMachine = nil
	s.parseErr = nil
	s.selectedLayer, s.selectedState = -1, -1
	s.selectedAnim = stingray.FileID{}
	s.pendingPreview = nil
	s.parsedAnimID = stingray.FileID{}
	s.parsedAnim = nil
	s.animParseErr = nil
	s.usage.Lock()
	s.usage.dir = gd.DataDir
	s.usage.generation++
	s.usage.progress = 0
	s.usage.total = 0
	s.usage.done = false
	s.usage.refs = nil
	s.usage.err = nil
	s.usage.Unlock()
	s.startUsageScan(ctx, gd)
}

func (s *stateMachineExplorerState) startUsageScan(parent context.Context, gd *GameData) {
	if gd == nil || gd.DataDir == nil {
		return
	}
	units := make([]stingray.FileID, 0)
	for id := range gd.DataDir.Files {
		if id.Type == stingray.Sum("unit") {
			units = append(units, id)
		}
	}
	slices.SortFunc(units, stingray.FileID.Cmp)
	ctx, cancel := context.WithCancel(parent)
	s.usage.Lock()
	s.usage.generation++
	generation := s.usage.generation
	s.usage.cancel = cancel
	s.usage.progress = 0
	s.usage.total = len(units)
	s.usage.done = false
	s.usage.refs = nil
	s.usage.err = nil
	s.usage.Unlock()

	dir := gd.DataDir
	go func() {
		refs := make(map[stingray.FileID][]stingray.FileID)
		unreadable := 0
		for i, unitID := range units {
			if ctx.Err() != nil {
				return
			}
			if stateID, ok := unitStateMachineReference(dir, unitID); ok {
				refs[stateID] = append(refs[stateID], unitID)
			} else if !dataDirHasMain(dir, unitID) {
				unreadable++
			}
			if i%48 == 47 || i == len(units)-1 {
				s.usage.Lock()
				if s.usage.generation == generation {
					s.usage.progress = i + 1
				}
				s.usage.Unlock()
			}
		}
		for id := range refs {
			slices.SortFunc(refs[id], func(a, b stingray.FileID) int {
				return strings.Compare(gd.KnownFileNames[a], gd.KnownFileNames[b])
			})
		}
		s.usage.Lock()
		if s.usage.generation == generation {
			s.usage.refs = refs
			s.usage.progress = len(units)
			s.usage.done = true
			s.usage.cancel = nil
			if unreadable > 0 {
				s.usage.err = fmt.Errorf("%d unit resources could not be read", unreadable)
			}
		}
		s.usage.Unlock()
	}()
}

func unitStateMachineReference(dir *stingray.DataDir, id stingray.FileID) (stateID stingray.FileID, ok bool) {
	defer func() {
		if recover() != nil {
			stateID, ok = stingray.FileID{}, false
		}
	}()
	data, err := dir.Read(id, stingray.DataMain)
	if err != nil {
		return stingray.FileID{}, false
	}
	info, err := unit.LoadInfo(bytes.NewReader(data))
	if err != nil || info.StateMachine.Value == 0 {
		return stingray.FileID{}, false
	}
	return stingray.NewFileID(info.StateMachine, stingray.Sum("state_machine")), true
}

func (s *stateMachineExplorerState) usageSnapshot() (map[stingray.FileID][]stingray.FileID, bool, int, int, error) {
	s.usage.Lock()
	defer s.usage.Unlock()
	return s.usage.refs, s.usage.done, s.usage.progress, s.usage.total, s.usage.err
}

func (s *stateMachineExplorerState) loadMachine(gd *GameData) {
	if s.selectedMachine == s.parsedMachineID {
		return
	}
	s.parsedMachineID = s.selectedMachine
	s.parsedMachine = nil
	s.parseErr = nil
	if s.selectedMachine == (stingray.FileID{}) || gd == nil || gd.DataDir == nil {
		return
	}
	data, err := gd.DataDir.Read(s.selectedMachine, stingray.DataMain)
	if err != nil {
		s.parseErr = err
		return
	}
	s.parsedMachine, s.parseErr = state_machine.LoadStateMachine(bytes.NewReader(data))
}

func (s *stateMachineExplorerState) loadAnimation(gd *GameData) {
	if s.selectedAnim == s.parsedAnimID {
		return
	}
	s.parsedAnimID = s.selectedAnim
	s.parsedAnim = nil
	s.animParseErr = nil
	if s.selectedAnim == (stingray.FileID{}) || gd == nil || gd.DataDir == nil {
		return
	}
	data, err := gd.DataDir.Read(s.selectedAnim, stingray.DataMain)
	if err != nil {
		s.animParseErr = err
		return
	}
	s.parsedAnim, s.animParseErr = animation.LoadAnimation(bytes.NewReader(data))
}

func (a *guiApp) drawStateMachineExplorerWindow() {
	if !a.stateMachineExplorerOpen {
		return
	}
	if a.stateMachineExplorer == nil {
		a.stateMachineExplorer = &stateMachineExplorerState{selectedLayer: -1, selectedState: -1}
	}
	s := a.stateMachineExplorer
	visible := imgui.BeginV("State Machines", &a.stateMachineExplorerOpen, 0)
	if visible {
		if a.gameData == nil || a.gameData.DataDir == nil {
			imgui.TextUnformatted("Load a game build to inspect its state machines and animation references.")
		} else {
			s.syncBuild(a.ctx, a.gameData)
			s.draw(a)
		}
	}
	imgui.End()
}

func (s *stateMachineExplorerState) draw(a *guiApp) {
	gd := a.gameData
	imgui.TextUnformatted("Inspect the selected build's state-machine states, animation links, events, variables, and unit references.")
	imgui.SetNextItemWidth(-1)
	imgui.InputTextWithHint("##StateMachineSearch", "Filter state machines by name or hash...", &s.query, 0, nil)

	avail := imgui.ContentRegionAvail()
	if s.leftWidth <= 0 {
		s.leftWidth = min(imutils.S(330), avail.X*0.34)
	}
	minLeft, minRight := imutils.S(180), imutils.S(300)
	maxLeft := max(minLeft, avail.X-minRight-imutils.S(8))
	s.leftWidth = min(max(s.leftWidth, minLeft), maxLeft)
	if imgui.BeginChildStrV("##StateMachineList", imgui.NewVec2(s.leftWidth, 0), imgui.ChildFlagsBorders, imgui.WindowFlagsNone) {
		matches := make([]stingray.FileID, 0, len(s.machines))
		for _, id := range s.machines {
			name := gd.LookupHash(id.Name)
			if strings.TrimSpace(s.query) == "" || strings.Contains(strings.ToLower(name), strings.ToLower(strings.TrimSpace(s.query))) || strings.Contains(strings.ToLower(id.Name.String()), strings.ToLower(strings.TrimSpace(s.query))) {
				matches = append(matches, id)
			}
		}
		imutils.Textcf(imgui.NewVec4(0.7, 0.7, 0.7, 1), "%d / %d state machines", len(matches), len(s.machines))
		clipper := imgui.NewListClipper()
		clipper.Begin(int32(len(matches)))
		for clipper.Step() {
			for row := clipper.DisplayStart(); row < clipper.DisplayEnd(); row++ {
				id := matches[row]
				label := stateMachineResourceLabel(gd, id)
				imgui.PushIDStr(id.Name.String())
				if imgui.SelectableBoolV(label, id == s.selectedMachine, imgui.SelectableFlagsNone, imgui.NewVec2(0, 0)) {
					s.selectedMachine = id
					s.selectedLayer, s.selectedState = -1, -1
					s.selectedAnim = stingray.FileID{}
					s.pendingPreview = nil
					s.parsedAnimID = stingray.FileID{}
					s.parsedAnim = nil
				}
				imgui.PopID()
			}
		}
		clipper.End()
	}
	imgui.EndChild()
	imgui.SameLine()
	splitterSize := imgui.NewVec2(imutils.S(6), avail.Y)
	splitterPos := imgui.CursorScreenPos()
	imgui.InvisibleButton("##StateMachineSplitter", splitterSize)
	splitterColor := imgui.ColorConvertFloat4ToU32(imgui.NewVec4(0.18, 0.23, 0.31, 1))
	if imgui.IsItemHovered() || imgui.IsItemActive() {
		imgui.SetMouseCursor(imgui.MouseCursorResizeEW)
		imgui.SetItemTooltip("Drag to resize the state-machine list and details")
		splitterColor = imgui.ColorConvertFloat4ToU32(imgui.NewVec4(0.32, 0.48, 0.68, 1))
	}
	if imgui.IsItemActive() {
		s.leftWidth = min(max(s.leftWidth+imgui.CurrentIO().MouseDelta().X, minLeft), maxLeft)
	}
	imgui.WindowDrawList().AddRectFilled(splitterPos, imgui.NewVec2(splitterPos.X+splitterSize.X, splitterPos.Y+splitterSize.Y), splitterColor)
	imgui.SameLine()
	if imgui.BeginChildStrV("##StateMachineDetails", imgui.NewVec2(0, 0), imgui.ChildFlagsBorders, imgui.WindowFlagsNone) {
		s.loadMachine(gd)
		refs, scanDone, progress, total, scanErr := s.usageSnapshot()
		if scanDone && s.pendingPreview != nil {
			request := s.pendingPreview
			s.pendingPreview = nil
			if request.machineID == s.selectedMachine {
				s.openAnimationPreview(a, refs, request.animationID, request.stateName, request.preferBlend)
			}
		}
		if !scanDone {
			imgui.TextUnformatted(fmt.Sprintf("Indexing unit → state-machine links: %d / %d", progress, total))
			if total > 0 {
				imgui.ProgressBar(float32(progress) / float32(total))
			}
		} else if scanErr != nil {
			imgui.TextDisabled(fmt.Sprintf("Reference scan complete with skipped resources: %v", scanErr))
		}
		if s.selectedMachine == (stingray.FileID{}) {
			imgui.TextUnformatted("No state-machine resources found in this build.")
		} else {
			imgui.TextWrapped(stateMachineResourceLabel(gd, s.selectedMachine))
			imutils.Textcf(imgui.NewVec4(0.65, 0.65, 0.65, 1), "Hash: %s", s.selectedMachine.Name.String())
			if s.parseErr != nil {
				imutils.TextError(fmt.Errorf("reading state machine: %w", s.parseErr))
			} else if s.parsedMachine != nil {
				machine := s.parsedMachine
				stateCount, animationCount := 0, 0
				for _, layer := range machine.Layers {
					stateCount += len(layer.States)
					for _, state := range layer.States {
						animationCount += len(state.AnimationHashes)
					}
				}
				imutils.Textf("%d layers · %d states · %d animation references · %d events · %d variables · %d blend masks", len(machine.Layers), stateCount, animationCount, len(machine.AnimationEventHashes), len(machine.AnimationVariableNames), len(machine.BlendMaskList))
				imgui.Separator()
				s.drawMachineUsage(a, refs, scanDone)
				s.drawMachineGlobals(gd, machine)
				s.drawMachineLayers(a, machine)
				s.drawSelectedAnimation(a)
			}
		}
	}
	imgui.EndChild()
}

func stateMachineResourceLabel(gd *GameData, id stingray.FileID) string {
	name := gd.LookupHash(id.Name)
	if name == id.Name.String() {
		return fmt.Sprintf("%s.state_machine", id.Name)
	}
	return name
}

func (s *stateMachineExplorerState) drawMachineUsage(a *guiApp, refs map[stingray.FileID][]stingray.FileID, scanDone bool) {
	imgui.TextUnformatted("Units that link to this state machine")
	if !scanDone {
		imgui.TextDisabled("Results will appear when the reference scan finishes.")
		return
	}
	units := refs[s.selectedMachine]
	if len(units) == 0 {
		imgui.TextDisabled("No unit directly references this state machine in the loaded build.")
		return
	}
	if imgui.BeginTableV("##StateMachineUnits", 2, imgui.TableFlagsBorders|imgui.TableFlagsRowBg|imgui.TableFlagsScrollY|imgui.TableFlagsResizable, imgui.NewVec2(0, imutils.S(120)), 0) {
		imgui.TableSetupColumnV("Unit", imgui.TableColumnFlagsWidthStretch, 3, 0)
		imgui.TableSetupColumnV("Open", imgui.TableColumnFlagsWidthFixed, imutils.S(105), 0)
		imgui.TableHeadersRow()
		clipper := imgui.NewListClipper()
		clipper.Begin(int32(len(units)))
		for clipper.Step() {
			for i := clipper.DisplayStart(); i < clipper.DisplayEnd(); i++ {
				id := units[i]
				imgui.PushIDStr("unit-" + id.Name.String())
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.TextUnformatted(a.gameData.DisplayFileName(id))
				imgui.TableNextColumn()
				if imgui.SmallButton("Preview") {
					a.browserJumpToFileID = id
					imgui.SetWindowFocusStr(fnt.I.Preview + " Preview")
				}
				imgui.PopID()
			}
		}
		clipper.End()
		imgui.EndTable()
	}
}

func (s *stateMachineExplorerState) drawMachineGlobals(gd *GameData, machine *state_machine.StateMachine) {
	if imgui.TreeNodeExStrV("Variables and events", imgui.TreeNodeFlagsDefaultOpen) {
		if len(machine.AnimationVariableNames) == 0 {
			imgui.TextDisabled("No animation variables")
		} else if imgui.BeginTableV("##AnimationVariables", 2, imgui.TableFlagsBorders|imgui.TableFlagsRowBg, imgui.NewVec2(0, 0), 0) {
			imgui.TableSetupColumnV("Variable", imgui.TableColumnFlagsWidthStretch, 1, 0)
			imgui.TableSetupColumnV("Default", imgui.TableColumnFlagsWidthFixed, imutils.S(120), 0)
			imgui.TableHeadersRow()
			for i, hash := range machine.AnimationVariableNames {
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.TextUnformatted(gd.LookupThinHash(hash))
				imgui.TableNextColumn()
				value := float32(0)
				if i < len(machine.AnimationVariableValues) {
					value = machine.AnimationVariableValues[i]
				}
				imutils.Textf("%.4g", value)
			}
			imgui.EndTable()
		}
		if len(machine.AnimationEventHashes) == 0 {
			imgui.TextDisabled("No animation events")
		} else {
			imgui.TextUnformatted("Events: " + strings.Join(resolveThinHashes(gd, machine.AnimationEventHashes), ", "))
		}
		imgui.TreePop()
	}
}

func resolveThinHashes(gd *GameData, hashes []stingray.ThinHash) []string {
	values := make([]string, 0, len(hashes))
	for _, hash := range hashes {
		values = append(values, gd.LookupThinHash(hash))
	}
	slices.Sort(values)
	return values
}

func (s *stateMachineExplorerState) drawMachineLayers(a *guiApp, machine *state_machine.StateMachine) {
	gd := a.gameData
	for layerIndex, layer := range machine.Layers {
		defaultLabel := fmt.Sprintf("State %d", layer.DefaultState)
		if int(layer.DefaultState) < len(layer.States) {
			defaultLabel = stateMachineStateName(gd, layer.States[layer.DefaultState])
		}
		section := fmt.Sprintf("Layer %d · %d states · default: %s", layerIndex+1, len(layer.States), defaultLabel)
		imgui.PushIDInt(int32(layerIndex))
		if imgui.TreeNodeExStrV(section, imgui.TreeNodeFlagsDefaultOpen) {
			for stateIndex, state := range layer.States {
				imgui.PushIDInt(int32(stateIndex))
				isDefault := uint32(stateIndex) == layer.DefaultState
				stateTitle := fmt.Sprintf("%s · %s", stateMachineStateName(gd, state), state.Type.String())
				if isDefault {
					stateTitle = "Default · " + stateTitle
				}
				if imgui.TreeNodeExStrV(stateTitle, imgui.TreeNodeFlagsNone) {
					s.drawStateDetail(a, machine, layerIndex, stateIndex, state)
					imgui.TreePop()
				}
				imgui.PopID()
			}
			imgui.TreePop()
		}
		imgui.PopID()
	}
}

func stateMachineStateName(gd *GameData, state state_machine.State) string {
	name := gd.LookupHash(state.Name)
	return name
}

func stateMachineStateTypeHelp(t state_machine.StateType) string {
	switch t {
	case state_machine.StateType_Clip:
		return "Clip state: references one or more animation clips. Looping and blend settings affect playback."
	case state_machine.StateType_Empty:
		return "Empty state: contributes no clip pose, allowing lower animation layers to show through."
	case state_machine.StateType_Blend:
		return "Blend state: combines referenced clips using weights or controller-variable expressions."
	case state_machine.StateType_Time:
		return "Time state: controller time/variables drive how the referenced animation is sampled."
	case state_machine.StateType_Ragdoll:
		return "Ragdoll state: delegates pose control to the named ragdoll setup."
	default:
		return "This state type is not interpreted by Filediver yet."
	}
}

func (s *stateMachineExplorerState) drawStateDetail(a *guiApp, machine *state_machine.StateMachine, layerIndex, stateIndex int, state state_machine.State) {
	gd := a.gameData
	stateName := stateMachineStateName(gd, state)
	if state.Type == state_machine.StateType_Blend {
		validAnimationCount := 0
		for _, hash := range state.AnimationHashes {
			if dataDirHasMain(gd.DataDir, stingray.NewFileID(hash, stingray.Sum("animation"))) {
				validAnimationCount++
			}
		}
		if validAnimationCount > 1 {
			if imgui.Button("Preview blended state") {
				for _, hash := range state.AnimationHashes {
					if dataDirHasMain(gd.DataDir, stingray.NewFileID(hash, stingray.Sum("animation"))) {
						s.requestAnimationPreview(a, hash, stateName, true)
						break
					}
				}
			}
			imgui.SetItemTooltip("Open a unit that uses this state machine and play this state with its blend controls.")
		}
	}
	flags := make([]string, 0, 4)
	if state.Loop {
		flags = append(flags, "loops")
	}
	if state.Additive {
		flags = append(flags, "additive")
	}
	if len(flags) == 0 {
		flags = append(flags, "non-looping, non-additive")
	}
	imgui.TextUnformatted(strings.Join(flags, " · "))
	imgui.TextWrapped(stateMachineStateTypeHelp(state.Type))
	imgui.TextUnformatted(fmt.Sprintf("Blend mask index: %d · blend variable index: %d", state.BlendSetMaskIndex, state.BlendVariableIndex))
	if state.BlendSetMaskIndex >= 0 && int(state.BlendSetMaskIndex) < len(machine.BlendMaskList) {
		mask := machine.BlendMaskList[state.BlendSetMaskIndex]
		weightedBones := 0
		for _, weight := range mask {
			if weight > 0 {
				weightedBones++
			}
		}
		label := fmt.Sprintf("Blend mask weights · %d of %d bone indices active", weightedBones, len(mask))
		if imgui.TreeNodeExStrV(label, imgui.TreeNodeFlagsNone) {
			for boneIndex, weight := range mask {
				if weight > 0 {
					imgui.BulletText(fmt.Sprintf("bone index %d · weight %.3f", boneIndex, weight))
				}
			}
			imgui.TreePop()
		}
	}
	if state.EmitEndEvent.Value != 0 {
		imgui.TextUnformatted(fmt.Sprintf("End event: %s · end transition: %.3fs", gd.LookupThinHash(state.EmitEndEvent), state.EndTransitionTime))
	}
	if state.RagdollName.Value != 0 {
		imgui.TextUnformatted("Ragdoll: " + gd.LookupThinHash(state.RagdollName))
	}
	if state.Type == state_machine.StateType_Blend || state.Type == state_machine.StateType_Time {
		for i, blend := range state.CustomBlendFuncDefinition {
			variables := make([]string, len(machine.AnimationVariableNames))
			for j, name := range machine.AnimationVariableNames {
				variables[j] = gd.LookupThinHash(name)
			}
			driver, err := blend.ToDriver(variables)
			if err != nil {
				imgui.TextDisabled(fmt.Sprintf("Blend driver %d: %v", i+1, err))
				continue
			}
			imgui.TextUnformatted(fmt.Sprintf("Blend driver %d (%s): %s", i+1, driver.Type.String(), driver.Expression))
		}
	}

	if len(state.AnimationHashes) > 0 {
		imgui.TextUnformatted("Animation resources:")
		for i, hash := range state.AnimationHashes {
			id := stingray.NewFileID(hash, stingray.Sum("animation"))
			weight := float32(1)
			if i < len(state.AnimationWeights) {
				weight = state.AnimationWeights[i]
			}
			label := fmt.Sprintf("%s · weight %.4g", gd.LookupHash(hash), weight)
			if !dataDirHasMain(gd.DataDir, id) {
				label += " · missing resource"
			}
			imgui.PushIDStr("animation-" + hash.String())
			if imgui.SelectableBoolV(label, s.selectedAnim == id, imgui.SelectableFlagsNone, imgui.NewVec2(0, 0)) {
				s.selectedAnim = id
				s.parsedAnimID = stingray.FileID{}
				s.parsedAnim = nil
				s.animParseErr = nil
				if dataDirHasMain(gd.DataDir, id) {
					s.requestAnimationPreview(a, hash, stateName, false)
				}
			}
			imgui.PopID()
		}
	} else {
		imgui.TextDisabled("This state has no direct animation hash list.")
	}

	if len(state.StateTransitions) > 0 {
		imgui.TextUnformatted("Event transitions:")
		keys := make([]stingray.ThinHash, 0, len(state.StateTransitions))
		for key := range state.StateTransitions {
			keys = append(keys, key)
		}
		slices.SortFunc(keys, stingray.ThinHash.Cmp)
		if imgui.BeginTableV("##StateTransitions", 5, imgui.TableFlagsBorders|imgui.TableFlagsRowBg|imgui.TableFlagsSizingStretchProp, imgui.NewVec2(0, 0), 0) {
			for _, heading := range []string{"Event", "Target index", "Mode", "Blend", "Beat"} {
				imgui.TableSetupColumnV(heading, imgui.TableColumnFlagsWidthStretch, 1, 0)
			}
			imgui.TableHeadersRow()
			for _, key := range keys {
				link := state.StateTransitions[key]
				target := fmt.Sprintf("state %d", link.Index)
				if link.Index < uint32(len(machine.Layers[layerIndex].States)) {
					target = stateMachineStateName(gd, machine.Layers[layerIndex].States[link.Index])
				}
				mode := strings.TrimPrefix(link.Type.String(), "LinkType_")
				blend := "immediate"
				if link.BlendTime >= 0 {
					blend = fmt.Sprintf("%.3fs", link.BlendTime)
				}
				beat := "—"
				if link.Beat.Value != 0 {
					beat = gd.LookupThinHash(link.Beat)
				}
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.TextUnformatted(gd.LookupThinHash(key))
				imgui.TableNextColumn()
				imgui.TextUnformatted(target)
				imgui.TableNextColumn()
				imgui.TextUnformatted(mode)
				imgui.TableNextColumn()
				imgui.TextUnformatted(blend)
				imgui.TableNextColumn()
				imgui.TextUnformatted(beat)
			}
			imgui.EndTable()
		}
	}
}

func (s *stateMachineExplorerState) requestAnimationPreview(a *guiApp, animationID stingray.Hash, stateName string, preferBlend bool) {
	if a == nil || a.preview == nil || a.gameData == nil {
		return
	}
	refs, scanDone, _, _, _ := s.usageSnapshot()
	if !scanDone {
		s.pendingPreview = &stateMachineAnimationPreviewRequest{
			machineID: s.selectedMachine, animationID: animationID, stateName: stateName, preferBlend: preferBlend,
		}
		return
	}
	s.openAnimationPreview(a, refs, animationID, stateName, preferBlend)
}

func (s *stateMachineExplorerState) openAnimationPreview(a *guiApp, refs map[stingray.FileID][]stingray.FileID, animationID stingray.Hash, stateName string, preferBlend bool) {
	if a == nil || a.preview == nil || a.gameData == nil {
		return
	}
	units := refs[s.selectedMachine]
	if len(units) > 0 {
		unitID := units[0]
		if active := a.preview.ActiveID(); active.Type == stingray.Sum("unit") && slices.Contains(units, active) {
			unitID = active
		}
		a.preview.QueueLinkedAnimationPreview(unitID, animationID, stateName, preferBlend)
		a.browserJumpToFileID = unitID
		imgui.SetWindowFocusStr("Animation")
		return
	}
	// If there is no directly linked unit, still open the raw animation resource
	// so the Animation window can display/play its standalone track data.
	a.browserJumpToFileID = stingray.NewFileID(animationID, stingray.Sum("animation"))
	imgui.SetWindowFocusStr("Animation")
}

func (s *stateMachineExplorerState) drawSelectedAnimation(a *guiApp) {
	gd := a.gameData
	if s.selectedAnim == (stingray.FileID{}) {
		return
	}
	s.loadAnimation(gd)
	imgui.Separator()
	imgui.TextUnformatted("Selected animation")
	imgui.TextWrapped(gd.LookupHash(s.selectedAnim.Name))
	if !dataDirHasMain(gd.DataDir, s.selectedAnim) {
		imgui.TextDisabled("The state machine references this animation hash, but the resource is not present in the loaded build.")
		return
	}
	if s.animParseErr != nil {
		imutils.TextError(fmt.Errorf("reading animation resource: %w", s.animParseErr))
		return
	}
	if s.parsedAnim == nil {
		return
	}
	clip := s.parsedAnim
	imgui.TextUnformatted(fmt.Sprintf("Duration %.3fs · %d bones · %d transform keys · %d beats", clip.Header.AnimationLength, clip.Header.BoneCount, len(clip.Entries), len(clip.Header.Beats)))
	imgui.TextDisabled("These are static asset references; runtime code and mission conditions may choose a subset or never enter a state.")
	if len(clip.Header.Beats) > 0 {
		if imgui.TreeNodeExStrV("Animation beats", imgui.TreeNodeFlagsNone) {
			for _, beat := range clip.Header.Beats {
				imgui.BulletText(fmt.Sprintf("%.3fs · %s", beat.TimeStamp, gd.LookupThinHash(beat.Name)))
			}
			imgui.TreePop()
		}
	}
	if imgui.Button("Open animation resource in Browser") {
		a.browserJumpToFileID = s.selectedAnim
		imgui.SetWindowFocusStr(fnt.I.Preview + " Preview")
	}
}
