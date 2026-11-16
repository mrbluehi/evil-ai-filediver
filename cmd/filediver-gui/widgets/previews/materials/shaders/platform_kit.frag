#version 430 core

#include "lighting.frag"

out vec4 fragColor;

in vec3 fragPosition;
in vec3 fragInstPosition;

uniform sampler2D pattern_data;
uniform sampler2DArray surface_data_array;

layout(shared, binding = 0) uniform PlatformKitSettingsBlock {
    float pattern_tiling;
    float surface_tiling;
    float material_surface;
    float pattern_value_tint;
    float roughness_surface;
    float base_roughness;
    float roughness_pattern;
    vec3 base_color;
    vec3 secondary_color;
    vec3 tertiary_color;
};

vec3 triWeights(vec3 normal) {
    vec3 weights = pow(abs(normal), vec3(8.0));
    float total = weights.x + weights.y + weights.z;
    return total > 1e-6 ? weights / total : vec3(0.0, 1.0, 0.0);
}

vec4 sampleTriplanar(sampler2D tex, vec3 position, vec3 weights, float scale) {
    vec3 p = position * max(abs(scale), 0.0001);
    vec4 xProjection = texture(tex, p.yz);
    vec4 yProjection = texture(tex, p.xz);
    vec4 zProjection = texture(tex, p.xy);
    return xProjection * weights.x + yProjection * weights.y + zProjection * weights.z;
}

vec4 sampleSurface(vec3 position, vec3 weights, float scale, float layer) {
    vec3 p = position * max(abs(scale), 0.0001);
    vec3 xProjection = vec3(p.yz, layer);
    vec3 yProjection = vec3(p.xz, layer);
    vec3 zProjection = vec3(p.xy, layer);
    return texture(surface_data_array, xProjection) * weights.x
         + texture(surface_data_array, yProjection) * weights.y
         + texture(surface_data_array, zProjection) * weights.z;
}

void main() {
    // pattern_data is packed normal/mask information, not an RGB albedo map.
    // Use its packed mask channels to blend the instance's palette while the
    // normal and surface arrays contribute only small material detail.
    // Level instances can rotate this unit independently. Use the transformed
    // instance position so triplanar projections follow the placed surface.
    vec3 geometricNormal = cross(dFdx(fragInstPosition), dFdy(fragInstPosition));
    float normalLengthSquared = dot(geometricNormal, geometricNormal);
    vec3 normal = normalLengthSquared > 1e-12
        ? geometricNormal * inversesqrt(normalLengthSquared)
        : vec3(0.0, 1.0, 0.0);
    if (!gl_FrontFacing) {
        normal = -normal;
    }

    vec3 weights = triWeights(normal);
    vec4 pattern = sampleTriplanar(pattern_data, fragInstPosition, weights, pattern_tiling);
    int layerCount = textureSize(surface_data_array, 0).z;
    float layer = clamp(material_surface, 0.0, float(max(layerCount - 1, 0)));
    vec4 surface = sampleSurface(fragInstPosition, weights, surface_tiling, layer);

    float secondaryMask = clamp((pattern.a - 0.5) * 2.0, 0.0, 1.0);
    float tertiaryMask = clamp((0.5 - pattern.b) * 2.0, 0.0, 1.0);
    vec3 albedo = mix(base_color, secondary_color, secondaryMask);
    albedo = mix(albedo, tertiary_color, tertiaryMask);

    // The surface array is a packed normal/roughness resource. Its packed
    // channels are deliberately kept out of albedo to avoid the color cast
    // caused by treating data maps as regular image textures.
    float roughness = clamp(base_roughness + (surface.a - 0.5) * roughness_surface
        + (pattern_value_tint * 0.02) + (pattern.b - 0.5) * roughness_pattern, 0.045, 1.0);
    float ambientOcclusion = clamp(0.8 + surface.b * 0.2, 0.0, 1.0);
    normal = normalize(normal);
    vec3 lightDirection = normalize(vec3(0.35, 0.8, 0.45));
    float diffuse = max(dot(normal, lightDirection), 0.0);
    // This material family uses low authored palette values. Filediver has no
    // game environment probe, so preserve a broad studio fill instead of
    // letting horizontal concrete faces collapse to near black.
    float fillLight = mix(0.62, 1.0, diffuse);
    float surfaceFill = mix(0.90, 1.0, ambientOcclusion);
    vec3 linearColor = max(albedo, vec3(0.0)) * fillLight * surfaceFill;
    // Filediver's preview framebuffer is linear; encode for the display.
    fragColor = vec4(sRGBFromIntensity(linearColor), 1.0);
}
