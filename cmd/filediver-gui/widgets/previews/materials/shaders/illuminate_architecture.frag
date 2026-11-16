#version 430 core

#include "lighting.frag"

out vec4 fragColor;

in vec2 fragUV0;
in mat3 dbg_fragTBN;
in mat3 dbg_fragITBN;

uniform sampler2D mask;
uniform sampler2DArray albedo_array;
uniform sampler2DArray normal_array;
uniform sampler2D normal_map;
uniform sampler2D emissive;
uniform int has_mask;
uniform int has_normal_array;
uniform int has_normal_map;
uniform int has_emissive;

layout(shared, binding = 0) uniform IlluminateArchitectureBlock {
    vec3 base_color;
    float surface_tiling;
    float roughness;
    float metallic;
    vec3 emissive_color;
    float emissive_power;
};

vec3 decodeNormal(vec3 encoded) {
    return normalize(encoded * 2.0 - 1.0);
}

vec4 normalizedLayerWeights(vec2 uv, vec4 weights) {
    vec4 layer1 = texture(albedo_array, vec3(uv, 1.0));
    vec4 layer2 = texture(albedo_array, vec3(uv, 2.0));
    vec4 layer3 = texture(albedo_array, vec3(uv, 3.0));
    vec4 layer4 = texture(albedo_array, vec3(uv, 4.0));

    weights.r = clamp(weights.r / max(layer1.a, 0.001), 0.0, 1.0);
    weights.g = clamp(weights.g / max(layer2.a, 0.001), 0.0, 1.0);
    weights.b = clamp(weights.b / max(layer3.a, 0.001), 0.0, 1.0);
    weights.a = clamp(weights.a / max(layer4.a, 0.001), 0.0, 1.0);
    return weights;
}

vec3 layeredColor(vec2 uv, vec4 weights) {
    vec4 base = texture(albedo_array, vec3(uv, 0.0));
    vec4 layer1 = texture(albedo_array, vec3(uv, 1.0));
    vec4 layer2 = texture(albedo_array, vec3(uv, 2.0));
    vec4 layer3 = texture(albedo_array, vec3(uv, 3.0));
    vec4 layer4 = texture(albedo_array, vec3(uv, 4.0));

    vec3 color = mix(base.rgb, layer1.rgb, weights.r);
    color = mix(color, layer2.rgb, weights.g);
    color = mix(color, layer3.rgb, weights.b);
    color = mix(color, layer4.rgb, weights.a);
    return color;
}

vec3 layeredNormal(vec2 uv, vec4 weights) {
    vec3 base = decodeNormal(texture(normal_array, vec3(uv, 0.0)).xyz);
    vec3 layer1 = decodeNormal(texture(normal_array, vec3(uv, 1.0)).xyz);
    vec3 layer2 = decodeNormal(texture(normal_array, vec3(uv, 2.0)).xyz);
    vec3 layer3 = decodeNormal(texture(normal_array, vec3(uv, 3.0)).xyz);
    vec3 layer4 = decodeNormal(texture(normal_array, vec3(uv, 4.0)).xyz);
    vec3 normal = normalize(mix(base, layer1, weights.r));
    normal = normalize(mix(normal, layer2, weights.g));
    normal = normalize(mix(normal, layer3, weights.b));
    normal = normalize(mix(normal, layer4, weights.a));
    return normal;
}

void main() {
    vec2 maskUV = fragUV0;
    vec2 surfaceUV = fragUV0 * max(surface_tiling, 0.001);
    vec4 maskWeights = has_mask != 0 ? texture(mask, maskUV) : vec4(0.0);
    if (textureSize(albedo_array, 0).z > 1) {
        maskWeights = normalizedLayerWeights(surfaceUV, maskWeights);
    }

    vec3 color = textureSize(albedo_array, 0).z > 1
        ? layeredColor(surfaceUV, maskWeights)
        : texture(albedo_array, vec3(surfaceUV, 0.0)).rgb;
    color *= base_color;

    vec3 normal = vec3(0.0, 0.0, 1.0);
    if (has_normal_array != 0 && textureSize(normal_array, 0).z > 0) {
        normal = layeredNormal(surfaceUV, maskWeights);
    }
    if (has_normal_map != 0) {
        vec2 detailXY = texture(normal_map, surfaceUV).rg * 2.0 - 1.0;
        vec3 detail = vec3(detailXY, reconstructNormalZ(detailXY));
        normal = reorientNormalMaps(normal, detail);
    }

    float roughnessValue = clamp(roughness, 0.08, 1.0);
    vec4 lit = calculateLighting(vec4(color, 1.0), roughnessValue, metallic, 0.0, 1.0, normal, 1.0, 1.0);
    if (has_emissive != 0) {
        lit.rgb += texture(emissive, surfaceUV).rgb * emissive_color * max(emissive_power, 0.0);
    }
    fragColor = vec4(lit.rgb, 1.0);
}
