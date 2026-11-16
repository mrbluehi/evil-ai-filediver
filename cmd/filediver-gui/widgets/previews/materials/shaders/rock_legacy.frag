#version 430 core

#include "grading.frag"

out vec4 fragColor;

in vec3 fragInstPosition;

uniform sampler2D weathering_albedo;
uniform sampler2D ground_albedo;
uniform sampler2D vista_detail_albedo;
uniform sampler2D triplanar_detail_albedo;
uniform sampler2D base_normal_ao_dirt;
uniform sampler2D emissive_texture;

layout(shared, binding = 11) uniform RockBlock {
    float weathering_tiling;
    float albedo_intensity;
    float albedo_intensity_ground;
    float albedo_intensity_vista_detail;
    float albedo_intensity_rock;
    float emissive_intensity;
    float snow_amount;
    vec3 dirt_color;
    vec3 ice_fuzz_color;
    vec3 fallback_tint;
    uint use_fallback_tint;
    uint has_emissive;
    int albedo_source;
    float grading_group_id;
    float grading_secondary_group_id;
    uint use_asset_grading;
};

vec3 safeNormalize(vec3 value, vec3 fallbackValue) {
    float lengthSquared = dot(value, value);
    if (isnan(lengthSquared) || isinf(lengthSquared) || lengthSquared < 1e-12) {
        return fallbackValue;
    }
    return value * inversesqrt(lengthSquared);
}

vec2 rockUVX(vec3 position, float tiling) {
    return vec2(position.y, position.z) * tiling;
}

vec2 rockUVY(vec3 position, float tiling) {
    return vec2(position.x, position.z) * tiling;
}

vec2 rockUVZ(vec3 position, float tiling) {
    return vec2(position.x, position.y) * tiling;
}

vec4 sampleRockMap(vec2 uv) {
    if (albedo_source == 1) return texture(weathering_albedo, uv);
    if (albedo_source == 2) return texture(ground_albedo, uv);
    if (albedo_source == 3) return texture(vista_detail_albedo, uv);
    if (albedo_source == 4) return texture(triplanar_detail_albedo, uv);
    return vec4(1.0);
}

vec4 sampleRockAlbedo(vec3 position, vec3 normal, float tiling) {
    vec3 weights = pow(abs(normal), vec3(4.0));
    float weightSum = max(weights.x + weights.y + weights.z, 1e-5);
    weights /= weightSum;

    return sampleRockMap(rockUVX(position, tiling)) * weights.x
         + sampleRockMap(rockUVY(position, tiling)) * weights.y
         + sampleRockMap(rockUVZ(position, tiling)) * weights.z;
}

float sampleRockColorMask(vec3 position, vec3 normal, float tiling) {
    vec3 weights = pow(abs(normal), vec3(4.0));
    weights /= max(weights.x + weights.y + weights.z, 1e-5);
    return texture(base_normal_ao_dirt, rockUVX(position, tiling)).a * weights.x
         + texture(base_normal_ao_dirt, rockUVY(position, tiling)).a * weights.y
         + texture(base_normal_ao_dirt, rockUVZ(position, tiling)).a * weights.z;
}

vec3 sampleRockEmission(vec3 position, vec3 normal, float tiling) {
    vec3 weights = pow(abs(normal), vec3(4.0));
    float weightSum = max(weights.x + weights.y + weights.z, 1e-5);
    weights /= weightSum;

    return texture(emissive_texture, rockUVX(position, tiling)).rgb * weights.x
         + texture(emissive_texture, rockUVY(position, tiling)).rgb * weights.y
         + texture(emissive_texture, rockUVZ(position, tiling)).rgb * weights.z;
}

vec3 gradeRockColor(vec3 srgbColor, float groupId) {
    int groupPixel = gradingPixel(groupId);
    if (groupPixel < 0) {
        return srgbColor;
    }
    vec3 linearColor = pow(max(srgbColor, vec3(0.0)), vec3(2.2));
    vec3 graded = gradeColor(linearColor, gradingMatrix(groupPixel));
    return pow(max(graded, vec3(0.0)), vec3(1.0 / 2.2));
}

void main() {
    vec3 dx = dFdx(fragInstPosition);
    vec3 dy = dFdy(fragInstPosition);
    vec3 normal = safeNormalize(cross(dx, dy), vec3(0.0, 0.0, 1.0));
    if (!gl_FrontFacing) {
        normal = -normal;
    }

    float tiling = weathering_tiling > 0.0001 ? weathering_tiling : 0.1875;
    float intensity = albedo_intensity;
    if (albedo_source == 2 && albedo_intensity_ground > 0.0001) {
        intensity = albedo_intensity_ground;
    } else if (albedo_source == 3 && albedo_intensity_vista_detail > 0.0001) {
        intensity = albedo_intensity_vista_detail;
    } else if (albedo_source == 4 && albedo_intensity_rock > 0.0001) {
        intensity = albedo_intensity_rock;
    }
    intensity = intensity > 0.0001 ? intensity : 1.0;

    vec3 baseColor = sampleRockAlbedo(fragInstPosition, normal, tiling).rgb;
    if (use_fallback_tint != 0u) {
        baseColor = use_asset_grading != 0u && any(greaterThan(dirt_color, vec3(0.01)))
            ? dirt_color
            : fallback_tint;
    }
    if (use_asset_grading != 0u) {
        float colorMask = clamp(sampleRockColorMask(fragInstPosition, normal, tiling), 0.0, 1.0);
        vec3 primary = gradeRockColor(baseColor, grading_group_id);
        vec3 secondary = gradeRockColor(baseColor, grading_secondary_group_id);
        baseColor = mix(primary, secondary, colorMask);
        // A missing or identity world LUT leaves the default dirt_color at
        // white. Keep rocks visibly stone-colored in that case.
        if (all(greaterThan(baseColor, vec3(0.97)))) {
            baseColor = fallback_tint;
        }
    }

    vec3 lightDirection = normalize(vec3(0.35, 0.8, 0.45));
    float diffuse = max(dot(normal, lightDirection), 0.0);
    float lighting = 0.58 + 0.42 * diffuse;
    float emissionIntensity = emissive_intensity > 0.0001 ? emissive_intensity : 1.0;
    vec3 emission = vec3(0.0);
    if (has_emissive != 0u) {
        emission = sampleRockEmission(fragInstPosition, normal, tiling);
    }

    // Rock meshes are solid; packed mask alpha must never become opacity.
    fragColor = vec4(baseColor * intensity * lighting + emission * emissionIntensity, 1.0);
}
