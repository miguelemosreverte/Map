package renderer

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/png"
	"math"
	"sync"
	"unsafe"

	"github.com/rajveermalviya/go-webgpu/wgpu"

	"mapviewer/internal/camera"
	"mapviewer/internal/config"
	"mapviewer/internal/vectortile"
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
	Texture *wgpu.Texture
	View    *wgpu.TextureView
}

// CityData represents a city for the mask shader (max 64 cities)
type CityData struct {
	X      float32 // Longitude
	Y      float32 // Latitude
	Radius float32 // Base radius (based on rank)
	_      float32 // Padding for alignment
}

const MaxCities = 64

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

	// City mask data
	vectorTileCache *vectortile.VectorTileCache
	cities          []CityData
	citiesMu        sync.RWMutex

	width  uint32
	height uint32
}

// NewRenderer creates a new WebGPU renderer
func NewRenderer(adapter *wgpu.Adapter, device *wgpu.Device, queue *wgpu.Queue, surface *wgpu.Surface, width, height uint32, vectorTileCache *vectortile.VectorTileCache) (*Renderer, error) {
	r := &Renderer{
		adapter:         adapter,
		device:          device,
		queue:           queue,
		surface:         surface,
		width:           width,
		height:          height,
		textures:        make(map[string]*TileTexture),
		vectorTileCache: vectorTileCache,
		cities:          make([]CityData, 0, MaxCities),
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

	// Create shader module with city mask support
	shaderCode := `
struct VertexInput {
    @location(0) position: vec2<f32>,
    @location(1) texCoord: vec2<f32>,
}

struct VertexOutput {
    @builtin(position) position: vec4<f32>,
    @location(0) texCoord: vec2<f32>,
    @location(1) worldPos: vec2<f32>,
}

struct TileInfo {
    offset: vec2<f32>,
    scale: vec2<f32>,
    // Geo bounds of this tile (minLon, minLat, maxLon, maxLat)
    geoBounds: vec4<f32>,
}

struct CityMaskParams {
    radiusPercent: f32,       // 0-100, controls city visibility
    enableMask: f32,          // 1.0 = enabled, 0.0 = disabled
    cityCount: f32,           // Number of active cities
    baseRadius: f32,          // Base radius in degrees
}

struct City {
    pos: vec2<f32>,     // lon, lat
    radius: f32,        // base radius multiplier based on rank
    _padding: f32,
}

@group(0) @binding(0) var<uniform> tile: TileInfo;
@group(0) @binding(1) var tileSampler: sampler;
@group(0) @binding(2) var tileTexture: texture_2d<f32>;
@group(0) @binding(3) var<uniform> maskParams: CityMaskParams;
@group(0) @binding(4) var<storage, read> cities: array<City>;

@vertex
fn vs_main(in: VertexInput) -> VertexOutput {
    var out: VertexOutput;
    // Transform position: scale by tile size, offset, then to NDC
    let pos = in.position * tile.scale + tile.offset;
    out.position = vec4<f32>(pos, 0.0, 1.0);
    out.texCoord = in.texCoord;

    // Calculate world position (lon/lat) from texture coords and geo bounds
    let lon = mix(tile.geoBounds.x, tile.geoBounds.z, in.texCoord.x);
    let lat = mix(tile.geoBounds.w, tile.geoBounds.y, in.texCoord.y); // Y is flipped
    out.worldPos = vec2<f32>(lon, lat);

    return out;
}

// Calculate distance between two lon/lat points (approximate, in degrees)
fn geoDistance(p1: vec2<f32>, p2: vec2<f32>) -> f32 {
    let latScale = cos(radians((p1.y + p2.y) * 0.5));
    let dx = (p2.x - p1.x) * latScale;
    let dy = p2.y - p1.y;
    return sqrt(dx * dx + dy * dy);
}

@fragment
fn fs_main(in: VertexOutput) -> @location(0) vec4<f32> {
    let texColor = textureSample(tileTexture, tileSampler, in.texCoord);

    // If mask disabled or radius is 100%, show full texture
    if (maskParams.enableMask < 0.5 || maskParams.radiusPercent >= 99.9) {
        return texColor;
    }

    // If radius is 0%, show only sea (alpha = 0 for land)
    if (maskParams.radiusPercent <= 0.1) {
        // Return desaturated/fog color for areas outside cities
        let fog = vec4<f32>(0.7, 0.75, 0.8, 1.0);
        return fog;
    }

    // Calculate minimum distance to any city
    var minDist: f32 = 1000.0;
    let cityCount = i32(maskParams.cityCount);

    for (var i: i32 = 0; i < cityCount; i = i + 1) {
        let city = cities[i];
        let dist = geoDistance(in.worldPos, city.pos);
        // Adjust distance by city's radius multiplier
        let adjustedDist = dist / max(city.radius, 0.1);
        minDist = min(minDist, adjustedDist);
    }

    // Calculate effective radius based on slider (0-100%)
    // Base radius is in degrees (~0.1 degrees = ~11km at equator)
    let effectiveRadius = maskParams.baseRadius * (maskParams.radiusPercent / 100.0);

    // Smooth falloff at city edges
    let edge = effectiveRadius * 0.8;
    let fade = smoothstep(effectiveRadius, edge, minDist);

    // Mix between fog and texture based on distance
    let fog = vec4<f32>(0.75, 0.8, 0.85, 1.0);
    return mix(fog, texColor, fade);
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
				Visibility: wgpu.ShaderStage_Vertex | wgpu.ShaderStage_Fragment,
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
			{
				Binding:    3,
				Visibility: wgpu.ShaderStage_Fragment,
				Buffer:     wgpu.BufferBindingLayout{Type: wgpu.BufferBindingType_Uniform},
			},
			{
				Binding:    4,
				Visibility: wgpu.ShaderStage_Fragment,
				Buffer:     wgpu.BufferBindingLayout{Type: wgpu.BufferBindingType_ReadOnlyStorage},
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

	return &TileTexture{Texture: texture, View: view}, nil
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
	OffsetX   float32
	OffsetY   float32
	ScaleX    float32
	ScaleY    float32
	MinLon    float32
	MinLat    float32
	MaxLon    float32
	MaxLat    float32
}

// CityMaskParams matches shader uniform
type CityMaskParams struct {
	RadiusPercent float32
	EnableMask    float32
	CityCount     float32
	BaseRadius    float32
}

// tileToGeoBounds converts tile coordinates to geographic bounds
func tileToGeoBounds(x, y, zoom int) (minLon, minLat, maxLon, maxLat float64) {
	n := float64(int(1) << zoom)
	minLon = float64(x)/n*360.0 - 180.0
	maxLon = float64(x+1)/n*360.0 - 180.0

	// Web Mercator to latitude conversion
	maxLat = 180.0 / math.Pi * (2*math.Atan(math.Exp(math.Pi*(1-2*float64(y)/n))) - math.Pi/2)
	minLat = 180.0 / math.Pi * (2*math.Atan(math.Exp(math.Pi*(1-2*float64(y+1)/n))) - math.Pi/2)
	return
}

// UpdateCitiesForView fetches vector tile data for the current view and updates city positions
func (r *Renderer) UpdateCitiesForView(lat, lon float64, zoom int) {
	if r.vectorTileCache == nil {
		return
	}

	// Convert lat/lon to tile coordinates at a lower zoom for city data
	// Use zoom 10 for city data (covers larger area)
	cityZoom := 10
	if zoom < 10 {
		cityZoom = zoom
	}

	// Calculate tile coordinates
	n := float64(int(1) << cityZoom)
	tileX := int((lon + 180.0) / 360.0 * n)
	tileY := int((1.0 - math.Log(math.Tan(lat*math.Pi/180.0)+1.0/math.Cos(lat*math.Pi/180.0))/math.Pi) / 2.0 * n)

	// Fetch surrounding tiles
	cities := make([]CityData, 0, MaxCities)

	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			tx := tileX + dx
			ty := tileY + dy
			if tx < 0 || ty < 0 || tx >= int(n) || ty >= int(n) {
				continue
			}

			data, err := r.vectorTileCache.GetTile(cityZoom, tx, ty)
			if err != nil {
				continue
			}

			// Filter for cities and towns
			for _, place := range data.Places {
				if place.Class == "city" || place.Class == "town" {
					// Calculate radius based on rank (lower rank = larger city)
					radius := float32(1.0)
					if place.Rank > 0 {
						radius = float32(15.0 / float64(place.Rank+5))
					}

					cities = append(cities, CityData{
						X:      float32(place.Location.Lon()),
						Y:      float32(place.Location.Lat()),
						Radius: radius,
					})

					if len(cities) >= MaxCities {
						break
					}
				}
			}
			if len(cities) >= MaxCities {
				break
			}
		}
		if len(cities) >= MaxCities {
			break
		}
	}

	r.citiesMu.Lock()
	r.cities = cities
	r.citiesMu.Unlock()
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

	// Get config for city mask
	cfg := config.Get()
	radiusPercent := float32(cfg.Rendering.CityRadiusPercent)
	enableMask := float32(0.0)
	if cfg.Features.EnableCityMask {
		enableMask = 1.0
	}

	// Get city data
	r.citiesMu.RLock()
	cityCount := len(r.cities)
	cities := make([]CityData, len(r.cities))
	copy(cities, r.cities)
	r.citiesMu.RUnlock()

	// Create mask params uniform buffer
	maskParams := CityMaskParams{
		RadiusPercent: radiusPercent,
		EnableMask:    enableMask,
		CityCount:     float32(cityCount),
		BaseRadius:    0.15, // ~16km at equator
	}
	maskParamsBuffer, _ := r.device.CreateBufferInit(&wgpu.BufferInitDescriptor{
		Label:    "mask_params_uniform",
		Contents: wgpu.ToBytes([]CityMaskParams{maskParams}),
		Usage:    wgpu.BufferUsage_Uniform,
	})
	defer maskParamsBuffer.Release()

	// Create city storage buffer (need at least 1 element for valid buffer)
	if len(cities) == 0 {
		cities = append(cities, CityData{X: 0, Y: 0, Radius: 0})
	}
	cityBuffer, _ := r.device.CreateBufferInit(&wgpu.BufferInitDescriptor{
		Label:    "city_storage",
		Contents: wgpu.ToBytes(cities),
		Usage:    wgpu.BufferUsage_Storage,
	})
	defer cityBuffer.Release()

	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			coord := tiles.TileCoord{X: x, Y: y, Zoom: cam.Zoom}
			screenX, screenY := cam.GetTileScreenPosition(x, y)

			// Convert screen position to NDC (-1 to 1)
			// Screen coords: (0,0) top-left, (width,height) bottom-right
			// NDC coords: (-1,-1) bottom-left, (1,1) top-right
			ndcX := (float32(screenX)/w)*2 - 1
			ndcY := 1 - (float32(screenY)/h)*2 // Flip Y

			// Get geographic bounds for this tile
			minLon, minLat, maxLon, maxLat := tileToGeoBounds(x, y, cam.Zoom)

			tileInfo := TileInfo{
				OffsetX: ndcX,
				OffsetY: ndcY - scaleY, // Move down by tile height (since we draw from top-left)
				ScaleX:  scaleX,
				ScaleY:  -scaleY, // Negative to flip texture vertically
				MinLon:  float32(minLon),
				MinLat:  float32(minLat),
				MaxLon:  float32(maxLon),
				MaxLat:  float32(maxLat),
			}

			r.texturesMu.RLock()
			tex, exists := r.textures[coord.String()]
			r.texturesMu.RUnlock()

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
					{Binding: 0, Buffer: uniformBuffer, Size: uint64(unsafe.Sizeof(TileInfo{}))},
					{Binding: 1, Sampler: r.sampler},
					{Binding: 2, TextureView: func() *wgpu.TextureView {
						if exists && tex != nil {
							return tex.View
						}
						return r.placeholder.View
					}()},
					{Binding: 3, Buffer: maskParamsBuffer, Size: uint64(unsafe.Sizeof(CityMaskParams{}))},
					{Binding: 4, Buffer: cityBuffer, Size: uint64(len(cities) * int(unsafe.Sizeof(CityData{})))},
				},
			})

			pass.SetBindGroup(0, tempBindGroup, nil)
			pass.DrawIndexed(6, 1, 0, 0, 0)
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
		tex.View.Release()
		tex.Texture.Release()
	}
	r.texturesMu.Unlock()

	if r.placeholder != nil {
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
