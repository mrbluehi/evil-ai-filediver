#version 430 core

out vec4 fragColor;

in vec3 fragPosition;
in vec2 fragUV0;

float hash2(vec2 p) {
    p = fract(p * vec2(123.34, 456.21));
    p += dot(p, p + 45.32);
    return fract(p.x * p.y);
}

float terrainNoise2(vec2 p) {
    vec2 cell = floor(p);
    vec2 local = fract(p);
    local = local * local * (3.0 - 2.0 * local);
    float a = hash2(cell);
    float b = hash2(cell + vec2(1.0, 0.0));
    float c = hash2(cell + vec2(0.0, 1.0));
    float d = hash2(cell + vec2(1.0, 1.0));
    return mix(mix(a, b, local.x), mix(c, d, local.x), local.y);
}

void main() {
    vec3 dx = dFdx(fragPosition);
    vec3 dy = dFdy(fragPosition);
    vec3 geometricNormal = cross(dx, dy);
    float normalLengthSquared = dot(geometricNormal, geometricNormal);
    vec3 normal = vec3(0.0, 1.0, 0.0);
    if (normalLengthSquared > 1e-12) {
        normal = geometricNormal * inversesqrt(normalLengthSquared);
        if (!gl_FrontFacing) {
            normal = -normal;
        }
    }

    float broad = terrainNoise2(fragUV0 * 13.0);
    float medium = terrainNoise2(fragUV0 * 57.0);
    float fine = terrainNoise2(fragUV0 * 190.0);
    float variation = broad * 0.68 + medium * 0.24 + fine * 0.08;

    vec3 soil = mix(vec3(0.19, 0.18, 0.16), vec3(0.37, 0.34, 0.28), smoothstep(0.22, 0.78, variation));
    float slope = 1.0 - clamp(abs(normal.y), 0.0, 1.0);
    soil = mix(soil, vec3(0.27, 0.27, 0.25), smoothstep(0.20, 0.78, slope) * 0.58);

    vec3 lightDirection = normalize(vec3(0.35, 0.82, 0.44));
    float lighting = 0.48 + 0.52 * max(dot(normal, lightDirection), 0.0);
    fragColor = vec4(soil * lighting, 1.0);
}
