package main

import (
	"fmt"
	"mapviewer/internal/vectortile"
)

func main() {
	cache := vectortile.NewVectorTileCache()

	// Fetch tile for Amsterdam area at zoom 10
	// Amsterdam is around lat 52.37, lon 4.90
	// At zoom 10: x=527, y=339
	fmt.Println("Fetching vector tile for Amsterdam area (zoom 10)...")
	data, err := cache.GetTile(10, 527, 339)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Printf("\n=== Places: %d ===\n", len(data.Places))
	for i, p := range data.Places {
		if i < 15 {
			fmt.Printf("  %s (%s) rank=%d at (%.4f, %.4f)\n",
				p.Name, p.Class, p.Rank, p.Location.Lon(), p.Location.Lat())
		}
	}

	fmt.Printf("\n=== Transport lines: %d ===\n", len(data.Transport))
	classes := make(map[string]int)
	for _, t := range data.Transport {
		classes[t.Class]++
	}
	for class, count := range classes {
		fmt.Printf("  %s: %d\n", class, count)
	}

	fmt.Printf("\n=== Water features: %d ===\n", len(data.Water))

	fmt.Printf("\n=== Boundaries: %d ===\n", len(data.Boundaries))

	// Filter example: only cities
	cities := vectortile.FilterPlacesByClass(data.Places, "city")
	fmt.Printf("\n=== Cities only: %d ===\n", len(cities))
	for _, c := range cities {
		fmt.Printf("  %s (rank %d)\n", c.Name, c.Rank)
	}

	// Filter example: only rail
	rail := vectortile.FilterTransportByClass(data.Transport, "rail")
	fmt.Printf("\n=== Rail lines: %d ===\n", len(rail))
}
