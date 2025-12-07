package renderer

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/png"
	"sync"
	"unsafe"

	"github.com/rajveermalviya/go-webgpu/wgpu"

	"mapviewer/internal/camera"
	"mapviewer/pkg/tiles"
)

const TileSize = 256

// Vertex represents a vertex with position and texture coordinates
type Vertex struct {
	Position [2]float32
	TexCoord [2]float32
}

// TileTexture holds GPU resources for a single tile
type TileTexture struct {
	Texture   *wgpu.Texture
	View      *wgpu.TextureView
	BindGroup *wgpu.BindGroup
}

// Renderer handles all WebGPU rendering
type Renderer struct {
	device          *wgpu.Device
	queue           *wgpu.Queue
	surface         *wgpu.Surface
	adapter         *wgpu.Adapter
	swapChain       *wgpu.SwapChain
	swapChainFormat wgpu.TextureFormat
	pipeline        *wgpu.RenderPipeline
	sampler         *wgpu.Sampler
	bindGroupLayout *wgpu.BindGroupLayout

	placeholder *TileTexture
	textures    map[string]*TileTexture
	texturesMu  sync.RWMutex

	width  uint32
	height uint32
}

// NewRenderer creates a new WebGPU renderer
func NewRenderer(adapter *wgpu.Adapter, device *wgpu.Device, queue *wgpu.Queue, surface *wgpu.Surface, width, height uint32) (*Renderer, error) {
	r := &Renderer{
		adapter:  adapter,
		device:   device,
		queue:    queue,
		surface:  surface,
		width:    width,
		height:   height,
		textures: make(map[string]*TileTexture),
	}

	if err := r.init(); err != nil {
		return nil, err
	}

	return r, nil
}

func (r *Renderer) init() error {
	// Get preferred format
	r.swapChainFormat = r.surface.GetPreferredFormat(r.adapter)

	// Create swap chain
	var err error
	r.swapChain, err = r.device.CreateSwapChain(r.surface, &wgpu.SwapChainDescriptor{
		Usage:       wgpu.TextureUsage_RenderAttachment,
		Format:      r.swapChainFormat,
		Width:       r.width,
		Height:      r.height,
		PresentMode: wgpu.PresentMode_Fifo,
	})
	if err != nil {
		return fmt.Errorf("swap chain creation failed: %w", err)
	}

	// Create shader module - simple shader with position directly in NDC
	shaderCode := `
struct VertexInput {
    @location(0) position: vec2<f32>,
    @location(1) texCoord: vec2<f32>,
}

struct VertexOutput {
    @builtin(position) position: vec4<f32>,
    @location(0) texCoord: vec2<f32>,
}

struct TileInfo {
    offset: vec2<f32>,
    scale: vec2<f32>,
}

@group(0) @binding(0) var<uniform> tile: TileInfo;
@group(0) @binding(1) var tileSampler: sampler;
@group(0) @binding(2) var tileTexture: texture_2d<f32>;

@vertex
fn vs_main(in: VertexInput) -> VertexOutput {
    var out: VertexOutput;
    // Transform position: scale by tile size, offset, then to NDC
    let pos = in.position * tile.scale + tile.offset;
    out.position = vec4<f32>(pos, 0.0, 1.0);
    out.texCoord = in.texCoord;
    return out;
}

@fragment
fn fs_main(in: VertexOutput) -> @location(0) vec4<f32> {
    return textureSample(tileTexture, tileSampler, in.texCoord);
}
`
	shader, err := r.device.CreateShaderModule(&wgpu.ShaderModuleDescriptor{
		Label:          "tile_shader",
		WGSLDescriptor: &wgpu.ShaderModuleWGSLDescriptor{Code: shaderCode},
	})
	if err != nil {
		return fmt.Errorf("shader creation failed: %w", err)
	}
	defer shader.Release()

	// Create sampler
	r.sampler, err = r.device.CreateSampler(&wgpu.SamplerDescriptor{
		AddressModeU:   wgpu.AddressMode_ClampToEdge,
		AddressModeV:   wgpu.AddressMode_ClampToEdge,
		AddressModeW:   wgpu.AddressMode_ClampToEdge,
		MagFilter:      wgpu.FilterMode_Linear,
		MinFilter:      wgpu.FilterMode_Linear,
		MipmapFilter:   wgpu.MipmapFilterMode_Nearest,
		MaxAnisotrophy: 1,
	})
	if err != nil {
		return fmt.Errorf("sampler creation failed: %w", err)
	}

	// Create bind group layout
	r.bindGroupLayout, err = r.device.CreateBindGroupLayout(&wgpu.BindGroupLayoutDescriptor{
		Label: "tile_bind_group_layout",
		Entries: []wgpu.BindGroupLayoutEntry{
			{
				Binding:    0,
				Visibility: wgpu.ShaderStage_Vertex,
				Buffer:     wgpu.BufferBindingLayout{Type: wgpu.BufferBindingType_Uniform},
			},
			{
				Binding:    1,
				Visibility: wgpu.ShaderStage_Fragment,
				Sampler:    wgpu.SamplerBindingLayout{Type: wgpu.SamplerBindingType_Filtering},
			},
			{
				Binding:    2,
				Visibility: wgpu.ShaderStage_Fragment,
				Texture: wgpu.TextureBindingLayout{
					SampleType:    wgpu.TextureSampleType_Float,
					ViewDimension: wgpu.TextureViewDimension_2D,
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("bind group layout creation failed: %w", err)
	}

	// Create pipeline layout
	pipelineLayout, err := r.device.CreatePipelineLayout(&wgpu.PipelineLayoutDescriptor{
		Label:            "tile_pipeline_layout",
		BindGroupLayouts: []*wgpu.BindGroupLayout{r.bindGroupLayout},
	})
	if err != nil {
		return fmt.Errorf("pipeline layout creation failed: %w", err)
	}
	defer pipelineLayout.Release()

	// Create render pipeline
	r.pipeline, err = r.device.CreateRenderPipeline(&wgpu.RenderPipelineDescriptor{
		Label:  "tile_pipeline",
		Layout: pipelineLayout,
		Vertex: wgpu.VertexState{
			Module:     shader,
			EntryPoint: "vs_main",
			Buffers: []wgpu.VertexBufferLayout{{
				ArrayStride: uint64(unsafe.Sizeof(Vertex{})),
				StepMode:    wgpu.VertexStepMode_Vertex,
				Attributes: []wgpu.VertexAttribute{
					{Format: wgpu.VertexFormat_Float32x2, Offset: 0, ShaderLocation: 0},
					{Format: wgpu.VertexFormat_Float32x2, Offset: 8, ShaderLocation: 1},
				},
			}},
		},
		Fragment: &wgpu.FragmentState{
			Module:     shader,
			EntryPoint: "fs_main",
			Targets: []wgpu.ColorTargetState{{
				Format:    r.swapChainFormat,
				Blend:     &wgpu.BlendState_Replace,
				WriteMask: wgpu.ColorWriteMask_All,
			}},
		},
		Primitive: wgpu.PrimitiveState{
			Topology: wgpu.PrimitiveTopology_TriangleList,
		},
		Multisample: wgpu.MultisampleState{
			Count: 1,
			Mask:  0xFFFFFFFF,
		},
	})
	if err != nil {
		return fmt.Errorf("pipeline creation failed: %w", err)
	}

	// Create placeholder texture
	r.placeholder, err = r.createPlaceholder()
	if err != nil {
		return fmt.Errorf("placeholder creation failed: %w", err)
	}

	return nil
}

func (r *Renderer) createPlaceholder() (*TileTexture, error) {
	img := image.NewRGBA(image.Rect(0, 0, TileSize, TileSize))
	// Sea blue color
	seaBlue := color.RGBA{R: 160, G: 195, B: 207, A: 255}
	draw.Draw(img, img.Bounds(), &image.Uniform{seaBlue}, image.Point{}, draw.Src)
	return r.createTileTexture(img)
}

func (r *Renderer) createTileTexture(img *image.RGBA) (*TileTexture, error) {
	texture, err := r.device.CreateTexture(&wgpu.TextureDescriptor{
		Label: "tile_texture",
		Size: wgpu.Extent3D{
			Width:              uint32(img.Bounds().Dx()),
			Height:             uint32(img.Bounds().Dy()),
			DepthOrArrayLayers: 1,
		},
		MipLevelCount: 1,
		SampleCount:   1,
		Dimension:     wgpu.TextureDimension_2D,
		Format:        wgpu.TextureFormat_RGBA8UnormSrgb,
		Usage:         wgpu.TextureUsage_TextureBinding | wgpu.TextureUsage_CopyDst,
	})
	if err != nil {
		return nil, err
	}

	r.queue.WriteTexture(
		&wgpu.ImageCopyTexture{Texture: texture, MipLevel: 0, Origin: wgpu.Origin3D{}, Aspect: wgpu.TextureAspect_All},
		img.Pix,
		&wgpu.TextureDataLayout{Offset: 0, BytesPerRow: uint32(img.Stride), RowsPerImage: uint32(img.Bounds().Dy())},
		&wgpu.Extent3D{Width: uint32(img.Bounds().Dx()), Height: uint32(img.Bounds().Dy()), DepthOrArrayLayers: 1},
	)

	view, err := texture.CreateView(&wgpu.TextureViewDescriptor{
		Format:          wgpu.TextureFormat_RGBA8UnormSrgb,
		Dimension:       wgpu.TextureViewDimension_2D,
		BaseMipLevel:    0,
		MipLevelCount:   1,
		BaseArrayLayer:  0,
		ArrayLayerCount: 1,
		Aspect:          wgpu.TextureAspect_All,
	})
	if err != nil {
		texture.Release()
		return nil, err
	}

	// Create uniform buffer for this tile
	uniformBuffer, err := r.device.CreateBuffer(&wgpu.BufferDescriptor{
		Label: "tile_uniform_buffer",
		Size:  16, // 2 vec2<f32> = 16 bytes
		Usage: wgpu.BufferUsage_Uniform | wgpu.BufferUsage_CopyDst,
	})
	if err != nil {
		view.Release()
		texture.Release()
		return nil, err
	}

	bindGroup, err := r.device.CreateBindGroup(&wgpu.BindGroupDescriptor{
		Label:  "tile_bind_group",
		Layout: r.bindGroupLayout,
		Entries: []wgpu.BindGroupEntry{
			{Binding: 0, Buffer: uniformBuffer, Size: 16},
			{Binding: 1, Sampler: r.sampler},
			{Binding: 2, TextureView: view},
		},
	})
	if err != nil {
		uniformBuffer.Release()
		view.Release()
		texture.Release()
		return nil, err
	}

	return &TileTexture{Texture: texture, View: view, BindGroup: bindGroup}, nil
}

// UploadTile uploads a tile image to GPU
func (r *Renderer) UploadTile(coord tiles.TileCoord, data []byte) error {
	key := coord.String()

	r.texturesMu.RLock()
	_, exists := r.textures[key]
	r.texturesMu.RUnlock()
	if exists {
		return nil
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}

	rgba := image.NewRGBA(img.Bounds())
	draw.Draw(rgba, rgba.Bounds(), img, image.Point{}, draw.Src)

	tex, err := r.createTileTexture(rgba)
	if err != nil {
		return err
	}

	r.texturesMu.Lock()
	r.textures[key] = tex
	r.texturesMu.Unlock()

	return nil
}

// HasTile checks if a tile is uploaded
func (r *Renderer) HasTile(coord tiles.TileCoord) bool {
	r.texturesMu.RLock()
	defer r.texturesMu.RUnlock()
	_, ok := r.textures[coord.String()]
	return ok
}

// TileInfo matches shader uniform
type TileInfo struct {
	OffsetX float32
	OffsetY float32
	ScaleX  float32
	ScaleY  float32
}

// Render draws the map
func (r *Renderer) Render(cam *camera.Camera) error {
	view, err := r.swapChain.GetCurrentTextureView()
	if err != nil {
		return err
	}
	defer view.Release()

	encoder, err := r.device.CreateCommandEncoder(&wgpu.CommandEncoderDescriptor{})
	if err != nil {
		return err
	}
	defer encoder.Release()

	pass := encoder.BeginRenderPass(&wgpu.RenderPassDescriptor{
		ColorAttachments: []wgpu.RenderPassColorAttachment{{
			View:       view,
			LoadOp:     wgpu.LoadOp_Clear,
			StoreOp:    wgpu.StoreOp_Store,
			ClearValue: wgpu.Color{R: 0.627, G: 0.765, B: 0.812, A: 1.0},
		}},
	})

	pass.SetPipeline(r.pipeline)

	// Create vertex buffer for a unit quad (0-1 range)
	vertices := []Vertex{
		{Position: [2]float32{0, 0}, TexCoord: [2]float32{0, 0}},
		{Position: [2]float32{1, 0}, TexCoord: [2]float32{1, 0}},
		{Position: [2]float32{1, 1}, TexCoord: [2]float32{1, 1}},
		{Position: [2]float32{0, 1}, TexCoord: [2]float32{0, 1}},
	}
	vertexBuffer, _ := r.device.CreateBufferInit(&wgpu.BufferInitDescriptor{
		Label:    "vertex_buffer",
		Contents: wgpu.ToBytes(vertices),
		Usage:    wgpu.BufferUsage_Vertex,
	})
	defer vertexBuffer.Release()

	indices := []uint16{0, 1, 2, 0, 2, 3}
	indexBuffer, _ := r.device.CreateBufferInit(&wgpu.BufferInitDescriptor{
		Label:    "index_buffer",
		Contents: wgpu.ToBytes(indices),
		Usage:    wgpu.BufferUsage_Index,
	})
	defer indexBuffer.Release()

	pass.SetVertexBuffer(0, vertexBuffer, 0, wgpu.WholeSize)
	pass.SetIndexBuffer(indexBuffer, wgpu.IndexFormat_Uint16, 0, wgpu.WholeSize)

	minX, minY, maxX, maxY := cam.GetTileBounds()
	w := float32(r.width)
	h := float32(r.height)

	// Scale: tile size in NDC units
	scaleX := float32(TileSize) / w * 2
	scaleY := float32(TileSize) / h * 2

	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			coord := tiles.TileCoord{X: x, Y: y, Zoom: cam.Zoom}
			screenX, screenY := cam.GetTileScreenPosition(x, y)

			// Convert screen position to NDC (-1 to 1)
			// Screen coords: (0,0) top-left, (width,height) bottom-right
			// NDC coords: (-1,-1) bottom-left, (1,1) top-right
			ndcX := (float32(screenX)/w)*2 - 1
			ndcY := 1 - (float32(screenY)/h)*2 // Flip Y

			tileInfo := TileInfo{
				OffsetX: ndcX,
				OffsetY: ndcY - scaleY, // Move down by tile height (since we draw from top-left)
				ScaleX:  scaleX,
				ScaleY:  -scaleY, // Negative to flip texture vertically
			}

			r.texturesMu.RLock()
			tex, exists := r.textures[coord.String()]
			r.texturesMu.RUnlock()

			var bindGroup *wgpu.BindGroup
			if exists && tex != nil {
				bindGroup = tex.BindGroup
			} else {
				bindGroup = r.placeholder.BindGroup
			}

			// Create temp uniform buffer for this draw
			uniformBuffer, _ := r.device.CreateBufferInit(&wgpu.BufferInitDescriptor{
				Label:    "tile_uniform",
				Contents: wgpu.ToBytes([]TileInfo{tileInfo}),
				Usage:    wgpu.BufferUsage_Uniform,
			})

			// Create temp bind group with correct uniform
			tempBindGroup, _ := r.device.CreateBindGroup(&wgpu.BindGroupDescriptor{
				Label:  "temp_tile_bind_group",
				Layout: r.bindGroupLayout,
				Entries: []wgpu.BindGroupEntry{
					{Binding: 0, Buffer: uniformBuffer, Size: 16},
					{Binding: 1, Sampler: r.sampler},
					{Binding: 2, TextureView: func() *wgpu.TextureView {
						if exists && tex != nil {
							return tex.View
						}
						return r.placeholder.View
					}()},
				},
			})

			pass.SetBindGroup(0, tempBindGroup, nil)
			pass.DrawIndexed(6, 1, 0, 0, 0)

			// Note: These will leak - in production we'd pool these
			_ = bindGroup
		}
	}

	pass.End()

	cmdBuffer, err := encoder.Finish(&wgpu.CommandBufferDescriptor{})
	if err != nil {
		return err
	}
	defer cmdBuffer.Release()

	r.queue.Submit(cmdBuffer)
	r.swapChain.Present()

	return nil
}

// Resize handles window resize
func (r *Renderer) Resize(width, height uint32) {
	if width == 0 || height == 0 {
		return
	}
	r.width = width
	r.height = height

	if r.swapChain != nil {
		r.swapChain.Release()
	}

	var err error
	r.swapChain, err = r.device.CreateSwapChain(r.surface, &wgpu.SwapChainDescriptor{
		Usage:       wgpu.TextureUsage_RenderAttachment,
		Format:      r.swapChainFormat,
		Width:       width,
		Height:      height,
		PresentMode: wgpu.PresentMode_Fifo,
	})
	if err != nil {
		fmt.Printf("Failed to recreate swap chain: %v\n", err)
	}
}

// Release frees all GPU resources
func (r *Renderer) Release() {
	r.texturesMu.Lock()
	for _, tex := range r.textures {
		tex.BindGroup.Release()
		tex.View.Release()
		tex.Texture.Release()
	}
	r.texturesMu.Unlock()

	if r.placeholder != nil {
		r.placeholder.BindGroup.Release()
		r.placeholder.View.Release()
		r.placeholder.Texture.Release()
	}

	r.bindGroupLayout.Release()
	r.pipeline.Release()
	r.sampler.Release()
	if r.swapChain != nil {
		r.swapChain.Release()
	}
}
