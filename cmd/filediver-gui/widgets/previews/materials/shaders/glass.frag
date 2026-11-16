#version 430 core

out vec4 fragColor;

in vec3 fragPosition;
in vec2 fragUV0;

uniform sampler2D normal_xy_roughness_opacity;
uniform float previewOpacity;

layout(shared, binding = 0) uniform GlassSettingsBlock {
    vec3 base_color;
    vec3 alpha_color;
    float alpha_color_weight;
    float opacity_base;
    float roughness_base;
    float specular_base;
    float texture_tile;
};

void main() {
    vec3 color = mix(base_color, alpha_color, clamp(alpha_color_weight, 0.0, 1.0));
    vec4 packed = texture(normal_xy_roughness_opacity, fragUV0 * max(abs(texture_tile), 1.0));
    vec3 dx = dFdx(fragPosition);
    vec3 dy = dFdy(fragPosition);
    vec3 normal = normalize(cross(dx, dy));
    if (!gl_FrontFacing) normal = -normal;
    float diffuse = 0.76 + 0.24 * max(dot(normal, normalize(vec3(0.35, 0.8, 0.45))), 0.0);
    float opacity = clamp(opacity_base * packed.a * previewOpacity, 0.0, 1.0);
    fragColor = vec4(color * diffuse, opacity);
}
