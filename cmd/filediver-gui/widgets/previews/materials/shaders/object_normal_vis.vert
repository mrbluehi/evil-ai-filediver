#version 430 core

layout(location = 0) in vec3 inPosition;
layout(location = 1) in vec3 inNormal;
layout(location = 2) in vec4 inTangent;
layout(location = 3) in vec3 inBitangent;
layout(location = 4) in vec2 inUV;
layout(location = 14) in vec4 inBoneIndices;
layout(location = 15) in vec4 inBoneWeights;
layout(location = 7) in mat4 instModel;

layout(std430, binding = 5) readonly buffer AnimationSkinMatrices {
    mat4 animationSkinMatrices[];
};
layout(std430, binding = 6) readonly buffer AnimationNormalMatrices {
    mat4 animationNormalMatrices[];
};

out vec4 normalEndPosition;
out vec4 tangentEndPosition;
out vec4 bitangentEndPosition;
out vec2 vertexUV;

uniform mat4 model;
uniform mat4 view; // projection*view*model
uniform mat4 projection; // projection*view*model
uniform float len; // normal length
uniform int useAnimationSkinning;

void main() {
    vertexUV = inUV;
    mat4 mvp = projection * view * model * instModel;
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
            mat3 normalMatrix = mat3(animationNormalMatrices[bone]);
            skinnedNormal += normalMatrix * inNormal * weight;
            skinnedTangent += normalMatrix * inTangent.xyz * weight;
            skinnedBitangent += normalMatrix * inBitangent * weight;
            totalWeight += weight;
        }
        if (totalWeight > 0.0) {
            vertexPosition = skinnedPosition.xyz;
            vertexNormal = normalize(skinnedNormal);
            vertexTangent = normalize(skinnedTangent);
            vertexBitangent = normalize(skinnedBitangent);
        }
    }
    normalEndPosition    = mvp * vec4(vertexPosition + vertexNormal * len, 1.0);
    tangentEndPosition   = mvp * vec4(vertexPosition + vertexTangent * len, 1.0);
    bitangentEndPosition = mvp * vec4(vertexPosition + vertexBitangent * len, 1.0);
    gl_Position = mvp * vec4(vertexPosition, 1.0);
}
