package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/level"
)

type inferredLevelLabel struct {
	Base string
	Name string
}

type levelLabelScanResult struct {
	source *GameData
	labels map[stingray.FileID]inferredLevelLabel
}

func (a *guiApp) resetLevelNamesIfNeeded() {
	if a.levelLabelData == a.gameData {
		return
	}
	a.levelLabelData = a.gameData
	a.levelLabels = make(map[stingray.FileID]inferredLevelLabel)
	a.levelLabelsScheduled = make(map[stingray.FileID]struct{})
	a.levelLabelResults = make(chan levelLabelScanResult, 1)
	a.levelLabelScanRunning = false
}

// updateLevelNames reads level resources off the UI thread and derives compact
// labels from their placed-unit paths and entity-path metadata. Opaque hashes
// remain the search fallback and are still shown in each card's tooltip.
func (a *guiApp) updateLevelNames(ids []stingray.FileID) {
	if a.gameData == nil {
		return
	}
	a.resetLevelNamesIfNeeded()

	select {
	case result := <-a.levelLabelResults:
		if result.source == a.gameData {
			for id, label := range result.labels {
				a.levelLabels[id] = label
			}
			if len(result.labels) > 0 {
				formatLevelLabels(a.levelLabels)
				if a.levelGallery != nil {
					a.levelGallery.InvalidateFilteredResults()
				}
			}
		}
		a.levelLabelScanRunning = false
	default:
	}

	if a.levelLabelScanRunning {
		return
	}
	missing := make([]stingray.FileID, 0)
	for _, id := range ids {
		if _, done := a.levelLabelsScheduled[id]; done {
			continue
		}
		a.levelLabelsScheduled[id] = struct{}{}
		missing = append(missing, id)
	}
	if len(missing) == 0 {
		return
	}

	data := a.gameData
	currentDir := data.DataDir
	olderDir := a.olderBuildDataDir
	resultChannel := a.levelLabelResults
	ctx := a.ctx
	a.levelLabelScanRunning = true
	go func() {
		labels := scanLevelLabels(ctx, data, currentDir, olderDir, missing)
		select {
		case resultChannel <- levelLabelScanResult{source: data, labels: labels}:
		case <-ctx.Done():
		}
	}()
}

func (a *guiApp) levelDisplayName(id stingray.FileID) string {
	if label, ok := a.levelLabels[id]; ok && label.Name != "" {
		return label.Name
	}
	if a.gameData == nil {
		return id.Name.String()
	}
	return a.gameData.LookupHash(id.Name)
}

func scanLevelLabels(
	ctx context.Context,
	data *GameData,
	currentDir, olderDir *stingray.DataDir,
	ids []stingray.FileID,
) map[stingray.FileID]inferredLevelLabel {
	labels := make(map[stingray.FileID]inferredLevelLabel)
	for _, id := range ids {
		select {
		case <-ctx.Done():
			return labels
		default:
		}
		if id.Type != stingray.Sum("level") {
			continue
		}

		resourceName := data.LookupHash(id.Name)
		if resourceName != id.Name.String() && !strings.HasPrefix(resourceName, "0x") {
			labels[id] = inferredLevelLabel{Base: resourceName, Name: resourceName}
			continue
		}
		readDir := currentDir
		if len(readDir.Files[id]) == 0 && olderDir != nil && len(olderDir.Files[id]) > 0 {
			readDir = olderDir
		}
		if readDir == nil {
			continue
		}
		resource, err := readDir.Read(id, stingray.DataMain)
		if err != nil {
			continue
		}
		label := inferLevelLabel(resource, data.LookupHash, data.LookupThinHash)
		if label != "" {
			labels[id] = inferredLevelLabel{Base: label, Name: label}
		}
	}
	return labels
}

func formatLevelLabels(labels map[stingray.FileID]inferredLevelLabel) {
	for id, label := range labels {
		label.Base = strings.Join(strings.Fields(strings.ToLower(label.Base)), " ")
		label.Name = fmt.Sprintf("%s 0x%08x", label.Base, uint32(id.Name.Value))
		labels[id] = label
	}
}

func inferLevelLabel(
	resource []byte,
	lookupHash func(stingray.Hash) string,
	lookupThinHash func(stingray.ThinHash) string,
) string {
	info, err := level.LoadLevel(bytes.NewReader(resource), nil)
	if err != nil {
		return ""
	}
	if name := lookupHash(info.Name); name != info.Name.String() && !strings.HasPrefix(name, "0x") {
		return name
	}

	worldVotes := make(map[string]int)
	areaVotes := make(map[string]int)
	objectiveVotes := make(map[string]int)
	addPath := func(path string, weight int) {
		path = strings.ToLower(strings.Trim(path, "/"))
		if !strings.HasPrefix(path, "content/") {
			return
		}
		parts := strings.Split(path, "/")
		envIndex := -1
		for i, part := range parts {
			if strings.HasPrefix(part, "env_") {
				envIndex = i
				if world := levelWorldName(part); world != "" {
					worldVotes[world] += weight
				}
				break
			}
		}
		for _, part := range parts {
			if area := levelAreaName(part, envIndex >= 0 && parts[envIndex] == "env_illuminate"); area != "" {
				areaVotes[area] += weight
				break
			}
		}
		if objective := levelObjectiveName(path); objective != "" {
			objectiveVotes[objective] += weight
		}
	}

	for _, placed := range info.Units {
		addPath(lookupHash(placed.Hash), 2)
		name := lookupHash(placed.Name)
		if strings.Contains(name, "/") {
			addPath(name, 1)
		}
	}
	for _, placed := range info.Prefabs {
		addPath(lookupHash(placed.Path), 1)
	}
	for _, embedded := range info.EmbeddedPrefabs {
		for _, placed := range embedded.Units {
			addPath(lookupHash(placed.Hash), 1)
		}
		for _, nested := range embedded.NestedPrefabs {
			addPath(lookupHash(nested.Path), 1)
		}
	}
	for _, tree := range info.Speedtrees {
		addPath(lookupHash(tree.Hash), 1)
	}
	for _, entries := range info.Metadata {
		for _, entry := range entries {
			if entry.Type != level.LevelMetadata_string || !strings.EqualFold(entry.Key(lookupThinHash), "entity_path") {
				continue
			}
			addPath(entry.ValueString, 1)
		}
	}

	world := highestVote(worldVotes)
	area := highestVote(areaVotes)
	objective := highestVote(objectiveVotes)
	parts := make([]string, 0, 3)
	if world != "" {
		parts = append(parts, world)
	}
	if area != "" {
		parts = append(parts, area)
	}
	if objective != "" && !strings.EqualFold(objective, area) {
		parts = append(parts, objective)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

func highestVote(votes map[string]int) string {
	best := ""
	bestCount := 0
	for name, count := range votes {
		if count > bestCount || (count == bestCount && count > 0 && name < best) {
			best, bestCount = name, count
		}
	}
	return best
}

func levelWorldName(env string) string {
	switch env {
	case "env_super_earth":
		return "super earth"
	case "env_bugs":
		return "terminids"
	case "env_illuminate":
		return "illuminate"
	case "env_cyborg":
		return "automatons"
	case "env_automatons":
		return "automatons"
	}
	return ""
}

func levelAreaName(part string, illuminate bool) string {
	switch part {
	case "military_base", "military_bases":
		return "military base"
	case "cities", "city":
		return "city"
	case "colonies", "colony":
		return "colony"
	case "habs", "habitat":
		return "habitat"
	case "bug_hive", "hive":
		return "hive"
	case "bug_cave_bones", "bug_cave", "caves":
		return "cave"
	case "bug_rocks", "bug_rock":
		return "bug rocks"
	case "bug_goop":
		return "bug nest"
	case "fortifications", "fortification":
		return "fortification"
	case "outpost", "outposts":
		return "outpost"
	case "ruins":
		return "ruins"
	case "architecture":
		if illuminate {
			return "ruins"
		}
	case "generic_debris":
		return "debris field"
	case "rocks", "rock":
		return "rock field"
	}
	return ""
}

func levelObjectiveName(path string) string {
	switch {
	case strings.Contains(path, "/horde_mode_defend/"):
		return "horde defense"
	case strings.Contains(path, "/sam_site/"):
		return "sam site"
	case strings.Contains(path, "/seaf_gun/"):
		return "seaf gun"
	case strings.Contains(path, "/power_station/"):
		return "power station"
	case strings.Contains(path, "/bunker_activation_codes"):
		return "bunker codes"
	case strings.Contains(path, "/bug_spawner"):
		return "bug spawner"
	case strings.Contains(path, "/il_portal"):
		return "illuminate portal"
	case strings.Contains(path, "/il_teslatower"):
		return "tesla tower"
	}
	return ""
}
