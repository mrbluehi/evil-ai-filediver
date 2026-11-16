package materials

import "testing"

// Custom render routes must remain valid when upstream changes their shared vertex pipeline.
func TestCustomMaterialShaders(t *testing.T) {
	for name, sources := range map[string][]string{
		"compatibility-rock":      legacyRockShaderSources,
		"cyborg-trim":             cyTrimShaderSources,
		"glass":                   glassShaderSources,
		"terrain":                 terrainShaderSources,
		"platform-kit":            platformKitShaderSources,
		"illuminate-architecture": illuminateArchitectureShaderSources,
		"fallback":                fallbackShaderSources,
	} {
		t.Run(name, func(t *testing.T) {
			stages, err := CreateProgramFromSources(unitPreviewShaderCode, sources...)
			if err != nil {
				t.Fatal(err)
			}
			for _, stage := range stages {
				if err := stage.validate(); err != nil {
					t.Errorf("%s: %v", stage.stage, err)
				}
			}
		})
	}
}
