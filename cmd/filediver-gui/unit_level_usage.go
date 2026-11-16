package main

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/AllenDang/cimgui-go/imgui"
	fnt "github.com/xypwn/filediver/cmd/filediver-gui/fonts"
	"github.com/xypwn/filediver/cmd/filediver-gui/widgets/previews"
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/level"
	"github.com/xypwn/filediver/stingray/prefab"
)

type unitLevelUsageMatch struct {
	LevelID     stingray.FileID
	DirectUnits int
	PrefabUnits int
}

type unitLevelUsageResult struct {
	source     *GameData
	dataDir    *stingray.DataDir
	unitID     stingray.FileID
	buildLabel string
	scanned    int
	skipped    int
	matches    []unitLevelUsageMatch
	err        error
}

func scanUnitLevelUsage(ctx context.Context, data *GameData, dataDir *stingray.DataDir, unitID stingray.FileID, buildLabel string) unitLevelUsageResult {
	result := unitLevelUsageResult{source: data, dataDir: dataDir, unitID: unitID, buildLabel: buildLabel}
	if data == nil || dataDir == nil || unitID.Type != stingray.Sum("unit") {
		result.err = fmt.Errorf("select a loaded model from the Units grid")
		return result
	}

	prefabMemo := make(map[stingray.Hash]int)
	for levelID := range dataDir.Files {
		if levelID.Type != stingray.Sum("level") {
			continue
		}
		select {
		case <-ctx.Done():
			return result
		default:
		}
		levelBytes, err := dataDir.Read(levelID, stingray.DataMain)
		if err != nil {
			result.skipped++
			continue
		}
		levelInfo, err := level.LoadLevel(bytes.NewReader(levelBytes), data.EntityVarMapping)
		if err != nil {
			result.skipped++
			continue
		}
		result.scanned++

		directCount := countLevelUnitMatches(levelInfo.Units, unitID.Name)
		prefabCount := 0
		for _, embedded := range levelInfo.EmbeddedPrefabs {
			prefabCount += countPrefabUnitSliceMatches(embedded.Units, unitID.Name)
			for _, nested := range embedded.NestedPrefabs {
				prefabCount += countPrefabUnitMatches(ctx, dataDir, nested.Path, unitID.Name, prefabMemo, make(map[stingray.Hash]bool), 0)
			}
		}
		for _, placed := range levelInfo.Prefabs {
			prefabCount += countPrefabUnitMatches(ctx, dataDir, placed.Path, unitID.Name, prefabMemo, make(map[stingray.Hash]bool), 0)
		}
		if directCount+prefabCount > 0 {
			result.matches = append(result.matches, unitLevelUsageMatch{
				LevelID:     levelID,
				DirectUnits: directCount,
				PrefabUnits: prefabCount,
			})
		}
	}

	sort.Slice(result.matches, func(i, j int) bool {
		left := strings.ToLower(data.LookupHash(result.matches[i].LevelID.Name))
		right := strings.ToLower(data.LookupHash(result.matches[j].LevelID.Name))
		if left != right {
			return left < right
		}
		return result.matches[i].LevelID.Name.Value < result.matches[j].LevelID.Name.Value
	})
	return result
}

func countLevelUnitMatches(units []level.Unit, target stingray.Hash) int {
	count := 0
	for _, unit := range units {
		// Hash is the referenced unit resource path. Name is a separate display
		// label and may coincide with an unrelated resource hash.
		if unit.Hash == target {
			count++
		}
	}
	return count
}

func countPrefabUnitSliceMatches(units []prefab.Unit, target stingray.Hash) int {
	count := 0
	for _, unit := range units {
		if unit.Hash == target {
			count++
		}
	}
	return count
}

func countPrefabUnitMatches(
	ctx context.Context,
	dataDir *stingray.DataDir,
	prefabPath, target stingray.Hash,
	memo map[stingray.Hash]int,
	active map[stingray.Hash]bool,
	depth int,
) int {
	if prefabPath == (stingray.Hash{}) || depth > 48 {
		return 0
	}
	if count, ok := memo[prefabPath]; ok {
		return count
	}
	if active[prefabPath] {
		return 0
	}
	select {
	case <-ctx.Done():
		return 0
	default:
	}
	active[prefabPath] = true
	defer delete(active, prefabPath)

	resource, err := dataDir.Read(stingray.NewFileID(prefabPath, stingray.Sum("prefab")), stingray.DataMain)
	if err != nil {
		memo[prefabPath] = 0
		return 0
	}
	parsed, err := prefab.Load(bytes.NewReader(resource))
	if err != nil {
		memo[prefabPath] = 0
		return 0
	}
	count := 0
	for _, unit := range parsed.Units {
		if unit.Hash == target {
			count++
		}
	}
	for _, nested := range parsed.NestedPrefabs {
		count += countPrefabUnitMatches(ctx, dataDir, nested.Path, target, memo, active, depth+1)
	}
	memo[prefabPath] = count
	return count
}

func (a *guiApp) requestUnitLevelLookup(unitID stingray.FileID) {
	if a.gameData == nil || unitID.Type != stingray.Sum("unit") {
		return
	}
	dataDir := a.gameData.DataDir
	buildLabel := "current build"
	if _, present := dataDir.Files[unitID]; !present {
		a.unitVersionScan.Lock()
		olderDir := a.unitVersionScan.DataDir
		a.unitVersionScan.Unlock()
		if olderDir == nil {
			return
		}
		if _, present := olderDir.Files[unitID]; !present {
			return
		}
		dataDir = olderDir
		buildLabel = "older build"
	}
	if a.unitLevelLookupCancel != nil {
		a.unitLevelLookupCancel()
	}
	if a.unitLevelLookupResults == nil {
		a.unitLevelLookupResults = make(chan unitLevelUsageResult, 1)
	}
	a.unitLevelLookupOpen = true
	a.unitLevelLookupID = unitID
	a.unitLevelLookupLoading = true
	a.unitLevelLookupResult = nil
	data := a.gameData
	dataDirSnapshot := dataDir
	resultChannel := a.unitLevelLookupResults
	ctx, cancel := context.WithCancel(a.ctx)
	a.unitLevelLookupCancel = cancel
	go func() {
		result := scanUnitLevelUsage(ctx, data, dataDirSnapshot, unitID, buildLabel)
		select {
		case resultChannel <- result:
		case <-ctx.Done():
		}
		cancel()
	}()
}

func (a *guiApp) drawUnitLevelUsageWindow() {
	if a.unitLevelLookupResults != nil {
		select {
		case result := <-a.unitLevelLookupResults:
			if result.source == a.gameData && result.unitID == a.unitLevelLookupID {
				a.unitLevelLookupResult = &result
				a.unitLevelLookupLoading = false
				a.unitLevelLookupCancel = nil
			}
		default:
		}
	}
	if !a.unitLevelLookupOpen {
		if a.unitLevelLookupCancel != nil {
			a.unitLevelLookupCancel()
			a.unitLevelLookupCancel = nil
		}
		return
	}
	if a.gameData == nil {
		return
	}
	unitName := a.gameData.DisplayFileName(a.unitLevelLookupID)
	windowName := "Levels using " + unitName
	if imgui.BeginV(windowName, &a.unitLevelLookupOpen, imgui.WindowFlagsNone) {
		imgui.PushTextWrapPos()
		imgui.TextUnformatted("Model: " + unitName)
		imgui.PopTextWrapPos()
		imgui.Separator()
		switch {
		case a.unitLevelLookupLoading:
			imgui.TextUnformatted("Scanning level placements and prefabs…")
		case a.unitLevelLookupResult == nil:
			imgui.TextUnformatted("No scan results are available.")
		case a.unitLevelLookupResult.err != nil:
			imgui.TextUnformatted(a.unitLevelLookupResult.err.Error())
		default:
			result := a.unitLevelLookupResult
			imgui.TextUnformatted(fmt.Sprintf("Scanned %d levels in the %s; skipped %d unreadable levels.", result.scanned, result.buildLabel, result.skipped))
			if len(result.matches) == 0 {
				imgui.TextUnformatted("No level placements of this model were found.")
			} else if imgui.BeginTableV("##UnitLevelUsageMatches", 4, imgui.TableFlagsBorders|imgui.TableFlagsRowBg, imgui.NewVec2(0, 0), 0) {
				imgui.TableSetupColumn("Level")
				imgui.TableSetupColumn("Placements")
				imgui.TableSetupColumn("Direct")
				imgui.TableSetupColumn("Prefab")
				imgui.TableHeadersRow()
				for _, match := range result.matches {
					levelName := a.levelDisplayName(match.LevelID)
					if levelName == match.LevelID.Name.String() {
						levelName = a.gameData.LookupHash(match.LevelID.Name)
					}
					imgui.TableNextRow()
					imgui.TableNextColumn()
					if imgui.SelectableBoolV(
						levelName,
						match.LevelID == a.preview.ActiveID(),
						imgui.SelectableFlagsSpanAllColumns,
						imgui.NewVec2(0, 0),
					) {
						a.openUnitLevelUsageMatch(match, result)
					}
					imgui.TableNextColumn()
					imgui.TextUnformatted(fmt.Sprint(match.DirectUnits + match.PrefabUnits))
					imgui.TableNextColumn()
					imgui.TextUnformatted(fmt.Sprint(match.DirectUnits))
					imgui.TableNextColumn()
					imgui.TextUnformatted(fmt.Sprint(match.PrefabUnits))
				}
				imgui.EndTable()
			}
		}
	}
	imgui.End()
}

func (a *guiApp) openUnitLevelUsageMatch(match unitLevelUsageMatch, result *unitLevelUsageResult) {
	if result == nil || a.gameData == nil || a.preview == nil {
		return
	}
	previousID := a.preview.ActiveID()
	if result.dataDir != a.gameData.DataDir {
		a.unitVersionScan.Lock()
		currentOlderDataDir := a.unitVersionScan.DataDir
		a.unitVersionScan.Unlock()
		if result.dataDir != currentOlderDataDir {
			a.showErrorPopup(fmt.Errorf("the older-build scan changed; run the model's level search again"))
			return
		}
		if err := a.ensureOlderBuildPreview(); err != nil {
			a.showErrorPopup(err)
			return
		}
		a.preview = a.olderBuildPreview
	} else {
		a.activateCurrentPreview()
	}
	if previousID != match.LevelID {
		a.historyPush(previousID, match.LevelID)
	}
	if a.preview.ActiveID() != match.LevelID {
		a.preview.LoadFile(a.ctx, match.LevelID, a.preferences.PreviewVideoVerticalResolution, a.gameData.ColorGrading, a.gameData.EntityVarMapping)
	}
	var camera previews.UnitCamera
	var hasCachedCamera bool
	if a.levelGallery != nil {
		camera, hasCachedCamera = a.levelGallery.CameraFor(match.LevelID)
	}
	if !hasCachedCamera {
		var hasCurrentCamera bool
		camera, hasCurrentCamera = a.preview.UnitCamera()
		if !hasCurrentCamera {
			camera = previews.DefaultUnitCamera()
		}
	}
	a.preview.SetUnitCamera(camera)
	if !hasCachedCamera {
		a.preview.AutoZoomUnitNow()
	}
	a.unitLevelLookupOpen = false
	a.scrollToSelectedFile = true
	imgui.SetWindowFocusStr(fnt.I.Preview + " Preview")
}
