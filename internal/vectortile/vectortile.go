package vectortile

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/mvt"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/orb/maptile"
)

const (
	// OpenFreeMap vector tile endpoint
	TileURLTemplate = "https://tiles.openfreemap.org/planet/20251203_001001_pt/%d/%d/%d.pbf"
)

// VectorTile represents a parsed vector tile with its layers
type VectorTile struct {
	Coord  maptile.Tile
	Layers *mvt.Layers
}

// Place represents a city/town/village from the place layer
type Place struct {
	Name     string
	Class    string // city, town, village, hamlet, etc.
	Rank     int
	Location orb.Point
}

// TransportLine represents a road/rail from the transportation layer
type TransportLine struct {
	Class    string // motorway, rail, primary, secondary, etc.
	Geometry orb.Geometry
}

// WaterFeature represents water from the water layer
type WaterFeature struct {
	Class    string
	Geometry orb.Geometry
}

// TileData holds extracted features from a vector tile
type TileData struct {
	Places     []Place
	Transport  []TransportLine
	Water      []WaterFeature
	Boundaries []orb.Geometry
}

// VectorTileCache manages fetching and caching vector tiles
type VectorTileCache struct {
	client   *http.Client
	tiles    map[string]*TileData
	tilesMu  sync.RWMutex
	inFlight map[string]chan struct{}
	inFlightMu sync.Mutex
}

// NewVectorTileCache creates a new vector tile cache
func NewVectorTileCache() *VectorTileCache {
	return &VectorTileCache{
		client:   &http.Client{},
		tiles:    make(map[string]*TileData),
		inFlight: make(map[string]chan struct{}),
	}
}

// tileKey generates a cache key for a tile
func tileKey(z, x, y int) string {
	return fmt.Sprintf("%d/%d/%d", z, x, y)
}

// GetTile returns tile data, fetching if necessary
func (vtc *VectorTileCache) GetTile(z, x, y int) (*TileData, error) {
	key := tileKey(z, x, y)

	// Check cache
	vtc.tilesMu.RLock()
	if data, ok := vtc.tiles[key]; ok {
		vtc.tilesMu.RUnlock()
		return data, nil
	}
	vtc.tilesMu.RUnlock()

	// Check if fetch is in progress
	vtc.inFlightMu.Lock()
	if ch, exists := vtc.inFlight[key]; exists {
		vtc.inFlightMu.Unlock()
		<-ch
		vtc.tilesMu.RLock()
		data := vtc.tiles[key]
		vtc.tilesMu.RUnlock()
		return data, nil
	}

	// Mark as in-flight
	ch := make(chan struct{})
	vtc.inFlight[key] = ch
	vtc.inFlightMu.Unlock()

	// Fetch and parse
	data, err := vtc.fetchAndParse(z, x, y)

	vtc.inFlightMu.Lock()
	delete(vtc.inFlight, key)
	close(ch)
	vtc.inFlightMu.Unlock()

	if err != nil {
		return nil, err
	}

	// Cache result
	vtc.tilesMu.Lock()
	vtc.tiles[key] = data
	vtc.tilesMu.Unlock()

	return data, nil
}

// HasTile checks if a tile is cached
func (vtc *VectorTileCache) HasTile(z, x, y int) bool {
	key := tileKey(z, x, y)
	vtc.tilesMu.RLock()
	defer vtc.tilesMu.RUnlock()
	_, ok := vtc.tiles[key]
	return ok
}

// fetchAndParse downloads and parses a vector tile
func (vtc *VectorTileCache) fetchAndParse(z, x, y int) (*TileData, error) {
	url := fmt.Sprintf(TileURLTemplate, z, x, y)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "MapViewer/1.0")
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := vtc.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip error: %w", err)
		}
		defer gzReader.Close()
		reader = gzReader
	}

	rawData, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read error: %w", err)
	}

	// Parse MVT
	layers, err := mvt.Unmarshal(rawData)
	if err != nil {
		return nil, fmt.Errorf("mvt parse error: %w", err)
	}

	// Project to WGS84 coordinates
	tile := maptile.New(uint32(x), uint32(y), maptile.Zoom(z))
	layers.ProjectToWGS84(tile)

	// Extract features
	return extractFeatures(layers), nil
}

// extractFeatures extracts typed features from MVT layers
func extractFeatures(layers mvt.Layers) *TileData {
	data := &TileData{}

	for _, layer := range layers {
		switch layer.Name {
		case "place":
			data.Places = extractPlaces(layer)
		case "transportation":
			data.Transport = extractTransport(layer)
		case "water":
			data.Water = extractWater(layer)
		case "boundary":
			data.Boundaries = extractBoundaries(layer)
		}
	}

	return data
}

func extractPlaces(layer *mvt.Layer) []Place {
	places := make([]Place, 0, len(layer.Features))

	for _, f := range layer.Features {
		place := Place{}

		// Get properties
		if name, ok := f.Properties["name"].(string); ok {
			place.Name = name
		}
		if class, ok := f.Properties["class"].(string); ok {
			place.Class = class
		}
		if rank, ok := f.Properties["rank"].(float64); ok {
			place.Rank = int(rank)
		}

		// Get location
		if pt, ok := f.Geometry.(orb.Point); ok {
			place.Location = pt
			places = append(places, place)
		}
	}

	return places
}

func extractTransport(layer *mvt.Layer) []TransportLine {
	lines := make([]TransportLine, 0, len(layer.Features))

	for _, f := range layer.Features {
		line := TransportLine{Geometry: f.Geometry}

		if class, ok := f.Properties["class"].(string); ok {
			line.Class = class
		}

		lines = append(lines, line)
	}

	return lines
}

func extractWater(layer *mvt.Layer) []WaterFeature {
	features := make([]WaterFeature, 0, len(layer.Features))

	for _, f := range layer.Features {
		water := WaterFeature{Geometry: f.Geometry}

		if class, ok := f.Properties["class"].(string); ok {
			water.Class = class
		}

		features = append(features, water)
	}

	return features
}

func extractBoundaries(layer *mvt.Layer) []orb.Geometry {
	boundaries := make([]orb.Geometry, 0, len(layer.Features))

	for _, f := range layer.Features {
		boundaries = append(boundaries, f.Geometry)
	}

	return boundaries
}

// FilterPlacesByClass returns places matching the given classes
func FilterPlacesByClass(places []Place, classes ...string) []Place {
	classSet := make(map[string]bool)
	for _, c := range classes {
		classSet[c] = true
	}

	filtered := make([]Place, 0)
	for _, p := range places {
		if classSet[p.Class] {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

// FilterTransportByClass returns transport lines matching the given classes
func FilterTransportByClass(transport []TransportLine, classes ...string) []TransportLine {
	classSet := make(map[string]bool)
	for _, c := range classes {
		classSet[c] = true
	}

	filtered := make([]TransportLine, 0)
	for _, t := range transport {
		if classSet[t.Class] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// Compile-time check that we're using the geojson package (for potential future use)
var _ = geojson.NewFeature
