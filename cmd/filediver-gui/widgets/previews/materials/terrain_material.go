package materials

import "github.com/xypwn/filediver/stingray"

var terrainShaderSources = []string{
	"shaders/object.vert",
	"shaders/object.geom",
	"shaders/terrain.frag",
}

// TerrainMaterial shades generated level terrain with a neutral procedural
// surface. The per-level terrain resources currently available to Filediver
// are R16 control maps rather than RGB albedo textures.
type TerrainMaterial struct {
	*BaseMaterial
}

func NewTerrainMaterial(id stingray.FileID, name string, shown bool) *TerrainMaterial {
	return &TerrainMaterial{
		BaseMaterial: &BaseMaterial{
			id:       id.Name,
			override: id,
			name:     name,
			shown:    shown,
		},
	}
}

func (mat *TerrainMaterial) Generate() error {
	return mat.generate(terrainShaderSources, 0, baseUniforms)
}

var _ Material = &TerrainMaterial{}
