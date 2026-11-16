package materials

import (
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/unit/material"
)

// This game's military platform-kit material base is stored as an unnamed
// hashed resource. Its material instances use pattern_data and
// surface_data_array; sending those through the generic fallback treated
// packed pattern data as a color map and produced purple/yellow slabs.
var platformKitBaseMaterial = stingray.Hash{Value: 0x98f5b0b542f2f356}

var platformKitTextureNames = []string{
	"pattern_data",
	"surface_data_array",
	"ibl_brdf_lut",
}

var platformKitShaderSources = []string{
	"shaders/object.vert",
	"shaders/object.geom",
	"shaders/platform_kit.frag",
}

func isPlatformKitMaterial(mat *material.Material) bool {
	return mat != nil && mat.BaseMaterial == platformKitBaseMaterial
}

type PlatformKitMaterial struct {
	*BaseMaterial
}

func NewPlatformKitMaterial(id stingray.FileID, name string, shown bool) *PlatformKitMaterial {
	return &PlatformKitMaterial{
		BaseMaterial: &BaseMaterial{
			id:       id.Name,
			override: id,
			name:     name,
			shown:    shown,
		},
	}
}

func (mat *PlatformKitMaterial) Generate() error {
	return mat.generate(platformKitShaderSources, 0, append(baseUniforms, platformKitTextureNames...))
}

var _ Material = &PlatformKitMaterial{}

func (ml *MaterialManager) updatePlatformKitMaterial(preview *PlatformKitMaterial, mat *material.Material) {
	ml.setupMaterialCommon(platformKitTextureNames, preview, mat)
	ml.handleGlobalBlocks(preview.BaseMaterial)
}
