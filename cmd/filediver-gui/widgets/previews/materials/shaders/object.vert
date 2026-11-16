#version 430 core

layout(location = 0) in vec3 inPosition;
layout(location = 1) in vec3 inNormal;
layout(location = 2) in vec4 inTangent;
layout(location = 3) in vec3 inBitangent;
layout(location = 4) in vec2 inUV;
layout(location = 5) in vec2 inUV1;
layout(location = 6) in vec2 inUV2;
layout(location = 14) in vec4 inBoneIndices;
layout(location = 15) in vec4 inBoneWeights;
layout(location = 7) in mat4 instModel;
layout(location = 11) in mat3 normalMat;

layout(std430, binding = 5) readonly buffer AnimationSkinMatrices {
    mat4 animationSkinMatrices[];
};
layout(std430, binding = 6) readonly buffer AnimationNormalMatrices {
    mat4 animationNormalMatrices[];
};

out VertexOutput
{
    vec3 fragPosition;
    vec3 fragInstPosition;
    vec3 objPosition;
    vec2 fragUV0;
    vec2 fragUV1;
    vec2 fragUV2;
    vec3 fragTangentLightPosition; // tangent meaning in tangent space
    vec3 fragTangentViewPosition;
    vec3 fragTangentFragmentPosition;
    mat3 dbg_fragTBN;
    mat3 dbg_fragITBN;
    mat4 instanceModel;
};

uniform mat4 projection;
uniform mat4 view;
uniform mat4 model;
uniform vec3 viewPosition;
uniform int useAnimationSkinning;

const mat4 stingrayToGL = mat4(
    1, 0, 0, 0,
    0, 0, 1, 0,
    0, -1, 0, 0,
    0, 0, 0, 1
);

vec3 safeNormalize(vec3 value, vec3 fallbackValue) {
    float lengthSquared = dot(value, value);
    if (isnan(lengthSquared) || isinf(lengthSquared) || lengthSquared < 1e-12) {
        return fallbackValue;
    }
    return value * inversesqrt(lengthSquared);
}

void main() {
    vec3 vertexPosition = inPosition;
    vec3 vertexNormal = inNormal;
    vec3 vertexTangent = inTangent.xyz;
    vec3 vertexBitangent = inBitangent;
    if (useAnimationSkinning != 0) {
        vec4 skinnedPosition = vec4(0.0);
        vec3 skinnedNormal = vec3(0.0);
        vec3 skinnedTangent = vec3(0.0);
        vec3 skinnedBitangent = vec3(0.0);
        float totalWeight = 0.0;
        for (int influence = 0; influence < 4; ++influence) {
            float weight = inBoneWeights[influence];
            if (weight <= 0.0) continue;
            int bone = int(inBoneIndices[influence]);
            skinnedPosition += animationSkinMatrices[bone] * vec4(inPosition, 1.0) * weight;
            skinnedNormal += mat3(animationNormalMatrices[bone]) * inNormal * weight;
            skinnedTangent += mat3(animationNormalMatrices[bone]) * inTangent.xyz * weight;
            skinnedBitangent += mat3(animationNormalMatrices[bone]) * inBitangent * weight;
            totalWeight += weight;
        }
        if (totalWeight > 0.0) {
            vertexPosition = skinnedPosition.xyz;
            vertexNormal = safeNormalize(skinnedNormal, inNormal);
            vertexTangent = safeNormalize(skinnedTangent, inTangent.xyz);
            vertexBitangent = safeNormalize(skinnedBitangent, inBitangent);
        }
    }
    instanceModel = stingrayToGL * instModel;
    gl_Position = projection * view * model * instModel * vec4(vertexPosition, 1.0);
    fragPosition = vec3(model * vec4(vertexPosition, 1.0));
    fragInstPosition = vec3(instanceModel * vec4(vertexPosition, 1.0));
    objPosition = vertexPosition;
    fragUV0 = inUV;
    fragUV1 = inUV1;
    fragUV2 = inUV2;

    {
        vec3 n = safeNormalize(normalMat * vertexNormal, vec3(0.0, 0.0, 1.0));
        vec3 tangentInput = normalMat * vertexTangent;
        vec3 tangent = tangentInput - n * dot(n, tangentInput);
        if (dot(tangent, tangent) < 1e-12 || any(isnan(tangent)) || any(isinf(tangent))) {
            vec3 axis = abs(n.z) < 0.9 ? vec3(0.0, 0.0, 1.0) : vec3(0.0, 1.0, 0.0);
            tangent = cross(axis, n);
        }
        vec3 t = safeNormalize(tangent, vec3(1.0, 0.0, 0.0));
        vec3 bitangentInput = normalMat * vertexBitangent;
        float handedness = dot(cross(n, t), bitangentInput) < 0.0 ? -1.0 : 1.0;
        vec3 b = safeNormalize(cross(n, t) * handedness, vec3(0.0, 1.0, 0.0));

        mat3 tbn = mat3(t, b, n);
        mat3 itbn = transpose(tbn);

        dbg_fragTBN = tbn;
        dbg_fragITBN = itbn;

        vec3 lightPosition = safeNormalize(viewPosition, vec3(0.0, 0.0, 1.0)) * 10000.0; // light behind camera

        fragTangentLightPosition    = itbn * lightPosition;
        fragTangentViewPosition     = itbn * viewPosition;
        fragTangentFragmentPosition = itbn * fragPosition;
    }
}
