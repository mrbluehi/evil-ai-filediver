package materials

import (
	"slices"

	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/unit/material"
)

var glassBaseMaterials = []stingray.Hash{
	stingray.Sum("content/art_shared/base_shaders/base_glass"),
	stingray.Sum("content/art_shared/base_shaders/base_glass_new"),
	stingray.Sum("content/art_shared/base_shaders/base_glass_container"),
	stingray.Sum("content/art_shared/base_shaders/base_glass_visibility_mask"),
}

var glassTextureNames = []string{"normal_xy_roughness_opacity"}

var glassShaderSources = []string{
	"shaders/object.vert",
	"shaders/object.geom",
	"shaders/glass.frag",
}

func isGlassMaterial(mat *material.Material) bool {
	return mat != nil && slices.Contains(glassBaseMaterials, mat.BaseMaterial)
}

type GlassMaterial struct {
	*BaseMaterial
}

func NewGlassMaterial(id stingray.FileID, name string, shown bool) *GlassMaterial {
	return &GlassMaterial{BaseMaterial: &BaseMaterial{
		id: id.Name, override: id, name: name, shown: shown,
	}}
}

func (mat *GlassMaterial) Generate() error {
	return mat.generate(glassShaderSources, 0, append(baseUniforms, glassTextureNames...))
}

func (ml *MaterialManager) updateGlassMaterial(preview *GlassMaterial, mat *material.Material) {
	ml.setupMaterialCommon(glassTextureNames, preview, mat)
	ml.handleGlobalBlocks(preview.BaseMaterial)
}

var _ Material = &GlassMaterial{}
