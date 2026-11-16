package main

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/level"
)

// versionMatchResult pairs resources whose IDs differ between builds but whose
// names or contents strongly suggest that the newer resource replaced the old.
type versionMatchResult struct {
	gameData *GameData
	oldPath  string
	pairs    map[stingray.Hash]map[stingray.FileID]stingray.FileID
}

type levelVersionSignature struct {
	base string
	refs map[stingray.Hash]uint32
}

func findLikelyVersionReplacements(
	ctx context.Context,
	data *GameData,
	currentDir, olderDir *stingray.DataDir,
	addedByType, removedByType map[stingray.Hash]map[stingray.FileID]struct{},
) map[stingray.Hash]map[stingray.FileID]stingray.FileID {
	pairs := make(map[stingray.Hash]map[stingray.FileID]stingray.FileID)
	for fileType, sameIDPairs := range detectSameIDChanges(ctx, currentDir, olderDir) {
		pairs[fileType] = sameIDPairs
	}
	unitType := stingray.Sum("unit")
	if unitPairs := matchUnitsByStableLeaf(data, addedByType[unitType], removedByType[unitType]); len(unitPairs) > 0 {
		if pairs[unitType] == nil {
			pairs[unitType] = make(map[stingray.FileID]stingray.FileID)
		}
		for currentID, olderID := range unitPairs {
			pairs[unitType][currentID] = olderID
		}
	}
	levelType := stingray.Sum("level")
	if levelPairs := matchLevelsByContents(ctx, data, currentDir, olderDir, addedByType[levelType], removedByType[levelType]); len(levelPairs) > 0 {
		if pairs[levelType] == nil {
			pairs[levelType] = make(map[stingray.FileID]stingray.FileID)
		}
		for currentID, olderID := range levelPairs {
			pairs[levelType][currentID] = olderID
		}
	}
	return pairs
}

// When the resource path hash is unchanged, a compare still needs to detect
// in-place updates. File-size changes are cheap evidence; equal-size unit main
// files and all level main files are byte-compared in the background.
func detectSameIDChanges(ctx context.Context, currentDir, olderDir *stingray.DataDir) map[stingray.Hash]map[stingray.FileID]stingray.FileID {
	changed := map[stingray.Hash]map[stingray.FileID]stingray.FileID{
		stingray.Sum("unit"):  make(map[stingray.FileID]stingray.FileID),
		stingray.Sum("level"): make(map[stingray.FileID]stingray.FileID),
	}
	if currentDir == nil || olderDir == nil {
		return changed
	}
	for currentID, currentInfos := range currentDir.Files {
		if ctx.Err() != nil {
			return changed
		}
		if currentID.Type != stingray.Sum("unit") && currentID.Type != stingray.Sum("level") {
			continue
		}
		olderInfos := olderDir.Files[currentID]
		if len(currentInfos) == 0 || len(olderInfos) == 0 {
			continue
		}
		if !sameResourceSizes(currentInfos[0], olderInfos[0]) {
			changed[currentID.Type][currentID] = currentID
			continue
		}
		currentData, currentErr := currentDir.Read(currentID, stingray.DataMain)
		olderData, olderErr := olderDir.Read(currentID, stingray.DataMain)
		if currentErr == nil && olderErr == nil && !bytes.Equal(currentData, olderData) {
			changed[currentID.Type][currentID] = currentID
		}
	}
	return changed
}

func sameResourceSizes(a, b stingray.FileInfo) bool {
	for dataType := stingray.DataMain; dataType < stingray.NumDataType; dataType++ {
		if a.Files[dataType].Size != b.Files[dataType].Size {
			return false
		}
	}
	return true
}

// Unit IDs are hashes of resource paths. When a model moved folders between
// builds, a unique, recognized filename within the same faction/content group
// is a useful replacement hint. Unknown hash-only names are deliberately not
// guessed.
func matchUnitsByStableLeaf(data *GameData, added, removed map[stingray.FileID]struct{}) map[stingray.FileID]stingray.FileID {
	newByKey := make(map[string][]stingray.FileID)
	oldByKey := make(map[string][]stingray.FileID)
	for id := range added {
		if key := unitReplacementKey(data.LookupHash(id.Name)); key != "" {
			newByKey[key] = append(newByKey[key], id)
		}
	}
	for id := range removed {
		if key := unitReplacementKey(data.LookupHash(id.Name)); key != "" {
			oldByKey[key] = append(oldByKey[key], id)
		}
	}
	pairs := make(map[stingray.FileID]stingray.FileID)
	for key, newIDs := range newByKey {
		oldIDs := oldByKey[key]
		if len(newIDs) != 1 || len(oldIDs) != 1 {
			continue
		}
		pairs[newIDs[0]] = oldIDs[0]
	}
	return pairs
}

func unitReplacementKey(path string) string {
	path = strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
	path = strings.TrimSuffix(path, ".unit")
	if !strings.HasPrefix(path, "content/") || strings.HasPrefix(path, "content/0x") {
		return ""
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 {
		return ""
	}
	leaf := strings.TrimSpace(parts[len(parts)-1])
	if len(leaf) < 4 || leaf == "unit" || leaf == "default" {
		return ""
	}
	// Keep the faction or broad content family in the key, avoiding matches
	// between unrelated assets which happen to share a short basename.
	return strings.Join([]string{parts[0], parts[1], leaf}, "/")
}

func matchLevelsByContents(
	ctx context.Context,
	data *GameData,
	currentDir, olderDir *stingray.DataDir,
	added, removed map[stingray.FileID]struct{},
) map[stingray.FileID]stingray.FileID {
	newIDs := mapsKeys(added)
	oldIDs := mapsKeys(removed)
	slices.SortFunc(newIDs, stingray.FileID.Cmp)
	slices.SortFunc(oldIDs, stingray.FileID.Cmp)
	newSigs := make(map[stingray.FileID]levelVersionSignature, len(newIDs))
	oldSigs := make(map[stingray.FileID]levelVersionSignature, len(oldIDs))
	for _, id := range newIDs {
		if ctx.Err() != nil {
			return nil
		}
		if sig, ok := readLevelVersionSignature(data, currentDir, id); ok {
			newSigs[id] = sig
		}
	}
	for _, id := range oldIDs {
		if ctx.Err() != nil {
			return nil
		}
		if sig, ok := readLevelVersionSignature(data, olderDir, id); ok {
			oldSigs[id] = sig
		}
	}

	// Keep only mutual best matches with a healthy margin over alternatives.
	// This prevents similarly themed levels from being incorrectly paired.
	oldByRef := make(map[stingray.Hash][]stingray.FileID)
	for oldID, signature := range oldSigs {
		for ref := range signature.refs {
			oldByRef[ref] = append(oldByRef[ref], oldID)
		}
	}
	newCandidates := make(map[stingray.FileID][]levelCandidate)
	oldCandidates := make(map[stingray.FileID][]levelCandidate)
	for newID, newSig := range newSigs {
		if ctx.Err() != nil {
			return nil
		}
		sharedRefs := make(map[stingray.FileID]int)
		for ref, count := range newSig.refs {
			for _, oldID := range oldByRef[ref] {
				sharedRefs[oldID] += int(min(count, oldSigs[oldID].refs[ref]))
			}
		}
		for oldID, shared := range sharedRefs {
			if shared < 3 {
				continue
			}
			oldSig := oldSigs[oldID]
			candidate, ok := scoreLevelCandidate(newSig, oldSig)
			if !ok {
				continue
			}
			newCandidates[newID] = append(newCandidates[newID], levelCandidate{id: oldID, score: candidate.score, shared: candidate.shared})
			oldCandidates[oldID] = append(oldCandidates[oldID], levelCandidate{id: newID, score: candidate.score, shared: candidate.shared})
		}
	}
	newBest := make(map[stingray.FileID]levelCandidate)
	oldBest := make(map[stingray.FileID]levelCandidate)
	newUnambiguous := make(map[stingray.FileID]bool)
	oldUnambiguous := make(map[stingray.FileID]bool)
	for id, candidates := range newCandidates {
		newBest[id], newUnambiguous[id] = unambiguousLevelBest(candidates)
	}
	for id, candidates := range oldCandidates {
		oldBest[id], oldUnambiguous[id] = unambiguousLevelBest(candidates)
	}
	pairs := make(map[stingray.FileID]stingray.FileID)
	for newID, bestOld := range newBest {
		oldID := bestOld.id
		oldBestForNew, ok := oldBest[oldID]
		if !ok || oldBestForNew.id != newID || !newUnambiguous[newID] || !oldUnambiguous[oldID] {
			continue
		}
		pairs[newID] = oldID
	}
	return pairs
}

// mapsKeys is kept local to avoid mutating the comparison's shared maps.
func mapsKeys[V any](values map[stingray.FileID]V) []stingray.FileID {
	ids := make([]stingray.FileID, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	return ids
}

func readLevelVersionSignature(data *GameData, dir *stingray.DataDir, id stingray.FileID) (signature levelVersionSignature, ok bool) {
	if dir == nil || len(dir.Files[id]) == 0 {
		return signature, false
	}
	resource, err := dir.Read(id, stingray.DataMain)
	if err != nil {
		return signature, false
	}
	info, err := level.LoadLevel(bytes.NewReader(resource), nil)
	if err != nil {
		return signature, false
	}
	refs := make(map[stingray.Hash]uint32)
	addRef := func(hash stingray.Hash) {
		if hash == (stingray.Hash{}) {
			return
		}
		refs[hash]++
	}
	for _, placed := range info.Units {
		addRef(placed.Hash)
	}
	for _, placed := range info.Prefabs {
		addRef(placed.Path)
	}
	for _, tree := range info.Speedtrees {
		addRef(tree.Hash)
	}
	for _, particle := range info.Particles {
		addRef(particle.Hash)
	}
	for _, embedded := range info.EmbeddedPrefabs {
		addRef(embedded.EmbeddedPrefabTransform.Hash)
		for _, placed := range embedded.Prefab.Units {
			addRef(placed.Hash)
		}
		for _, nested := range embedded.Prefab.NestedPrefabs {
			addRef(nested.Path)
		}
	}
	for _, materials := range info.MaterialOverrides {
		for _, material := range materials {
			addRef(material)
		}
	}
	for _, entries := range info.Metadata {
		for _, entry := range entries {
			if entry.Type == level.LevelMetadata_string && strings.HasPrefix(strings.ToLower(entry.ValueString), "content/") {
				addRef(stingray.Sum(strings.ToLower(filepath.ToSlash(entry.ValueString))))
			}
		}
	}
	base := inferLevelLabel(resource, data.LookupHash, data.LookupThinHash)
	base = strings.Join(strings.Fields(strings.ToLower(base)), " ")
	return levelVersionSignature{base: base, refs: refs}, true
}

type levelCandidate struct {
	id     stingray.FileID
	score  float64
	shared int
}

func scoreLevelCandidate(a, b levelVersionSignature) (levelCandidate, bool) {
	shared, union := 0, 0
	for ref, countA := range a.refs {
		countB := b.refs[ref]
		shared += int(min(countA, countB))
		union += int(max(countA, countB))
	}
	for ref, countB := range b.refs {
		if _, exists := a.refs[ref]; !exists {
			union += int(countB)
		}
	}
	if shared < 3 || union == 0 {
		return levelCandidate{}, false
	}
	similarity := float64(shared) / float64(union)
	sameInferredName := a.base != "" && a.base == b.base
	if sameInferredName {
		if similarity < 0.38 {
			return levelCandidate{}, false
		}
	} else if similarity < 0.68 || shared < 5 {
		return levelCandidate{}, false
	}
	score := similarity
	if sameInferredName {
		score += 0.2
	}
	return levelCandidate{score: score, shared: shared}, true
}

func betterLevelCandidate(candidate, previous levelCandidate) bool {
	return candidate.score > previous.score || candidate.score == previous.score && candidate.shared > previous.shared
}

func unambiguousLevelBest(candidates []levelCandidate) (levelCandidate, bool) {
	var best, second levelCandidate
	for _, candidate := range candidates {
		if betterLevelCandidate(candidate, best) {
			second, best = best, candidate
		} else if betterLevelCandidate(candidate, second) {
			second = candidate
		}
	}
	return best, best.score > 0 && best.score-second.score >= 0.08
}
