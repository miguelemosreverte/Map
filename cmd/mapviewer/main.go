package main

import (
	"fmt"
	"os"

	"mapviewer/internal/app"
)

func main() {
	fmt.Println("Map Viewer - WebGPU")
	fmt.Println("Controls:")
	fmt.Println("  Mouse drag    : Pan")
	fmt.Println("  Mouse wheel   : Zoom")
	fmt.Println("  WASD / Arrows : Pan")
	fmt.Println("  Shift         : Zoom in")
	fmt.Println("  Space         : Zoom out")
	fmt.Println("  Escape        : Exit")
	fmt.Println()

	application, err := app.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer application.Cleanup()

	if err := application.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
