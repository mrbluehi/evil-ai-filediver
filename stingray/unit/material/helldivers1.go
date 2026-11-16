package material

import (
	"encoding/binary"

	"github.com/xypwn/filediver/stingray"
)

var helldivers1MaterialTextureSlots = []string{
	"ao_map",
	"albedo",
	"albedo_color",
	"albedo_iridescence",
	"base_color",
	"color_map",
	"diffuse_map",
	"emissive_color",
	"emissive_map",
	"metallic_map",
	"normal",
	"normal_map",
	"normal_map_01",
	"roughness_map",
	"specular_map",
	"texture_map",
}

// LoadHelldivers1Main reads the texture bindings for one named HD1 material
// slot from a legacy .material resource. HD1 material files can contain
// several named slots/variants, so knownMaterialNames bounds the selected
// slot's data block. If materialName is zero, texture bindings from the whole
// resource are considered (used by the standalone material preview).
func LoadHelldivers1Main(data []byte, materialName stingray.ThinHash, knownMaterialNames []stingray.ThinHash, textureExists func(stingray.Hash) bool) (*Material, error) {
	textureSlots := make(map[uint32]string, len(helldivers1MaterialTextureSlots))
	for _, slot := range helldivers1MaterialTextureSlots {
		textureSlots[stingray.Sum(slot).Thin().Value] = slot
	}

	var targetBlocks [][2]int
	if materialName.Value == 0 {
		targetBlocks = append(targetBlocks, [2]int{0, len(data)})
	} else {
		known := make(map[uint32]struct{}, len(knownMaterialNames)+1)
		known[materialName.Value] = struct{}{}
		for _, name := range knownMaterialNames {
			if name.Value != 0 {
				known[name.Value] = struct{}{}
			}
		}
		var markerOffsets []int
		for offset := 0; offset+8 <= len(data); offset += 4 {
			if _, ok := known[binary.LittleEndian.Uint32(data[offset:])]; ok {
				markerOffsets = append(markerOffsets, offset)
			}
		}
		for i, offset := range markerOffsets {
			if binary.LittleEndian.Uint32(data[offset:]) != materialName.Value {
				continue
			}
			end := len(data)
			if i+1 < len(markerOffsets) {
				end = markerOffsets[i+1]
			}
			start := offset + 8 // material-name hash and its companion word
			if start < end {
				targetBlocks = append(targetBlocks, [2]int{start, end})
			}
		}
		if len(targetBlocks) == 0 {
			return nil, nil
		}
	}

	// Some materials repeat a slot for multiple variants. Prefer the bounded
	// block with the most usable texture bindings instead of mixing variants.
	var best map[stingray.ThinHash]stingray.Hash
	for _, block := range targetBlocks {
		textures := make(map[stingray.ThinHash]stingray.Hash)
		for offset := block[0]; offset+12 <= block[1]; offset += 4 {
			slotName, ok := textureSlots[binary.LittleEndian.Uint32(data[offset:])]
			if !ok {
				continue
			}
			textureName := stingray.Hash{Value: binary.LittleEndian.Uint64(data[offset+4:])}
			if textureName.Value == 0 || (textureExists != nil && !textureExists(textureName)) {
				continue
			}
			slotName = helldivers1PreviewTextureSlot(slotName)
			if slotName == "" {
				continue
			}
			textures[stingray.Sum(slotName).Thin()] = textureName
		}
		if len(textures) > len(best) {
			best = textures
		}
	}

	if best == nil {
		best = make(map[stingray.ThinHash]stingray.Hash)
	}
	// HD1 materials have texture bindings but no HD2 StandardBlock settings.
	// Without explicit sampler flags, the standard preview shader ignores the
	// loaded color map and falls back to a zero base color, making the model
	// appear black.
	settings := make(map[stingray.ThinHash][]float32)
	setFloat := func(name string, value ...float32) {
		settings[stingray.Sum(name).Thin()] = value
	}
	setFloat("base_color", 1, 1, 1)
	setFloat("emissive", 0, 0, 0)
	setFloat("emissive_intensity", 0)
	setFloat("metallic", 0)
	setFloat("roughness", 0.8)
	setFloat("uv_offset", 0, 0)
	setFloat("uv_scale", 1, 1)
	for _, mapSetting := range []struct {
		texture string
		setting string
	}{
		{texture: "ao_map", setting: "use_ao_map"},
		{texture: "color_map", setting: "use_color_map"},
		{texture: "emissive_map", setting: "use_emissive_map"},
		{texture: "metallic_map", setting: "use_metallic_map"},
		{texture: "normal_map", setting: "use_normal_map"},
		{texture: "roughness_map", setting: "use_roughness_map"},
	} {
		value := float32(0)
		if _, exists := best[stingray.Sum(mapSetting.texture).Thin()]; exists {
			value = 1
		}
		setFloat(mapSetting.setting, value)
	}

	return &Material{
		// HD1's renderer shaders differ from HD2's. The preview uses its
		// general-purpose shader while keeping the original texture bindings.
		BaseMaterial: stingray.Sum("core/stingray_renderer/shader_import/standard"),
		Textures:     best,
		Settings:     settings,
	}, nil
}

func helldivers1PreviewTextureSlot(slot string) string {
	switch slot {
	case "ao_map":
		return "ao_map"
	case "albedo", "albedo_color", "albedo_iridescence", "base_color", "color_map", "diffuse_map", "texture_map":
		return "color_map"
	case "emissive_color", "emissive_map":
		return "emissive_map"
	case "metallic_map", "specular_map":
		return "metallic_map"
	case "normal", "normal_map", "normal_map_01":
		return "normal_map"
	case "roughness_map":
		return "roughness_map"
	default:
		return ""
	}
}
