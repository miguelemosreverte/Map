package renderer

// TileShader contains vertex and fragment shaders for tile rendering
const TileShader = `
struct Uniforms {
    mvp: mat4x4<f32>,
}

@group(0) @binding(0) var<uniform> uniforms: Uniforms;
@group(0) @binding(1) var tileSampler: sampler;
@group(0) @binding(2) var tileTexture: texture_2d<f32>;

struct VertexInput {
    @location(0) position: vec2<f32>,
    @location(1) texCoord: vec2<f32>,
}

struct VertexOutput {
    @builtin(position) position: vec4<f32>,
    @location(0) texCoord: vec2<f32>,
}

@vertex
fn vs_main(in: VertexInput) -> VertexOutput {
    var out: VertexOutput;
    out.position = uniforms.mvp * vec4<f32>(in.position, 0.0, 1.0);
    out.texCoord = in.texCoord;
    return out;
}

@fragment
fn fs_main(in: VertexOutput) -> @location(0) vec4<f32> {
    return textureSample(tileTexture, tileSampler, in.texCoord);
}
`
