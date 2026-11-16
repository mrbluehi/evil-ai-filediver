#version 430 core

out vec4 fragColor;

in vec3 fragPosition;
in vec2 fragUV0;

uniform sampler2D texAlbedo;
uniform sampler2DArray texAlbedoArray;
uniform sampler2D texOpacity;
uniform int useAlbedoArray;
uniform int useOpacityMap;
uniform int useOpacityClip;
uniform float previewOpacity;

layout(shared, binding = 12) uniform FallbackSettingsBlock {
    vec3 base_color;
    float opacity;
    vec3 emissive;
    float emissive_intensity;
};

void main() {
    // Some material families use array textures for their color maps. Treating
    // those as 2D textures silently selected an arbitrary layer and produced
    // conspicuously wrong colors on otherwise valid geometry.
    vec3 albedo = useAlbedoArray != 0
        ? texture(texAlbedoArray, vec3(fragUV0, 0.0)).rgb
        : texture(texAlbedo, fragUV0).rgb;
    float mapOpacity = useOpacityMap != 0 ? texture(texOpacity, fragUV0).r : 1.0;
    if (useOpacityClip != 0 && mapOpacity < 0.5) {
        discard;
    }
    vec3 dx = dFdx(fragPosition);
    vec3 dy = dFdy(fragPosition);
    vec3 geometricNormal = cross(dx, dy);
    float normalLengthSquared = dot(geometricNormal, geometricNormal);

    // Use a stable ambient value if position derivatives are degenerate.
    float light = 0.42;
    if (normalLengthSquared > 1e-12) {
        vec3 normal = geometricNormal * inversesqrt(normalLengthSquared);
        if (!gl_FrontFacing) {
            normal = -normal;
        }
        light += 0.58 * max(dot(normal, normalize(vec3(0.35, 0.8, 0.45))), 0.0);
    }

    fragColor = vec4(albedo * base_color * light + emissive * emissive_intensity, opacity * mapOpacity * previewOpacity);
}
