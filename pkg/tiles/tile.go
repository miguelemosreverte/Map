package tiles

import (
	"fmt"
	"math"
)

// TileCoord represents a tile coordinate in the slippy map format
type TileCoord struct {
	X    int
	Y    int
	Zoom int
}

func (t TileCoord) String() string {
	return fmt.Sprintf("%d/%d/%d", t.Zoom, t.X, t.Y)
}

// URL returns the OpenStreetMap tile URL (no labels variant)
func (t TileCoord) URL() string {
	// Using Carto's no-labels basemap for a cleaner Paradox-style look
	return fmt.Sprintf("https://basemaps.cartocdn.com/rastertiles/voyager_nolabels/%d/%d/%d.png", t.Zoom, t.X, t.Y)
}

// LatLonToTile converts latitude/longitude to tile coordinates at a given zoom level
func LatLonToTile(lat, lon float64, zoom int) TileCoord {
	n := math.Pow(2, float64(zoom))
	x := int((lon + 180.0) / 360.0 * n)
	latRad := lat * math.Pi / 180.0
	y := int((1.0 - math.Log(math.Tan(latRad)+1.0/math.Cos(latRad))/math.Pi) / 2.0 * n)

	// Clamp values
	if x < 0 {
		x = 0
	}
	maxTile := int(n) - 1
	if x > maxTile {
		x = maxTile
	}
	if y < 0 {
		y = 0
	}
	if y > maxTile {
		y = maxTile
	}

	return TileCoord{X: x, Y: y, Zoom: zoom}
}

// TileToLatLon converts tile coordinates to latitude/longitude (top-left corner)
func TileToLatLon(t TileCoord) (lat, lon float64) {
	n := math.Pow(2, float64(t.Zoom))
	lon = float64(t.X)/n*360.0 - 180.0
	latRad := math.Atan(math.Sinh(math.Pi * (1 - 2*float64(t.Y)/n)))
	lat = latRad * 180.0 / math.Pi
	return lat, lon
}

// GetAdjacentTiles returns adjacent tiles in priority order for prefetching
// Order: right, left, down, up (as specified)
func GetAdjacentTiles(t TileCoord) []TileCoord {
	maxTile := int(math.Pow(2, float64(t.Zoom))) - 1
	adjacent := make([]TileCoord, 0, 4)

	// Right
	if t.X+1 <= maxTile {
		adjacent = append(adjacent, TileCoord{X: t.X + 1, Y: t.Y, Zoom: t.Zoom})
	}
	// Left
	if t.X-1 >= 0 {
		adjacent = append(adjacent, TileCoord{X: t.X - 1, Y: t.Y, Zoom: t.Zoom})
	}
	// Down
	if t.Y+1 <= maxTile {
		adjacent = append(adjacent, TileCoord{X: t.X, Y: t.Y + 1, Zoom: t.Zoom})
	}
	// Up
	if t.Y-1 >= 0 {
		adjacent = append(adjacent, TileCoord{X: t.X, Y: t.Y - 1, Zoom: t.Zoom})
	}

	return adjacent
}

// GetVisibleTiles returns all tiles visible in a viewport
func GetVisibleTiles(centerLat, centerLon float64, zoom int, viewportWidth, viewportHeight int) []TileCoord {
	tileSize := 256 // Standard tile size

	centerTile := LatLonToTile(centerLat, centerLon, zoom)

	// Calculate how many tiles fit in the viewport (add buffer for smooth scrolling)
	tilesX := (viewportWidth / tileSize) + 3
	tilesY := (viewportHeight / tileSize) + 3

	halfX := tilesX / 2
	halfY := tilesY / 2

	maxTile := int(math.Pow(2, float64(zoom))) - 1
	tiles := make([]TileCoord, 0, tilesX*tilesY)

	for dy := -halfY; dy <= halfY; dy++ {
		for dx := -halfX; dx <= halfX; dx++ {
			x := centerTile.X + dx
			y := centerTile.Y + dy

			if x >= 0 && x <= maxTile && y >= 0 && y <= maxTile {
				tiles = append(tiles, TileCoord{X: x, Y: y, Zoom: zoom})
			}
		}
	}

	return tiles
}

// GetPrefetchTiles returns tiles to prefetch (5x viewport area)
func GetPrefetchTiles(centerLat, centerLon float64, zoom int, viewportWidth, viewportHeight int) []TileCoord {
	tileSize := 256

	centerTile := LatLonToTile(centerLat, centerLon, zoom)

	// 5x area means sqrt(5) ≈ 2.24x in each dimension, let's use 2.5x
	tilesX := int(float64(viewportWidth/tileSize+2) * 2.5)
	tilesY := int(float64(viewportHeight/tileSize+2) * 2.5)

	halfX := tilesX / 2
	halfY := tilesY / 2

	maxTile := int(math.Pow(2, float64(zoom))) - 1
	tiles := make([]TileCoord, 0, tilesX*tilesY*3) // Room for current + adjacent zoom levels

	// Current zoom level tiles (highest priority)
	for dy := -halfY; dy <= halfY; dy++ {
		for dx := -halfX; dx <= halfX; dx++ {
			x := centerTile.X + dx
			y := centerTile.Y + dy

			if x >= 0 && x <= maxTile && y >= 0 && y <= maxTile {
				tiles = append(tiles, TileCoord{X: x, Y: y, Zoom: zoom})
			}
		}
	}

	// Also prefetch adjacent zoom levels for smoother zooming
	for _, zoomOffset := range []int{-1, 1} {
		adjZoom := zoom + zoomOffset
		if adjZoom < 2 || adjZoom > 18 {
			continue
		}

		adjCenterTile := LatLonToTile(centerLat, centerLon, adjZoom)
		adjMaxTile := int(math.Pow(2, float64(adjZoom))) - 1

		// Fewer tiles for adjacent zoom levels
		adjHalfX := halfX / 2
		adjHalfY := halfY / 2
		if zoomOffset == 1 {
			// For zooming in, we need more tiles since they're smaller
			adjHalfX = halfX
			adjHalfY = halfY
		}

		for dy := -adjHalfY; dy <= adjHalfY; dy++ {
			for dx := -adjHalfX; dx <= adjHalfX; dx++ {
				x := adjCenterTile.X + dx
				y := adjCenterTile.Y + dy

				if x >= 0 && x <= adjMaxTile && y >= 0 && y <= adjMaxTile {
					tiles = append(tiles, TileCoord{X: x, Y: y, Zoom: adjZoom})
				}
			}
		}
	}

	return tiles
}
