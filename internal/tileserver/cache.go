package tileserver

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mapviewer/pkg/tiles"
)

// TileCache manages tile fetching and caching
type TileCache struct {
	cacheDir   string
	client     *http.Client
	inFlight   map[string]chan struct{}
	inFlightMu sync.Mutex
	fetchQueue chan tiles.TileCoord
	wg         sync.WaitGroup
}

// NewTileCache creates a new tile cache
func NewTileCache(cacheDir string, workers int) (*TileCache, error) {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	tc := &TileCache{
		cacheDir: cacheDir,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		inFlight:   make(map[string]chan struct{}),
		fetchQueue: make(chan tiles.TileCoord, 1000),
	}

	// Start background workers for prefetching
	for i := 0; i < workers; i++ {
		tc.wg.Add(1)
		go tc.worker()
	}

	return tc, nil
}

func (tc *TileCache) worker() {
	defer tc.wg.Done()
	for coord := range tc.fetchQueue {
		tc.fetchTile(coord)
	}
}

// Close shuts down the tile cache
func (tc *TileCache) Close() {
	close(tc.fetchQueue)
	tc.wg.Wait()
}

// tilePath returns the file path for a cached tile
func (tc *TileCache) tilePath(coord tiles.TileCoord) string {
	return filepath.Join(tc.cacheDir, fmt.Sprintf("%d_%d_%d.png", coord.Zoom, coord.X, coord.Y))
}

// GetTile returns tile data, fetching and caching if necessary
func (tc *TileCache) GetTile(coord tiles.TileCoord) ([]byte, error) {
	path := tc.tilePath(coord)

	// Check cache first
	if data, err := os.ReadFile(path); err == nil {
		return data, nil
	}

	// Fetch the tile
	data, err := tc.fetchTile(coord)
	if err != nil {
		return nil, err
	}

	// Queue adjacent tiles for prefetching
	tc.queuePrefetch(coord)

	return data, nil
}

// fetchTile downloads a tile from OSM and caches it
func (tc *TileCache) fetchTile(coord tiles.TileCoord) ([]byte, error) {
	key := coord.String()
	path := tc.tilePath(coord)

	// Check if already cached
	if data, err := os.ReadFile(path); err == nil {
		return data, nil
	}

	// Check if fetch is already in progress
	tc.inFlightMu.Lock()
	if ch, exists := tc.inFlight[key]; exists {
		tc.inFlightMu.Unlock()
		<-ch // Wait for the in-flight request to complete
		return os.ReadFile(path)
	}

	// Mark as in-flight
	ch := make(chan struct{})
	tc.inFlight[key] = ch
	tc.inFlightMu.Unlock()

	defer func() {
		tc.inFlightMu.Lock()
		delete(tc.inFlight, key)
		close(ch)
		tc.inFlightMu.Unlock()
	}()

	// Fetch from server
	url := coord.URL()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "MapViewer/1.0 (educational project)")

	resp, err := tc.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch tile: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tile server returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read tile data: %w", err)
	}

	// Cache to disk
	if err := os.WriteFile(path, data, 0644); err != nil {
		// Log but don't fail - we still have the data
		fmt.Printf("Warning: failed to cache tile: %v\n", err)
	}

	return data, nil
}

// queuePrefetch adds adjacent tiles to the prefetch queue
func (tc *TileCache) queuePrefetch(coord tiles.TileCoord) {
	adjacent := tiles.GetAdjacentTiles(coord)
	for _, adj := range adjacent {
		// Non-blocking send to queue
		select {
		case tc.fetchQueue <- adj:
		default:
			// Queue full, skip this tile
		}
	}
}

// PrefetchArea prefetches tiles for a given viewport area (5x area for smooth panning)
func (tc *TileCache) PrefetchArea(centerLat, centerLon float64, zoom int, viewportWidth, viewportHeight int) {
	tilesToFetch := tiles.GetPrefetchTiles(centerLat, centerLon, zoom, viewportWidth, viewportHeight)
	for _, coord := range tilesToFetch {
		select {
		case tc.fetchQueue <- coord:
		default:
			// Queue full
		}
	}
}

// IsCached checks if a tile is already cached
func (tc *TileCache) IsCached(coord tiles.TileCoord) bool {
	path := tc.tilePath(coord)
	_, err := os.Stat(path)
	return err == nil
}
