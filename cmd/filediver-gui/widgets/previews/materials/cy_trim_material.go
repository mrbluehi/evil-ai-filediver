package materials

import (
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/unit/material"
)

var cyTrimTextureNames = []string{
	"mask",
	"mra",
	"albedo_wear",
	"albedo",
	"albedo_color",
	"normal",
	"ibl_brdf_lut",
}

var cyTrimShaderSources = []string{
	"shaders/object.vert",
	"shaders/object.geom",
	"shaders/cy_trim.frag",
}

var cyTrimBaseMaterials = []stingray.Hash{
	stingray.Sum("content/art_shared/base_shaders/cy_trim"),
}

func isCyTrimMaterial(mat *material.Material) bool {
	return mat != nil && mat.BaseMaterial == cyTrimBaseMaterials[0]
}

type CyTrimMaterial struct {
	*BaseMaterial
}

func NewCyTrimMaterial(id stingray.FileID, name string, shown bool) *CyTrimMaterial {
	return &CyTrimMaterial{
		BaseMaterial: &BaseMaterial{
			id:       id.Name,
			name:     name,
			shown:    shown,
			override: id,
		},
	}
}

func (mat *CyTrimMaterial) Generate() error {
	return mat.generate(
		cyTrimShaderSources,
		0,
		append(baseUniforms, "emissive_color", "emissive_intensity", "emissive_multiplier", "mask", "mra", "albedo_wear", "albedo", "albedo_color", "normal", "ibl_brdf_lut"),
	)
}

var _ Material = &CyTrimMaterial{}

func (ml *MaterialManager) updateCyTrimMaterial(preview *CyTrimMaterial, mat *material.Material) {
	ml.setupMaterialCommon(cyTrimTextureNames, preview, mat)
	ml.handleGlobalBlocks(preview.BaseMaterial)
}
