package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/qmuntal/gltf"
	"github.com/xypwn/filediver/app"
	"github.com/xypwn/filediver/app/appconfig"
	fnt "github.com/xypwn/filediver/cmd/filediver-gui/fonts"
	"github.com/xypwn/filediver/cmd/filediver-gui/textutils"
	datalib "github.com/xypwn/filediver/datalibrary"
	"github.com/xypwn/filediver/exec"
	"github.com/xypwn/filediver/extractor/blend_helper"
	"github.com/xypwn/filediver/extractor/single_glb_helper"
	"github.com/xypwn/filediver/hashes"
	"github.com/xypwn/filediver/stingray"
	stingray_strings "github.com/xypwn/filediver/stingray/strings"
)

type GameDataExport struct {
	sync.Mutex
	Cancel           func()
	Done             bool
	Canceled         bool
	CurrentFileIndex int
	CurrentFileName  string
	NumFiles         int
}

type GameData struct {
	*app.App
	KnownFileNames            map[stingray.FileID]string
	HashFileNames             map[stingray.FileID]string
	InferredUnitNames         map[stingray.Hash]inferredUnitName
	SortedSearchResultFileIDs []stingray.FileID

	FilterExpr    *app.FilterExprProgram
	FilterExprErr error

	AssetOverridesDirty bool
	ColorGrading        stingray.FileID
}

func NewGameData(a *app.App) *GameData {
	gd := &GameData{App: a}
	gd.KnownFileNames = make(map[stingray.FileID]string)
	gd.HashFileNames = make(map[stingray.FileID]string)
	gd.InferredUnitNames = gd.inferUnitNames()
	for id := range gd.DataDir.Files {
		name := gd.LookupHash(id.Name)
		if id.Type == stingray.Sum("unit") {
			name = gd.DisplayFileName(id)
			if inferred, ok := gd.InferredUnitNames[id.Name]; ok {
				// Include the evidence in the browser search index so users can
				// find inferred names by their entity path, mesh name, or relation.
				name += " " + inferred.Source
			}
		}
		gd.KnownFileNames[id] = strings.ToLower(name + "." + gd.LookupHash(id.Type))
		gd.HashFileNames[id] = id.Name.String() + "." + id.Type.String()
	}
	gd.UpdateSearchQuery("", nil, nil)
	return gd
}

// DisplayFileName returns the real path when the hash database knows it.
// Otherwise, for unit resources only, it uses a source-labeled descriptor
// inferred from generated entity definitions or the unit's mesh-group names.
// The raw hash remains available in the browser tooltip.
func (gd *GameData) DisplayFileName(id stingray.FileID) string {
	name := gd.LookupHash(id.Name)
	if label, ok := gd.InferredUnitNames[id.Name]; id.Type == stingray.Sum("unit") && ok && label.Terrain {
		return label.Display
	}
	if id.Type != stingray.Sum("unit") || name != id.Name.String() {
		return name
	}
	if inferred, ok := gd.InferredUnitNames[id.Name]; ok {
		if inferred.Vista && !strings.Contains(strings.ToLower(inferred.Display), "vista") {
			return "vista shader: " + inferred.Display
		}
		return inferred.Display
	}
	return name
}

func (gd *GameData) UpdateSearchQuery(query string, allowedTypes map[stingray.Hash]struct{}, allowedArchives map[stingray.Hash]struct{}) {
	gd.FilterExpr, gd.FilterExprErr = nil, nil
	if idx := strings.Index(query, "?"); idx != -1 {
		exprStr := query[idx+1:]
		query = query[:idx]
		gd.FilterExpr, gd.FilterExprErr = app.CompileMetadataFilterExpr(exprStr)
		if gd.FilterExprErr != nil {
			return
		}
	}

	gd.SortedSearchResultFileIDs = gd.SortedSearchResultFileIDs[:0]

	// returns whether to continue
	maybeAdd := func(fileID stingray.FileID) bool {
		if len(allowedTypes) > 0 {
			if _, allowed := allowedTypes[fileID.Type]; !allowed {
				return true
			}
		}
		if !textutils.QueryMatchesAny(query, gd.KnownFileNames[fileID], gd.HashFileNames[fileID]) {
			return true
		}
		if gd.FilterExpr != nil {
			matches, err := app.MetadataFilterExprMatches(gd.FilterExpr, gd.Metadata[fileID])
			if err != nil {
				gd.FilterExprErr = err
				return false
			}
			if !matches {
				return true
			}
		}
		gd.SortedSearchResultFileIDs = append(gd.SortedSearchResultFileIDs, fileID)
		return true
	}

	if len(allowedArchives) == 0 {
		for fileID := range gd.DataDir.Files {
			if !maybeAdd(fileID) {
				break
			}
		}
	} else {
		seen := make(map[stingray.FileID]bool)
		medium_cape := stingray.NewFileID(stingray.Sum("content/fac_helldivers/capes/medium_cape"), stingray.Sum("unit"))
		shock_trooper_cape := stingray.NewFileID(stingray.Sum("content/fac_helldivers/capes/shock_trooper_cape"), stingray.Sum("unit"))
	archiveLoop:
		for archiveID := range allowedArchives {
			if armorSet, contains := gd.ArmorSets[archiveID]; contains && armorSet.Type == datalib.KitCape && !seen[medium_cape] {
				gd.SortedSearchResultFileIDs = append(gd.SortedSearchResultFileIDs, medium_cape, shock_trooper_cape)
				seen[medium_cape] = true
				seen[shock_trooper_cape] = true
			}
			if files, ok := gd.DataDir.Archives[archiveID]; ok {
				for _, fileID := range files {
					if seen[fileID] {
						continue
					}
					if !maybeAdd(fileID) {
						break archiveLoop
					}
					seen[fileID] = true
				}
			}
		}
	}
	gd.SortSearchResults()
}

func (gd *GameData) SortSearchResults() {
	slices.SortFunc(gd.SortedSearchResultFileIDs, func(a, b stingray.FileID) int {
		return strings.Compare(gd.KnownFileNames[a], gd.KnownFileNames[b])
	})
}

func (gd *GameData) fileSize(id stingray.FileID) uint64 {
	fileInfos := gd.DataDir.Files[id]
	if len(fileInfos) == 0 {
		return 0
	}
	var size uint64
	for _, dataType := range [...]stingray.DataType{stingray.DataMain, stingray.DataStream, stingray.DataGPU} {
		size += uint64(fileInfos[0].Files[dataType].Size)
	}
	return size
}

func (gd *GameData) UpdateAssetOverrides(planetName string, city bool) {
	gd.App.UpdateAssetOverrides(planetName, city)
	planet := gd.GetPlanet(planetName, city)
	gd.ColorGrading = stingray.FileID{}
	if planet != nil {
		gd.ColorGrading = stingray.NewFileID(planet.PaletteGroupLowland.AssetGrading, stingray.Sum("entity"))
	}
	gd.AssetOverridesDirty = true
}

func (gd *GameData) GoExport(extractCtx context.Context, files []stingray.FileID, outDir string, cfg appconfig.Config, runner *exec.Runner, archiveIDs []stingray.Hash, printer app.Printer, allowMp4Export bool) *GameDataExport {
	_ = cfg == cfg // make sure we can't create side effects

	ex := &GameDataExport{}
	ex.NumFiles = len(files)
	extractCtx, cancel := context.WithCancel(extractCtx)
	ex.Cancel = cancel

	go func() {
		defer func() {
			if err := recover(); err != nil {
				printer.Fatalf("%v", err)
			}

			printer.NoStatus()
			ex.Lock()
			ex.Done = true
			ex.Unlock()
		}()

		var documents map[string]*gltf.Document = make(map[string]*gltf.Document)
		var documentsToClose []func() error
		if cfg.Unit.SingleFile {
			for _, key := range []string{"unit", "geometry_group", "material", "level", "prefab", "speedtree"} {
				name := "combined_" + key
				var format string
				switch key {
				case "unit", "geometry_group", "level", "prefab", "speedtree":
					format = cfg.Model.Format
				case "material":
					format = cfg.Material.Format
				default:
					panic("unknown format: " + key)
				}
				doc, close := single_glb_helper.CreateCloseableGltfDocument(extractCtx, printer.Statusf, outDir, name, format, runner, gd.GameBuildInfo)
				documents[key] = doc
				documentsToClose = append(documentsToClose, func() error { return close(doc) })
			}
		}

		handleErr := func(err error) (canceled bool) {
			if errors.Is(err, context.Canceled) {
				ex.Lock()
				ex.Canceled = true
				ex.Unlock()
				canceled = true
			} else if scriptErr := (&blend_helper.ScriptExitError{}); errors.As(err, &scriptErr) {
				printer.Errorf("%v", scriptErr.FullError())
			} else {
				printer.Errorf("%v", err)
			}
			return
		}

		if cfg.Video.Format == "mp4" {
			for _, fileID := range files {
				if fileID.Type == stingray.Sum("bk2") {
					if runtime.GOOS == "windows" && !allowMp4Export {
						printer.Errorf("MP4 export not enabled, exporting as bk2 (you can enable MP4 export in Settings->Preferences)")
					} else if runtime.GOOS == "linux" {
						printer.Errorf("MP4 export is not available on Linux at the moment")
					}
					cfg.Video.Format = "bk2"
					break
				}
			}
		}

		for _, fileID := range files {
			currFileName := gd.LookupHash(fileID.Name) + "." + gd.LookupHash(fileID.Type)
			ex.Lock()
			ex.CurrentFileName = currFileName
			ex.Unlock()
			statusf := func(_ string, _ ...any) {}
			printer.Statusf("File: %v", currFileName)

			var gltfDoc *gltf.Document
			if doc, ok := documents[gd.LookupHash(fileID.Type)]; ok {
				gltfDoc = doc
			}
			_, err := gd.ExtractFile(extractCtx, fileID, outDir, cfg, runner, gltfDoc, archiveIDs, printer, statusf)
			if err != nil {
				if canceled := handleErr(err); canceled {
					break
				}
			}

			ex.Lock()
			ex.CurrentFileIndex++
			ex.Unlock()
		}
		if len(documentsToClose) > 0 {
			printer.Statusf("processing combined documents")
		}
		for _, close := range documentsToClose {
			if err := close(); err != nil {
				if canceled := handleErr(err); canceled {
					break
				}
			}
		}
	}()
	return ex
}

type GameDataLoad struct {
	sync.Mutex
	Progress float32
	Result   *GameData
	Err      error
	Done     bool
}

// UnitVersionScan stores the compared build's resource index while the
// selected build remains loaded and serves as the comparison baseline.
type UnitVersionScan struct {
	sync.Mutex
	Progress float32
	Units    map[stingray.FileID]struct{}
	DataDir  *stingray.DataDir
	Path     string
	Err      error
	Done     bool
}

func (s *UnitVersionScan) GoScan(ctx context.Context, gameDir string) {
	s.Lock()
	if !s.Done && s.Path != "" {
		s.Unlock()
		return
	}
	s.Path = gameDir
	s.Progress = 0
	s.Units = nil
	s.DataDir = nil
	s.Err = nil
	s.Done = false
	s.Unlock()

	go func() {
		dataDirPath := gameDir
		for _, candidate := range []string{gameDir, filepath.Join(gameDir, "contents"), filepath.Join(gameDir, "data")} {
			if info, err := os.Stat(filepath.Join(candidate, "settings.ini")); err == nil && info.Mode().IsRegular() {
				dataDirPath = candidate
				break
			}
		}
		dataDir, err := stingray.OpenDataDir(ctx, dataDirPath, func(current, total int) {
			s.Lock()
			if total > 0 {
				s.Progress = float32(current+1) / float32(total)
			}
			s.Unlock()
		})
		var units map[stingray.FileID]struct{}
		if err == nil {
			if dataDir.IsHelldivers1 {
				err = fmt.Errorf("the selected folder contains Helldivers 1 data; choose an older Helldivers 2 install")
			} else {
				units = make(map[stingray.FileID]struct{})
				for id := range dataDir.Files {
					if id.Type == stingray.Sum("unit") {
						units[id] = struct{}{}
					}
				}
			}
		}
		s.Lock()
		s.Units, s.DataDir, s.Err, s.Done = units, dataDir, err, true
		if err == nil {
			s.Progress = 1
		}
		s.Unlock()
	}()
}

func (gd *GameDataLoad) loadGameData(ctx context.Context, gameDir string) {
	if gameDir == "" {
		var err error
		gameDir, err = app.DetectGameDir()
		if err != nil {
			gd.Lock()
			gd.Err = fmt.Errorf("Helldivers 2 Steam installation path not found: %w, please select the game directory manually under \"%v Extractor config\"", err, fnt.I.SettingsApplications)
			gd.Done = true
			gd.Unlock()
			return
		}
	}

	a, err := app.OpenGameDir(ctx, gameDir, hashes.ParseHashes(hashes.Hashes), hashes.ParseHashes(hashes.ThinHashes), stingray_strings.LanguageFriendlyNameToHash["English (US)"], func(curr, total int) {
		gd.Lock()
		gd.Progress = float32(curr+1) / float32(total)
		gd.Unlock()
	})
	if err != nil {
		gd.Lock()
		gd.Err = err
		gd.Done = true
		gd.Unlock()
		return
	}

	res := NewGameData(a)
	gd.Lock()
	gd.Result = res
	gd.Done = true
	gd.Unlock()
}

// GoLoadGameData asynchronously loads the game data.
// Pass string "<auto-detect>" or empty string to gameDir to auto-detect.
func (gd *GameDataLoad) GoLoadGameData(ctx context.Context, gameDir string) {
	gd.Progress = 0
	gd.Result = nil
	gd.Err = nil
	gd.Done = false
	if gameDir == "<auto-detect>" {
		gameDir = ""
	}
	go gd.loadGameData(ctx, gameDir)
}
