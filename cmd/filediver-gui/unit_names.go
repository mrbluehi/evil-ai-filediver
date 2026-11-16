package main

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"maps"
	"slices"
	"strconv"
	"strings"

	datalib "github.com/xypwn/filediver/datalibrary"
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/level"
	"github.com/xypwn/filediver/stingray/unit"
	"github.com/xypwn/filediver/stingray/unit/material"
)

type inferredUnitName struct {
	Display string
	Source  string
	Terrain bool
	Vista   bool
}

type unitNameVote struct {
	display string
	source  string
	rank    int
	count   int
}

type unitMountContext struct {
	parents map[string]struct{}
	roles   map[string]int
	sources map[string]struct{}
}

// inferUnitNames recovers readable names from game-authored relationships
// instead of adding a hand-written hash-to-name alias for each unit. The
// generated entity definitions provide localized type names and asset paths,
// while component links identify child units by their in-game role.
func (gd *GameData) inferUnitNames() map[stingray.Hash]inferredUnitName {
	result := make(map[stingray.Hash]inferredUnitName)
	if gd == nil || gd.DataDir == nil || gd.DataDir.IsHelldivers1 {
		return result
	}
	entities, _ := datalib.ParseEntityComponentSettings()

	unitType := datalib.Sum("UnitComponentData")
	localUnitType := datalib.Sum("LocalUnitComponentData")
	encyclopediaType := datalib.Sum("EncyclopediaEntryComponentData")
	characterNameType := datalib.Sum("CharacterNameComponentData")
	mountType := datalib.Sum("MountComponentData")
	effectType := datalib.Sum("EffectReferenceComponentData")
	equipmentType := datalib.Sum("EquipmentComponentData")
	weaponCustomizationType := datalib.Sum("WeaponCustomizationComponentData")
	chainType := datalib.Sum("TwoPointChainComponentData")
	votes := make(map[stingray.Hash]map[string]*unitNameVote)
	unitPathsByEntity := make(map[stingray.Hash]stingray.Hash)
	entityPaths := make(map[stingray.Hash]string)

	add := func(unitPath stingray.Hash, display, source string, rank int) {
		if unitPath.Value == 0 || display == "" || !gd.hasUnitResource(unitPath) {
			return
		}
		// A known path is already the best possible name; only infer labels for
		// resources the bundled path dictionary cannot resolve.
		if gd.LookupHash(unitPath) != unitPath.String() {
			return
		}
		display = cleanUnitLabel(display)
		if !usefulUnitLabel(display) {
			return
		}
		key := strings.ToLower(display)
		if votes[unitPath] == nil {
			votes[unitPath] = make(map[string]*unitNameVote)
		}
		vote := votes[unitPath][key]
		if vote == nil {
			vote = &unitNameVote{display: display, source: source, rank: rank}
			votes[unitPath][key] = vote
		}
		vote.count++
		if rank > vote.rank {
			vote.rank = rank
			vote.source = source
		}
	}

	// First collect direct semantic names, owning entity paths, and unit paths.
	// An entity resource path is a real game-authored identifier, so it is a
	// useful fallback for unit resources that have no path in the hash table.
	for entityPath, entity := range entities {
		entityPaths[entityPath] = gd.LookupHash(entityPath)
		unitPath := stingray.Hash{}
		unitComponent, hasUnitComponent := entity.Components[unitType].(datalib.UnitComponent)
		if hasUnitComponent {
			unitPath = unitComponent.UnitPath
		}
		if unitPath.Value == 0 {
			if localUnit, ok := entity.Components[localUnitType].(datalib.LocalUnitComponent); ok {
				unitPath = localUnit.UnitPath
			}
		}
		if unitPath.Value != 0 {
			unitPathsByEntity[entityPath] = unitPath
			if entityName := entityPaths[entityPath]; entityName != entityPath.String() {
				add(unitPath, unitPathLabel(entityName), "entity definition "+entityName, 1)
			}
		}
		if unitPath.Value == 0 {
			continue
		}

		if entry, ok := entity.Components[encyclopediaType].(datalib.EncyclopediaEntryComponent); ok {
			source := "EncyclopediaEntryComponentData.LocName " + strconv.FormatUint(uint64(entry.LocName), 10) + " on entity " + entityPaths[entityPath]
			add(unitPath, gd.LookupString(entry.LocName), source, 3)
		}
		if character, ok := entity.Components[characterNameType].(datalib.CharacterNameComponent); ok {
			source := "CharacterNameComponentData.CharacterName " + strconv.FormatUint(uint64(character.CharacterName), 10) + " on entity " + entityPaths[entityPath]
			add(unitPath, gd.LookupString(character.CharacterName), source, 2)
		}
	}

	// Prefer a localized name for a parent when the path-backed resource has
	// one. This gives mounted-part descriptions a human-readable context.
	bestExistingLabel := func(unitPath stingray.Hash) string {
		best := bestUnitNameVote(votes[unitPath])
		if best != nil && best.rank >= 2 {
			return best.display
		}
		path := gd.LookupHash(unitPath)
		if path != unitPath.String() {
			return unitPathLabel(path)
		}
		if best != nil {
			return best.display
		}
		return ""
	}

	// MountComponent.Path identifies an ADHD entity, not a unit resource.
	// Follow that entity's UnitComponent to reach the actual mounted unit. This
	// avoids confusing an entity hash with a model hash and recovers attachments
	// that otherwise remain anonymous.
	mountContexts := make(map[stingray.Hash]*unitMountContext)
	for entityPath, entity := range entities {
		parentUnit := unitPathsByEntity[entityPath]
		parentLabel := bestExistingLabel(parentUnit)
		if parentLabel == "" && entityPaths[entityPath] != entityPath.String() {
			parentLabel = unitPathLabel(entityPaths[entityPath])
		}
		if parentLabel != "" {
			if mounts, ok := entity.Components[mountType].(datalib.MountComponent); ok {
				for _, mount := range mounts.Infos {
					if mount.Path.Value == 0 {
						continue
					}
					mountedUnit := unitPathsByEntity[mount.Path]
					if mountedUnit.Value == 0 {
						continue
					}
					slotName := gd.LookupThinHash(mount.Name)
					attachNode := gd.LookupThinHash(mount.AttachNode)
					ctx := mountContexts[mountedUnit]
					if ctx == nil {
						ctx = &unitMountContext{parents: make(map[string]struct{}), roles: make(map[string]int), sources: make(map[string]struct{})}
						mountContexts[mountedUnit] = ctx
					}
					ctx.parents[parentLabel] = struct{}{}
					role := unitMountRole(slotName, attachNode)
					if role != "mounted part" {
						ctx.roles[role]++
					}
					source := "MountComponent on " + parentLabel
					if usefulUnitLabel(slotName) {
						source += " (slot " + slotName + ")"
					}
					if usefulUnitLabel(attachNode) {
						source += " (attach node " + attachNode + ")"
					}
					childEntity := entityPaths[mount.Path]
					if childEntity == "" {
						childEntity = mount.Path.String()
					}
					source += " (target entity " + childEntity + ")"
					ctx.sources[source] = struct{}{}
				}
			}

			if equipment, ok := entity.Components[equipmentType].(datalib.EquipmentComponent); ok {
				add(equipment.HolsterUnit, "holster for "+parentLabel, "equipment holster unit on "+parentLabel, 1)
			}
			if weapon, ok := entity.Components[weaponCustomizationType].(datalib.WeaponCustomizationComponent); ok {
				for _, part := range []struct {
					path stingray.Hash
					role string
				}{{weapon.OpticsPath, "optics"}, {weapon.MagazinePath, "magazine"}, {weapon.MagazineSecondaryPath, "secondary magazine"}, {weapon.MuzzlePath, "muzzle"}, {weapon.UnknownHash, "weapon attachment"}} {
					add(part.path, part.role+" for "+parentLabel, "weapon customization on "+parentLabel, 1)
				}
			}
			if effects, ok := entity.Components[effectType].(datalib.EffectReferenceComponent); ok {
				for _, effectUnit := range effects.Units {
					name := gd.LookupThinHash(effectUnit.ID)
					if usefulUnitLabel(name) {
						add(effectUnit.Path, name, "named spawned unit effect on "+parentLabel, 2)
					}
				}
			}
			if chain, ok := entity.Components[chainType].(datalib.TwoPointChainComponent); ok {
				add(chain.ChainUnitPath, "chain unit for "+parentLabel, "two point chain component on "+parentLabel, 1)
			}
		}
	}

	// Some game entities reuse their full enemy encyclopedia label on a
	// mounted weapon unit. When the same resource is explicitly mounted on that
	// enemy, prefer the component's mount role and parent instead of calling the
	// weapon by the owner's name (for example, Factory Strider's l_weapon_whole).
	for unitPath, ctx := range mountContexts {
		if len(ctx.parents) == 0 {
			continue
		}
		parents := slices.Collect(maps.Keys(ctx.parents))
		slices.Sort(parents)
		roles := slices.Collect(maps.Keys(ctx.roles))
		slices.SortFunc(roles, func(a, b string) int {
			if ctx.roles[a] != ctx.roles[b] {
				return cmp.Compare(ctx.roles[b], ctx.roles[a])
			}
			return strings.Compare(a, b)
		})
		role := "mounted part"
		if len(roles) > 0 {
			role = roles[0]
		}
		display := role + " on " + strings.Join(parents, " / ")
		sources := slices.Collect(maps.Keys(ctx.sources))
		slices.Sort(sources)
		add(unitPath, display, strings.Join(sources, "; "), 5)
	}

	// Finally use the actual model's mesh-group names as a clearly marked
	// descriptor for resources with no stronger identity. These names are
	// stored in the unit geometry metadata itself, so this adds useful labels
	// without guessing a path or inventing a per-hash alias.
	knownPathsByDigest := make(map[[32]byte]map[string]struct{})
	unknownUnitsByDigest := make(map[[32]byte][]stingray.Hash)
	terrainUnits := make(map[stingray.Hash]bool)
	vistaUnits := make(map[stingray.Hash]bool)
	vistaMaterialCache := make(map[stingray.Hash]bool)
	vistaMaterialSeen := make(map[stingray.Hash]bool)
	boneNamesByUnit := make(map[stingray.Hash]map[string]string)
	boneNameUnitFrequency := make(map[string]int)
	for id := range gd.DataDir.Files {
		if id.Type != stingray.Sum("unit") {
			continue
		}
		data, err := gd.DataDir.Read(id, stingray.DataMain)
		if err != nil {
			continue
		}
		info, parseErr := unit.LoadInfo(bytes.NewReader(data))
		if parseErr == nil && len(info.TerrainInfos) > 0 {
			terrainUnits[id.Name] = true
		}
		digest := sha256.Sum256(data)
		path := gd.LookupHash(id.Name)
		if path != id.Name.String() {
			if knownPathsByDigest[digest] == nil {
				knownPathsByDigest[digest] = make(map[string]struct{})
			}
			knownPathsByDigest[digest][path] = struct{}{}
			continue
		}
		unknownUnitsByDigest[digest] = append(unknownUnitsByDigest[digest], id.Name)
		if parseErr != nil {
			continue
		}

		if len(info.TerrainInfos) > 0 {
			terrainUnits[id.Name] = true
			add(id.Name, "terrain ground patch "+id.Name.String(), "terrain heightmap/quadtree data embedded in unit", 0)
		}
		materialLabels := map[string]bool{}
		frvBody, frvWheels, flamethrower := false, false, false
		vistaFound := false
		for slot, h := range info.Materials {
			slotName := gd.LookupThinHash(slot)
			frvBody = frvBody || slotName == "m_frv_body"
			frvWheels = frvWheels || slotName == "m_frv_wheels"
			flamethrower = flamethrower || slotName == "m_flamethrower"
			if !vistaMaterialSeen[h] {
				vistaMaterialSeen[h] = true
				if raw, err := gd.DataDir.Read(stingray.NewFileID(h, stingray.Sum("material")), stingray.DataMain); err == nil {
					if mat, err := material.LoadMain(bytes.NewReader(raw)); err == nil {
						vistaMaterialCache[h] = strings.Contains(strings.ToLower(gd.LookupHash(mat.BaseMaterial)), "vista") || mat.BaseMaterial.Value == 0xc7098346fa00b5a1
					}
				}
			}
			vistaFound = vistaFound || vistaMaterialCache[h]
			if strings.Contains(gd.LookupThinHash(slot), "attachments") {
				if raw, err := gd.DataDir.Read(stingray.NewFileID(h, stingray.Sum("material")), stingray.DataMain); err == nil {
					if mat, err := material.LoadMain(bytes.NewReader(raw)); err == nil {
						shader := gd.LookupHash(mat.BaseMaterial)
						if strings.Contains(shader, "/weapon_customization") {
							add(id.Name, "weapon customization attachment "+id.Name.String(), "attachment material slot using "+shader, 0)
						}
					}
				}
			}
			path := gd.LookupHash(h)
			if !strings.HasPrefix(path, "content/") || strings.Contains(path, "/base_shaders/") {
				continue
			}
			label := strings.TrimPrefix(unitPathLabel(path), "m ")
			if usefulUnitLabel(label) && len(label) > 3 {
				materialLabels[label] = true
			}
		}
		if frvBody && frvWheels {
			label := "FRV vehicle"
			if flamethrower {
				label = "FRV vehicle (flamethrower)"
			}
			add(id.Name, label+" "+id.Name.String(), "unit material slots m_frv_body, m_frv_wheels"+func() string {
				if flamethrower {
					return ", m_flamethrower"
				}
				return ""
			}(), 2)
		}
		if vistaFound {
			vistaUnits[id.Name] = true
			add(id.Name, "vista shader model "+id.Name.String(), "unit material uses vista shader; exact model path unknown", 1)
		}
		labels := slices.Collect(maps.Keys(materialLabels))
		slices.Sort(labels)
		if len(labels) > 0 && len(labels) <= 2 {
			add(id.Name, "material: "+strings.Join(labels, " / ")+" "+id.Name.String(), "material resource paths: "+func() string {
				paths := []string{}
				for _, h := range info.Materials {
					p := gd.LookupHash(h)
					if strings.HasPrefix(p, "content/") {
						paths = append(paths, p)
					}
				}
				slices.Sort(paths)
				return strings.Join(paths, ", ")
			}(), 0)
		}
		unitBoneNames := make(map[string]string)
		for _, bone := range info.Bones {
			rawName := gd.LookupThinHash(bone.NameHash)
			name := normalizeUnitBoneName(rawName)
			if name != "" {
				unitBoneNames[name] = rawName
			}
		}
		if len(unitBoneNames) > 0 {
			boneNamesByUnit[id.Name] = unitBoneNames
			for name := range unitBoneNames {
				boneNameUnitFrequency[name]++
			}
		}

		meshNameCounts := make(map[string]int)
		meshNameSource := make(map[string]string)
		for _, meshInfo := range info.MeshInfos {
			rawName := gd.LookupThinHash(meshInfo.Header.MeshName)
			name := normalizeUnitMeshName(rawName)
			if name == "" {
				continue
			}
			meshNameCounts[name]++
			meshNameSource[name] = rawName
		}
		if len(meshNameCounts) > 0 {
			meshNames := slices.Collect(maps.Keys(meshNameCounts))
			slices.SortFunc(meshNames, func(a, b string) int {
				if meshNameCounts[a] != meshNameCounts[b] {
					return cmp.Compare(meshNameCounts[b], meshNameCounts[a])
				}
				if len(a) != len(b) {
					return cmp.Compare(len(b), len(a))
				}
				return strings.Compare(a, b)
			})
			if len(meshNames) > 2 {
				meshNames = meshNames[:2]
			}
			sources := make([]string, 0, len(meshNames))
			for _, name := range meshNames {
				sources = append(sources, meshNameSource[name])
			}
			add(id.Name, "mesh: "+strings.Join(meshNames, " / "), "mesh names in unit geometry ("+strings.Join(sources, ", ")+")", 0)
		}
	}

	// Terrain units are generated level ground patches, not ordinary named props.
	// Read level placement evidence once per build, without loading textures or GPU meshes.
	if len(terrainUnits) > 0 {
		usage := map[stingray.Hash]map[stingray.Hash]bool{}
		record := func(unitHash, levelHash stingray.Hash) {
			if !terrainUnits[unitHash] {
				return
			}
			if usage[unitHash] == nil {
				usage[unitHash] = map[stingray.Hash]bool{}
			}
			usage[unitHash][levelHash] = true
		}
		for id := range gd.DataDir.Files {
			if id.Type != stingray.Sum("level") {
				continue
			}
			data, err := gd.DataDir.Read(id, stingray.DataMain)
			if err != nil {
				continue
			}
			scene, err := level.LoadLevel(bytes.NewReader(data), gd.EntityVarMapping)
			if err != nil {
				continue
			}
			for _, placed := range scene.Units {
				record(placed.Hash, id.Name)
			}
			for _, p := range scene.EmbeddedPrefabs {
				for _, placed := range p.Units {
					record(placed.Hash, id.Name)
				}
			}
		}
		for h, levels := range usage {
			ids := slices.Collect(maps.Keys(levels))
			slices.SortFunc(ids, stingray.Hash.Cmp)
			sources := []string{}
			for _, id := range ids {
				sources = append(sources, gd.LookupHash(id))
			}
			add(h, "terrain ground patch "+h.String(), "terrain heightmap/quadtree data; placed in levels: "+strings.Join(sources, ", "), 1)
		}
	}
	// Rare, descriptive bone names can clarify resources whose geometry has no
	// useful mesh labels. Keep the prefix explicit: a skeleton clue is not a
	// guaranteed canonical name. Suppress common rig terms and only promote a
	// clue that appears in at most twenty distinct Unit resources.
	for unitPath, names := range boneNamesByUnit {
		candidates := make([]string, 0, len(names))
		for name := range names {
			if boneNameUnitFrequency[name] > 20 {
				continue
			}
			candidates = append(candidates, name)
		}
		if len(candidates) == 0 {
			continue
		}
		slices.SortFunc(candidates, func(a, b string) int {
			if boneNameUnitFrequency[a] != boneNameUnitFrequency[b] {
				return cmp.Compare(boneNameUnitFrequency[a], boneNameUnitFrequency[b])
			}
			if len(a) != len(b) {
				return cmp.Compare(len(b), len(a))
			}
			return strings.Compare(a, b)
		})
		if len(candidates) > 2 {
			candidates = candidates[:2]
		}
		sources := make([]string, 0, len(candidates))
		for _, name := range candidates {
			sources = append(sources, names[name])
		}
		add(unitPath, "rig: "+strings.Join(candidates, " / "), "rare unit bone names ("+strings.Join(sources, ", ")+")", 0)
	}

	// Exact byte-identical Unit resources are safe aliases. Ignore digest
	// groups with multiple named paths because those do not identify a unique
	// asset name.
	for digest, unknownUnits := range unknownUnitsByDigest {
		knownPaths := knownPathsByDigest[digest]
		if len(knownPaths) != 1 {
			continue
		}
		for path := range knownPaths {
			for _, unitPath := range unknownUnits {
				add(unitPath, unitPathLabel(path), "byte-identical unit data to "+path, 4)
			}
		}
	}

	for unitPath, candidates := range votes {
		best := bestUnitNameVote(candidates)
		if best != nil {
			source := best.source
			if vistaUnits[unitPath] && !strings.Contains(source, "vista") {
				source += "; uses vista shader"
			}
			result[unitPath] = inferredUnitName{Display: best.display, Source: source, Vista: vistaUnits[unitPath]}
		}
	}
	// Terrain is identified by its parsed terrain records, regardless of naming votes.
	for h := range terrainUnits {
		source := "terrain heightmap/quadtree data embedded in unit"
		if label, ok := result[h]; ok && strings.Contains(label.Source, "placed in levels:") {
			source = label.Source
		}
		if path := gd.LookupHash(h); path != h.String() {
			source += "; original resource path: " + path
		}
		result[h] = inferredUnitName{Display: "terrain ground patch " + h.String(), Source: source, Terrain: true}
	}
	return result
}

func (gd *GameData) hasUnitResource(unitPath stingray.Hash) bool {
	_, ok := gd.DataDir.Files[stingray.NewFileID(unitPath, stingray.Sum("unit"))]
	return ok
}

func bestUnitNameVote(candidates map[string]*unitNameVote) *unitNameVote {
	var best *unitNameVote
	for _, candidate := range candidates {
		if best == nil || candidate.rank > best.rank || candidate.rank == best.rank && candidate.count > best.count || candidate.rank == best.rank && candidate.count == best.count && candidate.display < best.display {
			best = candidate
		}
	}
	return best
}

func cleanUnitLabel(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func usefulUnitLabel(value string) bool {
	value = cleanUnitLabel(value)
	if len(value) < 2 || len(value) > 96 || strings.Contains(strings.ToLower(value), "string id not found") {
		return false
	}
	if _, err := strconv.ParseUint(value, 10, 32); err == nil {
		return false
	}
	return !strings.HasPrefix(strings.ToLower(value), "0x")
}

func unitPathLabel(resourcePath string) string {
	resourcePath = strings.TrimRight(resourcePath, "/")
	if slash := strings.LastIndex(resourcePath, "/"); slash >= 0 {
		resourcePath = resourcePath[slash+1:]
	}
	resourcePath = strings.TrimPrefix(resourcePath, "cha_")
	resourcePath = strings.ReplaceAll(resourcePath, "_", " ")
	return cleanUnitLabel(resourcePath)
}

func normalizeUnitMeshName(raw string) string {
	name := strings.ToLower(cleanUnitLabel(raw))
	if name == "" || strings.HasPrefix(name, "0x") {
		return ""
	}
	name = strings.TrimPrefix(name, "g_")
	if strings.HasPrefix(name, "c_") {
		return ""
	}
	for {
		before := name
		name = trimUnitMeshSuffix(name, "_lod0", "_lod1", "_lod2", "_lod3", "_lod4", "_shadowmesh", "_shadow_mesh", "_shadow", "_collision", "_cull", "_debris", "_rubble")
		if before == name {
			break
		}
	}
	name = strings.Trim(name, "_ -0123456789")
	name = strings.ReplaceAll(name, "_", " ")
	name = cleanUnitLabel(name)
	if strings.HasPrefix(name, "c ") {
		return ""
	}
	switch name {
	case "", "all", "base", "body", "collision", "debug", "game mesh", "main", "mesh", "object", "proxy", "root", "shadow", "skeleton", "unit":
		return ""
	}
	return name
}

func normalizeUnitBoneName(raw string) string {
	name := strings.ToLower(cleanUnitLabel(raw))
	if name == "" || strings.HasPrefix(name, "0x") {
		return ""
	}
	for _, prefix := range []string{"g_", "wep_"} {
		name = strings.TrimPrefix(name, prefix)
	}
	name = strings.ReplaceAll(name, "_", " ")
	name = cleanUnitLabel(name)
	for _, suffix := range []string{" jnt", " joint", " bone", " node", " cull", " culling", " shadow mesh", " shadow", " collision", " mesh"} {
		name = strings.TrimSuffix(name, suffix)
	}
	name = cleanUnitLabel(name)
	if strings.HasPrefix(name, "c ") {
		return ""
	}
	switch name {
	case "", "all", "base", "body", "boss", "rp", "terrain", "spine", "chest", "hips", "torso", "neck", "clavicle", "shoulder", "thigh", "elbow", "knee", "head", "hand", "root", "skeleton", "navigation", "debris", "particles", "game", "unit", "rig", "fx", "dummy", "camera", "light", "light pos", "shadow mesh", "collision", "static", "dynamic", "locator", "socket", "muzzle", "flash", "joint", "bone", "node", "mesh", "target", "audio", "interact", "hint", "action", "screen", "panic eject":
		return ""
	}
	for _, generic := range []string{"fx ", " light", "camera", "socket", "attach", "locator", "collision", "shadow", "cull", "root", "rig", "dummy", "variation", "translation", "rotation", "steering", "wheel", "door", "lid", "conduit", "motor", "flash", "navigation", "driver", "passenger", "entrance", "gear", "ik ", "bone", "mesh", "game ", "node", "lod", "muzzle", "audio", "interact", "hint", "destruction", "panic", "screen", "mapping", "occlusion", "killhorde"} {
		if strings.Contains(name, generic) {
			return ""
		}
	}
	return name
}

func trimUnitMeshSuffix(value string, suffixes ...string) string {
	for _, suffix := range suffixes {
		if strings.HasSuffix(value, suffix) {
			return strings.TrimSuffix(value, suffix)
		}
	}
	return value
}

func unitMountRole(slotName, attachNode string) string {
	combined := strings.ToLower(strings.TrimSpace(slotName + " " + attachNode))
	switch {
	case strings.Contains(combined, "gatling"), strings.Contains(combined, "gattling"), strings.Contains(combined, "rotary"):
		return "rotary gun"
	case strings.Contains(combined, "cannon"):
		return "cannon"
	case strings.Contains(combined, "turret"):
		return "turret"
	case strings.Contains(combined, "gun"):
		return "gun"
	case strings.Contains(combined, "weapon"):
		return "weapon"
	case strings.Contains(combined, "leg"):
		return "leg"
	case strings.Contains(combined, "arm"):
		return "arm"
	default:
		return "mounted part"
	}
}
