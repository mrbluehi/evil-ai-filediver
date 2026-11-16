package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io/fs"
	"maps"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/adrg/xdg"
	"github.com/ebitengine/oto/v3"
	"github.com/mattn/go-shellwords"
	"github.com/ncruces/zenity"
	"github.com/skratchdot/open-golang/open"
	filediverapp "github.com/xypwn/filediver/app"
	"github.com/xypwn/filediver/app/appconfig"
	"github.com/xypwn/filediver/appicons"
	fnt "github.com/xypwn/filediver/cmd/filediver-gui/fonts"
	"github.com/xypwn/filediver/cmd/filediver-gui/github"
	"github.com/xypwn/filediver/cmd/filediver-gui/imgui_wrapper"
	"github.com/xypwn/filediver/cmd/filediver-gui/imutils"
	"github.com/xypwn/filediver/cmd/filediver-gui/tasks"
	"github.com/xypwn/filediver/cmd/filediver-gui/widgets"
	"github.com/xypwn/filediver/cmd/filediver-gui/widgets/previews"
	"github.com/xypwn/filediver/config"
	datalib "github.com/xypwn/filediver/datalibrary"
	"github.com/xypwn/filediver/exec"
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/entity"
	"golang.design/x/clipboard"
)

//go:embed LICENSE
var license string

var (
	gameFileTypeDescriptions = map[stingray.Hash]string{
		stingray.Sum("bik"):            "video",
		stingray.Sum("bk2"):            "video",
		stingray.Sum("wwise_bank"):     "audio bank",
		stingray.Sum("wwise_stream"):   "loose audio",
		stingray.Sum("texture"):        "image/texture",
		stingray.Sum("unit"):           "3D model",
		stingray.Sum("prefab"):         "pre-arranged group of 3D models",
		stingray.Sum("level"):          "objectives/POIs",
		stingray.Sum("speedtree"):      "3D model for vegetation",
		stingray.Sum("strings"):        "text table",
		stingray.Sum("package"):        "file bundle",
		stingray.Sum("bones"):          "unit bones",
		stingray.Sum("physics"):        "unit physics",
		stingray.Sum("geometry_group"): "group of 3D models",
		stingray.Sum("material"):       "shader settings",
		stingray.Sum("xaml"):           "Noesis GUI [UI / vector graphics]",
	}
	gameFileTypeTooltipsExtra = map[stingray.Hash]string{
		stingray.Sum("wwise_stream"): "All wwise_streams are also contained in a wwise_bank.\nYou probably want to use wwise_bank instead.",
		stingray.Sum("package"):      "A package contains references to a bunch of other files.",
	}
)

const ffmpegFeatures = `- Preview video
- Convert audio to OGG/AAC/MP3
- Convert video to MP4`

const scriptsDistFeatures = `- Export models (units/geometry_groups/prefabs/levels) and materials to .blend (Blender)`

type guiApp struct {
	showErrorPopup func(error)

	ctx context.Context

	preferencesPath    string
	preferences        Preferences
	defaultPreferences Preferences

	otoCtx          *oto.Context
	audioSampleRate int

	gameDataLoad   GameDataLoad
	gameDataExport *GameDataExport
	gameData       *GameData

	preview                    *previews.AutoPreview
	currentPreview             *previews.AutoPreview
	olderBuildPreview          *previews.AutoPreview
	olderBuildDataDir          *stingray.DataDir
	olderBuildArmorSets        map[stingray.Hash]datalib.ArmorSet
	olderBuildArmorTableValid  bool
	unitGallery                *previews.UnitGallery
	levelGallery               *previews.UnitGallery
	textureGallery             *previews.TextureGallery
	olderTextureGallery        *previews.TextureGallery
	unitVersionScan            UnitVersionScan
	unitIDs                    []stingray.FileID
	unitIDsReady               bool
	levelIDs                   []stingray.FileID
	levelIDsReady              bool
	levelLabelData             *GameData
	levelLabels                map[stingray.FileID]inferredLevelLabel
	levelLabelsScheduled       map[stingray.FileID]struct{}
	levelLabelResults          chan levelLabelScanResult
	levelLabelScanRunning      bool
	unitLevelLookupResults     chan unitLevelUsageResult
	unitLevelLookupID          stingray.FileID
	unitLevelLookupResult      *unitLevelUsageResult
	unitLevelLookupLoading     bool
	unitLevelLookupOpen        bool
	unitLevelLookupCancel      context.CancelFunc
	unitGalleryDockID          imgui.ID
	unitGalleryDockConfigured  bool
	levelGalleryDockConfigured bool
	animationDockID            imgui.ID
	animationDockConfigured    bool
	stateMachineExplorerOpen   bool
	armorSet                   armorSetWindow
	isArmorSetOpen             bool
	stateMachineExplorer       *stateMachineExplorerState
	stringSearchDockConfigured bool
	unitComparisonAdded        map[stingray.FileID]struct{}
	unitComparisonRemoved      map[stingray.FileID]struct{}
	unitComparisonReady        bool
	unitComparisonGameData     *GameData
	unitComparisonOldPath      string
	comparisonAddedByType      map[stingray.Hash]map[stingray.FileID]struct{}
	comparisonRemovedByType    map[stingray.Hash]map[stingray.FileID]struct{}
	comparisonChangedByType    map[stingray.Hash]map[stingray.FileID]struct{}
	comparisonChangedPairs     map[stingray.Hash]map[stingray.FileID]stingray.FileID
	comparisonMatchResults     chan versionMatchResult
	comparisonMatchRunning     bool
	unitComparePath            string
	textureIDs                 []stingray.FileID
	textureIDsReady            bool
	textureGridScale           float32
	textureSort                int
	textureDescending          bool
	textureQuery               string
	texturePixelated           bool
	stringSearch               StringSearchState
	texture256px               bool

	gameFileSearchQuery      string
	filesSelectedForExport   map[stingray.FileID]struct{}
	allSelectedForExport     bool
	hashBulkImporter         HashBulkImporter
	gameFileTypeSearchQuery  string
	gameFileTypes            []widgets.FilterListSection[stingray.Hash]
	selectedGameFileTypes    map[stingray.Hash]struct{}
	archiveIDSearchQuery     string
	archiveIDs               []widgets.FilterListSection[stingray.Hash]
	selectedArchives         map[stingray.Hash]struct{}
	scrollToSelectedFile     bool
	browserJumpToFileID      stingray.FileID
	highlightedBrowserFileID stingray.FileID
	historyStack             []stingray.FileID
	historyStackIndex        int

	shouldSetupWindowFocus bool

	checkUpdatesOnStartupBGDone bool
	checkUpdatesOnStartupFGDone bool
	checkingForUpdates          bool
	checkUpdatesNewVersion      string // empty if unknown
	checkUpdatesDownloadURL     string
	checkUpdatesErr             error
	checkUpdatesLock            sync.Mutex
	unitAnimationScan           unitAnimationScanState

	exportDir                   string
	exportNotifyWhenDone        bool
	extractorConfig             appconfig.Config
	extractorConfigShowAdvanced bool
	extractorConfigPath         string
	extractorConfigSearchQuery  string

	logger *Logger

	downloadsDir               string
	ffmpegExtensionWidget      *widgets.Extension
	scriptsDistExtensionWidget *widgets.Extension
	prevFfmpegDownloaded       bool
	prevScriptsDistDownloaded  bool

	runner *exec.Runner

	popupManager *imutils.PopupManager

	isTypeFilterOpen    bool
	isArchiveFilterOpen bool
	isLogsOpen          bool

	toolsHashConverterState  *widgets.HashConverterState
	isToolsHashConverterOpen bool

	runUnitPreviewLoadingTest bool
	allUnits                  []stingray.Hash
	unitTestIdx               int
	unitTestErrors            map[stingray.Hash]error
	unitTestCooldown          int
	unitTestErrorsCopied      bool

	lastBrowserItemCopiedIndex int32
	lastBrowserItemCopiedTime  float64

	iconImage image.Image
	icon      *imutils.Image

	resetDockLayout bool
}

// Call Delete() when done.
// showErrorPopup should show an error popup without
// changing control flow, i.e. without exiting.
func newGUIApp(showErrorPopup func(error)) *guiApp {
	var extractorConfig appconfig.Config
	config.InitDefault(&extractorConfig)

	return &guiApp{
		showErrorPopup:             showErrorPopup,
		ctx:                        context.Background(),
		preferencesPath:            filepath.Join(xdg.DataHome, "filediver", "preferences.json"),
		audioSampleRate:            48000,
		filesSelectedForExport:     map[stingray.FileID]struct{}{},
		selectedGameFileTypes:      map[stingray.Hash]struct{}{},
		selectedArchives:           map[stingray.Hash]struct{}{},
		shouldSetupWindowFocus:     true,
		exportDir:                  filepath.Join(xdg.UserDirs.Download, "filediver_exports"),
		exportNotifyWhenDone:       true,
		extractorConfig:            extractorConfig,
		extractorConfigPath:        filepath.Join(xdg.DataHome, "filediver", "extractor_config.json"),
		logger:                     NewLogger(),
		downloadsDir:               filepath.Join(xdg.DataHome, "filediver"),
		runner:                     exec.NewRunner(),
		popupManager:               imutils.NewPopupManager(),
		toolsHashConverterState:    widgets.NewHashConverter(),
		lastBrowserItemCopiedIndex: -1,
		lastBrowserItemCopiedTime:  -math.MaxFloat64,
		stateMachineExplorerOpen:   true,
		isArmorSetOpen:             true,
		iconImage:                  appicons.Icon128Img(),
		runUnitPreviewLoadingTest:  false,
		allUnits:                   nil,
		unitTestIdx:                0,
		unitTestErrors:             make(map[stingray.Hash]error),
	}
}

func (a *guiApp) Delete() {
	if a.armorSet.gallery != nil {
		a.armorSet.gallery.Delete()
		a.armorSet.gallery = nil
	}
	a.unitAnimationScan.Lock()
	if a.unitAnimationScan.cancel != nil {
		a.unitAnimationScan.cancel()
		a.unitAnimationScan.cancel = nil
	}
	a.unitAnimationScan.Unlock()
	if a.unitLevelLookupCancel != nil {
		a.unitLevelLookupCancel()
	}
	if a.stateMachineExplorer != nil {
		a.stateMachineExplorer.cancelScan()
	}
	if a.icon != nil {
		a.icon.Delete()
	}
	if a.unitGallery != nil {
		a.unitGallery.Delete()
	}
	if a.levelGallery != nil {
		a.levelGallery.Delete()
	}
	if a.textureGallery != nil {
		a.textureGallery.Delete()
	}
	if a.olderTextureGallery != nil {
		a.olderTextureGallery.Delete()
	}
	if a.preview != nil {
		a.preview.Delete()
	}
	if a.currentPreview != nil && a.currentPreview != a.preview {
		a.currentPreview.Delete()
	}
	if a.olderBuildPreview != nil && a.olderBuildPreview != a.preview && a.olderBuildPreview != a.currentPreview {
		a.olderBuildPreview.Delete()
	}
}

func (a *guiApp) onInitWindow(state *imgui_wrapper.State) error {
	state.DarkWindowDecorations = true

	// Init audio
	{
		var readyChan chan struct{}
		var err error
		a.otoCtx, readyChan, err = oto.NewContext(&oto.NewContextOptions{
			SampleRate:   a.audioSampleRate,
			ChannelCount: 2,
			Format:       oto.FormatFloat32LE,
		})
		if err != nil {
			return fmt.Errorf("creating audio context: %w", err)
		}
		<-readyChan
	}
	if err := a.otoCtx.Err(); err != nil {
		return fmt.Errorf("audio context: %w", err)
	}

	// Init extension info
	{
		ffmpegAsset := github.ReleaseAsset{}
		var ffmpegVersion string
		if runtime.GOARCH != "amd64" {
			a.showErrorPopup(fmt.Errorf("unsupported CPU architecture: %v", runtime.GOARCH))
		}
		if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
			a.showErrorPopup(fmt.Errorf("unsupported operating system: %v", runtime.GOOS))
		}
		if runtime.GOOS == "windows" {
			ffmpegAsset.User = "xypwn"
			ffmpegAsset.Repo = "ffmpeg-bink2-builds"
			ffmpegVersion = "v0.0.3"
			ffmpegAsset.Filename = "ffmpeg-bink2-windows.zip"
		} else {
			ffmpegAsset.User = "BtbN"
			ffmpegAsset.Repo = "FFmpeg-Builds"
			ffmpegVersion = "latest"
			ffmpegAsset.Filename = "ffmpeg-master-latest-linux64-gpl.tar.xz"
		}
		ffmpegInfo, err := ffmpegAsset.FetchInfo(ffmpegVersion)
		if err != nil {
			a.showErrorPopup(err)
		}

		scriptsDistAsset := github.ReleaseAsset{}
		scriptsDistAsset.User = "mrbluehi"
		scriptsDistAsset.Repo = "evil-ai-filediver"
		if runtime.GOOS == "windows" {
			scriptsDistAsset.Filename = "scripts-dist-windows.zip"
		} else {
			scriptsDistAsset.Filename = "scripts-dist-linux.tar.xz"
		}
		var scriptsDistInfo github.ReleaseAssetInfo
		scriptsDistVersion := version
		if scriptsDistVersion == "v0.0.0" {
			scriptsDistVersion = ""
		}
		scriptsDistInfo, err = scriptsDistAsset.FetchInfo(scriptsDistVersion)
		if err != nil {
			a.showErrorPopup(err)
		}

		a.ffmpegExtensionWidget = widgets.NewExtension(a.ctx, a.downloadsDir, "ffmpeg", ffmpegInfo, true)
		a.scriptsDistExtensionWidget = widgets.NewExtension(a.ctx, a.downloadsDir, "filediver-scripts", scriptsDistInfo, true)
	}

	// Delete legacy extensions
	if err := detectAndMaybeDeleteLegacyExtensions(a.downloadsDir); err != nil {
		a.showErrorPopup(err)
	}

	// Delete old executable
	if err := func() error {
		oldExePtrPath := filepath.Join(a.downloadsDir, "old_executable")
		if oldExe, err := os.ReadFile(oldExePtrPath); err == nil {
			if err := os.Remove(string(oldExe)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.Remove(oldExePtrPath); err != nil {
				return err
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}(); err != nil {
		a.showErrorPopup(err)
	}

	// Detect extensions
	a.prevFfmpegDownloaded = a.ffmpegExtensionWidget.HaveRequestedVersion()
	a.prevScriptsDistDownloaded = a.scriptsDistExtensionWidget.HaveRequestedVersion()
	a.redetectRunnerProgs()
	if version != "" {
		a.popupManager.Open["Missing or out of date extensions"] =
			!a.ffmpegExtensionWidget.HaveRequestedVersion() || !a.scriptsDistExtensionWidget.HaveRequestedVersion()
		a.popupManager.Open["Welcome to Filediver GUI"] = true
	}

	// Init preferences
	{
		a.defaultPreferences = Preferences{
			AutoCheckForUpdates:            true,
			PreviewVideoVerticalResolution: 720,
			UnitPreviewAngle:               [2]float32{math.Pi, 0},
		}

		a.defaultPreferences.GUIScale = state.GUIScale
		a.defaultPreferences.TargetFPS = state.FrameRate

		a.preferences = a.defaultPreferences
		if err := a.preferences.Load(a.preferencesPath); err != nil {
			return fmt.Errorf("loading preferences: %w", err)
		}
		a.unitComparePath = a.preferences.UnitComparePath
		state.GUIScale = a.preferences.GUIScale
		state.FrameRate = a.preferences.TargetFPS
	}

	// Load extractor config
	{
		if err := a.extractorConfig.Load(a.extractorConfigPath); err != nil {
			return fmt.Errorf("loading extractor config: %w", err)
		}
	}

	// Check for app updates
	if a.preferences.AutoCheckForUpdates && version != "" {
		a.goCheckForUpdates(true)
	}

	// Load game files
	a.gameDataLoad.GoLoadGameData(a.ctx, a.extractorConfig.Gamedir)
	//a.gameData = &GameData{App: &app.App{DataDir: &stingray.DataDir{}}}

	return nil
}

func (a *guiApp) onPreDraw(state *imgui_wrapper.State) error {
	imutils.GlobalScale = state.GUIScale
	if a.gameData != nil && a.preview == nil {
		getOverride := func(fileId stingray.FileID) stingray.FileID {
			if a.gameData != nil && a.gameData.AssetOverrides != nil && *a.gameData.AssetOverrides != nil {
				if val, contains := (*a.gameData.AssetOverrides)[fileId]; contains {
					return val
				}
			}
			return fileId
		}
		var err error
		a.preview, err = previews.NewAutoPreview(
			a.otoCtx, a.audioSampleRate,
			a.gameData.Hashes,
			a.gameData.ThinHashes,
			a.gameData.DataDir.IsHelldivers1,
			func(allowOverride bool) func(id stingray.FileID, typ stingray.DataType) (data []byte, exists bool, err error) {
				return func(id stingray.FileID, typ stingray.DataType) (data []byte, exists bool, err error) {
					override := getOverride(id)
					if allowOverride {
						id = override
					}
					data, err = a.gameData.DataDir.Read(id, typ)
					if err == stingray.ErrFileNotExist || err == stingray.ErrFileDataTypeNotExist {
						return nil, false, nil
					}
					if err != nil {
						return nil, false, err
					}
					return data, true, nil
				}
			},
			getOverride,
			a.runner,
			previews.ExtractorPlanetParameters{
				Name:                 &a.extractorConfig.Planet.Name,
				UseCity:              &a.extractorConfig.Planet.City,
				UpdateAssetOverrides: a.gameData.UpdateAssetOverrides,
			},
			previews.ExtractorArmorParameters{
				ArmorSets:        a.gameData.ArmorSets,
				SelectedArchives: func() []stingray.Hash { return slices.SortedFunc(maps.Keys(a.selectedArchives), stingray.Hash.Cmp) },
			},
			func(unitID stingray.FileID, materialName stingray.ThinHash) (stingray.FileID, []byte, bool, error) {
				materialID, data, err := a.gameData.DataDir.ResolveHelldivers1Material(unitID, materialName)
				if err == stingray.ErrFileNotExist {
					return stingray.FileID{}, nil, false, nil
				}
				return materialID, data, err == nil, err
			},
		)
		if err != nil {
			return fmt.Errorf("creating preview: %w", err)
		}
		animationResources := make([]stingray.FileID, 0)
		for id := range a.gameData.DataDir.Files {
			if id.Type == stingray.Sum("animation") {
				animationResources = append(animationResources, id)
			}
		}
		slices.SortFunc(animationResources, stingray.FileID.Cmp)
		a.preview.SetAnimationResources(animationResources)
	}
	if a.preview != nil && a.unitGallery == nil {
		var err error
		a.unitGallery, err = a.preview.NewUnitGallery()
		if err != nil {
			return fmt.Errorf("creating unit gallery: %w", err)
		}
		cacheSource := filepath.Clean(a.gameData.DataDir.Path)
		if build := a.gameData.GameBuildInfo; build != nil {
			cacheSource += "\n" + build.Combined + "\n" + build.Commit + "\n" + build.Hash
		}
		cacheIdentity := sha256.Sum256([]byte(cacheSource))
		cacheDir := filepath.Join(xdg.CacheHome, "filediver", "unit-thumbnails-v1", hex.EncodeToString(cacheIdentity[:]))
		migratedAngle, err := a.unitGallery.ConfigureUnitGrid(cacheDir, a.preferences.UnitPreviewAngle, a.preferences.UnitPreviewAngleSet)
		if err != nil {
			a.unitGallery.Delete()
			a.unitGallery = nil
			return err
		}
		if migratedAngle {
			a.preferences.UnitPreviewAngle = a.unitGallery.UnitPreviewAngle()
			a.preferences.UnitPreviewAngleSet = true
			if err := a.preferences.Save(a.preferencesPath); err != nil {
				a.unitGallery.Delete()
				a.unitGallery = nil
				return fmt.Errorf("saving migrated Units preview angle: %w", err)
			}
		}
		if a.preferences.UnitPreviewAngleSet || migratedAngle {
			// The global preference is now the source of truth. Remove any old
			// per-build copy so Reset Preferences cannot resurrect a stale angle.
			_ = os.Remove(filepath.Join(cacheDir, "unit-preview-angle.json"))
		}
	}
	if a.preview != nil && a.levelGallery == nil {
		var err error
		a.levelGallery, err = a.preview.NewLevelGallery()
		if err != nil {
			return fmt.Errorf("creating level gallery: %w", err)
		}
		cacheSource := filepath.Clean(a.gameData.DataDir.Path)
		if build := a.gameData.GameBuildInfo; build != nil {
			cacheSource += "\n" + build.Combined + "\n" + build.Commit + "\n" + build.Hash
		}
		cacheIdentity := sha256.Sum256([]byte(cacheSource))
		cacheDir := filepath.Join(xdg.CacheHome, "filediver", "level-thumbnails", hex.EncodeToString(cacheIdentity[:]))
		if err := a.levelGallery.ConfigureLevelGrid(cacheDir); err != nil {
			a.levelGallery.Delete()
			a.levelGallery = nil
			return err
		}
	}
	if a.preview != nil && a.textureGallery == nil {
		a.textureGallery = a.preview.NewTextureGallery()
	}
	if a.currentPreview == nil && a.preview != nil {
		a.currentPreview = a.preview
	}
	return nil
}

func (a *guiApp) onDraw(state *imgui_wrapper.State) {
	// Set up dock space and arrange window nodes
	viewport := imgui.MainViewport()
	dockSpacePos := viewport.Pos()
	dockSpaceSize := viewport.Size()
	imgui.SetNextWindowPos(dockSpacePos)
	imgui.SetNextWindowSize(dockSpaceSize)
	const dockSpaceWindowFlags = imgui.WindowFlagsNoTitleBar | imgui.WindowFlagsNoCollapse |
		imgui.WindowFlagsNoResize | imgui.WindowFlagsNoMove | imgui.WindowFlagsNoDocking |
		imgui.WindowFlagsNoBringToFrontOnFocus | imgui.WindowFlagsNoNavFocus | imgui.WindowFlagsMenuBar //| imgui.WindowFlagsNoBackground
	imgui.PushStyleVarVec2(imgui.StyleVarWindowPadding, imgui.NewVec2(0, 0))
	if imgui.BeginV("##MainWindow", nil, dockSpaceWindowFlags) {
		imgui.PopStyleVar()
		a.drawMenuBar()
		id := imgui.IDStr("MainWindow")
		if imgui.InternalDockBuilderGetNode(id).CData == nil || a.resetDockLayout {
			imgui.InternalDockBuilderAddNodeV(id, imgui.DockNodeFlags(imgui.DockNodeFlagsDockSpace))
			imgui.InternalDockBuilderSetNodeSize(id, dockSpaceSize)
			var leftID, topLeftID, bottomLeftID, rightID, topRightID, centerID, bottomRightID, sideBarID imgui.ID
			imgui.InternalDockBuilderSplitNode(id, imgui.DirLeft, 0.25, &leftID, &rightID)
			imgui.InternalDockBuilderSplitNode(leftID, imgui.DirDown, 0.4, &bottomLeftID, &topLeftID)
			imgui.InternalDockBuilderSplitNode(rightID, imgui.DirRight, 0.25, &sideBarID, &centerID)
			imgui.InternalDockBuilderSplitNode(centerID, imgui.DirDown, 0.4, &bottomRightID, &topRightID)
			imgui.InternalDockBuilderDockWindow(fnt.I.ViewList+" Browser", topLeftID)
			imgui.InternalDockBuilderDockWindow(fnt.I.FileExport+" Export", bottomLeftID)
			imgui.InternalDockBuilderDockWindow(fnt.I.SettingsApplications+" Extractor config", bottomLeftID)
			imgui.InternalDockBuilderDockWindow(fnt.I.Preview+" Preview", topRightID)
			imgui.InternalDockBuilderDockWindow("Animation", topRightID)
			imgui.InternalDockBuilderDockWindow("State Machines", topRightID)
			imgui.InternalDockBuilderDockWindow(fnt.I.Tag+" Metadata", topRightID)
			imgui.InternalDockBuilderDockWindow(fnt.I.ViewList+" Units", topRightID)
			imgui.InternalDockBuilderDockWindow("Levels", topRightID)
			imgui.InternalDockBuilderDockWindow("Compare", topRightID)
			imgui.InternalDockBuilderDockWindow(fnt.I.Image+" Textures", topRightID)
			imgui.InternalDockBuilderDockWindow("String Search", topRightID)
			imgui.InternalDockBuilderDockWindow(fnt.I.DisplaySettings+" Material Settings", bottomRightID)
			imgui.InternalDockBuilderDockWindow(fnt.I.FolderEye+" Visibility Mask Selection", sideBarID)
			imgui.InternalDockBuilderDockWindow(fnt.I.Settings+" Tree View", sideBarID)
			imgui.InternalDockBuilderFinish(id)
			a.resetDockLayout = false
		}
		winClass := imgui.NewWindowClass() // passing nil as window class causes a nil pointer dereference (probably an error in the binding generation)
		imgui.DockSpaceV(id, imgui.NewVec2(0, 0), 0, winClass)
	} else {
		imgui.PopStyleVar()
	}
	imgui.End()

	if a.isToolsHashConverterOpen {
		if imgui.BeginV("Hash Converter", &a.isToolsHashConverterOpen, imgui.WindowFlagsAlwaysAutoResize) {
			widgets.DrawHashConverter(a.toolsHashConverterState)
		}
		imgui.End()
	}

	a.drawBrowserWindow()
	a.drawTypeFilterWindow()
	a.drawArchiveFilterWindow()
	a.drawExportWindow()
	a.drawExtractorConfigWindow()
	a.drawLogWindow()
	a.drawPreviewWindow(state)
	a.drawAnimationWindow()
	a.drawStateMachineExplorerWindow()
	a.drawArmorSetWindow()
	a.drawMetadataWindow()
	a.drawUnitGalleryWindow()
	a.drawLevelGalleryWindow()
	a.drawUnitLevelUsageWindow()
	a.drawUnitVersionCompareWindow()
	a.drawTextureGalleryWindow()
	a.drawStringSearchWindow()
	a.drawSettingsWindow()

	if a.shouldSetupWindowFocus {
		imgui.SetWindowFocusStr(fnt.I.Preview + " Preview")
		a.shouldSetupWindowFocus = false
	}

	// drawXXXPopup functions use an
	// imutils.PopupManager, meaning
	// the popups will appear in the
	// order they're drawn.
	a.drawCheckForUpdatesPopup()
	a.drawWelcomePopup()
	a.drawExtensionsWarningPopup()
	a.drawExtensionsPopup()
	a.drawPreferencesPopup(state)
	a.drawAboutPopup()
	a.drawUnitPreviewTestPopup()
}

func (a *guiApp) goCheckForUpdates(isOnStartup bool) {
	a.checkUpdatesLock.Lock()
	defer a.checkUpdatesLock.Unlock()
	if a.checkingForUpdates {
		return
	}
	a.checkingForUpdates = true
	a.checkUpdatesNewVersion = ""
	a.checkUpdatesDownloadURL = ""
	a.checkUpdatesErr = nil
	go func() {
		ver, url, err := getNewestVersion()
		if err == nil { // check for 404 response -> specific binary not yet available -> build not done yet
			resp, rErr := http.Get(url)
			if rErr != nil {
				err = rErr
			} else {
				resp.Body.Close()
				if resp.StatusCode == 404 {
					ver, url = version, ""
				}
			}
		}

		a.checkUpdatesLock.Lock()
		if isOnStartup {
			a.checkUpdatesOnStartupBGDone = true
		}
		a.checkingForUpdates = false
		a.checkUpdatesNewVersion, a.checkUpdatesDownloadURL, a.checkUpdatesErr = ver, url, err
		a.checkUpdatesLock.Unlock()
	}()
}

func (a *guiApp) redetectRunnerProgs() {
	var ffmpegBasePath string
	switch runtime.GOOS {
	case "windows":
		ffmpegBasePath = a.ffmpegExtensionWidget.Dir()
	case "linux":
		ffmpegBasePath = filepath.Join(a.ffmpegExtensionWidget.Dir(), "bin")
	}
	ffmpegPath := filepath.Join(ffmpegBasePath, "ffmpeg")
	ffmpegArgs := []string{"-y", "-hide_banner", "-loglevel", "error"}
	if !a.runner.Add(ffmpegPath, ffmpegArgs...) {
		// Try to use a local FFmpeg instance if the extension isn't installed
		a.runner.Add("ffmpeg", ffmpegArgs...)
	}
	ffprobePath := filepath.Join(ffmpegBasePath, "ffprobe")
	ffprobeArgs := []string{"-hide_banner", "-loglevel", "error"}
	if !a.runner.Add(ffprobePath, ffprobeArgs...) {
		// Try to use a local FFprobe instance if the extension isn't installed
		a.runner.Add("ffprobe", ffprobeArgs...)
	}
	blenderImporterArgs := []string{filepath.Join(a.scriptsDistExtensionWidget.Dir(), "hd2_accurate_blender_importer", "hd2_accurate_blender_importer")}
	if value := os.Getenv("FILEDIVER_BLENDER_IMPORTER_COMMAND"); value != "" {
		// Custom blender importer path (intended for dev only)
		if args, err := shellwords.Parse(value); err == nil && len(args) >= 1 {
			a.logger.Infof("Using custom blender importer command: %v", args)
			blenderImporterArgs = args
		}
	}
	a.runner.AddWithName("hd2_accurate_blender_importer", blenderImporterArgs[0], blenderImporterArgs[1:]...)
}

// We use state tracking for allSelectedForExport
// since recalculating each frame would eat up
// ~5ms (on a good PC!) for nothing.
// This function should be used whenever
// the status of all items being selected may have
// changed.
func (a *guiApp) calcAllSelectedForExport() bool {
	if a.gameData == nil || len(a.gameData.SortedSearchResultFileIDs) == 0 {
		return false
	}
	for _, id := range a.gameData.SortedSearchResultFileIDs {
		_, sel := a.filesSelectedForExport[id]
		if !sel {
			return false
		}
	}
	return true
}

func (a *guiApp) historyPush(prevItem, newItem stingray.FileID) {
	a.historyStack = a.historyStack[:a.historyStackIndex]
	if (prevItem != stingray.FileID{}) {
		a.historyStack = append(a.historyStack, prevItem)
	}
	a.historyStack = append(a.historyStack, newItem)
	a.historyStackIndex = len(a.historyStack) - 1

	const limit = 128
	if len(a.historyStack) > limit {
		cut := len(a.historyStack) - limit
		a.historyStack = a.historyStack[cut:]
		a.historyStackIndex -= cut
	}
}

// newItem is only valid if changed is true.
func (a *guiApp) drawHistoryButtons() (newItem stingray.FileID, changed bool) {
	imgui.BeginDisabledV(len(a.historyStack) == 0)
	imgui.BeginDisabledV(a.historyStackIndex == 0)
	imgui.SetNextItemShortcut(imgui.KeyChord(imgui.ModAlt | imgui.KeyLeftArrow))
	if imgui.Button(fnt.I.ArrowBack) {
		a.historyStackIndex--
		newItem = a.historyStack[a.historyStackIndex]
		changed = true
	}
	imgui.SetItemTooltip("Jump to previous item in history (Alt+Left-arrow)")
	imgui.EndDisabled()
	imgui.SameLineV(0, imutils.S(4))
	imgui.BeginDisabledV(a.historyStackIndex == len(a.historyStack)-1)
	imgui.SetNextItemShortcut(imgui.KeyChord(imgui.ModAlt | imgui.KeyRightArrow))
	if imgui.Button(fnt.I.ArrowForward) {
		a.historyStackIndex++
		newItem = a.historyStack[a.historyStackIndex]
		changed = true
	}
	imgui.SetItemTooltip("Jump to next item in history (Alt+Right-arrow)")
	imgui.EndDisabled()
	imgui.EndDisabled()
	return
}

func (a *guiApp) drawMenuBar() {
	if imgui.BeginMenuBar() {
		if imgui.BeginMenu("Help") {
			if imgui.MenuItemBool(fnt.I.Info + " Info") {
				a.popupManager.Open["Info"] = true
			}
			imgui.EndMenu()
		}
		if imgui.BeginMenu("Tools") {
			imgui.MenuItemBoolPtrV(fnt.I.TableConvert+" Hash Converter", "", &a.isToolsHashConverterOpen, true)
			imgui.MenuItemBoolPtrV("State Machine Explorer", "", &a.stateMachineExplorerOpen, true)
			imgui.MenuItemBoolPtrV("Armor Set", "", &a.isArmorSetOpen, true)
			if imgui.MenuItemBool(fnt.I.LabResearch + " Run Unit Preview Test") {
				a.allUnits = slices.Collect(func(yield func(stingray.Hash) bool) {
					for file := range a.gameData.DataDir.Files {
						if file.Type == stingray.Sum("unit") && !yield(file.Name) {
							return
						}
					}
				})
				a.unitTestIdx = 0
				a.unitTestErrors = make(map[stingray.Hash]error)
				a.runUnitPreviewLoadingTest = true
				a.popupManager.Open["Running Unit Preview Load Test"] = true
			}
			imgui.EndMenu()
		}
		if imgui.BeginMenu("Settings") {
			if imgui.MenuItemBool(fnt.I.Extension + " Extensions") {
				a.popupManager.Open["Extensions"] = true
			}
			if imgui.MenuItemBool(fnt.I.Sync + " Check for updates") {
				a.goCheckForUpdates(false)
				a.popupManager.Open["Check for updates"] = true
			}
			if imgui.MenuItemBool(fnt.I.Settings + " Preferences") {
				a.popupManager.Open["Preferences"] = true
			}
			var debug bool
			if a.preview != nil {
				debug = a.preview.Debug()
			}
			if imgui.MenuItemBoolPtr(fnt.I.BugReport+" Preview Debugging", "", &debug) && a.preview != nil {
				a.preview.SetDebug(debug)
			}
			imgui.EndMenu()
		}
		imgui.EndMenuBar()
	}
}

func (a *guiApp) drawBrowserWindow() {
	if imgui.Begin(fnt.I.ViewList + " Browser") {
		if a.gameData == nil {
			a.gameDataLoad.Lock()
			if a.gameDataLoad.Done {
				if a.gameDataLoad.Err == nil {
					if a.gameData == nil {
						a.gameData = a.gameDataLoad.Result
						types := make(map[stingray.Hash]struct{})
						for id := range a.gameData.DataDir.Files {
							types[id.Type] = struct{}{}
						}
						a.gameFileTypes = []widgets.FilterListSection[stingray.Hash]{
							{Title: "Previewable and exportable"},
							{Title: "Just exportable"},
							{Title: "Not exportable"},
						}
						for _, typ := range slices.SortedFunc(maps.Keys(types), stingray.Hash.Cmp) {
							var sectionIdx int
							switch typ {
							case // previewable and exportable
								stingray.Sum("bik"),
								stingray.Sum("bk2"),
								stingray.Sum("texture"),
								stingray.Sum("material"),
								stingray.Sum("wwise_bank"),
								stingray.Sum("wwise_stream"),
								stingray.Sum("unit"),
								stingray.Sum("prefab"),
								stingray.Sum("level"),
								stingray.Sum("speedtree"),
								stingray.Sum("strings"),
								stingray.Sum("xaml"),
								stingray.Sum("ttf"),
								stingray.Sum("otf"):
								sectionIdx = 0
							default:
								typName := a.gameData.LookupHash(typ)
								if appconfig.Extractable[typName] {
									// just exportable
									sectionIdx = 1
								} else {
									// not exportable
									sectionIdx = 2
								}
							}
							a.gameFileTypes[sectionIdx].Items = append(a.gameFileTypes[sectionIdx].Items, typ)
						}
						a.archiveIDs = []widgets.FilterListSection[stingray.Hash]{
							{
								Items: slices.SortedFunc(maps.Keys(a.gameData.DataDir.Archives), func(h1, h2 stingray.Hash) int {
									return strings.Compare(a.gameData.LookupHash(h1), a.gameData.LookupHash(h2))
								}),
							},
						}
						a.gameData.UpdateAssetOverrides(a.extractorConfig.Planet.Name, a.extractorConfig.Planet.City)
					}
				} else {
					imutils.TextError(a.gameDataLoad.Err)
				}
			} else {
				progress := a.gameDataLoad.Progress
				if progress != 1 {
					imutils.Textf(fnt.I.HourglassTop + " Loading game data...")
				} else {
					imutils.Textf(fnt.I.HourglassTop + " Processing game data...")
					progress = -float32(imgui.Time())
				}
				imgui.ProgressBar(progress)
			}
			a.gameDataLoad.Unlock()
		} else {
			searchBarWidth := imgui.ContentRegionAvail().X
			{
				style := imgui.CurrentStyle()
				imgui.CalcItemWidth()
				searchBarWidth -= imgui.CalcTextSize(fnt.I.Help).X + imgui.CalcTextSize(fnt.I.ConvertToText).X +
					2*style.ItemSpacing().X +
					2*2*style.FramePadding().X
			}
			imgui.SetNextItemWidth(searchBarWidth)
			if imgui.Shortcut(imgui.KeyChord(imgui.ModCtrl | imgui.KeyF)) {
				imgui.SetKeyboardFocusHere()
			}
			if imgui.InputTextWithHint("##SearchName", fnt.I.Search+" Filter by file name...", &a.gameFileSearchQuery, 0, nil) {
				a.gameData.UpdateSearchQuery(a.gameFileSearchQuery, a.selectedGameFileTypes, a.selectedArchives)
				a.allSelectedForExport = a.calcAllSelectedForExport()
				a.scrollToSelectedFile = true
			}
			searchInputTextData := imgui.CurrentContext().LastItemData()
			imgui.SetItemTooltip("Filter by file name (Ctrl+F)")
			imgui.SameLine()
			var hashBulkImporterOpened bool
			imgui.SetNextItemShortcut(imgui.KeyChord(imgui.ModCtrl | imgui.KeyI))
			if imgui.Button(fnt.I.ConvertToText) {
				hashBulkImporterOpened = true
				imgui.OpenPopupStr("##BulkImportHashes")
			}
			imgui.SetItemTooltip("Bulk import hashes (Ctrl+I) [auto-fills with clipboard]")
			if imgui.BeginPopup("##BulkImportHashes") {
				if a.hashBulkImporter.Draw(hashBulkImporterOpened) {
					var expr strings.Builder
					expr.WriteString("? name in [")
					for _, h := range a.hashBulkImporter.Hashes {
						expr.WriteString("\"")
						expr.WriteString(h.String())
						expr.WriteString("\",")
					}
					expr.WriteString("]")
					a.gameFileSearchQuery = expr.String()

					clear(a.selectedGameFileTypes)
					clear(a.selectedArchives)
					a.gameData.UpdateSearchQuery(a.gameFileSearchQuery, a.selectedGameFileTypes, a.selectedArchives)
					a.allSelectedForExport = a.calcAllSelectedForExport()
					a.scrollToSelectedFile = true

					imgui.CloseCurrentPopup()
				}
				imgui.EndPopup()
			}
			imgui.SameLine()
			if imgui.Button(fnt.I.Help) {
				imgui.OpenPopupStr("##MetadataSearchHelp")
			}
			imgui.SetItemTooltip("Metadata search help")
			if imgui.BeginPopup("##MetadataSearchHelp") {
				DrawMetadataSearchHelp()
				imgui.EndPopup()
			}
			if a.gameData.FilterExprErr != nil {
				itm := searchInputTextData
				bottomLeft := imgui.NewVec2(itm.Rect().Min.X, itm.Rect().Max.Y)
				width := itm.Rect().Max.X - itm.Rect().Min.X
				flags := imgui.ChildFlagsFrameStyle | imgui.ChildFlagsAutoResizeY | imgui.ChildFlagsAlwaysAutoResize
				imgui.SetNextWindowPos(bottomLeft)
				if imgui.BeginChildStrV("FilterExprErr", imgui.NewVec2(width, 0), flags, 0) {
					imgui.PushFont(imgui_wrapper.FontMono(), 0)
					imutils.TextError(a.gameData.FilterExprErr)
					imgui.PopFont()
				}
				imgui.EndChild()
			}

			var newActiveFileID stingray.FileID
			if a.preview != nil {
				newActiveFileID = a.preview.ActiveID()
			}
			if jumpToFile, ok := widgets.PopGamefileLinkFile(); ok {
				newActiveFileID = jumpToFile
				a.scrollToSelectedFile = true
			}
			if a.browserJumpToFileID != (stingray.FileID{}) {
				// A saved browser query or type/archive filter can hide the target.
				// Clear those filters only when needed so the jump always lands on
				// a visible row, while ordinary audio jumps preserve the current view.
				if !slices.Contains(a.gameData.SortedSearchResultFileIDs, a.browserJumpToFileID) {
					a.gameFileSearchQuery = ""
					clear(a.selectedGameFileTypes)
					clear(a.selectedArchives)
					a.gameData.UpdateSearchQuery("", nil, nil)
				}
				newActiveFileID = a.browserJumpToFileID
				a.highlightedBrowserFileID = a.browserJumpToFileID
				a.browserJumpToFileID = stingray.FileID{}
				a.scrollToSelectedFile = true
			}

			noPushToHistory := false // item selected through a history button
			if newID, ok := a.drawHistoryButtons(); ok {
				newActiveFileID = newID
				a.scrollToSelectedFile = true
				noPushToHistory = true
			}
			imgui.SameLine()
			widgets.FilterListButton("Types", &a.isTypeFilterOpen, a.selectedGameFileTypes)
			imgui.SameLine()
			widgets.FilterListButton("Archives", &a.isArchiveFilterOpen, a.selectedArchives)

			imgui.BeginDisabledV(len(a.gameData.SortedSearchResultFileIDs) == 0)
			imgui.PushItemFlag(imgui.ItemFlags(imgui.ItemFlagsMixedValue),
				!a.allSelectedForExport && len(a.filesSelectedForExport) > 0)
			if imgui.Checkbox("Select all for export", &a.allSelectedForExport) {
				if a.allSelectedForExport {
					for _, id := range a.gameData.SortedSearchResultFileIDs {
						a.filesSelectedForExport[id] = struct{}{}
					}
				} else {
					for _, id := range a.gameData.SortedSearchResultFileIDs {
						delete(a.filesSelectedForExport, id)
					}
				}
			}
			imgui.PopItemFlag()
			imgui.EndDisabled()
			if len(a.gameData.SortedSearchResultFileIDs) != 0 {
				if a.allSelectedForExport {
					imgui.SetItemTooltip("Deselect all currently visible files for export")
				} else {
					imgui.SetItemTooltip("Select all currently visible files for export")
				}
			}
			{
				size := imgui.ContentRegionAvail()
				size.Y -= imgui.TextLineHeightWithSpacing()
				imgui.SetNextWindowSize(size)
			}
			const tableFlags = imgui.TableFlagsResizable | imgui.TableFlagsBorders | imgui.TableFlagsScrollY | imgui.TableFlagsRowBg
			if imgui.BeginTableV("##Game Files", 3, tableFlags, imgui.NewVec2(0, 0), 0) {
				imgui.TableSetupColumnV(fnt.I.FileExport, imgui.TableColumnFlagsWidthFixed|imgui.TableColumnFlagsNoResize, imgui.FrameHeight(), 0)
				imgui.TableSetupColumnV("Name", imgui.TableColumnFlagsWidthStretch, 3, 0)
				imgui.TableSetupColumnV("Type", imgui.TableColumnFlagsWidthStretch, 1, 0)
				imgui.TableSetupScrollFreeze(0, 1)
				imgui.TableHeadersRow()

				clipper := imgui.NewListClipper()
				clipper.Begin(int32(len(a.gameData.SortedSearchResultFileIDs)))
				if a.scrollToSelectedFile {
					if i := slices.Index(a.gameData.SortedSearchResultFileIDs, newActiveFileID); i != -1 {
						clipper.IncludeItemByIndex(int32(i))
					}
				}
				for clipper.Step() {
					for row := clipper.DisplayStart(); row < clipper.DisplayEnd(); row++ {
						id := a.gameData.SortedSearchResultFileIDs[row]
						imgui.PushIDStr(id.Name.String() + id.Type.String())

						// Kind of hacky way to not add to checkbox's tooltip
						ttExport := false

						toggleExport := func(id stingray.FileID) {
							_, export := a.filesSelectedForExport[id]
							if export {
								delete(a.filesSelectedForExport, id)
							} else {
								a.filesSelectedForExport[id] = struct{}{}
							}
							a.allSelectedForExport = a.calcAllSelectedForExport()
						}

						imgui.TableNextColumn()
						imgui.PushItemFlag(imgui.ItemFlagsNoNav, true)
						_, export := a.filesSelectedForExport[id]
						if imgui.Checkbox("", &export) {
							toggleExport(id)
						}
						imgui.PopItemFlag()
						if imgui.IsItemHovered() {
							ttExport = true
							if export {
								imgui.SetTooltip("Deselect for export")
							} else {
								imgui.SetTooltip("Select for export")
							}
						}

						imgui.TableNextColumn()
						highlighted := id == a.highlightedBrowserFileID
						if highlighted {
							imgui.PushStyleColorVec4(imgui.ColHeader, imgui.NewVec4(0.72, 0.56, 0.08, 0.92))
							imgui.PushStyleColorVec4(imgui.ColHeaderHovered, imgui.NewVec4(0.88, 0.69, 0.10, 0.96))
							imgui.PushStyleColorVec4(imgui.ColHeaderActive, imgui.NewVec4(1.0, 0.78, 0.12, 1.0))
						}
						selected := imgui.SelectableBoolV(
							a.gameData.DisplayFileName(id),
							id == newActiveFileID,
							imgui.SelectableFlagsSpanAllColumns|imgui.SelectableFlags(imgui.SelectableFlagsSelectOnNav),
							imgui.NewVec2(0, 0),
						) || (a.scrollToSelectedFile && id == newActiveFileID)
						if highlighted {
							imgui.PopStyleColorV(3)
						}
						copied := false
						if id == newActiveFileID && imgui.Shortcut(imgui.KeyChord(imgui.ModCtrl|imgui.KeyC)) {
							copied = true
						}
						if id == newActiveFileID && imgui.Shortcut(imgui.KeyChord(imgui.KeySpace)) {
							toggleExport(id)
						}
						if imgui.IsItemClickedV(imgui.MouseButtonRight) {
							copied = true
						}
						if copied {
							a.lastBrowserItemCopiedIndex = row
							a.lastBrowserItemCopiedTime = imgui.Time()
							imgui.SetClipboardText(id.Name.String())
						}
						if !ttExport && imgui.IsItemHovered() {
							var text strings.Builder
							if a.lastBrowserItemCopiedIndex == row &&
								imgui.Time()-a.lastBrowserItemCopiedTime < 1 {
								fmt.Fprintf(&text, "%v Copied '%v'\n", fnt.I.Check, id.Name.String())
							} else {
								if inferred, ok := a.gameData.InferredUnitNames[id.Name]; id.Type == stingray.Sum("unit") && ok {
									fmt.Fprintf(&text, "Name: %v (inferred from %v), hash=%v\n", inferred.Display, inferred.Source, id.Name)
								} else if name, ok := a.gameData.Hashes[id.Name]; ok {
									fmt.Fprintf(&text, "Name: %v, hash=%v\n", name, id.Name)
								} else {
									fmt.Fprintf(&text, "Name: hash=%v\n", id.Name)
								}
								fmt.Fprintf(&text, "Type: %v, hash=%v\n", a.gameData.LookupHash(id.Type), id.Type)
								fmt.Fprintf(&text, "%v Left-click to preview\n", fnt.I.Preview)
								fmt.Fprintf(&text, "%v Right-click or Ctrl+C to copy name hash to clipboard\n", fnt.I.ContentCopy)
								fmt.Fprintf(&text, "%v Down/up arrow keys to select next/previous\n", fnt.I.UnfoldMore)
								fmt.Fprintf(&text, "%v Use the checkbox or space key to select/deselect for export\n", fnt.I.FileExport)
							}
							imgui.SetTooltip(text.String())
						}
						imgui.TableNextColumn()

						imgui.TextUnformatted(a.gameData.LookupHash(id.Type))
						imgui.PopID()

						if selected {
							if id != a.highlightedBrowserFileID && imgui.IsItemClickedV(imgui.MouseButtonLeft) {
								a.highlightedBrowserFileID = stingray.FileID{}
							}
							newActiveFileID = id
							if a.scrollToSelectedFile {
								imgui.SetScrollHereY()
								// BUG: When using nav (tab/arrow keys) to navigate after
								// a scrollToNewActiveFileID frame, the nav thinks it's at
								// the last item that was manually selected, not the one
								// that was automatically scrolled to.
							}
						}
					}
				}

				imgui.EndTable()
			}

			if a.preview != nil && newActiveFileID != a.preview.ActiveID() {
				if !noPushToHistory {
					a.historyPush(a.preview.ActiveID(), newActiveFileID)
				}
				a.activateCurrentPreview()
				a.preview.LoadFile(a.ctx, newActiveFileID, a.preferences.PreviewVideoVerticalResolution, a.gameData.ColorGrading, a.gameData.EntityVarMapping)
			}
			if a.preview != nil && a.gameData.AssetOverridesDirty {
				var entityInfo *entity.Entity
				entityData, err := a.gameData.DataDir.Read(a.gameData.ColorGrading, stingray.DataMain)
				if err == nil {
					entityInfo, err = entity.LoadEntity(bytes.NewReader(entityData), a.gameData.EntityVarMapping)
				}
				a.preview.UpdateAssetOverrides(entityInfo)
				a.gameData.AssetOverridesDirty = false
			}

			imutils.Textf("Showing %v/%v files (%v selected for export)", len(a.gameData.SortedSearchResultFileIDs), len(a.gameData.DataDir.Files), len(a.filesSelectedForExport))
		}
	}
	imgui.End()

	a.scrollToSelectedFile = false
}

func (a *guiApp) drawTypeFilterWindow() {
	gameFileTypeLabel := func(x stingray.Hash) string {
		items := make([]string, 0, 3)
		if name, ok := a.gameData.Hashes[x]; ok {
			items = append(items, name)
		}
		if desc, ok := gameFileTypeDescriptions[x]; ok {
			items = append(items, "("+desc+")")
		}
		if _, ok := gameFileTypeTooltipsExtra[x]; ok {
			items = append(items, fnt.I.Info)
		}
		return strings.Join(items, " ")
	}
	imgui.SetNextWindowSizeV(imutils.SVec2(250, 500), imgui.CondOnce)
	if widgets.FilterListWindow("Type Filter",
		&a.isTypeFilterOpen,
		"Search Types",
		&a.gameFileTypeSearchQuery,
		a.gameFileTypes,
		&a.selectedGameFileTypes,
		gameFileTypeLabel,
		func(x stingray.Hash, checked *bool) {
			label := gameFileTypeLabel(x)
			imgui.Checkbox(label, checked)
			tt := fmt.Sprintf("hash=%v", x)
			if ttExtra, ok := gameFileTypeTooltipsExtra[x]; ok {
				tt += "\n" + ttExtra
			}
			imgui.SetItemTooltip(tt)
		},
	) {
		a.gameData.UpdateSearchQuery(a.gameFileSearchQuery, a.selectedGameFileTypes, a.selectedArchives)
		a.allSelectedForExport = a.calcAllSelectedForExport()
		a.scrollToSelectedFile = true
	}
}

func (a *guiApp) drawArchiveFilterWindow() {
	imgui.SetNextWindowSizeV(imutils.SVec2(250, 500), imgui.CondOnce)
	if widgets.FilterListWindow("Archive Filter",
		&a.isArchiveFilterOpen,
		"Search Archives",
		&a.archiveIDSearchQuery,
		a.archiveIDs,
		&a.selectedArchives,
		func(x stingray.Hash) string {
			items := make([]string, 0, 4)
			if as, ok := a.gameData.ArmorSets[x]; ok {
				items = append(items, as.Name)
				items = append(items, as.Type.String())
			}
			if name, ok := a.gameData.Hashes[x]; ok {
				items = append(items, name)
			}
			items = append(items, x.String())
			return strings.Join(items, " ")
		},
		func(x stingray.Hash, checked *bool) {
			var label string
			if armorSet, ok := a.gameData.ArmorSets[x]; ok {
				label = x.String() + " (" + armorSet.Name + " - " + armorSet.Type.String() + ")"
			} else {
				label, ok = a.gameData.Hashes[x]
				if !ok {
					label = x.String()
				}
			}
			imgui.Checkbox(label, checked)
			copied := imgui.IsItemClickedV(imgui.MouseButtonRight)
			if imgui.IsItemFocused() && imgui.Shortcut(imgui.KeyChord(imgui.ModCtrl|imgui.KeyC)) {
				copied = true
			}
			if imutils.StickyActivate(copied, 1) {
				imgui.SetItemTooltip(fmt.Sprintf("%v Copied '%v'", fnt.I.Check, x))
			} else {
				var tt strings.Builder
				if name, ok := a.gameData.Hashes[x]; ok {
					fmt.Fprintf(&tt, "Archive: %v, hash=%v\n", name, x)
				} else {
					fmt.Fprintf(&tt, "Archive: hash=%v\n", x)
				}
				if armorSet, ok := a.gameData.ArmorSets[x]; ok {
					fmt.Fprintf(&tt, "Armor set: %v\n", armorSet.Name)
				}
				fmt.Fprintf(&tt, "Contains %v files\n", len(a.gameData.DataDir.Archives[x]))
				fmt.Fprintf(&tt, "%v Left-click or Space to select/deselect\n", fnt.I.LeftClick)
				fmt.Fprintf(&tt, "%v Right-click or Ctrl+C to copy name hash to clipboard\n", fnt.I.ContentCopy)
				imgui.SetItemTooltip(tt.String())
			}
			if copied {
				imgui.SetClipboardText(x.String())
			}
		},
	) {
		a.gameData.UpdateSearchQuery(a.gameFileSearchQuery, a.selectedGameFileTypes, a.selectedArchives)
		a.allSelectedForExport = a.calcAllSelectedForExport()
		a.scrollToSelectedFile = true
	}
}

func (a *guiApp) drawExportWindow() {
	if imgui.Begin(fnt.I.FileExport + " Export") {
		imgui.BeginDisabledV(a.gameDataExport != nil)
		imutils.FilePicker("Output directory", &a.exportDir, true)
		imgui.EndDisabled()

		imgui.Checkbox(fnt.I.Notifications+" Notify when done", &a.exportNotifyWhenDone)

		imgui.Separator()

		{
			imgui.PushIDStr("Open/close extractor logs")
			var numErrsWarnsStr string
			if a.logger.NumErrs() > 0 || a.logger.NumWarns() > 0 {
				var items []string
				if a.logger.NumErrs() > 0 {
					items = append(items, fmt.Sprintf("%v %v", a.logger.NumErrs(), fnt.I.Error))
				}
				if a.logger.NumWarns() > 0 {
					items = append(items, fmt.Sprintf("%v %v", a.logger.NumWarns(), fnt.I.Warning))
				}
				numErrsWarnsStr = fmt.Sprintf(" (%v)", strings.Join(items, ","))
			}
			var label string
			if a.isLogsOpen {
				label = fmt.Sprintf("%v Close extractor logs%v %v", fnt.I.List, numErrsWarnsStr, fnt.I.Close)
			} else {
				label = fmt.Sprintf("%v Open extractor logs%v %v", fnt.I.List, numErrsWarnsStr, fnt.I.OpenInNew)
			}
			if imgui.ButtonV(label, imgui.NewVec2(-math.SmallestNonzeroFloat32, 0)) {
				a.isLogsOpen = !a.isLogsOpen
			}
			imgui.PopID()
		}

		if imgui.ButtonV(fnt.I.FolderOpen+" Open output folder", imgui.NewVec2(-math.SmallestNonzeroFloat32, 0)) {
			_ = os.MkdirAll(a.exportDir, os.ModePerm)
			open.Start(a.exportDir)
		}

		imgui.Separator()

		if a.gameDataExport == nil {
			imgui.PushIDStr("Begin export button")
			imgui.BeginDisabledV(len(a.filesSelectedForExport) == 0 || a.gameData == nil)
			label := fmt.Sprintf("%v Begin export (%v)", fnt.I.FileExport, len(a.filesSelectedForExport))
			if imgui.ButtonV(label, imgui.NewVec2(-math.SmallestNonzeroFloat32, 0)) && a.gameData != nil {
				a.logger.Reset()
				a.gameDataExport = a.gameData.GoExport(
					a.ctx,
					slices.SortedFunc(maps.Keys(a.filesSelectedForExport), (stingray.FileID).Cmp),
					a.exportDir,
					a.extractorConfig,
					a.runner,
					slices.SortedFunc(maps.Keys(a.selectedArchives), (stingray.Hash).Cmp),
					a.logger,
					a.preferences.AllowVideoMP4Export,
				)
			}
			if a.gameData == nil {
				imgui.SetItemTooltip("Game data not loaded")
			} else if len(a.filesSelectedForExport) == 0 {
				imgui.SetItemTooltip("Nothing selected for export")
			}
			imgui.EndDisabled()
			imgui.PopID()
		} else {
			if a.gameDataExport.Done {
				if !a.gameDataExport.Canceled && a.exportNotifyWhenDone {
					pluralS := ""
					if a.gameDataExport.NumFiles != 1 {
						pluralS = "s"
					}
					var text string
					if !a.logger.HaveFatalErr() {
						text = fmt.Sprintf("Filediver has finished exporting %v file%v", a.gameDataExport.NumFiles, pluralS)
					} else {
						text = "An internal error occurred during exporting. Please create an issue on GitHub."
					}
					if a.logger.NumErrs() > 0 || a.logger.NumWarns() > 0 {
						text += "\n"
						text += fmt.Sprintf("Errors: %v, Warnings: %v", a.logger.NumErrs(), a.logger.NumWarns())
						text += "\nSee logs."
					}
					if a.logger.NumErrs() > 0 {
						a.isLogsOpen = true
					}
					zenity.Notify(text,
						zenity.Title("Finished exporting"),
						zenity.InfoIcon,
					)
				}
				a.gameDataExport = nil
			} else {
				imgui.ProgressBarV(
					float32(a.gameDataExport.CurrentFileIndex)/float32(a.gameDataExport.NumFiles),
					imgui.NewVec2(-math.SmallestNonzeroFloat32, 0),
					fmt.Sprintf("%v/%v", a.gameDataExport.CurrentFileIndex+1, a.gameDataExport.NumFiles),
				)
				imutils.Textf("%v", a.gameDataExport.CurrentFileName)
				if imgui.ButtonV(fnt.I.Cancel+" Cancel export", imgui.NewVec2(-math.SmallestNonzeroFloat32, 0)) {
					a.gameDataExport.Cancel()
				}
			}
		}
	}
	imgui.End()
}

func (a *guiApp) drawExtractorConfigWindow() {
	if imgui.Begin(fnt.I.SettingsApplications + " Extractor config") {
		prevExtrCfg := a.extractorConfig
		if widgets.ConfigEditor(&a.extractorConfig, &a.extractorConfigShowAdvanced, &a.extractorConfigSearchQuery) {
			if a.extractorConfig.Gamedir != prevExtrCfg.Gamedir {
				a.gameData = nil
				a.levelIDs = nil
				a.levelIDsReady = false
				a.gameDataLoad.GoLoadGameData(a.ctx, a.extractorConfig.Gamedir)
			}
			if a.extractorConfig.Planet.Name != prevExtrCfg.Planet.Name || a.extractorConfig.Planet.City != prevExtrCfg.Planet.City {
				a.gameData.UpdateAssetOverrides(a.extractorConfig.Planet.Name, a.extractorConfig.Planet.City)
			}
			if a.extractorConfig != prevExtrCfg {
				if err := a.extractorConfig.Save(a.extractorConfigPath); err != nil {
					a.showErrorPopup(err)
				}
			}
		}
	}
	imgui.End()
}

func (a *guiApp) drawLogWindow() {
	if !a.isLogsOpen {
		return
	}

	viewport := imgui.MainViewport()
	imgui.SetNextWindowPosV(viewport.Center(), imgui.CondOnce, imgui.NewVec2(0.5, 0.5))
	imgui.SetNextWindowSizeV(imutils.SVec2(350, 350), imgui.CondOnce)
	if imgui.BeginV("Extractor logs", &a.isLogsOpen, 0) {
		avail := imgui.ContentRegionAvail()
		avail.Y -= imgui.FrameHeightWithSpacing()
		imgui.SetNextWindowSize(avail)
		if imgui.BeginChildStr("Log view") {
			LogView(a.logger)
		}
		imgui.EndChild()

		ctx := imgui.CurrentContext()
		btnLabel := fnt.I.ContentCopy + " Copy all to clipboard"
		btnID := imgui.IDStr(btnLabel)
		if imgui.ButtonV(btnLabel, imgui.NewVec2(-math.SmallestNonzeroFloat32, 0)) {
			imgui.SetClipboardText(a.logger.String())
		}
		if ctx.LastActiveId() == btnID && ctx.LastActiveIdTimer() < 1 {
			imgui.SetItemTooltip(fnt.I.Check + " Copied")
		}
	}
	imgui.End()
}

func (a *guiApp) drawPreviewWindow(state *imgui_wrapper.State) {
	previewVisible := imgui.Begin(fnt.I.Preview + " Preview")
	if dockID := imgui.WindowDockID(); dockID != 0 {
		a.unitGalleryDockID = dockID
		a.animationDockID = dockID
	}
	if a.preview != nil {
		a.preview.SetHideLevelGround(a.preferences.HideLevelGround)
	}
	if previewVisible {
		if a.preview != nil {
			defer func() {
				if a.runUnitPreviewLoadingTest {
					if a.preview.Err() != nil && a.unitTestCooldown%5 == 0 && a.unitTestIdx < len(a.allUnits) {
						a.unitTestErrors[a.allUnits[a.unitTestIdx]] = a.preview.Err()
						fmt.Printf("[error] %v\n", a.preview.Err())
						a.unitTestErrorsCopied = false
					}
					if err := recover(); err != nil {
						a.unitTestErrors[a.allUnits[a.unitTestIdx]] = fmt.Errorf("%v", err)
						fmt.Printf("[error] %v\nstack trace: %s\n", err, debug.Stack())
						a.preview.LoadFile(a.ctx, stingray.FileID{}, 0, stingray.FileID{}, nil)
						a.runUnitPreviewLoadingTest = false
					} else if a.unitTestIdx < len(a.allUnits) && a.unitTestCooldown%5 == 0 {
						a.unitTestIdx = a.unitTestIdx + 1
					} else if a.unitTestCooldown%5 == 0 {
						a.runUnitPreviewLoadingTest = false
						a.preview.LoadFile(a.ctx, stingray.FileID{}, 0, stingray.FileID{}, nil)
					}
				}
			}()
			if a.runUnitPreviewLoadingTest && a.unitTestIdx >= len(a.allUnits) {
				a.runUnitPreviewLoadingTest = false
			}
			if a.runUnitPreviewLoadingTest && a.unitTestCooldown%5 == 0 && a.unitTestIdx < len(a.allUnits) {
				fmt.Printf("Loading %v.unit\n", a.gameData.LookupHash(a.allUnits[a.unitTestIdx]))
				a.preview.LoadFile(a.ctx, stingray.NewFileID(a.allUnits[a.unitTestIdx], stingray.Sum("unit")), a.preferences.PreviewVideoVerticalResolution, a.gameData.ColorGrading, a.gameData.EntityVarMapping)
			}
			if a.runUnitPreviewLoadingTest {
				a.unitTestCooldown = a.unitTestCooldown + 1
			}
			if !a.preview.Draw("Preview") {
				active := a.preview.ActiveID()
				imgui.PushTextWrapPos()
				if active.Name.Value == 0 {
					imgui.TextUnformatted("Nothing selected to preview. Select an item from the Browser.")
				} else if a.gameData != nil {
					imutils.Textf("Cannot preview type %v.", a.gameData.LookupHash(active.Type))
				}
			}
			if a.preview.NeedCJKFont() && !state.LoadCJKFonts {
				state.LoadCJKFonts = true
			}
		} else {
			imgui.PushTextWrapPos()
			imgui.TextUnformatted("Loading game data...")
		}
	}
	imgui.End()
}

func (a *guiApp) drawAnimationWindow() {
	if !a.animationDockConfigured && a.animationDockID != 0 {
		imgui.SetNextWindowDockIDV(a.animationDockID, imgui.CondFirstUseEver)
		a.animationDockConfigured = true
	}
	visible := imgui.Begin("Animation")
	if visible {
		if a.preview == nil {
			imgui.TextUnformatted("Loading game data...")
		} else {
			a.preview.DrawAnimation("Animation")
		}
	}
	imgui.End()
}

func (a *guiApp) drawMetadataWindow() {
	if imgui.Begin(fnt.I.Tag + " Metadata") {
		if a.gameData != nil && a.preview != nil && a.preview.ActiveID() != (stingray.FileID{}) {
			widgets.FileMetadata(a.gameData.Metadata[a.preview.ActiveID()])
		} else {
			imgui.PushTextWrapPos()
			imgui.TextUnformatted("No file selected")
			imgui.PopTextWrapPos()
		}
	}
	imgui.End()
}

func (a *guiApp) drawUnitGalleryWindow() {
	windowName := fnt.I.ViewList + " Units"
	if !a.unitGalleryDockConfigured && a.unitGalleryDockID != 0 {
		imgui.SetNextWindowDockIDV(a.unitGalleryDockID, imgui.CondFirstUseEver)
		a.unitGalleryDockConfigured = true
	}
	windowVisible := imgui.Begin(windowName)
	var selectedID stingray.FileID
	var didSelect bool
	if windowVisible {
		switch {
		case a.gameData == nil:
			imgui.TextUnformatted("Loading game data...")
		case a.unitGallery == nil:
			imgui.TextUnformatted("Preparing the unit renderer...")
		default:
			a.unitGallery.SetUnitLevelLookupCallback(a.requestUnitLevelLookup)
			if !a.unitIDsReady {
				a.unitIDs = make([]stingray.FileID, 0)
				for id := range a.gameData.DataDir.Files {
					if id.Type == stingray.Sum("unit") {
						a.unitIDs = append(a.unitIDs, id)
					}
				}
				slices.SortFunc(a.unitIDs, func(left, right stingray.FileID) int {
					return strings.Compare(a.gameData.KnownFileNames[left], a.gameData.KnownFileNames[right])
				})
				a.unitIDsReady = true
			}
			if camera, ok := a.preview.UnitCamera(); ok {
				a.unitGallery.RememberCamera(a.preview.ActiveID(), camera)
			}
			a.refreshUnitComparison()
			galleryIDs := a.unitIDs
			a.unitGallery.SetVersionChanges(a.unitComparisonAdded, a.comparisonChangedByType[stingray.Sum("unit")], a.unitComparisonRemoved)
			if a.unitGallery.HasAnimationsFilterEnabled() {
				a.startUnitAnimationScan(galleryIDs)
			}
			animatedUnits, animationScanDone, animationScanProgress, animationScanTotal := a.unitAnimationScanSnapshot()
			a.unitGallery.SetAnimationScanResults(animatedUnits, animationScanDone, animationScanProgress, animationScanTotal)
			unitAngleTooltip := "Use the current Preview camera angle and recapture existing unit thumbnails"
			if imgui.Button("Set preview angle") {
				camera, ok := a.preview.UnitCamera()
				if !ok {
					camera = previews.DefaultUnitCamera()
				}
				count, err := a.unitGallery.SetUnitPreviewAngle(camera.Rotation, galleryIDs)
				if err != nil {
					a.showErrorPopup(err)
				} else {
					a.preferences.UnitPreviewAngle = camera.Rotation
					a.preferences.UnitPreviewAngleSet = true
					if err := a.preferences.Save(a.preferencesPath); err != nil {
						a.showErrorPopup(fmt.Errorf("saving Units preview angle: %w", err))
					} else if count > 0 {
						unitAngleTooltip = fmt.Sprintf("Angle saved; queued %d existing thumbnails for recapture", count)
					} else {
						unitAngleTooltip = "Angle saved. It will persist across app restarts and cache resets."
					}
				}
			}
			imgui.SetItemTooltip(unitAngleTooltip)
			statsFor := func(id stingray.FileID) previews.UnitGalleryStats {
				meta := a.gameData.Metadata[id]
				return previews.UnitGalleryStats{
					TriangleCount:   meta.TriangleCount,
					AllLODTriangles: meta.AllLODTriangles,
					ModelSize:       meta.ModelSize,
					ModelExtent:     meta.ModelExtent,
					FileSize:        a.gameData.fileSize(id),
				}
			}
			selectedID, didSelect = a.unitGallery.DrawGrid(
				galleryIDs,
				a.preview.ActiveID(),
				a.gameData.DisplayFileName,
				func(id stingray.FileID) string {
					if inferred, ok := a.gameData.InferredUnitNames[id.Name]; ok {
						return inferred.Source
					}
					return ""
				},
				statsFor,
			)
		}
	}
	imgui.End()

	if a.unitGallery != nil && !windowVisible {
		a.unitGallery.Suspend()
	}
	if !windowVisible || a.unitGallery == nil || a.preview == nil {
		return
	}
	if didSelect {
		previousID := a.preview.ActiveID()
		camera, haveCamera := a.unitGallery.CameraFor(selectedID)
		// Removed resources belong to the selected (older) build, whose preview
		// renderer is already active. The comparison path is treated as newer.
		a.activateCurrentPreview()
		if previousID != selectedID {
			a.historyPush(previousID, selectedID)
			a.preview.LoadFile(a.ctx, selectedID, a.preferences.PreviewVideoVerticalResolution, a.gameData.ColorGrading, a.gameData.EntityVarMapping)
		}
		if haveCamera {
			a.preview.SetUnitCamera(camera)
		} else {
			camera := previews.DefaultUnitCamera()
			if loadedCamera, ok := a.preview.UnitCamera(); ok {
				camera.ModelPos = loadedCamera.ModelPos
			}
			a.preview.SetUnitCamera(camera)
			a.preview.AutoZoomUnitNow()
		}
		a.scrollToSelectedFile = true
		imgui.SetWindowFocusStr(fnt.I.Preview + " Preview")
	}
	a.unitGallery.DrawCaptureFrame()
}

func (a *guiApp) drawLevelGalleryWindow() {
	const windowName = "Levels"
	if !a.levelGalleryDockConfigured && a.unitGalleryDockID != 0 {
		imgui.SetNextWindowDockIDV(a.unitGalleryDockID, imgui.CondFirstUseEver)
		a.levelGalleryDockConfigured = true
	}
	windowVisible := imgui.Begin(windowName)
	var selectedID stingray.FileID
	var didSelect bool
	if windowVisible {
		switch {
		case a.gameData == nil:
			imgui.TextUnformatted("Loading game data...")
		case a.levelGallery == nil:
			imgui.TextUnformatted("Preparing the level renderer...")
		default:
			if a.gameData != nil {
				a.levelGallery.SetAssetGradingKey(a.gameData.ColorGrading.Name.String())
			}
			a.resetLevelNamesIfNeeded()
			if !a.levelIDsReady {
				a.levelIDs = make([]stingray.FileID, 0)
				for id := range a.gameData.DataDir.Files {
					if id.Type == stingray.Sum("level") {
						a.levelIDs = append(a.levelIDs, id)
					}
				}
				slices.SortFunc(a.levelIDs, func(left, right stingray.FileID) int {
					return strings.Compare(a.gameData.KnownFileNames[left], a.gameData.KnownFileNames[right])
				})
				a.levelIDsReady = true
			}
			angleTooltip := "Use the current Preview camera angle and recapture existing level thumbnails"
			if imgui.Button("Set preview angle") {
				camera, ok := a.preview.UnitCamera()
				if !ok {
					camera = previews.DefaultUnitCamera()
				}
				count, err := a.levelGallery.SetLevelPreviewAngle(camera.Rotation, a.levelIDs)
				if err != nil {
					a.showErrorPopup(err)
				} else if count > 0 {
					angleTooltip = fmt.Sprintf("Angle applied; queued %d existing thumbnails for recapture", count)
				} else {
					angleTooltip = "Angle saved. Cached thumbnails will use it when they are next rendered."
				}
			}
			imgui.SetItemTooltip(angleTooltip)
			if a.preview != nil {
				a.preview.SetHideLevelGround(a.preferences.HideLevelGround)
			}
			a.refreshUnitComparison()
			levelType := stingray.Sum("level")
			addedLevels := a.comparisonAddedByType[levelType]
			changedLevels := a.comparisonChangedByType[levelType]
			removedLevels := a.comparisonRemovedByType[levelType]
			a.levelGallery.SetVersionChanges(addedLevels, changedLevels, removedLevels)
			if queued := a.levelGallery.SetHideLevelGround(a.preferences.HideLevelGround, a.levelIDs); queued > 0 {
				imgui.SameLine()
				imgui.TextDisabled(fmt.Sprintf("Recapturing %d cached levels", queued))
			}
			imgui.SameLine()
			imgui.TextDisabled("Uses the current Preview view; cached JPEGs are recaptured in the background.")

			statsFor := func(id stingray.FileID) previews.UnitGalleryStats {
				meta := a.gameData.Metadata[id]
				return previews.UnitGalleryStats{
					ModelExtent: meta.ModelExtent,
					FileSize:    a.gameData.fileSize(id),
				}
			}
			galleryIDs := a.levelIDs
			a.updateLevelNames(galleryIDs)
			selectedID, didSelect = a.levelGallery.DrawGrid(
				galleryIDs,
				a.preview.ActiveID(),
				a.levelDisplayName,
				nil,
				statsFor,
			)
		}
	}
	imgui.End()

	if a.levelGallery != nil && !windowVisible {
		a.levelGallery.Suspend()
	}
	if !windowVisible || a.levelGallery == nil || a.preview == nil {
		return
	}
	if didSelect {
		previousID := a.preview.ActiveID()
		// The selected build is the old side of the comparison, so removed
		// levels are available from the current selected-build renderer.
		a.activateCurrentPreview()
		if previousID != selectedID {
			a.historyPush(previousID, selectedID)
			a.preview.LoadFile(a.ctx, selectedID, a.preferences.PreviewVideoVerticalResolution, a.gameData.ColorGrading, a.gameData.EntityVarMapping)
		}
		if camera, ok := a.levelGallery.CameraFor(selectedID); ok {
			a.preview.SetUnitCamera(camera)
		} else {
			camera, ok := a.preview.UnitCamera()
			if !ok {
				camera = previews.DefaultUnitCamera()
			}
			a.preview.SetUnitCamera(camera)
			a.preview.AutoZoomUnitNow()
		}
		a.scrollToSelectedFile = true
		imgui.SetWindowFocusStr(fnt.I.Preview + " Preview")
	}
	a.levelGallery.DrawCaptureFrame()
}

func (a *guiApp) activateCurrentPreview() {
	if a.currentPreview != nil {
		a.preview = a.currentPreview
	}
}

func (a *guiApp) clearOlderBuildPreview() {
	if a.preview == a.olderBuildPreview {
		a.activateCurrentPreview()
	}
	if a.unitGallery != nil {
		a.unitGallery.ClearRemovedSource()
	}
	if a.levelGallery != nil {
		a.levelGallery.ClearRemovedSource()
	}
	if a.armorSet.gallery != nil {
		a.armorSet.gallery.ClearRemovedSource()
	}
	if a.olderTextureGallery != nil {
		a.olderTextureGallery.Delete()
		a.olderTextureGallery = nil
	}
	if a.olderBuildPreview != nil {
		a.olderBuildPreview.Delete()
	}
	a.olderBuildPreview = nil
	a.olderBuildDataDir = nil
	a.olderBuildArmorSets = nil
	a.olderBuildArmorTableValid = false
}

func (a *guiApp) ensureOlderBuildPreview() error {
	a.unitVersionScan.Lock()
	dataDir, scanErr := a.unitVersionScan.DataDir, a.unitVersionScan.Err
	a.unitVersionScan.Unlock()
	if scanErr != nil {
		return scanErr
	}
	if dataDir == nil {
		return fmt.Errorf("scan the compared build before viewing its assets")
	}
	if a.olderBuildPreview != nil && a.olderBuildDataDir == dataDir && a.olderBuildArmorSets != nil {
		return nil
	}
	oldArmorSets, armorErr := filediverapp.LoadArmorSetsForGameDir(dataDir.Path, a.gameData.LanguageMap)
	if armorErr != nil {
		// Older sealed builds may not be readable without the optional game key.
		// Keep removed-unit preview working while exposing no false kit removals.
		oldArmorSets = a.gameData.ArmorSets
	}
	if a.olderBuildPreview != nil {
		a.clearOlderBuildPreview()
	}
	getResourceGenerator := previews.GetResourceGeneratorFunc(func(_ bool) func(stingray.FileID, stingray.DataType) ([]byte, bool, error) {
		return func(id stingray.FileID, typ stingray.DataType) ([]byte, bool, error) {
			data, err := dataDir.Read(id, typ)
			if err == stingray.ErrFileNotExist || err == stingray.ErrFileDataTypeNotExist {
				return nil, false, nil
			}
			if err != nil {
				return nil, false, err
			}
			return data, true, nil
		}
	})
	identityOverride := func(id stingray.FileID) stingray.FileID { return id }
	oldPreview, err := previews.NewAutoPreview(
		a.otoCtx,
		a.audioSampleRate,
		a.gameData.Hashes,
		a.gameData.ThinHashes,
		false,
		getResourceGenerator,
		identityOverride,
		a.runner,
		previews.ExtractorPlanetParameters{},
		previews.ExtractorArmorParameters{
			ArmorSets: oldArmorSets,
			SelectedArchives: func() []stingray.Hash {
				return slices.SortedFunc(maps.Keys(a.selectedArchives), stingray.Hash.Cmp)
			},
		},
		nil,
	)
	if err != nil {
		return fmt.Errorf("creating older-build preview: %w", err)
	}
	animationResources := make([]stingray.FileID, 0)
	for id := range dataDir.Files {
		if id.Type == stingray.Sum("animation") {
			animationResources = append(animationResources, id)
		}
	}
	slices.SortFunc(animationResources, stingray.FileID.Cmp)
	oldPreview.SetAnimationResources(animationResources)
	a.olderBuildPreview = oldPreview
	a.olderBuildDataDir = dataDir
	a.olderBuildArmorSets = oldArmorSets
	a.olderBuildArmorTableValid = armorErr == nil
	return nil
}

func (a *guiApp) refreshUnitComparison() {
	if a.gameData == nil {
		return
	}
	a.unitVersionScan.Lock()
	done, scanErr, oldPath := a.unitVersionScan.Done, a.unitVersionScan.Err, a.unitVersionScan.Path
	oldDataDir := a.unitVersionScan.DataDir
	a.unitVersionScan.Unlock()
	if a.comparisonMatchResults != nil {
		select {
		case result := <-a.comparisonMatchResults:
			a.comparisonMatchRunning = false
			if result.gameData == a.gameData && result.oldPath == oldPath && a.unitComparisonReady && a.unitComparisonGameData == a.gameData {
				for fileType, pairs := range result.pairs {
					changed := make(map[stingray.FileID]struct{}, len(pairs))
					validPairs := make(map[stingray.FileID]stingray.FileID, len(pairs))
					for currentID, olderID := range pairs {
						if currentID == olderID {
							changed[currentID] = struct{}{}
							validPairs[currentID] = olderID
							continue
						}
						if _, isAdded := a.comparisonAddedByType[fileType][currentID]; !isAdded {
							continue
						}
						if _, isRemoved := a.comparisonRemovedByType[fileType][olderID]; !isRemoved {
							continue
						}
						delete(a.comparisonAddedByType[fileType], currentID)
						delete(a.comparisonRemovedByType[fileType], olderID)
						changed[currentID] = struct{}{}
						validPairs[currentID] = olderID
					}
					a.comparisonChangedByType[fileType] = changed
					a.comparisonChangedPairs[fileType] = validPairs
				}
				a.unitComparisonAdded = a.comparisonAddedByType[stingray.Sum("unit")]
				a.unitComparisonRemoved = a.comparisonRemovedByType[stingray.Sum("unit")]
				if a.unitGallery != nil {
					a.unitGallery.SetVersionChanges(a.unitComparisonAdded, a.comparisonChangedByType[stingray.Sum("unit")], a.unitComparisonRemoved)
				}
				if a.levelGallery != nil {
					typ := stingray.Sum("level")
					a.levelGallery.SetVersionChanges(a.comparisonAddedByType[typ], a.comparisonChangedByType[typ], a.comparisonRemovedByType[typ])
				}
			}
		default:
		}
	}
	if !done || scanErr != nil {
		if a.unitComparisonReady {
			a.unitComparisonAdded = nil
			a.unitComparisonRemoved = nil
			a.comparisonAddedByType = nil
			a.comparisonRemovedByType = nil
			a.comparisonChangedByType = nil
			a.comparisonChangedPairs = nil
			a.comparisonMatchRunning = false
			a.unitComparisonReady = false
			a.unitComparisonGameData = nil
			if a.unitGallery != nil {
				a.unitGallery.SetVersionChanges(nil, nil, nil)
			}
			if a.levelGallery != nil {
				a.levelGallery.SetVersionChanges(nil, nil, nil)
			}
		}
		return
	}
	if a.unitComparisonReady && a.unitComparisonGameData == a.gameData && a.unitComparisonOldPath == oldPath {
		return
	}
	// The loaded/selected game is the baseline (older) build. The folder in
	// Compare is the newer build, so additions come from that folder and
	// removals are resources that exist only in the selected build.
	newDataDir := oldDataDir
	selectedOldDataDir := a.gameData.DataDir
	addedByType := make(map[stingray.Hash]map[stingray.FileID]struct{})
	removedByType := make(map[stingray.Hash]map[stingray.FileID]struct{})
	for _, fileType := range []stingray.Hash{stingray.Sum("unit"), stingray.Sum("level"), stingray.Sum("texture"), stingray.Sum("strings")} {
		current := make(map[stingray.FileID]struct{})
		added := make(map[stingray.FileID]struct{})
		removed := make(map[stingray.FileID]struct{})
		for id := range newDataDir.Files {
			if id.Type != fileType {
				continue
			}
			current[id] = struct{}{}
			if _, exists := selectedOldDataDir.Files[id]; !exists {
				added[id] = struct{}{}
			}
		}
		for id := range selectedOldDataDir.Files {
			if id.Type != fileType {
				continue
			}
			if _, exists := newDataDir.Files[id]; !exists {
				removed[id] = struct{}{}
			}
		}
		addedByType[fileType] = added
		removedByType[fileType] = removed
	}
	added := addedByType[stingray.Sum("unit")]
	removed := removedByType[stingray.Sum("unit")]
	a.unitComparisonAdded = added
	a.unitComparisonRemoved = removed
	a.comparisonAddedByType = addedByType
	a.comparisonRemovedByType = removedByType
	a.comparisonChangedByType = make(map[stingray.Hash]map[stingray.FileID]struct{})
	a.comparisonChangedPairs = make(map[stingray.Hash]map[stingray.FileID]stingray.FileID)
	for _, fileType := range []stingray.Hash{stingray.Sum("unit"), stingray.Sum("level")} {
		a.comparisonChangedByType[fileType] = make(map[stingray.FileID]struct{})
	}
	a.unitComparisonReady = true
	a.unitComparisonGameData = a.gameData
	a.unitComparisonOldPath = oldPath
	if a.unitGallery != nil {
		a.unitGallery.SetVersionChanges(added, a.comparisonChangedByType[stingray.Sum("unit")], removed)
	}
	if a.comparisonMatchResults == nil {
		a.comparisonMatchResults = make(chan versionMatchResult, 1)
	}
	a.comparisonMatchRunning = true
	matchContext, data := a.ctx, a.gameData
	go func() {
		pairs := findLikelyVersionReplacements(matchContext, data, newDataDir, selectedOldDataDir, addedByType, removedByType)
		select {
		case a.comparisonMatchResults <- versionMatchResult{gameData: data, oldPath: oldPath, pairs: pairs}:
		case <-matchContext.Done():
		}
	}()
}

func textureFileSize(dataDir *stingray.DataDir, id stingray.FileID) uint64 {
	if dataDir == nil {
		return 0
	}
	fileInfos := dataDir.Files[id]
	if len(fileInfos) == 0 {
		return 0
	}
	var size uint64
	for _, typ := range [...]stingray.DataType{stingray.DataMain, stingray.DataStream, stingray.DataGPU} {
		size += uint64(fileInfos[0].Files[typ].Size)
	}
	return size
}

func (a *guiApp) drawUnitVersionCompareWindow() {
	if !a.unitGalleryDockConfigured && a.unitGalleryDockID != 0 {
		imgui.SetNextWindowDockIDV(a.unitGalleryDockID, imgui.CondFirstUseEver)
	}
	if imgui.Begin("Compare") {
		if a.gameData == nil {
			imgui.TextUnformatted("Load the current game build first.")
		} else {
			imgui.TextUnformatted("Compare the selected build (older baseline) with a newer Helldivers 2 installation.")
			imgui.SetNextItemWidth(max(imutils.S(200), imgui.ContentRegionAvail().X-imutils.S(130)))
			imgui.InputTextWithHint("##OldUnitBuildPath", "Newer game folder or data folder", &a.unitComparePath, 0, nil)
			imgui.SameLine()
			if imgui.Button("Browse…") {
				if path, err := zenity.SelectFile(zenity.Title("Select newer Helldivers 2 installation"), zenity.Directory()); err == nil {
					a.unitComparePath = path
				}
			}
			imgui.SameLine()
			a.unitVersionScan.Lock()
			scanning := !a.unitVersionScan.Done && a.unitVersionScan.Path != ""
			a.unitVersionScan.Unlock()
			imgui.BeginDisabledV(strings.TrimSpace(a.unitComparePath) == "" || scanning)
			if imgui.Button("Scan compared build") {
				a.preferences.UnitComparePath = strings.TrimSpace(a.unitComparePath)
				if err := a.preferences.Save(a.preferencesPath); err != nil {
					a.showErrorPopup(fmt.Errorf("saving older-build comparison path: %w", err))
				}
				a.clearOlderBuildPreview()
				a.unitComparisonReady = false
				a.unitComparisonAdded = nil
				a.unitComparisonRemoved = nil
				a.unitComparisonGameData = nil
				a.comparisonAddedByType = nil
				a.comparisonRemovedByType = nil
				a.comparisonChangedByType = nil
				a.comparisonChangedPairs = nil
				a.comparisonMatchRunning = false
				if a.unitGallery != nil {
					a.unitGallery.SetVersionChanges(nil, nil, nil)
				}
				if a.levelGallery != nil {
					a.levelGallery.SetVersionChanges(nil, nil, nil)
				}
				a.unitVersionScan.GoScan(a.ctx, strings.TrimSpace(a.unitComparePath))
			}
			imgui.EndDisabled()

			a.unitVersionScan.Lock()
			done, progress, scanErr := a.unitVersionScan.Done, a.unitVersionScan.Progress, a.unitVersionScan.Err
			a.unitVersionScan.Unlock()
			if scanning {
				imgui.ProgressBarV(progress, imgui.NewVec2(-1, 0), "Scanning compared build...")
			} else if done && scanErr != nil {
				imutils.TextError(scanErr)
			} else if done && scanErr == nil {
				a.refreshUnitComparison()
				if err := a.ensureOlderBuildPreview(); err != nil {
					imutils.TextError(err)
				}
				currentCount := 0
				for id := range a.gameData.DataDir.Files {
					if id.Type == stingray.Sum("unit") {
						currentCount++
					}
				}
				imgui.Separator()
				imgui.TextUnformatted(fmt.Sprintf("Selected older build: %d units", currentCount))
				imgui.TextUnformatted(fmt.Sprintf("Added in compared newer build: %d", len(a.unitComparisonAdded)))
				imgui.TextUnformatted(fmt.Sprintf("Changed / likely replaced units: %d", len(a.comparisonChangedByType[stingray.Sum("unit")])))
				imgui.TextUnformatted(fmt.Sprintf("Removed from compared newer build: %d", len(a.unitComparisonRemoved)))
				levelType := stingray.Sum("level")
				imgui.TextUnformatted(fmt.Sprintf("Levels: %d added, %d changed / likely replaced, %d removed", len(a.comparisonAddedByType[levelType]), len(a.comparisonChangedByType[levelType]), len(a.comparisonRemovedByType[levelType])))
				if a.comparisonMatchRunning {
					imgui.TextDisabled("Comparing resource names and level contents to find likely replacements…")
				}
				imgui.TextDisabled("New, Changed, and Removed filters are also available in the Units and Levels grids. Removed entries come from the selected build; New entries come from the compared build.")
				a.drawUnitChangeList("Added units", a.unitComparisonAdded, true)
				a.drawChangedResourceList("Likely changed units", a.comparisonChangedPairs[stingray.Sum("unit")])
				a.drawUnitChangeList("Removed units", a.unitComparisonRemoved, false)
				a.drawChangedResourceList("Likely changed levels", a.comparisonChangedPairs[levelType])
			}
		}
	}
	imgui.End()
}

func (a *guiApp) drawChangedResourceList(label string, pairs map[stingray.FileID]stingray.FileID) {
	if len(pairs) == 0 || !imgui.CollapsingHeaderBoolPtr(label+fmt.Sprintf(" (%d)", len(pairs)), nil) {
		return
	}
	currentIDs := slices.SortedFunc(maps.Keys(pairs), stingray.FileID.Cmp)
	imgui.BeginChildStrV("##"+label, imgui.NewVec2(0, imutils.S(180)), imgui.ChildFlagsBorders, imgui.WindowFlagsNone)
	clipper := imgui.NewListClipper()
	clipper.Begin(int32(len(currentIDs)))
	for clipper.Step() {
		for row := clipper.DisplayStart(); row < clipper.DisplayEnd(); row++ {
			currentID := currentIDs[row]
			olderID := pairs[currentID]
			currentName := a.gameData.DisplayFileName(currentID)
			olderName := a.gameData.DisplayFileName(olderID)
			text := fmt.Sprintf("%s  ←  %s", currentName, olderName)
			if currentID == olderID {
				text = currentName + "  (resource data changed at the same path)"
			}
			if imgui.SelectableBool(text) {
				clipboard.Write(clipboard.FmtText, []byte(currentID.Name.String()))
			}
			if imgui.IsItemHovered() {
				imgui.SetTooltip("Likely replacement: current-build hash ← older-build hash. Click to copy the current hash.")
			}
		}
	}
	imgui.EndChild()
}

func (a *guiApp) drawUnitChangeList(label string, ids map[stingray.FileID]struct{}, current bool) {
	if !imgui.CollapsingHeaderBoolPtr(label+fmt.Sprintf(" (%d)", len(ids)), nil) {
		return
	}
	ordered := slices.SortedFunc(maps.Keys(ids), stingray.FileID.Cmp)
	imgui.BeginChildStrV("##"+label, imgui.NewVec2(0, imutils.S(180)), imgui.ChildFlagsBorders, imgui.WindowFlagsNone)
	clipper := imgui.NewListClipper()
	clipper.Begin(int32(len(ordered)))
	for clipper.Step() {
		for row := clipper.DisplayStart(); row < clipper.DisplayEnd(); row++ {
			id := ordered[row]
			name := id.Name.String()
			if current && a.gameData != nil {
				name = a.gameData.DisplayFileName(id)
			}
			labelText := fmt.Sprintf("%s  (%s)", name, id.Name.String())
			if imgui.SelectableBool(labelText) {
				clipboard.Write(clipboard.FmtText, []byte(id.Name.String()))
			}
			if imgui.IsItemHovered() {
				imgui.SetTooltip("Click to copy unit hash")
			}
		}
	}
	imgui.EndChild()
}

func (a *guiApp) drawTextureGalleryWindow() {
	windowName := fnt.I.Image + " Textures"
	if !imgui.Begin(windowName) {
		imgui.End()
		return
	}
	defer imgui.End()
	if a.gameData == nil || a.preview == nil {
		imgui.TextUnformatted("Loading game data...")
		return
	}
	a.refreshUnitComparison()
	if !a.textureIDsReady {
		for id := range a.gameData.DataDir.Files {
			if id.Type == stingray.Sum("texture") {
				a.textureIDs = append(a.textureIDs, id)
			}
		}
		a.textureIDsReady = true
	}
	width := imgui.ContentRegionAvail().X
	imgui.SetNextItemWidth(max(imutils.S(120), width-imutils.S(800)))
	imgui.InputTextWithHint("##TextureGallerySearch", fnt.I.Search+" Filter textures...", &a.textureQuery, 0, nil)
	imgui.SameLine()
	imgui.Checkbox("Sharp", &a.texturePixelated)
	imgui.SetItemTooltip("Use nearest-pixel filtering to remove thumbnail blur")
	imgui.SameLine()
	imgui.Checkbox("256 px", &a.texture256px)
	imgui.SetItemTooltip("Limit decoded thumbnails to 256 pixels per side")
	imgui.SameLine()
	imgui.SetNextItemWidth(imutils.S(140))
	if a.textureGridScale == 0 {
		a.textureGridScale = 1
	}
	imgui.SliderFloat("Grid scale", &a.textureGridScale, 0.5, 1.5)
	imgui.SameLine()
	imgui.SetNextItemWidth(imutils.S(155))
	sortLabels := []string{"Name", "File size", "Resolution"}
	if a.unitComparisonReady {
		sortLabels = append(sortLabels, "New", "Removed")
	} else if a.textureSort >= 3 {
		a.textureSort = 0
	}
	if a.textureSort >= len(sortLabels) {
		a.textureSort = 0
	}
	if imgui.BeginCombo("Sort by", sortLabels[a.textureSort]) {
		for i, label := range sortLabels {
			if imgui.SelectableBool(label) {
				a.textureSort = i
			}
		}
		imgui.EndCombo()
	}
	imgui.SameLine()
	direction := "Ascending"
	if a.textureDescending {
		direction = "Descending"
	}
	if imgui.Button(fnt.I.Sort + " " + direction) {
		a.textureDescending = !a.textureDescending
	}
	if a.textureGallery != nil {
		maxDim := 512
		if a.texture256px {
			maxDim = 256
		}
		if a.textureSort == 3 {
			if err := a.ensureOlderBuildPreview(); err != nil {
				imutils.TextError(err)
			} else if a.olderTextureGallery == nil {
				a.olderTextureGallery = a.olderBuildPreview.NewTextureGallery()
			}
			if a.olderTextureGallery != nil {
				a.olderTextureGallery.BeginFrame(maxDim, a.texturePixelated)
			}
		} else {
			a.textureGallery.BeginFrame(maxDim, a.texturePixelated)
		}
	}
	ids := make([]stingray.FileID, 0, len(a.textureIDs))
	query := strings.ToLower(a.textureQuery)
	textureType := stingray.Sum("texture")
	versionSet := map[stingray.FileID]struct{}(nil)
	if a.textureSort == 3 {
		versionSet = a.comparisonAddedByType[textureType]
	} else if a.textureSort == 4 {
		versionSet = a.comparisonRemovedByType[textureType]
	}
	appendIfMatch := func(id stingray.FileID) {
		name := a.gameData.LookupHash(id.Name)
		if query == "" || strings.Contains(strings.ToLower(name), query) || strings.Contains(strings.ToLower(id.Name.String()), query) {
			ids = append(ids, id)
		}
	}
	if a.textureSort == 3 || a.textureSort == 4 {
		for id := range versionSet {
			appendIfMatch(id)
		}
	} else {
		for _, id := range a.textureIDs {
			appendIfMatch(id)
		}
	}
	slices.SortFunc(ids, func(left, right stingray.FileID) int {
		lm, rm := a.gameData.Metadata[left], a.gameData.Metadata[right]
		leftDir, rightDir := a.gameData.DataDir, a.gameData.DataDir
		if a.textureSort == 3 {
			leftDir, rightDir = a.unitVersionScan.DataDir, a.unitVersionScan.DataDir
		}
		var result int
		switch a.textureSort {
		case 1:
			result = cmp.Compare(textureFileSize(leftDir, left), textureFileSize(rightDir, right))
		case 2:
			result = cmp.Compare(lm.Width*lm.Height, rm.Width*rm.Height)
			if result == 0 {
				result = cmp.Compare(textureFileSize(leftDir, left), textureFileSize(rightDir, right))
			}
		default:
			result = strings.Compare(a.gameData.LookupHash(left.Name), a.gameData.LookupHash(right.Name))
		}
		if a.textureDescending {
			result = -result
		}
		return result
	})
	imgui.TextUnformatted(fmt.Sprintf("%d textures", len(ids)))
	gridContent := imgui.ContentRegionAvail()
	// Keep the texture columns at a stable width while the table scrollbar is
	// updated as thumbnail rows are decoded and uploaded.
	gridWidth := max(0, gridContent.X-imgui.CurrentStyle().ScrollbarSize())
	cell := imutils.S(190 * a.textureGridScale)
	spacing := imgui.CurrentStyle().ItemSpacing().X
	columns := max(1, int((gridWidth+spacing)/(cell+spacing)))
	cell = (gridWidth - spacing*float32(columns-1)) / float32(columns)
	if imgui.BeginTableV("##TextureGalleryGrid", int32(columns), imgui.TableFlagsSizingFixedFit|imgui.TableFlagsScrollY, imgui.NewVec2(gridWidth, gridContent.Y), 0) {
		for range columns {
			imgui.TableSetupColumnV("", imgui.TableColumnFlagsWidthFixed, cell, 0)
		}
		clipper := imgui.NewListClipper()
		clipper.Begin(int32((len(ids) + columns - 1) / columns))
		for clipper.Step() {
			for row := clipper.DisplayStart(); row < clipper.DisplayEnd(); row++ {
				imgui.TableNextRow()
				for col := 0; col < columns; col++ {
					imgui.TableNextColumn()
					idx := int(row)*columns + col
					if idx >= len(ids) {
						break
					}
					id := ids[idx]
					meta := a.gameData.Metadata[id]
					gallery := a.textureGallery
					if a.textureSort == 3 {
						gallery = a.olderTextureGallery
					}
					pos := imgui.CursorScreenPos()
					imageSize := min(cell-imutils.S(12), imutils.S(160*a.textureGridScale))
					imagePos := pos.Add(imgui.NewVec2((cell-imageSize)*0.5, 0))
					imgui.PushIDStr(id.Name.String())
					clickedImage := false
					if gallery != nil {
						clickedImage = gallery.DrawThumbnail(id, imagePos, imgui.NewVec2(imageSize, imageSize))
					}
					imgui.PopID()
					imgui.SetCursorScreenPos(pos.Add(imgui.NewVec2(0, imageSize+imutils.S(4))))
					textureDir := a.gameData.DataDir
					if a.textureSort == 3 {
						textureDir = a.unitVersionScan.DataDir
					}
					label := fmt.Sprintf("%s · %dx%d\n%s · %d KiB", filepath.Base(a.gameData.LookupHash(id.Name)), meta.Width, meta.Height, meta.Format, textureFileSize(textureDir, id)/1024)
					if a.textureSort == 3 {
						label = fmt.Sprintf("%s · compared build\n%d KiB", filepath.Base(a.gameData.LookupHash(id.Name)), textureFileSize(textureDir, id)/1024)
					}
					clickedCard := imgui.ButtonV(label, imgui.NewVec2(cell, imutils.S(48)))
					if clickedImage || clickedCard {
						if a.textureSort == 3 {
							if err := a.ensureOlderBuildPreview(); err == nil {
								previous := a.preview.ActiveID()
								a.preview = a.olderBuildPreview
								a.historyPush(previous, id)
								a.preview.LoadFile(a.ctx, id, a.preferences.PreviewVideoVerticalResolution, stingray.FileID{}, nil)
							}
						} else {
							a.activateCurrentPreview()
							a.historyPush(a.preview.ActiveID(), id)
							a.preview.LoadFile(a.ctx, id, a.preferences.PreviewVideoVerticalResolution, a.gameData.ColorGrading, a.gameData.EntityVarMapping)
						}
						a.scrollToSelectedFile = true
						imgui.SetWindowFocusStr(fnt.I.Preview + " Preview")
					}
				}
			}
		}
		imgui.EndTable()
	}
}

func (a *guiApp) drawSettingsWindow() {
	if a.gameData == nil || a.preview == nil {
		return
	}
	hideGroundBefore := a.preferences.HideLevelGround
	a.preview.DrawSettings()
	hideGroundAfter := a.preview.HideLevelGround()
	if hideGroundAfter == hideGroundBefore {
		return
	}
	a.preferences.HideLevelGround = hideGroundAfter
	if err := a.preferences.Save(a.preferencesPath); err != nil {
		a.showErrorPopup(err)
	}
	if a.currentPreview != nil {
		a.currentPreview.SetHideLevelGround(hideGroundAfter)
	}
	if a.olderBuildPreview != nil {
		a.olderBuildPreview.SetHideLevelGround(hideGroundAfter)
	}
	if a.levelGallery != nil && a.levelIDsReady {
		if queued := a.levelGallery.SetHideLevelGround(hideGroundAfter, a.levelIDs); queued > 0 {
			fmt.Printf("recapturing %d cached level thumbnails after ground visibility change\n", queued)
		}
	}
}

func (a *guiApp) drawCheckForUpdatesPopup() {
	a.checkUpdatesLock.Lock()
	if a.checkUpdatesOnStartupBGDone && !a.checkUpdatesOnStartupFGDone {
		if a.checkUpdatesErr == nil && a.checkUpdatesNewVersion != version {
			a.popupManager.Open["Check for updates"] = true
		}
		a.checkUpdatesOnStartupFGDone = true
	}
	a.checkUpdatesLock.Unlock()

	imgui.SetNextWindowPosV(imgui.MainViewport().Center(), imgui.CondAlways, imgui.NewVec2(0.5, 0.5))
	a.popupManager.Popup("Check for updates", func(close func()) {
		a.checkUpdatesLock.Lock()
		if a.checkUpdatesErr == nil {
			if a.checkingForUpdates {
				imgui.ProgressBarV(-1*float32(imgui.Time()), imutils.SVec2(250, 0), "Checking for updates")
			} else if a.checkUpdatesNewVersion == version {
				imutils.Textcf(imgui.NewVec4(0, 0.7, 0, 1), fnt.I.Check+"Up-to-date (version %v)", version)
			} else {
				imutils.Textcf(imgui.NewVec4(0.8, 0.8, 0, 1), fnt.I.Exclamation+"New fork release available: %v", a.checkUpdatesNewVersion)
				imgui.TextLinkOpenURLV(fnt.I.OpenInBrowser+" Download from Browser", a.checkUpdatesDownloadURL)
				imgui.TextWrapped("Updates are downloaded manually from this fork; Filediver will not replace its executable.")
			}
		} else {
			imutils.TextError(a.checkUpdatesErr)
		}
		a.checkUpdatesLock.Unlock()
		imgui.Separator()
		if imgui.ButtonV("Close", imutils.SVec2(250, 0)) {
			close()
		}
	}, imgui.WindowFlagsAlwaysAutoResize, true)
}

func (a *guiApp) drawWelcomePopup() {
	viewport := imgui.MainViewport()
	imgui.SetNextWindowPosV(viewport.Center(), imgui.CondAlways, imgui.NewVec2(0.5, 0.5))
	a.popupManager.Popup("Welcome to Filediver GUI", func(close func()) {
		imgui.PushTextWrapPos()
		imgui.TextUnformatted("Filediver GUI is still work-in-progress.")
		imgui.TextUnformatted("If you encounter any bugs, please create an issue on the project's GitHub page.")
		imgui.Separator()
		imgui.TextLinkOpenURLV("Filediver Issues", "https://github.com/xypwn/filediver/issues")
		imgui.Separator()
		if imgui.ButtonV("OK", imutils.SVec2(300, 0)) {
			close()
		}
	}, imgui.WindowFlagsAlwaysAutoResize, true)
}

func (a *guiApp) drawUnitPreviewTestPopup() {
	viewport := imgui.MainViewport()
	imgui.SetNextWindowPosV(viewport.Center(), imgui.CondAlways, imgui.NewVec2(0.5, 0.5))
	a.popupManager.Popup("Running Unit Preview Load Test", func(close func()) {
		buttonLabel := "Cancel"
		imgui.PushTextWrapPos()
		if a.unitTestIdx < len(a.allUnits) {
			imutils.Textf("[%v/%v] Previewing %v.unit", a.unitTestIdx+1, len(a.allUnits), filepath.Base(a.gameData.LookupHash(a.allUnits[a.unitTestIdx])))
		} else {
			imutils.Textf("[%v/%v] Done", a.unitTestIdx, len(a.allUnits))
		}
		imgui.ProgressBar(float32(a.unitTestIdx+1) / float32(len(a.allUnits)))
		errorFiles := slices.SortedFunc(maps.Keys(a.unitTestErrors), stingray.Hash.Cmp)
		if len(a.unitTestErrors) > 0 {
			imgui.Separator()
			if imgui.Button("Copy Errors") {
				builder := strings.Builder{}
				for _, file := range errorFiles {
					builder.WriteString(fmt.Sprintf("%v.unit: %v\n", a.gameData.LookupHash(file), a.unitTestErrors[file]))
				}
				imgui.SetClipboardText(builder.String())
				a.unitTestErrorsCopied = true
			}
			if a.unitTestErrorsCopied {
				imgui.SetItemTooltip("Copied!")
			}
		}
		for _, file := range errorFiles {
			imutils.TextError(fmt.Errorf("%v.unit: %v", filepath.Base(a.gameData.LookupHash(file)), a.unitTestErrors[file]))
		}
		imgui.Separator()
		if !a.runUnitPreviewLoadingTest {
			buttonLabel = "Close"
		}
		if imgui.ButtonV(buttonLabel, imutils.SVec2(300, 0)) {
			a.runUnitPreviewLoadingTest = false
			close()
		}
	}, imgui.WindowFlagsAlwaysAutoResize, false)
}

func (a *guiApp) drawExtensionsWarningPopup() {
	viewport := imgui.MainViewport()
	imgui.SetNextWindowPosV(viewport.Center(), imgui.CondAlways, imgui.NewVec2(0.5, 0.5))
	a.popupManager.Popup("Missing or out of date extensions", func(close func()) {
		imgui.PushTextWrapPos()
		imgui.TextUnformatted("The following recommended extensions are missing or out of date:")
		if !a.ffmpegExtensionWidget.HaveRequestedVersion() {
			imgui.TextWrapped("FFmpeg")
			imgui.Indent()
			imgui.TextWrapped(ffmpegFeatures)
			imgui.Unindent()
		}
		if !a.scriptsDistExtensionWidget.HaveRequestedVersion() {
			imgui.TextWrapped("ScriptsDist")
			imgui.Indent()
			imgui.TextWrapped(scriptsDistFeatures)
			imgui.Unindent()
		}
		imgui.TextUnformatted("You can download these extensions by clicking \"Manage extensions\", or by going to Settings->Extensions.")
		imgui.Separator()
		if imgui.ButtonV("Close", imutils.SVec2(100, 0)) {
			close()
		}
		imgui.SameLine()
		if imgui.ButtonV("Manage extensions", imutils.SVec2(120, 0)) {
			close()
			a.popupManager.Open["Extensions"] = true
		}
	}, imgui.WindowFlagsAlwaysAutoResize, true)
}

func (a *guiApp) drawExtensionsPopup() {
	viewport := imgui.MainViewport()
	imgui.SetNextWindowPosV(viewport.Center(), imgui.CondAlways, imgui.NewVec2(0.5, 0.5))
	if a.popupManager.Popup("Extensions", func(close func()) {
		a.ffmpegExtensionWidget.DrawAndUpdate("FFmpeg", ffmpegFeatures)
		imgui.Separator()
		a.scriptsDistExtensionWidget.DrawAndUpdate("ScriptsDist", scriptsDistFeatures)
		imgui.Separator()
		if imgui.ButtonV(fnt.I.FolderOpen+" Open extensions folder", imgui.NewVec2(imgui.ContentRegionAvail().X, 0)) {
			_ = os.MkdirAll(a.downloadsDir, os.ModePerm)
			if err := open.Start(a.downloadsDir); err != nil {
				a.showErrorPopup(err)
			}
		}
		imgui.Separator()
		if imgui.ButtonV("Close", imgui.NewVec2(imgui.ContentRegionAvail().X, 0)) {
			close()
		}
	}, imgui.WindowFlagsAlwaysAutoResize, true).Closed {
		a.redetectRunnerProgs()
	}
}

func (a *guiApp) drawPreferencesPopup(state *imgui_wrapper.State) {
	viewport := imgui.MainViewport()
	imgui.SetNextWindowPosV(viewport.Center(), imgui.CondAlways, imgui.NewVec2(0.5, 0.5))

	// Bink2 info popup
	bink2PopupBody := func() {
		imutils.Textf("The patched FFmpeg version's bink2 video support has\nbugs and produces video artifacts.")
		imutils.Textf("To get a clean video, export as bk2 and convert the resulting\nfile using RAD Video Tools.")
		imgui.Bullet()
		imgui.TextLinkOpenURLV("RAD Video Tools Download", "https://www.radgametools.com/bnkdown.htm")
	}
	a.popupManager.Popup("MP4 video export info", func(close func()) {
		bink2PopupBody()
		imgui.Separator()
		if imgui.ButtonV("OK", imgui.NewVec2(imgui.ContentRegionAvail().X, 0)) {
			close()
		}
	}, 0, false)
	a.popupManager.Popup("Allow video export as MP4", func(close func()) {
		bink2PopupBody()
		imgui.Separator()
		imutils.CenterAlignButtons("Allow anyway", "Cancel")
		if imgui.ButtonV("Allow anyway", imgui.NewVec2(0, 0)) {
			a.preferences.AllowVideoMP4Export = true
			if err := a.preferences.Save(a.preferencesPath); err != nil {
				a.showErrorPopup(err)
			}
			close()
		}
		imgui.SameLine()
		if imgui.ButtonV("Cancel", imgui.NewVec2(0, 0)) {
			close()
		}
	}, 0, false)

	a.popupManager.Popup("Preferences", func(close func()) {
		prevPrefs := a.preferences
		imutils.ComboChoice(
			"GUI Scale",
			&a.preferences.GUIScale,
			[]float32{0.5, 0.75, 1.0, 1.25, 1.5, 1.75, 2, 2.25, 2.5, 2.75, 3},
		)
		imutils.ComboChoice(
			"Target FPS",
			&a.preferences.TargetFPS,
			[]float64{15, 30, 60, 75, 90, 120, 144, 165, 244, 300},
		)
		imutils.ComboChoiceAny(
			"Video Preview Resolution",
			&a.preferences.PreviewVideoVerticalResolution,
			[]int{160, 240, 360, 480, 720, 1080, 1440, 2160},
			func(a, b int) bool { return a == b },
			func(x int) string { return fmt.Sprintf("%vp", x) },
		)
		imgui.SetItemTooltip(fnt.I.Info + ` The preview player isn't very well optimized, so higher
resolutions may cause low frame rates and poor responsiveness.`)
		imgui.Checkbox("Check for updates on start", &a.preferences.AutoCheckForUpdates)
		if runtime.GOOS == "windows" {
			// TODO(darwin): Add ffmpeg Linux builds so this can also be enabled on Linux
			imgui.SetItemTooltip(fnt.I.Info + " The patched FFmpeg version's bink2 video support has bugs and produces video artifacts.")
			mp4ExportCheckboxEnabled := a.preferences.AllowVideoMP4Export
			if imgui.Checkbox("Allow video export as MP4 "+fnt.I.Warning, &mp4ExportCheckboxEnabled) {
				if mp4ExportCheckboxEnabled {
					a.popupManager.Open["Allow video export as MP4"] = true
				} else {
					a.preferences.AllowVideoMP4Export = false
				}
			}
			if imgui.IsItemClickedV(imgui.MouseButtonRight) {
				a.popupManager.Open["MP4 video export info"] = true
			}
			imgui.SetItemTooltip(fnt.I.Warning + " FFmpeg produces video artifacts (right-click for more info)")
		}

		imgui.BeginDisabledV(a.preferences == a.defaultPreferences)
		if imgui.ButtonV(fnt.I.Undo+" Reset all preferences", imgui.NewVec2(imgui.ContentRegionAvail().X, 0)) {
			a.preferences = a.defaultPreferences
			if a.unitGallery != nil {
				if _, err := a.unitGallery.SetUnitPreviewAngle(a.defaultPreferences.UnitPreviewAngle, a.unitIDs); err != nil {
					a.showErrorPopup(err)
				}
			}
		}
		imgui.EndDisabled()

		if a.preferences != prevPrefs {
			state.GUIScale = a.preferences.GUIScale
			state.FrameRate = a.preferences.TargetFPS
			if err := a.preferences.Save(a.preferencesPath); err != nil {
				a.showErrorPopup(err)
			}
		}

		imgui.Separator()
		if imgui.ButtonV(fnt.I.FolderOpen+" Open preferences folder", imgui.NewVec2(imgui.ContentRegionAvail().X, 0)) {
			dir := filepath.Dir(a.preferencesPath)
			_ = os.MkdirAll(dir, os.ModePerm)
			if err := open.Start(dir); err != nil {
				a.showErrorPopup(err)
			}
		}
		if imgui.ButtonV(fnt.I.ReopenWindow+" Reset dock layout", imgui.NewVec2(imgui.ContentRegionAvail().X, 0)) {
			a.resetDockLayout = true
		}
		imgui.Separator()
		if imgui.ButtonV("Close", imgui.NewVec2(imgui.ContentRegionAvail().X, 0)) {
			close()
		}
	}, imgui.WindowFlagsAlwaysAutoResize, true)
}

func (a *guiApp) drawAboutPopup() {
	viewport := imgui.MainViewport()

	imgui.SetNextWindowSizeV(imutils.SVec2(400, 350), imgui.CondOnce)
	imgui.SetNextWindowSizeConstraints(imutils.SVec2(250, 170), viewport.Size())
	imgui.SetNextWindowPosV(viewport.Center(), imgui.CondAlways, imgui.NewVec2(0.5, 0.5))
	a.popupManager.Popup("Info", func(close func()) {
		imutils.PushFontScale(1.3)
		imgui.TextUnformatted("Filediver GUI")
		imgui.SameLine()
		imgui.TextLinkOpenURLV("(GitHub)", "https://github.com/xypwn/filediver")
		if a.iconImage != nil {
			if a.icon == nil {
				var err error
				a.icon, err = imutils.NewImage(a.iconImage)
				if err != nil {
					a.iconImage = nil // got invalid image => don't try to create again
				}
			}
			imgui.Image(a.icon.TextureRef, imutils.SVec2(64, 64))
		}
		imutils.PopFontScale()
		if version != "" {
			imutils.Textf("version: %v", version)
		} else {
			imgui.TextUnformatted("development version")
		}
		drawLicense := func(heading, body string) {
			if imgui.CollapsingHeaderBoolPtr(heading, nil) {
				imgui.PushTextWrapPos()
				imgui.PushFont(imgui_wrapper.FontMono(), 0)
				imgui.TextUnformatted(body)
				imgui.PopFont()
				imgui.PopTextWrapPos()
			}
		}
		imgui.Separator()
		imutils.PushFontScale(1.3)
		imgui.TextUnformatted("Resources")
		imutils.PopFontScale()
		imgui.TextLinkOpenURLV(fnt.I.Article+" Filediver Wiki", "https://github.com/xypwn/filediver/wiki")
		imgui.TextLinkOpenURLV(fnt.I.CollectionsBookmark+" HD2 Archive Labeling", "https://docs.google.com/spreadsheets/d/1oQys_OI5DWou4GeRE3mW56j7BIi4M7KftBIPAl1ULFw")
		imgui.TextLinkOpenURLV(fnt.I.Forum+" HD2 Archive Discord", "https://discord.gg/y7P38h2B9Q")
		imgui.Separator()
		imutils.PushFontScale(1.3)
		imgui.TextUnformatted("Licenses")
		imutils.PopFontScale()
		drawLicense("Filediver", license)
		imgui.Separator()
		if imgui.CollapsingHeaderBoolPtr("Fonts", nil) {
			imgui.Indent()
			drawLicense("Noto", fnt.TextFontLicense)
			drawLicense("Material Symbols", fnt.IconFontLicense)
			imgui.Unindent()
		}
		imgui.Separator()
		if imgui.ButtonV("Close", imgui.NewVec2(imgui.ContentRegionAvail().X, 0)) {
			close()
		}
	}, imgui.WindowFlagsNoMove, true)
}

func detectAndMaybeDeleteLegacyExtensions(downloadsDir string) error {
	dlDir, err := os.ReadDir(downloadsDir)
	if err != nil {
		return err
	}

	var toDelete []string
	for _, ent := range dlDir {
		ext := filepath.Ext(ent.Name())
		if ext != "" && ext != ".complete" && ext != ".tmp" {
			continue
		}
		name := strings.TrimSuffix(ent.Name(), ext)
		_, err := strconv.Atoi(name)
		if err == nil {
			toDelete = append(toDelete, filepath.Join(downloadsDir, ent.Name()))
		}
	}
	if len(toDelete) == 0 {
		return nil
	}

	if zenity.Question(
		fmt.Sprintf(
			"Delete unneeded legacy extensions?\nThe following files will be deleted:\n%v",
			strings.Join(toDelete, "\n"),
		),
		zenity.Title("Legacy extensions detected"),
	) == nil {
		for _, path := range toDelete {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		}
	}

	return nil
}

func getNewestVersion() (newVersion, downloadURL string, err error) {
	asset := github.ReleaseAsset{
		Repository: github.Repository{
			User: "mrbluehi",
			Repo: "evil-ai-filediver",
		},
	}
	if runtime.GOOS == "windows" {
		asset.Filename = "filediver-gui-windows.exe"
	} else {
		asset.Filename = "filediver-gui-linux"
	}
	info, err := asset.FetchInfo("")
	if err != nil {
		return "", "", err
	}
	return info.ResolvedVersion, info.DownloadUrl, nil
}

func main() {
	if (version == "" || version == "v0.0.0") && !(os.Getenv("FILEDIVER_BLENDER_IMPORTER_COMMAND") != "") {
		fmt.Println(`Development version detected.
To use blender importer, EITHER:
 - use 'go run -ldflags "-X main.version=v0.0.0" ./cmd/filediver-gui' (replace v0.0.0 with a real version)
 - set FILEDIVER_BLENDER_IMPORTER_COMMAND to a custom command (e.g. uv run scripts/hd2_accurate_blender_importer.py)`)
	}

	clipboardOk := clipboard.Init() == nil

	onError := func(err error) {
		fmt.Println("Error:", err)

		text := "Error: " + err.Error()
		if clipboardOk {
			clipboard.Write(clipboard.FmtText, []byte(text))
			text += "\n\n(error copied to clipboard)"
		}
		zenity.Info(text,
			zenity.Title("Internal Filediver error"),
			zenity.ErrorIcon,
		)
	}

	defer func() {
		if err := recover(); err != nil {
			onError(fmt.Errorf("panic: %v\nstack trace: %s", err, debug.Stack()))
		}
	}()

	app := newGUIApp(onError)
	defer app.Delete()
	if err := imgui_wrapper.Main("Filediver GUI", imgui_wrapper.Options{
		WindowSize:     imgui.NewVec2(800, 700),
		WindowMinSize:  imgui.NewVec2(250, 150),
		OnInitWindow:   app.onInitWindow,
		OnPreDraw:      app.onPreDraw,
		OnDraw:         app.onDraw,
		GLDebugContext: true,
		Icons: []image.Image{
			appicons.Icon128Img(),
			appicons.Icon64Img(),
			appicons.Icon48Img(),
			appicons.Icon32Img(),
			appicons.Icon24Img(),
			appicons.Icon16Img(),
		},
		IniFilename: filepath.Join(xdg.DataHome, "filediver", "dearimgui.ini"),
	}); err != nil {
		onError(err)
		return
	}
}
