package main

import (
	"bytes"
	"context"
	"hash/fnv"
	"sort"
	"strings"
	"sync"

	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/state_machine"
	"github.com/xypwn/filediver/stingray/unit"
)

type unitAnimationScanState struct {
	sync.Mutex
	currentDir *stingray.DataDir
	olderDir   *stingray.DataDir
	idsHash    uint64
	idsCount   int
	generation uint64
	cancel     context.CancelFunc
	results    map[stingray.FileID]struct{}
	progress   int
	total      int
	done       bool
}

func unitAnimationIDsHash(ids []stingray.FileID) uint64 {
	// The Units list is mostly stable, but removed entries may be appended from
	// a map in arbitrary order. Sort-independent accumulation avoids restarting
	// this one-time scan just because that order changes between frames.
	var xor, sum uint64
	for _, id := range ids {
		value := id.Name.Value ^ (id.Type.Value * 0x9e3779b97f4a7c15)
		value ^= value >> 30
		value *= 0xbf58476d1ce4e5b9
		value ^= value >> 27
		value *= 0x94d049bb133111eb
		value ^= value >> 31
		xor ^= value
		sum += value
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte{byte(len(ids)), byte(len(ids) >> 8), byte(len(ids) >> 16), byte(len(ids) >> 24)})
	var countHash [8]byte
	for i := range countHash {
		countHash[i] = byte(uint64(len(ids)) >> (8 * i))
	}
	_, _ = h.Write(countHash[:])
	return xor ^ (sum << 17) ^ (sum >> 47) ^ h.Sum64()
}

func (a *guiApp) startUnitAnimationScan(ids []stingray.FileID) {
	if a.gameData == nil || a.gameData.DataDir == nil {
		return
	}
	currentDir := a.gameData.DataDir
	olderDir := a.olderBuildDataDir
	idsHash := unitAnimationIDsHash(ids)

	state := &a.unitAnimationScan
	state.Lock()
	if state.currentDir == currentDir && state.olderDir == olderDir && state.idsHash == idsHash && state.idsCount == len(ids) && (state.done || state.cancel != nil) {
		state.Unlock()
		return
	}
	if state.cancel != nil {
		state.cancel()
	}
	ctx, cancel := context.WithCancel(a.ctx)
	state.generation++
	generation := state.generation
	state.currentDir = currentDir
	state.olderDir = olderDir
	state.idsHash = idsHash
	state.idsCount = len(ids)
	state.cancel = cancel
	state.results = nil
	state.progress = 0
	state.total = len(ids)
	state.done = false
	state.Unlock()

	unitIDs := append([]stingray.FileID(nil), ids...)
	lookupHash := a.gameData.LookupHash
	go func() {
		results := scanUnitsWithAnimations(ctx, currentDir, olderDir, unitIDs, lookupHash, func(progress, total int) {
			state.Lock()
			if state.generation == generation {
				state.progress = progress
				state.total = total
			}
			state.Unlock()
		})
		state.Lock()
		if state.generation == generation {
			state.results = results
			state.progress = len(unitIDs)
			state.total = len(unitIDs)
			state.done = true
			state.cancel = nil
		}
		state.Unlock()
	}()
}

func (a *guiApp) unitAnimationScanSnapshot() (map[stingray.FileID]struct{}, bool, int, int) {
	state := &a.unitAnimationScan
	state.Lock()
	defer state.Unlock()
	return state.results, state.done, state.progress, state.total
}

func scanUnitsWithAnimations(
	ctx context.Context,
	currentDir, olderDir *stingray.DataDir,
	ids []stingray.FileID,
	lookupHash func(stingray.Hash) string,
	progress func(int, int),
) map[stingray.FileID]struct{} {
	animated := make(map[stingray.FileID]struct{})
	if lookupHash == nil {
		return animated
	}
	localAnimationPaths := map[*stingray.DataDir][]string{
		currentDir: animationResourcePaths(currentDir, lookupHash),
	}
	if olderDir != nil && olderDir != currentDir {
		localAnimationPaths[olderDir] = animationResourcePaths(olderDir, lookupHash)
	}

	for index, id := range ids {
		if ctx.Err() != nil {
			return animated
		}
		dir := currentDir
		if !dataDirHasMain(dir, id) && olderDir != nil && dataDirHasMain(olderDir, id) {
			dir = olderDir
		}
		if dir != nil && dataDirHasMain(dir, id) && unitHasLocalAnimation(id, lookupHash, localAnimationPaths[dir]) {
			animated[id] = struct{}{}
		} else if dir != nil && dataDirHasMain(dir, id) && unitHasLinkedAnimation(dir, id) {
			animated[id] = struct{}{}
		}
		if progress != nil && (index%16 == 15 || index == len(ids)-1) {
			progress(index+1, len(ids))
		}
	}
	return animated
}

func animationResourcePaths(dir *stingray.DataDir, lookupHash func(stingray.Hash) string) []string {
	if dir == nil {
		return nil
	}
	paths := make([]string, 0)
	for id := range dir.Files {
		if id.Type != stingray.Sum("animation") || !dataDirHasMain(dir, id) {
			continue
		}
		path := normalizedAnimationResourcePath(lookupHash(id.Name))
		if path != "" && !strings.HasPrefix(path, "0x") {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths
}

func dataDirHasMain(dir *stingray.DataDir, id stingray.FileID) bool {
	if dir == nil {
		return false
	}
	files := dir.Files[id]
	return len(files) > 0 && files[0].Files[stingray.DataMain].Exists()
}

func normalizedAnimationResourcePath(path string) string {
	return strings.ToLower(strings.Trim(strings.ReplaceAll(path, `\`, "/"), "/"))
}

func unitHasLocalAnimation(id stingray.FileID, lookupHash func(stingray.Hash) string, animationPaths []string) bool {
	unitPath := normalizedAnimationResourcePath(lookupHash(id.Name))
	if unitPath == "" || strings.HasPrefix(unitPath, "0x") || len(animationPaths) == 0 {
		return false
	}
	prefix := unitPath + "/animations/"
	index := sort.SearchStrings(animationPaths, prefix)
	return index < len(animationPaths) && strings.HasPrefix(animationPaths[index], prefix)
}

func unitHasLinkedAnimation(dir *stingray.DataDir, id stingray.FileID) bool {
	unitData, err := dir.Read(id, stingray.DataMain)
	if err != nil {
		return false
	}
	info, err := unit.LoadInfo(bytes.NewReader(unitData))
	if err != nil || info.StateMachine.Value == 0 {
		return false
	}
	stateID := stingray.NewFileID(info.StateMachine, stingray.Sum("state_machine"))
	stateData, err := dir.Read(stateID, stingray.DataMain)
	if err != nil {
		return false
	}
	sm, err := state_machine.LoadStateMachine(bytes.NewReader(stateData))
	if err != nil {
		return false
	}
	animationType := stingray.Sum("animation")
	for _, layer := range sm.Layers {
		for _, state := range layer.States {
			for _, animationHash := range state.AnimationHashes {
				animationID := stingray.NewFileID(animationHash, animationType)
				files := dir.Files[animationID]
				if len(files) > 0 && files[0].Files[stingray.DataMain].Exists() {
					return true
				}
			}
		}
	}
	return false
}
