package materials

import (
	"fmt"
	"slices"
	"strings"

	"github.com/go-gl/gl/v4.3-core/gl"
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/unit/material"
)

var rockTextureSlots = []string{
	"weathering_albedo",
	"ground_albedo",
	"vista_detail_albedo",
	"triplanar_detail_albedo",
	"base_normal_ao_dirt",
	"emissive_texture",
}

var legacyRockShaderSources = []string{
	"shaders/object.vert",
	"shaders/object.geom",
	"shaders/rock_legacy.frag",
}

var legacyRockBaseMaterials = []stingray.Hash{
	stingray.Sum("content/art_shared/base_shaders/rock_default"),
	stingray.Sum("content/art_shared/base_shaders/rock_default_vista_new"),
	stingray.Sum("content/art_shared/base_shaders/rock_default_vista_new_emissive"),
	stingray.Sum("content/art_shared/base_shaders/rock_emissive"),
	stingray.Sum("content/art_shared/base_shaders/rock_ice"),
	stingray.Sum("content/art_shared/base_shaders/rock_snow"),
}

var vistaRockBaseMaterials = []stingray.Hash{
	stingray.Sum("content/art_shared/base_shaders/rock_default_vista_new"),
	stingray.Sum("content/art_shared/base_shaders/rock_default_vista_new_emissive"),
}

var assetGradingLutHash = stingray.Sum("asset_grading_lut")

func isLegacyRockMaterial(mat *material.Material) bool {
	return mat != nil && slices.Contains(vistaRockBaseMaterials, mat.BaseMaterial)
}

type LegacyRockMaterial struct {
	*BaseMaterial
	sourceMaterial *material.Material
	hasAlbedo      bool
	hasEmissive    bool
	albedoSource   int32
}

func NewLegacyRockMaterial(id stingray.FileID, name string, shown bool) *LegacyRockMaterial {
	return &LegacyRockMaterial{
		BaseMaterial: &BaseMaterial{
			id:       id.Name,
			override: id,
			name:     name,
			shown:    shown,
		},
	}
}

func (mat *LegacyRockMaterial) Generate() error {
	return mat.generate(
		legacyRockShaderSources,
		0,
		append(baseUniforms, rockTextureSlots...),
	)
}

func (ml *MaterialManager) updateLegacyRockMaterial(preview *LegacyRockMaterial, mat *material.Material) {
	// Rock materials expose several possible base-color slots. The detail
	// slots frequently contain black dummy textures, so choose an actual
	// weathering/ground map before wiring the renderer's canonical samplers.
	_, albedoSlot, albedoSource, hasAlbedo := ml.rockAlbedo(mat)
	preview.sourceMaterial = mat
	preview.hasAlbedo = hasAlbedo
	preview.albedoSource = albedoSource
	textureNames := make([]string, 0, 3)
	textureNames = append(textureNames, "base_normal_ao_dirt")
	if hasAlbedo {
		textureNames = append(textureNames, albedoSlot)
	}
	hasEmissive := false
	if hash, ok := mat.Textures[stingray.Sum("emissive_texture").Thin()]; ok && !ml.isDummyRockTexture(hash) {
		textureNames = append(textureNames, "emissive_texture")
		hasEmissive = true
	}
	preview.hasEmissive = hasEmissive

	// Keep the game's original recognized slot names so Filediver's hash lookup
	// binds the chosen map to the matching GLSL sampler.
	ml.setupMaterialCommon(textureNames, preview, mat)
	if err := ml.UpdateAssetGradingLut(); err != nil {
		fmt.Printf("updating rock color grading LUT: %v\n", err)
	}
	ml.addRockGradingLut(preview)
	ml.setRockFallback(preview, mat, hasAlbedo, hasEmissive, ml.assetGradingEnabled, albedoSource)
	ml.handleGlobalBlocks(preview.BaseMaterial)
}

// RefreshAssetGrading updates the grade toggle when a preview changes its
// planet palette without rebuilding the already-loaded mesh and material.
func (ml *MaterialManager) RefreshAssetGrading(material Material) {
	preview, ok := material.(*LegacyRockMaterial)
	if !ok || preview.sourceMaterial == nil {
		return
	}
	ml.setRockFallback(
		preview,
		preview.sourceMaterial,
		preview.hasAlbedo,
		preview.hasEmissive,
		ml.assetGradingEnabled,
		preview.albedoSource,
	)
}

func (ml *MaterialManager) addRockGradingLut(preview *LegacyRockMaterial) {
	for idx := range preview.textures {
		if preview.textures[idx].slot == "asset_grading_lut" {
			preview.textures[idx].id = ml.assetGradingLutTexture
			preview.textures[idx].target = gl.TEXTURE_BUFFER
			preview.textures[idx].loaded = true
			return
		}
	}
	preview.textures = append(preview.textures, Texture{
		id:       ml.assetGradingLutTexture,
		target:   gl.TEXTURE_BUFFER,
		name:     assetGradingLutHash,
		override: stingray.NewFileID(assetGradingLutHash, stingray.Sum("texture")),
		slot:     "asset_grading_lut",
		loaded:   true,
		filter:   gl.NEAREST,
	})
}

func (ml *MaterialManager) rockAlbedo(mat *material.Material) (stingray.Hash, string, int32, bool) {
	candidates := []struct {
		slot   string
		source int32
	}{
		{slot: "weathering_albedo", source: 1},
		{slot: "ground_albedo", source: 2},
		{slot: "vista_detail_albedo", source: 3},
		{slot: "triplanar_detail_albedo", source: 4},
	}
	if slices.Contains(vistaRockBaseMaterials, mat.BaseMaterial) {
		candidates[0], candidates[1], candidates[2] = candidates[1], candidates[2], candidates[0]
	}
	for _, candidate := range candidates {
		hash, ok := mat.Textures[stingray.Sum(candidate.slot).Thin()]
		if ok && hash.Value != 0 && !ml.isDummyRockTexture(hash) {
			return hash, candidate.slot, candidate.source, true
		}
	}
	return stingray.Hash{}, "", 0, false
}

func (ml *MaterialManager) isDummyRockTexture(hash stingray.Hash) bool {
	name := strings.ToLower(ml.lookupHash(hash))
	return strings.Contains(name, "dummy")
}

func rockFallbackTint(mat *material.Material) []float32 {
	if mat.BaseMaterial == stingray.Sum("content/art_shared/base_shaders/rock_ice") {
		if color, ok := mat.Settings[stingray.Sum("ice_fuzz_color").Thin()]; ok && len(color) >= 3 {
			if !rockColorIsNearWhite(color) {
				return color[:3]
			}
		}
		return []float32{0.55, 0.71, 0.79}
	}
	dirtEnabled := true
	if intensity, ok := mat.Settings[stingray.Sum("dirt_intensity").Thin()]; ok && len(intensity) > 0 {
		dirtEnabled = intensity[0] > 0.0001
	}
	if dirtEnabled {
		if color, ok := mat.Settings[stingray.Sum("dirt_color").Thin()]; ok && len(color) >= 3 {
			// The default rock material stores white here because the game
			// expects its asset color-grading LUT to supply the biome palette.
			// Without an entity LUT, use a stone fallback instead of white.
			if !rockColorIsNearWhite(color) {
				return color[:3]
			}
		}
	}
	if mat.BaseMaterial == stingray.Sum("content/art_shared/base_shaders/rock_snow") {
		// rock_snow often ships with dummy albedos and expects the active
		// planet's grading entity to tint its grading groups. When no planet is
		// selected, keep the fallback visibly stone-colored instead of treating
		// the white material tint as a real albedo.
		return []float32{0.50, 0.53, 0.56}
	}
	if mat.BaseMaterial == stingray.Sum("content/art_shared/base_shaders/rock_emissive") {
		return []float32{0.42, 0.37, 0.34}
	}
	return []float32{0.58, 0.55, 0.50}
}

func rockColorIsNearWhite(color []float32) bool {
	return len(color) >= 3 && color[0] > 0.96 && color[1] > 0.96 && color[2] > 0.96 &&
		max(color[0], color[1], color[2])-min(color[0], color[1], color[2]) <= 0.04
}

func (ml *MaterialManager) setRockFallback(preview *LegacyRockMaterial, mat *material.Material, hasAlbedo, hasEmissive, hasAssetGrading bool, source int32) {
	useFallback := uint32(0)
	if !hasAlbedo {
		useFallback = 1
	}
	emissive := uint32(0)
	if hasEmissive {
		emissive = 1
	}
	useAssetGrading := uint32(0)
	if hasAssetGrading {
		useAssetGrading = 1
	}
	for _, block := range preview.uniformBlocks {
		if block.name != "RockBlock" {
			continue
		}
		block.set("use_fallback_tint", []uint32{useFallback})
		block.setDefault("use_fallback_tint", []uint32{useFallback})
		block.set("has_emissive", []uint32{emissive})
		block.setDefault("has_emissive", []uint32{emissive})
		block.set("fallback_tint", rockFallbackTint(mat))
		block.setDefault("fallback_tint", rockFallbackTint(mat))
		block.set("albedo_source", []int32{source})
		block.setDefault("albedo_source", []int32{source})
		block.set("use_asset_grading", []uint32{useAssetGrading})
		block.setDefault("use_asset_grading", []uint32{useAssetGrading})
	}
}

var _ Material = &LegacyRockMaterial{}
