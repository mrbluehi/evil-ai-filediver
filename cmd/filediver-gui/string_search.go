package main

import (
	"bytes"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/AllenDang/cimgui-go/imgui"
	fnt "github.com/xypwn/filediver/cmd/filediver-gui/fonts"
	"github.com/xypwn/filediver/cmd/filediver-gui/imutils"
	"github.com/xypwn/filediver/stingray"
	stingray_strings "github.com/xypwn/filediver/stingray/strings"
)

type StringSearchResult struct {
	FileID   stingray.FileID
	OldBuild bool
	Kind     string
	Faction  string
	Language string
	ID       string
	Source   string
	Value    string
	search   string
}

type StringSearchState struct {
	sync.Mutex
	data               *GameData
	results            []StringSearchResult
	oldDataDir         *stingray.DataDir
	oldResults         []StringSearchResult
	oldProgress        float32
	oldIndexed         bool
	oldRunning         bool
	oldErr             string
	addedStrings       []StringSearchResult
	removedStrings     []StringSearchResult
	diffDataDir        *stingray.DataDir
	diffProgress       float32
	diffReady          bool
	diffRunning        bool
	versionFilter      int
	progress           float32
	indexed            bool
	running            bool
	err                string
	query              string
	lastQuery          string
	shown              []int
	englishOnly        bool
	hideNonStrings     bool
	sortColumn         int
	sortReady          bool
	sortDescending     bool
	hideDataLike       bool
	hideSubtitles      bool
	audioMessage       string
	audioIndexDir      *stingray.DataDir
	audioIndex         map[uint32][]subtitleAudioCandidate
	audioIndexRunning  bool
	audioIndexReady    bool
	audioIndexProgress float32
	audioIndexErr      string
	audioPending       *StringSearchResult
}

func (s *StringSearchState) startOld(gd *GameData, dataDir *stingray.DataDir) {
	if dataDir == nil {
		return
	}
	s.Lock()
	if s.oldDataDir == dataDir && (s.oldRunning || s.oldIndexed) {
		s.Unlock()
		return
	}
	s.oldDataDir, s.oldResults = dataDir, nil
	s.oldProgress, s.oldIndexed, s.oldRunning = 0, false, true
	s.oldErr = ""
	s.addedStrings, s.removedStrings = nil, nil
	s.diffDataDir, s.diffReady, s.diffRunning = nil, false, false
	s.lastQuery = "\x00"
	s.Unlock()

	go func() {
		fileIDs := slices.Collect(func(yield func(stingray.FileID) bool) {
			for id := range dataDir.Files {
				if id.Type != stingray.Sum("strings") {
					continue
				}
				if !yield(id) {
					return
				}
			}
		})
		slices.SortFunc(fileIDs, stingray.FileID.Cmp)
		results := make([]StringSearchResult, 0, len(fileIDs)*2)
		for i, id := range fileIDs {
			data, err := dataDir.Read(id, stingray.DataMain)
			if err == nil {
				if table, loadErr := safeLoadStringTable(data); loadErr == nil {
					language := "Unknown [" + table.Language.String() + "]"
					if friendly, ok := stingray_strings.LanguageHashToFriendlyName[table.Language]; ok {
						language = friendly
					}
					source := gd.LookupHash(id.Name) + ".strings"
					for stringID, value := range table.Strings {
						faction := factionForEnemyLabel(value)
						if faction == "Unassigned" {
							faction = factionForPath(source)
						}
						row := StringSearchResult{FileID: id, OldBuild: true, Kind: "Localized string", Faction: faction, Language: language, ID: fmt.Sprintf("%d", stringID), Source: source, Value: value}
						row.search = strings.ToLower(strings.Join([]string{row.Kind, row.Faction, row.Language, row.ID, row.Source, row.Value}, " "))
						results = append(results, row)
					}
				}
			}
			if i%64 == 0 || i == len(fileIDs)-1 {
				s.Lock()
				if s.oldDataDir == dataDir {
					s.oldProgress = float32(i+1) / float32(max(1, len(fileIDs)))
				}
				s.Unlock()
			}
		}
		s.Lock()
		if s.oldDataDir == dataDir {
			s.oldResults = results
			s.oldRunning = false
			s.oldIndexed = true
			s.oldProgress = 1
			s.diffDataDir = nil
			s.diffReady = false
			s.lastQuery = "\x00"
		}
		s.Unlock()
	}()
}

func (s *StringSearchState) startStringDiff(gd *GameData, dataDir *stingray.DataDir) {
	s.Lock()
	if !s.indexed || !s.oldIndexed || s.oldDataDir != dataDir || (s.diffDataDir == dataDir && (s.diffReady || s.diffRunning)) {
		s.Unlock()
		return
	}
	currentRows, oldRows := s.results, s.oldResults
	s.diffDataDir = dataDir
	s.diffReady, s.diffRunning = false, true
	s.diffProgress = 0
	s.Unlock()

	go func() {
		oldByKey := make(map[string]StringSearchResult)
		for i, row := range oldRows {
			oldByKey[stringDiffKey(row)] = row
			if i%4096 == 0 {
				s.Lock()
				if s.data == gd && s.diffDataDir == dataDir {
					s.diffProgress = float32(i+1) / float32(max(1, len(oldRows)*2))
				}
				s.Unlock()
			}
		}
		currentByKey := make(map[string]StringSearchResult)
		for _, row := range currentRows {
			if row.Kind == "Localized string" {
				currentByKey[stringDiffKey(row)] = row
			}
		}
		added := make([]StringSearchResult, 0)
		removed := make([]StringSearchResult, 0)
		for key, current := range currentByKey {
			old, exists := oldByKey[key]
			if !exists || old.Value != current.Value {
				added = append(added, current)
			}
		}
		for key, old := range oldByKey {
			current, exists := currentByKey[key]
			if !exists || current.Value != old.Value {
				removed = append(removed, old)
			}
		}
		s.Lock()
		if s.data == gd && s.diffDataDir == dataDir {
			s.addedStrings, s.removedStrings = added, removed
			s.diffRunning, s.diffReady = false, true
			s.diffProgress = 1
			s.lastQuery = "\x00"
		}
		s.Unlock()
	}()
}

func stringDiffKey(row StringSearchResult) string {
	return row.Language + "\x00" + row.ID
}

func (s *StringSearchState) start(gd *GameData) {
	s.Lock()
	if s.data == gd && (s.running || s.indexed) {
		s.Unlock()
		return
	}
	if s.data != gd {
		s.audioIndexDir = nil
		s.audioIndex = nil
		s.audioIndexRunning = false
		s.audioIndexReady = false
		s.audioIndexProgress = 0
		s.audioIndexErr = ""
		s.audioPending = nil
		s.audioMessage = ""
	}
	s.data, s.results, s.shown = gd, nil, nil
	s.hideDataLike = true
	s.addedStrings, s.removedStrings = nil, nil
	s.diffDataDir, s.diffReady, s.diffRunning = nil, false, false
	s.progress, s.indexed, s.running = 0, false, true
	s.err = ""
	s.Unlock()

	go func() {
		results := make([]StringSearchResult, 0, len(gd.DataDir.Files)*2)
		fileIDs := slices.Collect(func(yield func(stingray.FileID) bool) {
			for id := range gd.DataDir.Files {
				if !yield(id) {
					return
				}
			}
		})
		slices.SortFunc(fileIDs, stingray.FileID.Cmp)
		for i, id := range fileIDs {
			if id.Type == stingray.Sum("unit") {
				name := gd.KnownFileNames[id]
				if name == "" {
					name = id.Name.String() + ".unit"
				}
				faction := factionForPath(name)
				row := StringSearchResult{FileID: id, Kind: "Unit dev name", Faction: faction, Language: "—", ID: id.Name.String(), Source: "unit", Value: name}
				row.search = strings.ToLower(strings.Join([]string{row.Kind, faction, row.ID, row.Source, row.Value}, " "))
				results = append(results, row)
			}
			if id.Type == stingray.Sum("strings") {
				data, err := gd.DataDir.Read(id, stingray.DataMain)
				if err == nil {
					if table, loadErr := safeLoadStringTable(data); loadErr == nil {
						language := "Unknown [" + table.Language.String() + "]"
						if friendly, ok := stingray_strings.LanguageHashToFriendlyName[table.Language]; ok {
							language = friendly
						}
						source := gd.KnownFileNames[id]
						if source == "" {
							source = id.Name.String() + ".strings"
						}
						for stringID, value := range table.Strings {
							faction := factionForEnemyLabel(value)
							if faction == "Unassigned" {
								faction = factionForPath(source)
							}
							row := StringSearchResult{FileID: id, Kind: "Localized string", Faction: faction, Language: language, ID: fmt.Sprintf("%d", stringID), Source: source, Value: value}
							row.search = strings.ToLower(strings.Join([]string{row.Kind, row.Faction, row.Language, row.ID, row.Source, row.Value}, " "))
							results = append(results, row)
						}
					}
				}
			}
			if i%64 == 0 || i == len(fileIDs)-1 {
				s.Lock()
				if s.data == gd {
					s.progress = float32(i+1) / float32(max(1, len(fileIDs)))
				}
				s.Unlock()
			}
		}
		s.Lock()
		if s.data == gd {
			s.results = results
			s.running = false
			s.indexed = true
			s.progress = 1
			s.lastQuery = "\x00"
		}
		s.Unlock()
	}()
}

func safeLoadStringTable(data []byte) (table *stingray_strings.Strings, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			table = nil
			err = fmt.Errorf("invalid string table: %v", recovered)
		}
	}()
	return stingray_strings.Load(bytes.NewReader(data))
}

func factionForPath(path string) string {
	path = strings.ToLower(path)
	switch {
	case strings.Contains(path, "fac_bugs"), strings.Contains(path, "terminid"):
		return "Terminids"
	case strings.Contains(path, "fac_automaton"), strings.Contains(path, "fac_cyborg"), strings.Contains(path, "automaton"):
		return "Automatons"
	case strings.Contains(path, "fac_illuminate"), strings.Contains(path, "illuminate"):
		return "Illuminate"
	default:
		return "Unassigned"
	}
}

func factionForEnemyLabel(value string) string {
	name := strings.ToLower(strings.Trim(strings.TrimSpace(value), " .,:;!?"))
	switch name {
	case "alpha commander", "alpha warrior", "bile spewer", "bile spitter", "bile titan", "bile warrior",
		"brood commander", "charger", "charger behemoth", "dragonroach", "hive guard", "hive lord", "hunter", "impaler",
		"noxious spewer", "nursing spewer", "pouncer", "predator hunter", "predator stalker", "scavenger", "shrieker",
		"spore burst bile titan", "spore burst hunter", "spore burst scavenger", "spore burst warrior", "stalker", "warrior":
		return "Terminids"
	case "berserker", "commissar", "conscript", "devastator", "factory strider", "hulk", "marauder", "raider", "scout strider", "tank", "trooper":
		return "Automatons"
	case "crescent overseer", "elevated overseer", "fleshmob", "harvester", "interloper", "leviathan", "overseer", "stingray", "voteless", "watcher":
		return "Illuminate"
	default:
		return "Unassigned"
	}
}

func (a *guiApp) drawStringSearchWindow() {
	if !a.stringSearchDockConfigured && a.unitGalleryDockID != 0 {
		imgui.SetNextWindowDockIDV(a.unitGalleryDockID, imgui.CondFirstUseEver)
		a.stringSearchDockConfigured = true
	}
	if !imgui.Begin("String Search") {
		imgui.End()
		return
	}
	defer imgui.End()
	if a.gameData == nil {
		imgui.TextUnformatted("Load a game installation to search its strings and unit names.")
		return
	}

	s := &a.stringSearch
	s.start(a.gameData)
	a.applyPendingRelatedAudio()
	a.refreshUnitComparison()
	imgui.TextWrapped("Search localized game strings and hash-resolved unit paths (dev names). Use |, ;, or a new line between phrases, such as: bile titan | devastator | harvester | interloper | noxious spewer")
	imgui.SetNextItemWidth(-1)
	if imgui.InputTextWithHint("##StringSearchQuery", "Search phrases or enemy names...", &s.query, 0, nil) {
		s.Lock()
		s.lastQuery = "\x00"
		s.Unlock()
	}
	if !a.unitComparisonReady {
		s.versionFilter = 0
		imgui.TextDisabled("Scan an older build in Compare to filter new or removed strings and units here.")
	} else {
		imgui.SetNextItemWidth(imutils.S(150))
		versionLabels := []string{"Current build", "New", "Removed"}
		if imgui.BeginCombo("Compare", versionLabels[s.versionFilter]) {
			for i, label := range versionLabels {
				if imgui.SelectableBool(label) {
					s.versionFilter = i
					s.Lock()
					s.lastQuery = "\x00"
					s.Unlock()
				}
			}
			imgui.EndCombo()
		}
		if s.versionFilter != 0 {
			a.unitVersionScan.Lock()
			oldDataDir := a.unitVersionScan.DataDir
			a.unitVersionScan.Unlock()
			s.startOld(a.gameData, oldDataDir)
			s.startStringDiff(a.gameData, oldDataDir)
		}
	}
	if imgui.Checkbox("English (US) only", &s.englishOnly) {
		s.Lock()
		s.lastQuery = "\x00"
		s.Unlock()
	}
	if imgui.IsItemHovered() {
		imgui.SetTooltip("Show only English (US) localized strings. Unit developer paths are language-neutral and remain visible.")
	}
	if imgui.Checkbox("Hide data-like strings", &s.hideDataLike) {
		s.Lock()
		s.lastQuery = "\x00"
		s.Unlock()
	}
	if imgui.IsItemHovered() {
		imgui.SetTooltip("Hides entries that look like numeric or structured asset data instead of readable text. Turn this off to inspect every indexed entry.")
	}
	if imgui.Checkbox("Hide subtitle strings", &s.hideSubtitles) {
		s.Lock()
		s.lastQuery = "\x00"
		s.Unlock()
	}
	if imgui.IsItemHovered() {
		imgui.SetTooltip("Hides localized lines that begin with subtitle timing markers, such as {0.12->2.27}.")
	}
	if imgui.Checkbox("Hide non-string results", &s.hideNonStrings) {
		s.Lock()
		s.lastQuery = "\x00"
		s.Unlock()
	}
	if imgui.IsItemHovered() {
		imgui.SetTooltip("Hides unit developer-name results while keeping localized string entries, even when their Source references a unit or other asset.")
	}

	s.Lock()
	if s.running {
		imgui.ProgressBarV(s.progress, imgui.NewVec2(-1, 0), "Indexing game strings and units...")
	}
	if s.versionFilter != 0 && s.oldRunning {
		imgui.ProgressBarV(s.oldProgress, imgui.NewVec2(-1, 0), "Indexing localized strings from older build...")
	}
	if s.versionFilter != 0 && s.diffRunning {
		imgui.ProgressBarV(s.diffProgress, imgui.NewVec2(-1, 0), "Comparing localized string IDs and text...")
	}
	if s.err != "" {
		imutils.TextError(fmt.Errorf("%s", s.err))
	}
	if !s.running && !s.indexed {
		imgui.TextUnformatted("Preparing search index...")
		s.Unlock()
		return
	}
	rows := s.results
	if s.versionFilter == 1 {
		rows = s.addedStrings
	} else if s.versionFilter == 2 {
		rows = s.removedStrings
	}
	terms := searchTerms(s.query)
	filterKey := fmt.Sprintf("%d:%t:%t:%t:%t:%s", s.versionFilter, s.englishOnly, s.hideDataLike, s.hideSubtitles, s.hideNonStrings, s.query)
	if s.lastQuery != filterKey {
		s.shown = s.shown[:0]
		for i := range rows {
			if s.hideNonStrings && rows[i].Kind != "Localized string" {
				continue
			}
			if s.hideDataLike && rows[i].Kind == "Localized string" && isDataLikeString(rows[i].Value) {
				continue
			}
			if s.hideSubtitles && rows[i].Kind == "Localized string" && isSubtitleString(rows[i].Value) {
				continue
			}
			if s.englishOnly && rows[i].Kind == "Localized string" && rows[i].Language != "English (US)" {
				continue
			}
			if len(terms) == 0 {
				s.shown = append(s.shown, i)
				continue
			}
			for _, term := range terms {
				if strings.Contains(rows[i].search, term) {
					s.shown = append(s.shown, i)
					break
				}
			}
		}
		s.sortShown(rows)
		s.lastQuery = filterKey
	}
	if s.versionFilter != 0 && (s.oldRunning || s.diffRunning || !s.diffReady) {
		imgui.TextUnformatted("Localized string changes are being compared...")
	}
	imgui.TextUnformatted(fmt.Sprintf("%d matches · %d indexed records", len(s.shown), len(rows)))
	if s.audioMessage != "" {
		imgui.TextDisabled(s.audioMessage)
	}
	if s.audioIndexRunning {
		imgui.ProgressBarV(s.audioIndexProgress, imgui.NewVec2(-1, 0), "Matching subtitle links to Wwise voice streams...")
	}
	imgui.TextDisabled("Click a column heading to sort; click it again to reverse the order.")
	const flags = imgui.TableFlagsResizable | imgui.TableFlagsBorders | imgui.TableFlagsScrollY | imgui.TableFlagsRowBg | imgui.TableFlagsSortable
	if imgui.BeginTableV("##StringSearchResults", 5, flags, imgui.NewVec2(0, 0), 0) {
		imgui.TableSetupColumnV("Kind", imgui.TableColumnFlagsWidthFixed, 112, 0)
		imgui.TableSetupColumnV("Faction", imgui.TableColumnFlagsWidthFixed, 100, 0)
		imgui.TableSetupColumnV("Language / ID", imgui.TableColumnFlagsWidthFixed, 125, 0)
		imgui.TableSetupColumnV("Source", imgui.TableColumnFlagsWidthStretch, 1.3, 0)
		imgui.TableSetupColumnV("Name / text", imgui.TableColumnFlagsWidthStretch, 2, 0)
		imgui.TableSetupScrollFreeze(0, 1)
		imgui.TableHeadersRow()
		if sortSpecs := imgui.TableGetSortSpecs(); sortSpecs != nil && sortSpecs.SpecsDirty() {
			if spec := sortSpecs.Specs(); spec != nil {
				s.sortColumn = int(spec.ColumnIndex())
				s.sortDescending = spec.SortDirection() == imgui.SortDirectionDescending
				s.sortReady = true
				s.sortShown(rows)
			}
			sortSpecs.SetSpecsDirty(false)
		}
		clipper := imgui.NewListClipper()
		clipper.Begin(int32(len(s.shown)))
		for clipper.Step() {
			for rowIndex := clipper.DisplayStart(); rowIndex < clipper.DisplayEnd(); rowIndex++ {
				row := rows[s.shown[rowIndex]]
				imgui.PushIDStr(stringSearchResultID(row))
				imgui.TableNextRow()
				imgui.TableNextColumn()
				imgui.TextUnformatted(row.Kind)
				imgui.TableNextColumn()
				imgui.TextUnformatted(row.Faction)
				imgui.TableNextColumn()
				imgui.TextUnformatted(row.Language + " · " + row.ID)
				imgui.TableNextColumn()
				imgui.TextUnformatted(row.Source)
				imgui.TableNextColumn()
				if row.Kind == "Localized string" {
					if a.drawStringSearchResultText(row, terms) && a.preview != nil {
						a.openStringSearchResult(row)
					}
				} else {
					drawHighlightedText(row.Value, terms)
				}
				imgui.PopID()
			}
		}
		imgui.EndTable()
	}
	s.Unlock()
}

// openRelatedSubtitleAudio resolves a caption through the indexed Wwise Sound
// object and the source stream referenced by its bank. It is called from the
// String Search draw path while that state's mutex is held.
func (a *guiApp) openRelatedSubtitleAudio(row StringSearchResult) {
	if a.gameData == nil || a.gameData.DataDir == nil || row.OldBuild {
		a.stringSearch.audioMessage = "Related audio lookup is available for the current build only."
		return
	}
	subtitleID, err := strconv.ParseUint(row.ID, 10, 32)
	if err != nil {
		a.stringSearch.audioMessage = "This subtitle does not have a numeric caption ID to match against the voice-line index."
		return
	}
	if _, ok, err := lookupSubtitleAudioReference(uint32(subtitleID)); err != nil {
		a.stringSearch.audioMessage = "Could not read the bundled subtitle-to-audio reference: " + err.Error()
		return
	} else if !ok {
		a.stringSearch.audioMessage = "No voice-line link is recorded for this subtitle ID."
		return
	}
	if a.stringSearch.audioIndexDir == a.gameData.DataDir && a.stringSearch.audioIndexReady {
		a.openBestRelatedAudio(row, a.stringSearch.audioIndex[uint32(subtitleID)])
		return
	}
	copyRow := row
	a.stringSearch.audioPending = &copyRow
	a.stringSearch.audioMessage = "Building the voice-line index for this game build..."
	if a.stringSearch.audioIndexDir == a.gameData.DataDir && a.stringSearch.audioIndexRunning {
		return
	}
	dataDir := a.gameData.DataDir
	gd := a.gameData
	state := &a.stringSearch
	state.audioIndexDir = dataDir
	state.audioIndex = nil
	state.audioIndexRunning = true
	state.audioIndexReady = false
	state.audioIndexProgress = 0
	state.audioIndexErr = ""
	go func() {
		index, buildErr := buildSubtitleAudioIndex(gd, func(progress float32) {
			state.Lock()
			if state.audioIndexDir == dataDir {
				state.audioIndexProgress = progress
			}
			state.Unlock()
		})
		state.Lock()
		if state.audioIndexDir == dataDir {
			state.audioIndex = index
			state.audioIndexErr = errorString(buildErr)
			state.audioIndexRunning = false
			state.audioIndexReady = buildErr == nil
			state.audioIndexProgress = 1
			if buildErr != nil {
				state.audioMessage = "Could not index Wwise voice streams: " + buildErr.Error()
			} else {
				state.audioMessage = "Voice-line index ready; opening the caption match..."
			}
		}
		state.Unlock()
	}()
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (a *guiApp) applyPendingRelatedAudio() {
	s := &a.stringSearch
	s.Lock()
	if s.audioPending == nil || s.audioIndexRunning {
		s.Unlock()
		return
	}
	row := *s.audioPending
	s.audioPending = nil
	if s.audioIndexErr != "" {
		s.audioMessage = "Could not index Wwise voice streams: " + s.audioIndexErr
		s.Unlock()
		return
	}
	if a.gameData == nil || a.gameData.DataDir != s.audioIndexDir {
		s.audioMessage = "The active game build changed before the audio link was ready. Try the lookup again."
		s.Unlock()
		return
	}
	subtitleID, err := strconv.ParseUint(row.ID, 10, 32)
	if err != nil {
		s.audioMessage = "The subtitle ID could not be read."
		s.Unlock()
		return
	}
	candidates := append([]subtitleAudioCandidate(nil), s.audioIndex[uint32(subtitleID)]...)
	s.Unlock()
	a.openBestRelatedAudio(row, candidates)
}

func (a *guiApp) openBestRelatedAudio(row StringSearchResult, candidates []subtitleAudioCandidate) {
	ordered := relatedAudioVariantsFromList(candidates, row.Language)
	if len(ordered) == 0 {
		a.stringSearch.audioMessage = "The caption is known, but its linked Wwise stream is not present in this build."
		return
	}
	a.openSpecificRelatedAudio(row, ordered[0])
	if len(ordered) > 1 {
		a.stringSearch.audioMessage += fmt.Sprintf(" · %d voice variants are indexed; right-click the caption to choose another.", len(ordered))
	}
}

func (a *guiApp) openSpecificRelatedAudio(row StringSearchResult, candidate subtitleAudioCandidate) {
	a.browserJumpToFileID = candidate.FileID
	a.scrollToSelectedFile = true
	if a.gameData != nil && a.preview != nil {
		a.activateCurrentPreview()
		if oldID := a.preview.ActiveID(); oldID != candidate.FileID {
			a.historyPush(oldID, candidate.FileID)
			a.preview.LoadFile(a.ctx, candidate.FileID, a.preferences.PreviewVideoVerticalResolution, a.gameData.ColorGrading, a.gameData.EntityVarMapping)
		}
	}
	name := a.gameData.KnownFileNames[candidate.FileID]
	if name == "" {
		name = candidate.FileID.Name.String() + ".wwise_stream"
	}
	certainty := "caption-linked"
	if !candidate.Certain {
		certainty = "transcript match; caption ID may be shared by identical lines"
	}
	if candidate.LanguageFallback {
		certainty += "; opened English (US) audio because this language has no indexed voice counterpart"
	}
	a.stringSearch.audioMessage = fmt.Sprintf("Opened %s in Preview · source %d · %s · highlighted in Browser.", name, candidate.SourceID, certainty)
	imgui.SetWindowFocusStr(fnt.I.Preview + " Preview")
}

// isDataLikeString catches localization entries that are actually structured
// numeric payloads (for example, many lines of "0.00 X") while keeping normal
// multiline prose and short labels visible.
func isDataLikeString(value string) bool {
	lines := strings.Split(value, "\n")
	dataLines, shortPayloadLines, nonEmptyLines := 0, 0, 0
	for _, line := range lines {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		nonEmptyLines++
		first := strings.Trim(fields[0], ",;:")
		if _, err := strconv.ParseFloat(first, 64); err != nil {
			continue
		}
		dataLines++
		payloadLength := 0
		for _, field := range fields[1:] {
			payloadLength += len(field)
		}
		if len(fields) <= 3 && payloadLength <= 3 {
			shortPayloadLines++
		}
	}
	if nonEmptyLines < 6 {
		return false
	}
	threshold := (nonEmptyLines*2 + 2) / 3
	return dataLines >= threshold && shortPayloadLines >= threshold
}

func isSubtitleString(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "{") {
		return false
	}
	close := strings.IndexByte(value, '}')
	if close < 0 || strings.TrimSpace(value[close+1:]) == "" {
		return false
	}
	timing := strings.Split(strings.TrimSpace(value[1:close]), "->")
	if len(timing) != 2 {
		return false
	}
	start, startErr := strconv.ParseFloat(strings.TrimSpace(timing[0]), 64)
	end, endErr := strconv.ParseFloat(strings.TrimSpace(timing[1]), 64)
	return startErr == nil && endErr == nil && start >= 0 && end >= start
}

func (a *guiApp) openStringSearchResult(row StringSearchResult) {
	if row.OldBuild {
		if err := a.ensureOlderBuildPreview(); err != nil {
			return
		}
		oldID := a.preview.ActiveID()
		a.preview = a.olderBuildPreview
		if oldID != row.FileID {
			a.historyPush(oldID, row.FileID)
			a.preview.LoadFile(a.ctx, row.FileID, a.preferences.PreviewVideoVerticalResolution, stingray.FileID{}, nil)
		}
	} else {
		oldID := a.preview.ActiveID()
		if oldID != row.FileID {
			a.historyPush(oldID, row.FileID)
			a.activateCurrentPreview()
			a.preview.LoadFile(a.ctx, row.FileID, a.preferences.PreviewVideoVerticalResolution, a.gameData.ColorGrading, a.gameData.EntityVarMapping)
		}
	}
	imgui.SetWindowFocusStr(fnt.I.Preview + " Preview")
}

type highlightedSearchPart struct {
	text    string
	matched bool
}

func stringSearchResultID(row StringSearchResult) string {
	return row.FileID.Name.String() + ":" + row.FileID.Type.String() + ":" + strconv.FormatBool(row.OldBuild) + ":" + row.Kind + ":" + row.Language + ":" + row.ID
}

func (a *guiApp) drawStringSearchResultText(row StringSearchResult, terms []string) bool {
	parts := highlightedSearchParts(row.Value, terms)
	imgui.PushStyleVarVec2(imgui.StyleVarFramePadding, imgui.NewVec2(0, 0))
	defer imgui.PopStyleVar()
	isSubtitle := isSubtitleString(row.Value)
	tooltip := "Click to open this strings resource in Preview"
	if isSubtitle {
		tooltip += "\nRight-click: Find related audio"
	}

	clicked := false
	for i, part := range parts {
		if i > 0 {
			imgui.SameLineV(0, 0)
		}
		imgui.PushIDInt(int32(i))
		if part.matched {
			imgui.PushStyleColorVec4(imgui.ColText, imgui.NewVec4(1, 0.82, 0.12, 1))
		}
		clicked = imgui.SelectableBool(part.text) || clicked
		if part.matched {
			imgui.PopStyleColor()
		}
		if imgui.IsItemHovered() {
			imgui.SetTooltip(tooltip)
		}
		if isSubtitle && imgui.BeginPopupContextItemV("##StringResultOptions", imgui.PopupFlagsMouseButtonRight) {
			if imgui.MenuItemBool("Find related audio") {
				a.openRelatedSubtitleAudio(row)
			}
			if a.stringSearch.audioIndexReady {
				if variants := relatedAudioVariants(a.stringSearch.audioIndex, row); len(variants) > 1 && imgui.BeginMenu("Choose another voice variant") {
					for _, candidate := range variants {
						label := relatedAudioVariantLabel(candidate)
						if imgui.MenuItemBool(fmt.Sprintf("%s##%s-%d", label, candidate.FileID.Name.String(), candidate.SourceID)) {
							a.openSpecificRelatedAudio(row, candidate)
						}
					}
					imgui.EndMenu()
				}
			}
			imgui.EndPopup()
		}
		imgui.PopID()
	}
	return clicked
}

func drawHighlightedText(text string, terms []string) {
	for i, part := range highlightedSearchParts(text, terms) {
		if i > 0 {
			imgui.SameLineV(0, 0)
		}
		if part.matched {
			imgui.TextColored(imgui.NewVec4(1, 0.82, 0.12, 1), part.text)
		} else {
			imgui.TextUnformatted(part.text)
		}
	}
}

func highlightedSearchParts(text string, terms []string) []highlightedSearchPart {
	if len(terms) == 0 || text == "" {
		return []highlightedSearchPart{{text: text}}
	}

	lowerText := strings.ToLower(text)
	parts := make([]highlightedSearchPart, 0, 4)
	position := 0
	for position < len(text) {
		matchStart, matchEnd := -1, -1
		for _, term := range terms {
			term = strings.ToLower(term)
			if term == "" {
				continue
			}
			relativeStart := strings.Index(lowerText[position:], term)
			if relativeStart < 0 {
				continue
			}
			start := position + relativeStart
			end := start + len(term)
			if matchStart < 0 || start < matchStart || (start == matchStart && end > matchEnd) {
				matchStart, matchEnd = start, end
			}
		}
		if matchStart < 0 {
			appendSearchPart(&parts, text[position:], false)
			break
		}
		appendSearchPart(&parts, text[position:matchStart], false)
		appendSearchPart(&parts, text[matchStart:matchEnd], true)
		position = matchEnd
	}
	if len(parts) == 0 {
		return []highlightedSearchPart{{text: text}}
	}
	return parts
}

func appendSearchPart(parts *[]highlightedSearchPart, text string, matched bool) {
	if text == "" {
		return
	}
	if len(*parts) > 0 && (*parts)[len(*parts)-1].matched == matched {
		(*parts)[len(*parts)-1].text += text
		return
	}
	*parts = append(*parts, highlightedSearchPart{text: text, matched: matched})
}

func (s *StringSearchState) sortShown(rows []StringSearchResult) {
	if !s.sortReady {
		return
	}
	value := func(row StringSearchResult) string {
		switch s.sortColumn {
		case 0:
			return row.Kind
		case 1:
			return row.Faction
		case 2:
			return row.Language + " · " + row.ID
		case 3:
			return row.Source
		default:
			return row.Value
		}
	}
	sort.SliceStable(s.shown, func(i, j int) bool {
		left := value(rows[s.shown[i]])
		right := value(rows[s.shown[j]])
		if s.sortDescending {
			return strings.Compare(left, right) > 0
		}
		return strings.Compare(left, right) < 0
	})
}

func searchTerms(query string) []string {
	var terms []string
	for _, term := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return r == '|' || r == ';' || r == '\n' || r == '\r' }) {
		term = strings.TrimSpace(term)
		if term != "" {
			terms = append(terms, term)
		}
	}
	return terms
}
