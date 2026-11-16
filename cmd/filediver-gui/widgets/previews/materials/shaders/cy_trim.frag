#version 430 core

#include "lighting.frag"

out vec4 fragColor;

in vec2 fragUV0;
in vec2 fragUV1;

uniform sampler2D mask;
uniform sampler2D mra;
uniform sampler2D albedo_wear;
uniform sampler2D albedo;
uniform sampler2D albedo_color;
uniform sampler2D normal;

layout(shared, binding = 0) uniform CyTrimSettingsBlock {
    vec3 emissive_color;
    float emissive_intensity;
    float emissive_multiplier;
};

vec3 overlayColor(vec3 base, vec3 layer) {
    vec3 low = 2.0 * base * layer;
    vec3 high = 1.0 - 2.0 * (1.0 - base) * (1.0 - layer);
    return mix(low, high, step(vec3(0.5), base));
}

void main() {
    // cy_trim uses the second UV layer for wear/color masks and the first
    // layer for its base albedo, material response and normal map.
    vec3 maskValue = texture(mask, fragUV1).rgb;
    vec2 materialResponse = texture(mra, fragUV0).rg;
    vec3 wearColor = texture(albedo_wear, fragUV1).rgb;
    vec3 baseColor = texture(albedo, fragUV0).rgb;
    vec3 colorLayer = texture(albedo_color, fragUV1).rgb;

    float colorLayerWeight = clamp(colorLayer.r * 10.0, 0.0, 1.0);
    vec3 layeredColor = mix(wearColor, colorLayer, colorLayerWeight);
    vec3 surfaceColor = overlayColor(baseColor, layeredColor);

    // The game shader uses the red mask to blend toward its per-instance
    // emissive tint. Its preview default is neutral grey when intensity is 0.
    vec3 tintColor = vec3(0.4) + (emissive_color - vec3(0.4)) * emissive_intensity;
    surfaceColor = mix(surfaceColor, tintColor, clamp(maskValue.r, 0.0, 1.0));

    float wearRoughness = min(2.0 * pow(max(wearColor.g, 0.0), 0.4), 1.0);
    float maskedRoughness = mix(1.0, materialResponse.g, clamp(maskValue.b, 0.0, 1.0));
    float roughness = clamp(maskedRoughness * (1.0 - maskValue.g * 0.3) * wearRoughness, 0.0, 1.0);
    roughness = clamp(roughness * 0.955 + 0.045, 0.045, 1.0);

    vec2 normalXY = texture(normal, fragUV0).rg * 2.0 - 1.0;
    vec3 tangentNormal = vec3(normalXY, reconstructNormalZ(normalXY));
    tangentNormal = safeNormalizeLighting(tangentNormal, vec3(0.0, 0.0, 1.0));

    fragColor = calculateLighting(
        vec4(max(surfaceColor + maskValue.r * emissive_color * emissive_multiplier * emissive_intensity, vec3(0.0)), 1.0),
        roughness,
        clamp(materialResponse.r, 0.0, 1.0),
        0.0,
        1.0,
        tangentNormal,
        1.0,
        1.0
    );
}
