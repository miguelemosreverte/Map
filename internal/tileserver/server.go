package tileserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"mapviewer/pkg/tiles"
)

// Server provides HTTP endpoints for tile fetching
type Server struct {
	cache  *TileCache
	port   int
	server *http.Server
}

// NewServer creates a new tile server
func NewServer(cache *TileCache, port int) *Server {
	return &Server{
		cache: cache,
		port:  port,
	}
}

// Start starts the tile server
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/tile/", s.handleTile)
	mux.HandleFunc("/prefetch", s.handlePrefetch)
	mux.HandleFunc("/health", s.handleHealth)

	s.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: mux,
	}

	fmt.Printf("Tile server starting on port %d\n", s.port)
	return s.server.ListenAndServe()
}

// Stop stops the tile server
func (s *Server) Stop() error {
	if s.server != nil {
		return s.server.Close()
	}
	return nil
}

// handleTile serves tile requests: /tile/{zoom}/{x}/{y}
func (s *Server) handleTile(w http.ResponseWriter, r *http.Request) {
	// Parse path: /tile/zoom/x/y
	path := strings.TrimPrefix(r.URL.Path, "/tile/")
	parts := strings.Split(path, "/")

	if len(parts) != 3 {
		http.Error(w, "Invalid tile path", http.StatusBadRequest)
		return
	}

	zoom, err := strconv.Atoi(parts[0])
	if err != nil {
		http.Error(w, "Invalid zoom", http.StatusBadRequest)
		return
	}

	x, err := strconv.Atoi(parts[1])
	if err != nil {
		http.Error(w, "Invalid x", http.StatusBadRequest)
		return
	}

	// Remove .png extension if present
	yStr := strings.TrimSuffix(parts[2], ".png")
	y, err := strconv.Atoi(yStr)
	if err != nil {
		http.Error(w, "Invalid y", http.StatusBadRequest)
		return
	}

	coord := tiles.TileCoord{X: x, Y: y, Zoom: zoom}
	data, err := s.cache.GetTile(coord)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get tile: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "max-age=86400") // Cache for 24 hours
	w.Write(data)
}

// PrefetchRequest represents a prefetch request
type PrefetchRequest struct {
	CenterLat      float64 `json:"centerLat"`
	CenterLon      float64 `json:"centerLon"`
	Zoom           int     `json:"zoom"`
	ViewportWidth  int     `json:"viewportWidth"`
	ViewportHeight int     `json:"viewportHeight"`
}

// handlePrefetch handles prefetch requests
func (s *Server) handlePrefetch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req PrefetchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Start prefetching in background
	go s.cache.PrefetchArea(req.CenterLat, req.CenterLon, req.Zoom, req.ViewportWidth, req.ViewportHeight)

	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"status":"prefetching"}`))
}

// handleHealth provides a health check endpoint
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}
