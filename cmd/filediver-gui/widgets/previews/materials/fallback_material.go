package materials

import (
	"maps"
	"slices"
	"strings"

	"github.com/go-gl/gl/v4.3-core/gl"
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/unit/material"
)

var fallbackShaderSources = []string{
	"shaders/object.vert",
	"shaders/object.geom",
	"shaders/fallback.frag",
}

type FallbackMaterial struct {
	*BaseMaterial
}

func NewFallbackMaterial(id stingray.FileID, name string, shown bool) *FallbackMaterial {
	return &FallbackMaterial{
		BaseMaterial: &BaseMaterial{
			id:       id.Name,
			override: id,
			name:     name,
			shown:    shown,
		},
	}
}

func (mat *FallbackMaterial) Generate() error {
	return mat.generate(
		fallbackShaderSources,
		0,
		append(baseUniforms, "texAlbedo", "texOpacity", "useOpacityMap", "useOpacityClip"),
	)
}

var _ Material = &FallbackMaterial{}

func (ml *MaterialManager) updateFallbackMaterial(material *FallbackMaterial, mat *material.Material) {
	albedoRemoveAlphaTextures := []string{
		"covering_albedo", "input_image", "albedo",
	}
	albedoTextures := []string{
		"albedo_iridescence", "albedo_emissive", "base_color_emissive_map",
		"base_color_emissive", "base_color_metal_map", "albedo", "albedo_map", "albedo_texture",
		"diffuse_map", "diffuse_texture", "diffuse", "albedo_color", "albedo_color_map", "diffuse_color_map",
		"basecolor", "basecolor_map", "basecolor_texture", "color_emissive_map",
		"color_roughness", "color_specular_b", "base_color_map", "base_color_texture",
		"color_map", "color_texture", "base_color",
		"covering_albedo", "input_image",
		"albedoopacity_01",
	}
	allTextures := make([]string, 0)
	allTextures = append(allTextures, albedoTextures...)
	opacitySlots := []string{"opacity_clip_map", "opacity_map", "opacity", "alpha_map", "alpha_texture", "sclar_field_opacity"}
	opacityName := stingray.Hash{}
	opacitySlot := ""
	if mat != nil {
		for _, slot := range opacitySlots {
			if name, contains := mat.Textures[stingray.Sum(slot).Thin()]; contains {
				opacityName = name
				opacitySlot = slot
				allTextures = append(allTextures, slot)
				break
			}
		}
	}
	albedoRemoveAlpha := false
	albedoName := stingray.Hash{}
	albedoSlot := ""
	for _, texture := range albedoTextures {
		if mat == nil {
			break
		}
		if name, contains := mat.Textures[stingray.Sum(texture).Thin()]; contains && !ml.isDummyFallbackTexture(name) {
			albedoRemoveAlpha = slices.Contains(albedoRemoveAlphaTextures, texture)
			albedoName = name
			albedoSlot = texture
			break
		}
	}
	dynamicAlbedoSlot := ""
	dynamicAlbedoName := stingray.Hash{}
	if albedoName.Value == 0 && mat != nil {
		slots := slices.SortedFunc(maps.Keys(mat.Textures), stingray.ThinHash.Cmp)
		for _, slotHash := range slots {
			slot := strings.ToLower(ml.lookupThinHash(slotHash))
			if looksLikeFallbackAlbedoSlot(slot) && !ml.isDummyFallbackTexture(mat.Textures[slotHash]) {
				dynamicAlbedoSlot = ml.lookupThinHash(slotHash)
				dynamicAlbedoName = mat.Textures[slotHash]
				break
			}
		}
	}
	if albedoName.Value == 0 {
		albedoName = dynamicAlbedoName
		albedoSlot = dynamicAlbedoSlot
		albedoRemoveAlpha = slices.Contains(albedoRemoveAlphaTextures, dynamicAlbedoSlot)
		if dynamicAlbedoSlot != "" && !slices.Contains(allTextures, dynamicAlbedoSlot) {
			allTextures = append(allTextures, dynamicAlbedoSlot)
		}
	}
	ml.loadMaterialTexturesAsync(mat, allTextures)
	var albedoTarget uint32 = gl.TEXTURE_2D
	albedoSampler := "texAlbedo"
	if albedoSlot != "" && getTarget(albedoSlot) == gl.TEXTURE_2D_ARRAY {
		albedoTarget = gl.TEXTURE_2D_ARRAY
		albedoSampler = "texAlbedoArray"
	}
	albedoID := stingray.NewFileID(albedoName, stingray.Sum("texture"))
	albedoOverride := albedoID
	if ml.getOverride != nil {
		albedoOverride = ml.getOverride(albedoID)
	}
	albedoTexture := Texture{
		id:           0,
		target:       albedoTarget,
		name:         albedoName,
		override:     albedoOverride,
		slot:         albedoSampler,
		created:      false,
		loaded:       false,
		defaultColor: []byte{255, 255, 255, 255},
		filter:       gl.LINEAR,
	}
	if albedoRemoveAlpha {
		data := ml.textureData[albedoName]
		if data.Swizzles == nil {
			data.Swizzles = []uint32{gl.RED, gl.GREEN, gl.BLUE, gl.ALPHA}
		}
		data.Swizzles[3] = gl.ONE
		ml.textureData[albedoName] = data
	}
	material.textures = append(material.textures, albedoTexture)
	for _, block := range material.uniformBlocks {
		if block.name != "FallbackSettingsBlock" {
			continue
		}
		block.set("base_color", []float32{1, 1, 1})
		block.setDefault("base_color", []float32{1, 1, 1})
		block.set("opacity", []float32{1})
		block.setDefault("opacity", []float32{1})
		block.set("emissive", []float32{0, 0, 0})
		block.setDefault("emissive", []float32{0, 0, 0})
		block.set("emissive_intensity", []float32{1})
		block.setDefault("emissive_intensity", []float32{1})
	}
	if mat != nil {
		material.InitSettings(mat.Settings, ml.lookupThinHash)
	}
	useAlbedoArray := int32(0)
	if albedoTarget == gl.TEXTURE_2D_ARRAY {
		useAlbedoArray = 1
	}
	gl.ProgramUniform1i(material.program, material.uniforms.locations["useAlbedoArray"], useAlbedoArray)
	opacityID := stingray.NewFileID(opacityName, stingray.Sum("texture"))
	opacityOverride := opacityID
	if ml.getOverride != nil {
		opacityOverride = ml.getOverride(opacityID)
	}
	opacityTexture := Texture{
		id:           0,
		target:       gl.TEXTURE_2D,
		name:         opacityName,
		override:     opacityOverride,
		slot:         "texOpacity",
		created:      false,
		loaded:       false,
		defaultColor: []byte{255, 255, 255, 255},
		filter:       gl.LINEAR,
	}
	material.textures = append(material.textures, opacityTexture)
	useOpacityMap := int32(0)
	useOpacityClip := int32(0)
	if opacityName.Value != 0 {
		useOpacityMap = 1
		if opacitySlot == "opacity_clip_map" {
			useOpacityClip = 1
		}
	}
	gl.ProgramUniform1i(material.program, material.uniforms.locations["useOpacityMap"], useOpacityMap)
	gl.ProgramUniform1i(material.program, material.uniforms.locations["useOpacityClip"], useOpacityClip)
	gl.ProgramUniform1i(material.program, material.uniforms.locations["hasVisibilityMasks"], 0)

	ml.handleGlobalBlocks(material.BaseMaterial)
}

func (ml *MaterialManager) isDummyFallbackTexture(hash stingray.Hash) bool {
	if hash.Value == 0 {
		return true
	}
	name := strings.ToLower(ml.lookupHash(hash))
	return strings.Contains(name, "dummy") || strings.Contains(name, "placeholder")
}

func looksLikeFallbackAlbedoSlot(slot string) bool {
	colorSlot := slot == "color" || slot == "color_map" || slot == "color_texture" ||
		strings.Contains(slot, "albedo") || strings.Contains(slot, "base_color") ||
		strings.Contains(slot, "basecolor") || strings.Contains(slot, "diffuse")
	if !colorSlot {
		return false
	}
	// Some games pack metallic into the alpha channel of the base color map.
	// Keep those color maps eligible while excluding standalone metal maps.
	if strings.HasPrefix(slot, "base_color_metal") {
		return !strings.Contains(slot, "normal") && !strings.Contains(slot, "opacity") && !strings.Contains(slot, "mask")
	}
	for _, nonColorUse := range []string{"normal", "bump", "rough", "metal", "specular", "opacity", "mask", "height", "displacement", "ambient_occlusion", "occlusion", "_ao", "_nar", "_nac"} {
		if strings.Contains(slot, nonColorUse) {
			return false
		}
	}
	return true
}
