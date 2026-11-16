package materials

import (
	"github.com/go-gl/gl/v4.3-core/gl"
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/unit/material"
)

var illuminateArchitectureBase = stingray.Sum("content/art_shared/base_shaders/m_illuminate_architecture")

var illuminateArchitectureTextureNames = []string{
	"mask",
	"albedo_array",
	"normal_array",
	"normal_map",
	"emissive",
	"ibl_brdf_lut",
}

var illuminateArchitectureShaderSources = []string{
	"shaders/object.vert",
	"shaders/object.geom",
	"shaders/illuminate_architecture.frag",
}

func isIlluminateArchitectureMaterial(mat *material.Material) bool {
	return mat != nil && mat.BaseMaterial == illuminateArchitectureBase
}

type IlluminateArchitectureMaterial struct {
	*BaseMaterial
}

func NewIlluminateArchitectureMaterial(id stingray.FileID, name string, shown bool) *IlluminateArchitectureMaterial {
	return &IlluminateArchitectureMaterial{
		BaseMaterial: &BaseMaterial{
			id:       id.Name,
			override: id,
			name:     name,
			shown:    shown,
		},
	}
}

func (mat *IlluminateArchitectureMaterial) Generate() error {
	return mat.generate(
		illuminateArchitectureShaderSources,
		0,
		append(baseUniforms, illuminateArchitectureTextureNames...),
	)
}

var _ Material = &IlluminateArchitectureMaterial{}

func (ml *MaterialManager) updateIlluminateArchitectureMaterial(preview *IlluminateArchitectureMaterial, mat *material.Material) {
	ml.setupMaterialCommon(illuminateArchitectureTextureNames, preview, mat)

	// The game's architecture shader blends five albedo/normal array layers
	// through the RGBA material mask. Use the same slots here so a normal map
	// can never be mistaken for the visible color map by the generic fallback.
	for _, block := range preview.uniformBlocks {
		if block.name != "IlluminateArchitectureBlock" {
			continue
		}
		defaults := map[string][]float32{
			"base_color":     {1, 1, 1},
			"surface_tiling": {1},
			"roughness":      {0.78},
			"metallic":       {0.05},
			"emissive_color": {0, 0, 0},
			"emissive_power": {0},
		}
		for name, value := range defaults {
			if _, exists := block.uniformOffsets[name]; !exists {
				continue
			}
			if _, supplied := mat.Settings[stingray.Sum(name).Thin()]; supplied {
				continue
			}
			block.set(name, value)
			block.setDefault(name, value)
		}
	}

	setTexturePresent := func(name string) {
		present := int32(0)
		if hash, ok := mat.Textures[stingray.Sum(name).Thin()]; ok && hash.Value != 0 {
			present = 1
		}
		if location, ok := preview.uniforms.locations["has_"+name]; ok && location >= 0 {
			gl.ProgramUniform1i(preview.program, location, present)
		}
	}
	setTexturePresent("mask")
	setTexturePresent("normal_array")
	setTexturePresent("normal_map")
	setTexturePresent("emissive")
	ml.handleGlobalBlocks(preview.BaseMaterial)
}
