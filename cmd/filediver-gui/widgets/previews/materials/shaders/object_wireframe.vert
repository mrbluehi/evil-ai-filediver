#version 430 core

layout(location = 0) in vec3 inPosition;
layout(location = 4) in vec2 inUV;
layout(location = 14) in vec4 inBoneIndices;
layout(location = 15) in vec4 inBoneWeights;
layout(location = 7) in mat4 instModel;

layout(std430, binding = 5) readonly buffer AnimationSkinMatrices {
    mat4 animationSkinMatrices[];
};

out vec2 UV;

uniform mat4 model;
uniform mat4 projection; // projection*view*model
uniform mat4 view; // projection*view*model
uniform int useAnimationSkinning;

void main() {
    vec3 vertexPosition = inPosition;
    if (useAnimationSkinning != 0) {
        vec4 skinnedPosition = vec4(0.0);
        float totalWeight = 0.0;
        for (int influence = 0; influence < 4; ++influence) {
            float weight = inBoneWeights[influence];
            if (weight <= 0.0) continue;
            int bone = int(inBoneIndices[influence]);
            skinnedPosition += animationSkinMatrices[bone] * vec4(inPosition, 1.0) * weight;
            totalWeight += weight;
        }
        if (totalWeight > 0.0) vertexPosition = skinnedPosition.xyz;
    }
    gl_Position = projection * view * model * instModel * vec4(vertexPosition, 1.0);
    UV = inUV;
}
