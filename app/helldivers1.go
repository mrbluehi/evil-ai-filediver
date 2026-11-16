package app

import (
	"encoding/binary"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/unit"
)

var helldivers1ResourcePathPattern = regexp.MustCompile(`(?:[A-Za-z0-9_.-]+/){1,}[A-Za-z0-9_.-]+`)

// recoverHelldivers1Names uses paths embedded in the game's Lua bytecode as
// candidate names, then accepts a name only when its hash matches an installed
// resource. This recovers readable folders without guessing names from hashes.
func recoverHelldivers1Names(dataDir *stingray.DataDir, knownPaths []string) map[stingray.Hash]string {
	resourceNames := make(map[stingray.Hash]struct{}, len(dataDir.Files))
	for fileID := range dataDir.Files {
		resourceNames[fileID.Name] = struct{}{}
	}

	prefixes := make(map[string]struct{})
	for _, path := range knownPaths {
		parts := strings.Split(path, "/")
		if len(parts) > 2 && parts[0] == "content" {
			prefixes["content/"+parts[1]+"/"] = struct{}{}
		}
	}
	orderedPrefixes := make([]string, 0, len(prefixes))
	for prefix := range prefixes {
		orderedPrefixes = append(orderedPrefixes, prefix)
	}
	slices.Sort(orderedPrefixes)

	candidates := make(map[string]struct{})
	for fileID := range dataDir.Files {
		if fileID.Type != stingray.Sum("lua") {
			continue
		}
		data, err := dataDir.Read(fileID, stingray.DataMain)
		if err != nil || len(data) < 8 {
			continue
		}
		scriptSize := uint64(binary.LittleEndian.Uint32(data[:4]))
		if scriptSize > uint64(len(data)-8) {
			scriptSize = uint64(len(data) - 8)
		}
		for _, match := range helldivers1ResourcePathPattern.FindAll(data[8:8+int(scriptSize)], -1) {
			path := strings.Trim(string(match), "._-/")
			if path == "" || len(path) > 240 || strings.Contains(path, "//") {
				continue
			}
			candidates[path] = struct{}{}
			if !strings.HasPrefix(path, "content/") {
				candidates["content/"+path] = struct{}{}
				for _, prefix := range orderedPrefixes {
					candidates[prefix+path] = struct{}{}
				}
			}
		}
	}

	orderedCandidates := make([]string, 0, len(candidates))
	for candidate := range candidates {
		orderedCandidates = append(orderedCandidates, candidate)
	}
	slices.Sort(orderedCandidates)
	result := make(map[stingray.Hash]string)
	for _, candidate := range orderedCandidates {
		hash := stingray.Sum(candidate)
		if _, ok := resourceNames[hash]; ok {
			if _, alreadySet := result[hash]; !alreadySet {
				result[hash] = candidate
			}
		}
	}
	return result
}

func helldivers1Metadata(dataDir *stingray.DataDir) map[stingray.FileID]FileMetadata {
	metadata := make(map[stingray.FileID]FileMetadata, len(dataDir.Files))
	unitType := stingray.Sum("unit")
	for fileID, files := range dataDir.Files {
		fileMetadata := FileMetadata{
			AvailableFields: make(map[string]bool),
			Name:            fileID.Name,
			Type:            fileID.Type,
		}
		for _, file := range files {
			fileMetadata.Archives = append(fileMetadata.Archives, file.ArchiveID)
		}
		fileMetadata.addAvailableFields("Name", "Type", "Archives")
		if fileID.Type == unitType {
			mainData, err := dataDir.Read(fileID, stingray.DataMain)
			if err == nil {
				_, meshes, parseErr := unit.LoadHelldivers1(mainData)
				if parseErr == nil {
					var minBounds, maxBounds mgl32.Vec3
					foundPosition := false
					for _, mesh := range meshes {
						fileMetadata.VertexCount += uint64(len(mesh.Positions))
						for _, position := range mesh.Positions {
							valid := true
							for axis := range 3 {
								if math.IsNaN(float64(position[axis])) || math.IsInf(float64(position[axis]), 0) {
									valid = false
									break
								}
							}
							if !valid {
								continue
							}
							if !foundPosition {
								minBounds, maxBounds = position, position
								foundPosition = true
								continue
							}
							for axis := range 3 {
								minBounds[axis] = min(minBounds[axis], position[axis])
								maxBounds[axis] = max(maxBounds[axis], position[axis])
							}
						}
						for _, indices := range mesh.Indices {
							fileMetadata.TriangleCount += uint64(len(indices) / 3)
						}
					}
					if foundPosition {
						dimensions := maxBounds.Sub(minBounds)
						fileMetadata.ModelExtent = float64(max(dimensions[0], dimensions[1], dimensions[2]))
					}
					// HD1 units do not have an HD2-style LOD-group table; their
					// embedded mesh parts are all the available model geometry.
					fileMetadata.AllLODTriangles = fileMetadata.TriangleCount
					if len(files) > 0 {
						mainSize := uint64(files[0].Files[stingray.DataMain].Size)
						streamSize := uint64(files[0].Files[stingray.DataStream].Size)
						gpuSize := uint64(files[0].Files[stingray.DataGPU].Size)
						fileMetadata.ModelSize = mainSize + max(streamSize, gpuSize)
					}
					fileMetadata.addAvailableFields("VertexCount", "TriangleCount", "AllLODTriangles", "ModelSize", "ModelExtent")
				}
			}
		}
		metadata[fileID] = fileMetadata
	}
	return metadata
}
