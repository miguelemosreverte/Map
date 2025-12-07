package app

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework QuartzCore -framework Metal

#import <Cocoa/Cocoa.h>
#import <QuartzCore/CAMetalLayer.h>
#import <Metal/Metal.h>

void* setupMetalLayer(void* nsWindow) {
    if (nsWindow == NULL) {
        return NULL;
    }

    NSWindow* window = (__bridge NSWindow*)nsWindow;
    NSView* view = [window contentView];

    if (view == nil) {
        return NULL;
    }

    // Enable layer backing
    [view setWantsLayer:YES];

    // Create and configure Metal layer
    CAMetalLayer* metalLayer = [CAMetalLayer layer];
    metalLayer.device = MTLCreateSystemDefaultDevice();
    metalLayer.pixelFormat = MTLPixelFormatBGRA8Unorm;
    metalLayer.framebufferOnly = YES;
    metalLayer.frame = view.bounds;
    metalLayer.contentsScale = [window backingScaleFactor];

    // Set the layer
    [view setLayer:metalLayer];

    return (__bridge void*)metalLayer;
}
*/
import "C"

import (
	"fmt"
	"unsafe"

	"github.com/go-gl/glfw/v3.3/glfw"
	"github.com/rajveermalviya/go-webgpu/wgpu"
)

// CreateSurface creates a WebGPU surface from a GLFW window on macOS
func CreateSurface(instance *wgpu.Instance, window *glfw.Window) *wgpu.Surface {
	nsWindow := window.GetCocoaWindow()
	if nsWindow == nil {
		fmt.Println("Error: GetCocoaWindow returned nil")
		return nil
	}

	metalLayer := C.setupMetalLayer(nsWindow)
	if metalLayer == nil {
		fmt.Println("Error: setupMetalLayer returned nil")
		return nil
	}

	fmt.Printf("Metal layer created: %p\n", metalLayer)

	surface := instance.CreateSurface(&wgpu.SurfaceDescriptor{
		Label: "MainSurface",
		MetalLayer: &wgpu.SurfaceDescriptorFromMetalLayer{
			Layer: unsafe.Pointer(metalLayer),
		},
	})

	if surface == nil {
		fmt.Println("Error: CreateSurface returned nil")
	}

	return surface
}
