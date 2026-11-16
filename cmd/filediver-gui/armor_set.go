package main

// Armor Set window: starting from any armor piece (the unit open in Preview, or a search),
// shows the whole set it belongs to (armor + helmet, both body types, every piece), its
// name, description, rarity and passive, assembles the set in Preview, and selects all of
// its files for export.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/adrg/xdg"
	filediverapp "github.com/xypwn/filediver/app"
	fnt "github.com/xypwn/filediver/cmd/filediver-gui/fonts"
	"github.com/xypwn/filediver/cmd/filediver-gui/imutils"
	"github.com/xypwn/filediver/cmd/filediver-gui/widgets"
	"github.com/xypwn/filediver/cmd/filediver-gui/widgets/previews"
	datalib "github.com/xypwn/filediver/datalibrary"
	"github.com/xypwn/filediver/stingray"
)

var armorSetWindowName = fnt.I.Checkroom + " Armor Set"
var unitTypeHash = stingray.Sum("unit")

type armorPiece struct {
	unit stingray.Hash
	data datalib.UnitData
	kit  stingray.Hash // kit archive
}

type armorKitGroup struct { // one set: every kit sharing a set id
	setID uint32
	kits  []datalib.ArmorSet // armor, helmet (and more for multi-variant sets)
	name  string
}

type armorSetWindow struct {
	built          bool
	gameData       *GameData
	gallery        *previews.UnitGallery
	grid           bool
	gridIDs        []stingray.FileID
	gridNames      map[stingray.FileID]string
	gridKits       map[stingray.FileID]uint32
	dockConfigured bool
	byPiece        map[stingray.Hash][]stingray.Hash // piece unit -> kit archives using it
	bySet          map[uint32][]stingray.Hash        // set id -> kit archives
	useCount       map[stingray.Hash]int             // piece unit -> number of kits using it
	kitByName      []datalib.ArmorSet                // sorted for search

	followPreview    bool
	query            string
	queryLower       string
	matches          []armorKitGroup // sets matching the current piece / query
	matchFrom        string
	selectedSet      int // index into matches
	replaceExport    bool
	status           string
	lastActive       stingray.FileID
	kitComparePath   string
	kitCompareData   *GameData
	kitCompareLoaded bool
	kitCompareError  string
}

// Armor labels inferred from repeated mesh/material references in the embedded
// kit table. Keep the confidence visible because shared geometry can represent
// multiple paint or equipment variants. Keys are the 32-bit kit IDs shown in
// the armor grid, not archive hashes.
var inferredArmorKitNames = map[uint32]string{
	0x34eeb43a: "GS-17 Frontier Marshal (inferred)",
	0xafafe11e: "Gilded Quill (inferred)",
	0x2cbae902: "AF-91 Field Chemist (likely variant)",
	0x5dd731d3: "AF-91 Field Chemist (likely variant)",
	0x7edc1ab2: "UF-84 Doubt Killer (likely variant)",
	0x6344ad57: "CM-10 Clinician / EX-00 Prototype X (possible variant)",
	0xefa2bd81: "SC-37 Legionnaire (likely variant)",
	0xe1e36c37: "SC-37 Legionnaire (likely variant)",
	0xb8f4fc49: "Unidentified cape (shared cape texture family)",
	0x6f218a9c: "Unidentified cape (6f218a9c)",
	0xc4ee0d2c: "Unidentified cape (c4ee0d2c)",
	0x9a331a31: "Unidentified cape (9a331a31)",
}

func armorGridDisplayName(kit datalib.ArmorSet, fallback string) string {
	if name, ok := inferredArmorKitNames[kit.Id.Value]; ok {
		return name
	}
	// Some table variants expose the short displayed identifier through the
	// archive hash while the thin kit ID differs. Cover those records too.
	switch kit.Archive.String() {
	case "0x2114ebf84a977c4c":
		return inferredArmorKitNames[0x34eeb43a]
	case "0x681a512c5a5c0de7":
		return inferredArmorKitNames[0x2cbae902]
	case "0xfda9b1496320364e":
		return inferredArmorKitNames[0x5dd731d3]
	case "0x0fecb336be1d287b":
		return inferredArmorKitNames[0x7edc1ab2]
	case "0x5b38ab7e20b4a1f1":
		return inferredArmorKitNames[0x6344ad57]
	case "0xd5f85cb5efb44cd2":
		return inferredArmorKitNames[0xefa2bd81]
	case "0xc72998936b1d88a2":
		return inferredArmorKitNames[0xe1e36c37]
	case "0x61c29419f57085c2":
		return inferredArmorKitNames[0xafafe11e]
	case "0x48b5c01fd8f2ac64":
		return inferredArmorKitNames[0xb8f4fc49]
	}
	if strings.TrimSpace(fallback) == "" {
		return fmt.Sprintf("Unknown armor/cape %08x", kit.Id.Value)
	}
	return fallback
}

func armorKitVersionChanges(current, previous map[stingray.Hash]datalib.ArmorSet) (added, changed, removed map[stingray.FileID]struct{}, removedKits map[stingray.FileID]uint32, removedNames map[stingray.FileID]string) {
	added = make(map[stingray.FileID]struct{})
	changed = make(map[stingray.FileID]struct{})
	removed = make(map[stingray.FileID]struct{})
	removedKits = make(map[stingray.FileID]uint32)
	removedNames = make(map[stingray.FileID]string)
	type groupKey struct {
		setID   uint32
		archive stingray.Hash // SetId 0 records are independent entries.
	}
	groups := func(table map[stingray.Hash]datalib.ArmorSet) map[groupKey][]datalib.ArmorSet {
		out := make(map[groupKey][]datalib.ArmorSet)
		for archive, kit := range table {
			if kit.Type != datalib.KitArmor && kit.Type != datalib.KitCape {
				continue
			}
			key := groupKey{setID: kit.SetId}
			if kit.SetId == 0 {
				key.archive = archive
			}
			out[key] = append(out[key], kit)
		}
		for key := range out {
			slices.SortFunc(out[key], func(a, b datalib.ArmorSet) int {
				if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
					return c
				}
				return a.Archive.Cmp(b.Archive)
			})
		}
		return out
	}
	currentGroups, previousGroups := groups(current), groups(previous)
	for key, now := range currentGroups {
		representative := now[0] // The grid displays one card for each set.
		id := stingray.NewFileID(representative.Archive, previews.ArmorSetTypeHash)
		before, exists := previousGroups[key]
		if !exists {
			added[id] = struct{}{}
			continue
		}
		oldByArchive := make(map[stingray.Hash]datalib.ArmorSet, len(before))
		for _, kit := range before {
			oldByArchive[kit.Archive] = kit
		}
		changedGroup := len(now) != len(before)
		for _, kit := range now {
			old, found := oldByArchive[kit.Archive]
			if !found || kit.Id != old.Id || kit.SetId != old.SetId || kit.Type != old.Type || kit.Name != old.Name || !reflect.DeepEqual(kit.UnitMetadata, old.UnitMetadata) {
				changedGroup = true
				break
			}
		}
		if changedGroup {
			changed[id] = struct{}{}
		}
	}
	for key, before := range previousGroups {
		if _, exists := currentGroups[key]; exists {
			continue
		}
		representative := before[0]
		id := stingray.NewFileID(representative.Archive, previews.ArmorSetTypeHash)
		removed[id] = struct{}{}
		removedKits[id] = representative.Id.Value
		removedNames[id] = representative.Name
	}
	return
}

// ensureArmorComparisonGallery compares the compared/new kit table against
// the selected/old build. Removed armor is already present in the selected
// build and can use its own renderer and thumbnail source.
func (a *guiApp) ensureArmorComparisonGallery(w *armorSetWindow, gd *GameData) error {
	if w.gallery == nil || !a.olderBuildArmorTableValid {
		return nil
	}
	added, changed, removed, removedKits, removedNames := armorKitVersionChanges(a.olderBuildArmorSets, gd.ArmorSets)
	w.gallery.SetVersionChanges(added, changed, removed)

	for id, kitID := range removedKits {
		if _, alreadyPresent := w.gridNames[id]; !alreadyPresent {
			w.gridIDs = append(w.gridIDs, id)
			w.gridNames[id] = removedNames[id]
			w.gridKits[id] = kitID
		}
		w.gallery.AddArmorKit(id, kitID)
	}
	return nil
}

// openRemovedArmorPreview shows a set that exists in the selected build but
// not in the compared newer build. The selected build already owns its model,
// textures, renderer, and thumbnail pipeline.
func (a *guiApp) openRemovedArmorPreview(w *armorSetWindow, gd *GameData, id stingray.FileID) {
	a.activateCurrentPreview()
	previous := a.preview.ActiveID()
	if a.preview == nil || !a.preview.ShowArmorSet(w.gridKits[id], id.Name) {
		w.status = "This armor set could not be rendered from the selected build."
		return
	}
	a.historyPush(previous, a.preview.ActiveID())
	a.scrollToSelectedFile = true
	w.lastActive = a.preview.ActiveID()
	w.status = "Removed armor loaded from the selected build; its thumbnail uses the same assets."
	imgui.SetWindowFocusStr(fnt.I.Preview + " Preview")
}

// ensureArmorKitTableComparison reads only the compared build's kit table.
// It does not scan that build's whole resource tree.
func (a *guiApp) ensureArmorKitTableComparison(w *armorSetWindow, gd *GameData) error {
	path := strings.TrimSpace(a.unitComparePath)
	if path == "" {
		path = strings.TrimSpace(a.preferences.UnitComparePath)
	}
	if path == "" {
		return fmt.Errorf("select the newer compared build folder to load its armor kit table")
	}
	if w.kitCompareData == gd && w.kitComparePath == path && w.kitCompareLoaded {
		if w.kitCompareError != "" {
			return fmt.Errorf("%s", w.kitCompareError)
		}
		return nil
	}
	sets, err := filediverapp.LoadArmorSetsForGameDir(path, gd.LanguageMap)
	w.kitCompareLoaded = true
	w.kitCompareData = gd
	w.kitComparePath = path
	w.kitCompareError = ""
	if err != nil {
		a.olderBuildArmorSets = nil
		a.olderBuildArmorTableValid = false
		w.kitCompareError = err.Error()
		return err
	}
	a.olderBuildArmorSets = sets
	a.olderBuildArmorTableValid = true
	return nil
}

func (w *armorSetWindow) build(gd *GameData) {
	if w.gallery != nil {
		w.gallery.Delete()
		w.gallery = nil
	}
	w.gameData = gd
	w.matches = nil
	w.lastActive = stingray.FileID{}
	w.selectedSet = 0
	w.byPiece = map[stingray.Hash][]stingray.Hash{}
	w.bySet = map[uint32][]stingray.Hash{}
	w.useCount = map[stingray.Hash]int{}
	w.kitByName = w.kitByName[:0]
	for archive, set := range gd.ArmorSets {
		for unit := range set.UnitMetadata {
			w.byPiece[unit] = append(w.byPiece[unit], archive)
			w.useCount[unit]++
		}
		w.bySet[set.SetId] = append(w.bySet[set.SetId], archive)
		w.kitByName = append(w.kitByName, set)
	}
	slices.SortFunc(w.kitByName, func(a, b datalib.ArmorSet) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return a.Archive.Cmp(b.Archive)
	})
	w.built = true
	w.grid = true
	w.followPreview = true
	w.replaceExport = true
}

// group collects the kits that share the set id of this kit (cape kits have set id 0 and stand alone).
func (w *armorSetWindow) group(gd *GameData, kit datalib.ArmorSet) armorKitGroup {
	g := armorKitGroup{setID: kit.SetId}
	if kit.SetId == 0 {
		g.kits = []datalib.ArmorSet{kit}
	} else {
		for _, a := range w.bySet[kit.SetId] {
			g.kits = append(g.kits, gd.ArmorSets[a])
		}
	}
	slices.SortFunc(g.kits, func(a, b datalib.ArmorSet) int {
		if a.Type != b.Type {
			return int(a.Type) - int(b.Type) // Armor, Helmet, Cape
		}
		return a.Archive.Cmp(b.Archive)
	})
	g.name = g.kits[0].Name
	for _, k := range g.kits {
		if k.Type != datalib.KitCape && len(k.Name) > len(g.name) {
			g.name = k.Name
		}
	}
	return g
}

func (w *armorSetWindow) setMatches(gd *GameData, archives []stingray.Hash, from string) {
	seen := map[uint32]bool{}
	w.matches = w.matches[:0]
	for _, a := range archives {
		kit, ok := gd.ArmorSets[a]
		if !ok {
			continue
		}
		key := kit.SetId
		if key == 0 {
			key = kit.Id.Value
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		w.matches = append(w.matches, w.group(gd, kit))
	}
	slices.SortFunc(w.matches, func(a, b armorKitGroup) int { return strings.Compare(a.name, b.name) })
	w.matchFrom = from
	w.selectedSet = 0
}

// search matches a name fragment, a kit/set id, a piece/kit-archive hash, or a unit path.
func (w *armorSetWindow) search(gd *GameData) {
	q := strings.TrimSpace(w.query)
	w.queryLower = strings.ToLower(q)
	if q == "" {
		w.matches = w.matches[:0]
		w.matchFrom = ""
		return
	}
	var archives []stingray.Hash
	if h, err := stingray.ParseHash(strings.TrimPrefix(strings.ToLower(q), "0x")); err == nil && len(strings.TrimPrefix(strings.ToLower(q), "0x")) == 16 {
		if _, ok := gd.ArmorSets[h]; ok {
			archives = append(archives, h)
		}
		archives = append(archives, w.byPiece[h]...)
	} else if n, err := strconv.ParseUint(q, 10, 32); err == nil {
		for a, kit := range gd.ArmorSets {
			if uint64(kit.Id.Value) == n || uint64(kit.SetId) == n {
				archives = append(archives, a)
			}
		}
	} else if strings.Contains(q, "/") {
		archives = append(archives, w.byPiece[stingray.Sum(q)]...)
	} else {
		for _, kit := range w.kitByName {
			if kit.Type != datalib.KitArmor && kit.SetId != 0 {
				hasArmor := false
				for _, archive := range w.bySet[kit.SetId] {
					if gd.ArmorSets[archive].Type == datalib.KitArmor {
						hasArmor = true
						break
					}
				}
				if hasArmor {
					continue
				}
			}
			if strings.Contains(strings.ToLower(kit.Name), w.queryLower) || strings.Contains(strings.ToLower(kit.Description), w.queryLower) {
				archives = append(archives, kit.Archive)
			}
		}
	}
	w.setMatches(gd, archives, "search")
}

func (w *armorSetWindow) pieces(g armorKitGroup) []armorPiece {
	out := make([]armorPiece, 0)
	for _, kit := range g.kits {
		for unit, data := range kit.UnitMetadata {
			out = append(out, armorPiece{unit: unit, data: data, kit: kit.Archive})
		}
	}
	slices.SortFunc(out, func(a, b armorPiece) int {
		if a.data.BodyType != b.data.BodyType {
			return int(a.data.BodyType) - int(b.data.BodyType)
		}
		if a.data.Slot != b.data.Slot {
			return int(a.data.Slot) - int(b.data.Slot)
		}
		if a.data.Type != b.data.Type {
			return int(a.data.Type) - int(b.data.Type)
		}
		return a.unit.Cmp(b.unit)
	})
	return out
}

// setFiles lists every game file a set is made of: the piece units plus the LUT/decal textures
// the table names for them. Materials and textures of the units are pulled in by the unit
// exporter itself.
func (w *armorSetWindow) setFiles(gd *GameData, g armorKitGroup) (files []stingray.FileID, missing int) {
	texType := stingray.Sum("texture")
	seen := map[stingray.FileID]bool{}
	add := func(name stingray.Hash, typ stingray.Hash) {
		if name.Value == 0 {
			return
		}
		id := stingray.NewFileID(name, typ)
		if seen[id] {
			return
		}
		seen[id] = true
		if _, ok := gd.DataDir.Files[id]; !ok {
			missing++
			return
		}
		files = append(files, id)
	}
	for _, p := range w.pieces(g) {
		if p.unit.Value == 0 && gd.ArmorSets[p.kit].Type == datalib.KitCape {
			add(stingray.Sum("content/fac_helldivers/capes/medium_cape"), unitTypeHash)
		} else {
			add(p.unit, unitTypeHash)
		}
		for _, t := range []stingray.Hash{p.data.MaterialLut, p.data.PatternLut, p.data.CapeLut, p.data.CapeGradient, p.data.CapeNac, p.data.DecalScalarFields, p.data.BaseData, p.data.DecalSheet} {
			add(t, texType)
		}
		for _, c := range p.data.CamoflagePieces {
			add(c.MaterialLut, texType)
		}
	}
	return
}

func (a *guiApp) drawArmorSetWindow() {
	if !a.isArmorSetOpen {
		if a.armorSet.gallery != nil {
			a.armorSet.gallery.Suspend()
		}
		return
	}
	defer imgui.End()
	if !a.armorSet.dockConfigured && a.unitGalleryDockID != 0 {
		imgui.SetNextWindowDockIDV(a.unitGalleryDockID, imgui.CondFirstUseEver)
		a.armorSet.dockConfigured = true
	}
	if !imgui.BeginV(armorSetWindowName, &a.isArmorSetOpen, 0) {
		if a.armorSet.gallery != nil {
			a.armorSet.gallery.Suspend()
		}
		return
	}
	if a.gameData == nil || a.preview == nil {
		imgui.TextUnformatted("Loading game data...")
		return
	}
	gd := a.gameData
	w := &a.armorSet
	if !w.built || w.gameData != gd {
		w.build(gd)
	}
	if err := a.ensureArmorKitTableComparison(w, gd); err != nil {
		w.status = "Armor table comparison unavailable: " + err.Error()
	}
	if w.gallery == nil {
		gallery, err := a.preview.NewUnitGallery()
		if err != nil {
			imutils.TextError(err)
			return
		}
		w.gridIDs = nil
		w.gridNames = make(map[stingray.FileID]string)
		w.gridKits = make(map[stingray.FileID]uint32)
		unavailable := make([]stingray.FileID, 0)
		seen := make(map[uint32]bool)
		for _, kit := range w.kitByName {
			if kit.SetId != 0 && seen[kit.SetId] {
				continue
			}
			if kit.SetId != 0 {
				seen[kit.SetId] = true
			}
			group := w.group(gd, kit)
			id := stingray.NewFileID(kit.Archive, previews.ArmorSetTypeHash)
			w.gridIDs = append(w.gridIDs, id)
			w.gridNames[id] = armorGridDisplayName(kit, group.name)
			w.gridKits[id] = kit.Id.Value
			available := false
			for _, member := range group.kits {
				for hash := range member.UnitMetadata {
					if hash.Value == 0 && member.Type == datalib.KitCape {
						hash = stingray.Sum("content/fac_helldivers/capes/medium_cape")
					}
					if hash.Value != 0 {
						if _, ok := gd.DataDir.Files[stingray.NewFileID(hash, unitTypeHash)]; ok {
							available = true
							break
						}
					}
				}
				if available {
					break
				}
			}
			if !available {
				unavailable = append(unavailable, id)
			}
		}
		identity := filepath.Clean(gd.DataDir.Path)
		if build := gd.GameBuildInfo; build != nil {
			identity += "\n" + build.Combined + "\n" + build.Commit + "\n" + build.Hash
		}
		sum := sha256.Sum256([]byte(identity))
		if err := gallery.ConfigureArmorGrid(filepath.Join(xdg.CacheHome, "filediver", "armor-thumbnails-v1", hex.EncodeToString(sum[:])), w.gridKits); err != nil {
			gallery.Delete()
			imutils.TextError(err)
			return
		}
		for _, id := range unavailable {
			gallery.MarkArmorUnavailable(id, "No nonzero armor mesh from this set is present in the selected game build; the name may be an unresolved localization ID.")
		}
		w.gallery = gallery
	}
	if a.olderBuildArmorTableValid {
		if err := a.ensureArmorComparisonGallery(w, gd); err != nil {
			w.status = "Armor table comparison unavailable: " + err.Error()
		}
	}
	if imgui.Button("Grid") {
		w.grid = true
	}
	imgui.SameLine()
	if imgui.Button("Set details") {
		w.grid = false
	}
	if w.grid {
		if imgui.Button("Set preview angle") {
			camera, ok := a.preview.UnitCamera()
			if !ok {
				camera = previews.DefaultUnitCamera()
			}
			if _, err := w.gallery.SetUnitPreviewAngle(camera.Rotation, w.gridIDs); err != nil {
				a.showErrorPopup(err)
			}
		}
		imgui.SetItemTooltip("Use the Preview camera angle and recapture existing armor thumbnails")
		if !a.olderBuildArmorTableValid {
			imgui.TextDisabled("Choose a newer build in Compare to load its kit table and enable New, Changed, and Old filters.")
		} else {
			armorAdded, armorChanged, armorRemoved, _, _ := armorKitVersionChanges(a.olderBuildArmorSets, gd.ArmorSets)
			imgui.TextDisabled(fmt.Sprintf("One-time kit-table comparison: %d new · %d changed · %d old", len(armorAdded), len(armorChanged), len(armorRemoved)))
		}
		if w.status != "" {
			imgui.TextDisabled(w.status)
		}
		id, selected := w.gallery.DrawGrid(w.gridIDs, a.preview.ActiveID(), func(id stingray.FileID) string { return w.gridNames[id] }, nil, func(stingray.FileID) previews.UnitGalleryStats { return previews.UnitGalleryStats{} })
		if selected {
			_, _, removed, _, _ := armorKitVersionChanges(a.olderBuildArmorSets, gd.ArmorSets)
			if _, isRemoved := removed[id]; isRemoved {
				a.openRemovedArmorPreview(w, gd, id)
			} else {
				a.activateCurrentPreview()
				if a.preview.ShowArmorSet(w.gridKits[id], id.Name) {
					w.setMatches(gd, []stingray.Hash{id.Name}, "set "+w.gridNames[id])
					w.lastActive = a.preview.ActiveID()
					imgui.SetWindowFocusStr(fnt.I.Preview + " Preview")
				}
			}
		}
		w.gallery.DrawCaptureFrame()
		return
	}
	w.gallery.Suspend()

	// ---- input: the piece in Preview, or a search
	imutils.Textf("%v kits, from %v", len(gd.ArmorSets), gd.ArmorSetsSource)
	if imgui.IsItemHovered() {
		imgui.SetTooltip("Release builds ship their tables in plain form and are read directly.\nLater builds seal them: set FILEDIVER_HD2_KEYFILE to a key file (e.g. SHODAN's gamedata.lua)\nbefore starting filediver to read those; otherwise the live table embedded in filediver is used.")
	}
	imgui.Checkbox("Follow Preview", &w.followPreview)
	imgui.SetItemTooltip("Look up whatever unit is open in Preview (click a piece in the Browser or the Model Grid)")
	imgui.SameLine()
	imgui.SetNextItemWidth(-imgui.CalcTextSize("Search").X - 2*imgui.CurrentStyle().FramePadding().X)
	if imgui.InputTextWithHint("##armorsearch", "name, kit/set id, piece or archive hash, unit path", &w.query, imgui.InputTextFlagsEnterReturnsTrue, nil) {
		w.followPreview = false
		w.search(gd)
	}
	imgui.SameLine()
	if imgui.Button("Search") {
		w.followPreview = false
		w.search(gd)
	}
	active := a.preview.ActiveID()
	if w.followPreview && active != w.lastActive {
		w.lastActive = active
		if active.Type == unitTypeHash {
			w.setMatches(gd, w.byPiece[active.Name], "piece "+gd.LookupHash(active.Name))
		} else if active.Type == previews.ArmorSetTypeHash {
			// our own assembled set: keep what is shown
		} else {
			w.matches = w.matches[:0]
			w.matchFrom = ""
		}
	}

	if len(w.matches) == 0 {
		imgui.PushTextWrapPos()
		switch {
		case w.matchFrom != "":
			imutils.Textf("%v: not part of any armor kit in this game build's table.", w.matchFrom)
			if active.Type == unitTypeHash {
				imgui.TextUnformatted("Archives holding this unit (its siblings are the other units in them):")
				for _, fi := range gd.DataDir.Files[active] {
					imutils.Textf("  %v (%v files)", gd.LookupHash(fi.ArchiveID), len(gd.DataDir.Archives[fi.ArchiveID]))
				}
			}
		case w.followPreview:
			imgui.TextUnformatted("Open an armor piece in Preview (Browser search e.g. \"armor\", or the Model Grid), or search above.")
		default:
			imgui.TextUnformatted("Type a set name (e.g. ravager), a kit or set id, a piece hash, or a unit path, then Enter.")
		}
		imgui.PopTextWrapPos()
		return
	}

	// ---- which set (a shared mesh belongs to several)
	if len(w.matches) > 1 {
		imutils.Textf("%v sets use this (shared mesh; the unique pieces below tell sets apart):", len(w.matches))
		names := make([]string, len(w.matches))
		for i, g := range w.matches {
			names[i] = fmt.Sprintf("%v  (set %v)", g.name, g.setID)
		}
		if w.selectedSet >= len(names) {
			w.selectedSet = 0
		}
		imgui.SetNextItemWidth(-1)
		if imgui.BeginCombo("##setpick", names[w.selectedSet]) {
			for i, n := range names {
				if imgui.SelectableBool(n) {
					w.selectedSet = i
				}
			}
			imgui.EndCombo()
		}
	}
	g := w.matches[w.selectedSet]
	linkedCapes := make([]datalib.ArmorSet, 0)
	for _, kit := range g.kits {
		if kit.Type == datalib.KitCape {
			linkedCapes = append(linkedCapes, kit)
		}
	}
	if len(linkedCapes) > 0 && g.setID != 0 {
		chosen := linkedCapes[0]
		for _, cape := range linkedCapes {
			if cape.Archive == a.preview.ActiveID().Name {
				chosen = cape
				break
			}
		}
		imgui.SetNextItemWidth(-1)
		if imgui.BeginCombo("##linkedcape", "Linked cape: "+chosen.Name) {
			for _, cape := range linkedCapes {
				imgui.PushIDStr(cape.Archive.String())
				if imgui.SelectableBool(cape.Name) {
					if a.preview.ShowArmorSet(cape.Id.Value, cape.Archive) {
						w.lastActive = a.preview.ActiveID()
					}
				}
				imgui.PopID()
			}
			imgui.EndCombo()
		}
		imgui.SetItemTooltip("These capes share the set ID in the game data; a shared ID can include several armor variants.")
	}

	// ---- header: kits of the set
	imgui.Separator()
	for _, kit := range g.kits {
		imgui.PushIDStr(kit.Archive.String())
		imutils.Textf("%v %v", kit.Type.String(), kit.Name)
		imgui.SameLine()
		imgui.TextDisabled(fmt.Sprintf("kit %v  rarity %v", kit.Id.Value, kit.Rarity.String()))
		if kit.Passive != nil && kit.Passive.Name != "" {
			imgui.SameLine()
			imgui.TextDisabled(" passive " + kit.Passive.Name)
			if imgui.IsItemHovered() {
				imgui.SetTooltip(strings.Join(kit.Passive.ResolveDescription(), "\n"))
			}
		}
		imgui.SameLine()
		imgui.TextDisabled(" archive ")
		imgui.SameLine()
		imutils.CopyableTextfV(imutils.CopyableTextOptions{
			TooltipCopied: fnt.I.Check + " Copied", TooltipHovered: fnt.I.ContentCopy + " Click to copy the archive hash",
			ClipboardText: kit.Archive.String()}, "%v", kit.Archive.String())
		imgui.PopID()
	}
	if g.kits[0].Description != "" {
		imgui.PushTextWrapPos()
		imgui.TextDisabled(g.kits[0].Description)
		imgui.PopTextWrapPos()
	}

	// ---- actions
	if imgui.Button(fnt.I.Preview + " Assemble in Preview") {
		kit := g.kits[0]
		if a.preview.ShowArmorSet(kit.Id.Value, kit.Archive) {
			w.lastActive = a.preview.ActiveID()
			w.status = "Assembled " + g.name + " in Preview (Tree View shows the Any / Stocky / Slim groups)"
		} else {
			w.status = "Could not assemble this set"
		}
	}
	imgui.SetItemTooltip("Load every piece of the set (armor + helmet, all body types) in Preview with the set's own colours")
	imgui.SameLine()
	if imgui.Button(fnt.I.FileExport + " Select set files for export") {
		files, missing := w.setFiles(gd, g)
		if w.replaceExport {
			clear(a.filesSelectedForExport)
		}
		for _, id := range files {
			a.filesSelectedForExport[id] = struct{}{}
		}
		w.status = fmt.Sprintf("%v files selected for export", len(files))
		if missing > 0 {
			w.status += fmt.Sprintf(" (%v named in the table are not in this game build)", missing)
		}
		w.status += ". Use the Export window to write them."
	}
	imgui.SetItemTooltip("Tick the set's units and LUT/decal textures in the Browser's export selection. Units bring their materials and textures along when exported.")
	imgui.SameLine()
	imgui.Checkbox("replace current selection", &w.replaceExport)
	if w.status != "" {
		imgui.PushTextWrapPos()
		imgui.TextUnformatted(w.status)
		imgui.PopTextWrapPos()
	}

	// ---- pieces
	imgui.Separator()
	pcs := w.pieces(g)
	imutils.Textf("%v pieces", len(pcs))
	flags := imgui.TableFlagsBorders | imgui.TableFlagsRowBg | imgui.TableFlagsScrollY | imgui.TableFlagsSizingStretchProp | imgui.TableFlagsResizable
	if imgui.BeginTableV("##pieces", 6, flags, imgui.NewVec2(0, 0), 0) {
		imgui.TableSetupScrollFreeze(0, 1)
		imgui.TableSetupColumn("Body")
		imgui.TableSetupColumn("Slot")
		imgui.TableSetupColumn("Kind")
		imgui.TableSetupColumn("Weight")
		imgui.TableSetupColumn("Unit")
		imgui.TableSetupColumn("Used by")
		imgui.TableHeadersRow()
		for i, p := range pcs {
			imgui.PushIDInt(int32(i))
			imgui.TableNextRow()
			imgui.TableNextColumn()
			imgui.TextUnformatted(p.data.BodyType.String())
			imgui.TableNextColumn()
			imgui.TextUnformatted(p.data.Slot.String())
			imgui.TableNextColumn()
			imgui.TextUnformatted(p.data.Type.String())
			imgui.TableNextColumn()
			imgui.TextUnformatted(p.data.Weight.String())
			imgui.TableNextColumn()
			id := stingray.NewFileID(p.unit, unitTypeHash)
			if _, ok := gd.DataDir.Files[id]; ok {
				widgets.GamefileLinkTextF(id, "%v", gd.LookupHash(p.unit))
			} else {
				imgui.TextDisabled(gd.LookupHash(p.unit) + " (not in this build)")
			}
			imgui.TableNextColumn()
			if n := w.useCount[p.unit]; n <= 1 {
				imgui.TextUnformatted("unique")
			} else {
				imutils.Textf("%v kits", n)
				if imgui.IsItemHovered() {
					var tt strings.Builder
					for _, arc := range w.byPiece[p.unit] {
						k := gd.ArmorSets[arc]
						fmt.Fprintf(&tt, "%v %v\n", k.Type.String(), k.Name)
					}
					imgui.SetTooltip(strings.TrimSpace(tt.String()))
				}
			}
			imgui.PopID()
		}
		imgui.EndTable()
	}
}
