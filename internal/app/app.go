package app

import (
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/go-gl/glfw/v3.3/glfw"
	"github.com/rajveermalviya/go-webgpu/wgpu"

	"mapviewer/internal/camera"
	"mapviewer/internal/renderer"
	"mapviewer/internal/tileserver"
	"mapviewer/pkg/tiles"
)

const (
	AmsterdamLat = 52.3676
	AmsterdamLon = 4.9041
	DefaultZoom  = 12

	DefaultWidth  = 1280
	DefaultHeight = 720

	KeyPanSpeed = 10.0
)

type App struct {
	window   *glfw.Window
	instance *wgpu.Instance
	surface  *wgpu.Surface
	adapter  *wgpu.Adapter
	device   *wgpu.Device
	queue    *wgpu.Queue

	renderer  *renderer.Renderer
	camera    *camera.Camera
	tileCache *tileserver.TileCache

	keys   map[glfw.Key]bool
	keysMu sync.RWMutex

	tileRequests chan tiles.TileCoord
	stopChan     chan struct{}

	width, height int
}

func New() (*App, error) {
	runtime.LockOSThread()

	if err := glfw.Init(); err != nil {
		return nil, fmt.Errorf("GLFW init failed: %w", err)
	}

	glfw.WindowHint(glfw.ClientAPI, glfw.NoAPI)
	glfw.WindowHint(glfw.Resizable, glfw.True)
	glfw.WindowHint(glfw.CocoaRetinaFramebuffer, glfw.True)

	window, err := glfw.CreateWindow(DefaultWidth, DefaultHeight, "Map Viewer - Amsterdam", nil, nil)
	if err != nil {
		glfw.Terminate()
		return nil, fmt.Errorf("window creation failed: %w", err)
	}

	app := &App{
		window:       window,
		width:        DefaultWidth,
		height:       DefaultHeight,
		keys:         make(map[glfw.Key]bool),
		tileRequests: make(chan tiles.TileCoord, 500),
		stopChan:     make(chan struct{}),
	}

	if err := app.initWebGPU(); err != nil {
		window.Destroy()
		glfw.Terminate()
		return nil, err
	}

	cache, err := tileserver.NewTileCache(".tile_cache", 8)
	if err != nil {
		return nil, fmt.Errorf("tile cache creation failed: %w", err)
	}
	app.tileCache = cache

	app.camera = camera.NewCamera(AmsterdamLat, AmsterdamLon, DefaultZoom, DefaultWidth, DefaultHeight)

	app.renderer, err = renderer.NewRenderer(app.adapter, app.device, app.queue, app.surface, uint32(DefaultWidth), uint32(DefaultHeight))
	if err != nil {
		return nil, fmt.Errorf("renderer creation failed: %w", err)
	}

	app.setupCallbacks()

	// Start tile loaders
	for i := 0; i < 4; i++ {
		go app.tileLoader()
	}

	app.prefetchTiles()

	return app, nil
}

func (app *App) initWebGPU() error {
	// Create instance with Metal backend explicitly
	app.instance = wgpu.CreateInstance(&wgpu.InstanceDescriptor{
		Backends: wgpu.InstanceBackend_Metal,
	})
	if app.instance == nil {
		return fmt.Errorf("failed to create WebGPU instance")
	}

	// Create surface
	app.surface = CreateSurface(app.instance, app.window)
	if app.surface == nil {
		return fmt.Errorf("surface creation failed")
	}

	// Request adapter - try with surface first, then without
	var err error
	app.adapter, err = app.instance.RequestAdapter(&wgpu.RequestAdapterOptions{
		CompatibleSurface: app.surface,
		PowerPreference:   wgpu.PowerPreference_HighPerformance,
		ForceFallbackAdapter: false,
	})
	if err != nil {
		// Try without surface constraint
		fmt.Println("Trying adapter without surface constraint...")
		app.adapter, err = app.instance.RequestAdapter(&wgpu.RequestAdapterOptions{
			PowerPreference: wgpu.PowerPreference_HighPerformance,
		})
		if err != nil {
			return fmt.Errorf("adapter request failed: %w", err)
		}
	}

	// Print adapter info
	props := app.adapter.GetProperties()
	fmt.Printf("GPU: %s (%s)\n", props.Name, props.DriverDescription)

	app.device, err = app.adapter.RequestDevice(&wgpu.DeviceDescriptor{
		Label: "MapViewerDevice",
	})
	if err != nil {
		return fmt.Errorf("device request failed: %w", err)
	}

	app.queue = app.device.GetQueue()
	return nil
}

func (app *App) setupCallbacks() {
	app.window.SetFramebufferSizeCallback(func(w *glfw.Window, width, height int) {
		app.width = width
		app.height = height
		app.camera.SetViewport(width, height)
		app.renderer.Resize(uint32(width), uint32(height))
		app.prefetchTiles()
	})

	app.window.SetMouseButtonCallback(func(w *glfw.Window, button glfw.MouseButton, action glfw.Action, mods glfw.ModifierKey) {
		if button == glfw.MouseButtonLeft {
			x, y := w.GetCursorPos()
			if action == glfw.Press {
				app.camera.StartDrag(x, y)
			} else {
				app.camera.EndDrag()
				app.prefetchTiles()
			}
		}
	})

	app.window.SetCursorPosCallback(func(w *glfw.Window, x, y float64) {
		if app.camera.IsDragging() {
			app.camera.Drag(x, y)
		}
	})

	app.window.SetScrollCallback(func(w *glfw.Window, xoff, yoff float64) {
		x, y := w.GetCursorPos()
		if yoff > 0 {
			app.camera.ZoomAtPoint(1, x, y)
		} else if yoff < 0 {
			app.camera.ZoomAtPoint(-1, x, y)
		}
		app.prefetchTiles()
	})

	app.window.SetKeyCallback(func(w *glfw.Window, key glfw.Key, scancode int, action glfw.Action, mods glfw.ModifierKey) {
		app.keysMu.Lock()
		if action == glfw.Press {
			app.keys[key] = true
		} else if action == glfw.Release {
			app.keys[key] = false
		}
		app.keysMu.Unlock()

		// Handle single-press actions (not held)
		if action == glfw.Press {
			switch key {
			case glfw.KeyEscape:
				w.SetShouldClose(true)
			case glfw.KeySpace:
				app.camera.ZoomOut()
				app.prefetchTiles()
			case glfw.KeyLeftShift, glfw.KeyRightShift:
				app.camera.ZoomIn()
				app.prefetchTiles()
			}
		}
	})
}

func (app *App) processInput() {
	app.keysMu.RLock()
	defer app.keysMu.RUnlock()

	panX, panY := 0.0, 0.0

	// W/Up = move map down = camera moves up = positive pan
	if app.keys[glfw.KeyW] || app.keys[glfw.KeyUp] {
		panY += KeyPanSpeed
	}
	if app.keys[glfw.KeyS] || app.keys[glfw.KeyDown] {
		panY -= KeyPanSpeed
	}
	if app.keys[glfw.KeyA] || app.keys[glfw.KeyLeft] {
		panX += KeyPanSpeed
	}
	if app.keys[glfw.KeyD] || app.keys[glfw.KeyRight] {
		panX -= KeyPanSpeed
	}

	if panX != 0 || panY != 0 {
		app.camera.Pan(panX, panY)
	}

	// Note: Zoom is handled in key callback (single press only)
}

func (app *App) tileLoader() {
	for {
		select {
		case <-app.stopChan:
			return
		case coord := <-app.tileRequests:
			if app.renderer.HasTile(coord) {
				continue
			}
			data, err := app.tileCache.GetTile(coord)
			if err != nil {
				fmt.Printf("Tile load error %s: %v\n", coord.String(), err)
				continue
			}
			fmt.Printf("Loaded tile %s (%d bytes)\n", coord.String(), len(data))
			if err := app.renderer.UploadTile(coord, data); err != nil {
				fmt.Printf("Upload error %s: %v\n", coord.String(), err)
			}
		}
	}
}

func (app *App) prefetchTiles() {
	tilesToLoad := tiles.GetPrefetchTiles(app.camera.Lat, app.camera.Lon, app.camera.Zoom, app.width, app.height)
	for _, coord := range tilesToLoad {
		select {
		case app.tileRequests <- coord:
		default:
		}
	}
}

func (app *App) loadVisibleTiles() {
	visible := tiles.GetVisibleTiles(app.camera.Lat, app.camera.Lon, app.camera.Zoom, app.width, app.height)
	for _, coord := range visible {
		if !app.renderer.HasTile(coord) {
			select {
			case app.tileRequests <- coord:
			default:
			}
		}
	}
}

func (app *App) Run() error {
	lastTime := time.Now()
	frames := 0

	for !app.window.ShouldClose() {
		glfw.PollEvents()
		app.processInput()
		app.loadVisibleTiles()

		if err := app.renderer.Render(app.camera); err != nil {
			fmt.Printf("Render error: %v\n", err)
		}

		frames++
		if time.Since(lastTime) >= time.Second {
			app.window.SetTitle(fmt.Sprintf("Map Viewer - Amsterdam | Zoom: %d | FPS: %d", app.camera.Zoom, frames))
			frames = 0
			lastTime = time.Now()
		}
	}

	return nil
}

func (app *App) Cleanup() {
	close(app.stopChan)
	if app.renderer != nil {
		app.renderer.Release()
	}
	if app.tileCache != nil {
		app.tileCache.Close()
	}
	if app.queue != nil {
		app.queue.Release()
	}
	if app.device != nil {
		app.device.Release()
	}
	if app.adapter != nil {
		app.adapter.Release()
	}
	if app.surface != nil {
		app.surface.Release()
	}
	if app.instance != nil {
		app.instance.Release()
	}
	if app.window != nil {
		app.window.Destroy()
	}
	glfw.Terminate()
}
