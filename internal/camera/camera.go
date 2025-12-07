package camera

import (
	"math"
)

const (
	MinZoom = 2
	MaxZoom = 18
)

// Camera represents the map camera/viewport
type Camera struct {
	// Geographic position (center of view)
	Lat float64
	Lon float64

	// Zoom level (2-18 for OSM tiles)
	Zoom int

	// Sub-pixel offset for smooth panning
	OffsetX float64
	OffsetY float64

	// Viewport dimensions
	ViewportWidth  int
	ViewportHeight int

	// Movement speeds
	PanSpeed  float64
	ZoomSpeed float64

	// For smooth movement
	TargetLat float64
	TargetLon float64

	// State tracking
	isDragging   bool
	lastDragX    float64
	lastDragY    float64
}

// NewCamera creates a new camera centered on given coordinates
func NewCamera(lat, lon float64, zoom int, width, height int) *Camera {
	return &Camera{
		Lat:            lat,
		Lon:            lon,
		Zoom:           zoom,
		TargetLat:      lat,
		TargetLon:      lon,
		ViewportWidth:  width,
		ViewportHeight: height,
		PanSpeed:       0.001,
		ZoomSpeed:      1.0,
	}
}

// SetViewport updates the viewport dimensions
func (c *Camera) SetViewport(width, height int) {
	c.ViewportWidth = width
	c.ViewportHeight = height
}

// Pan moves the camera by the given pixel delta
func (c *Camera) Pan(deltaX, deltaY float64) {
	// Convert pixel movement to geographic movement
	// At zoom level z, there are 2^z tiles, each 256 pixels
	// The world is 360 degrees wide and ~170 degrees tall (Mercator)
	scale := math.Pow(2, float64(c.Zoom))
	tileSize := 256.0

	// Degrees per pixel
	lonPerPixel := 360.0 / (scale * tileSize)

	// Latitude is more complex due to Mercator projection
	latRad := c.Lat * math.Pi / 180.0
	metersPerPixel := 156543.03392 * math.Cos(latRad) / scale
	latPerPixel := metersPerPixel / 111319.9 // meters per degree at equator

	c.Lon -= deltaX * lonPerPixel
	c.Lat += deltaY * latPerPixel

	c.clampPosition()
}

// ZoomIn increases zoom level
func (c *Camera) ZoomIn() {
	if c.Zoom < MaxZoom {
		c.Zoom++
	}
}

// ZoomOut decreases zoom level
func (c *Camera) ZoomOut() {
	if c.Zoom > MinZoom {
		c.Zoom--
	}
}

// ZoomTo sets a specific zoom level
func (c *Camera) ZoomTo(zoom int) {
	if zoom < MinZoom {
		zoom = MinZoom
	}
	if zoom > MaxZoom {
		zoom = MaxZoom
	}
	c.Zoom = zoom
}

// ZoomAtPoint zooms in/out centered on a specific screen point
func (c *Camera) ZoomAtPoint(delta int, screenX, screenY float64) {
	// Get the geographic position under the cursor before zoom
	geoX, geoY := c.ScreenToGeo(screenX, screenY)

	// Apply zoom
	newZoom := c.Zoom + delta
	if newZoom < MinZoom {
		newZoom = MinZoom
	}
	if newZoom > MaxZoom {
		newZoom = MaxZoom
	}

	if newZoom == c.Zoom {
		return
	}

	c.Zoom = newZoom

	// Get the new screen position of the same geographic point
	newScreenX, newScreenY := c.GeoToScreen(geoX, geoY)

	// Adjust camera to keep the point under the cursor
	deltaScreenX := screenX - newScreenX
	deltaScreenY := screenY - newScreenY

	c.Pan(-deltaScreenX, deltaScreenY)
}

// ScreenToGeo converts screen coordinates to geographic coordinates
func (c *Camera) ScreenToGeo(screenX, screenY float64) (lon, lat float64) {
	scale := math.Pow(2, float64(c.Zoom))
	tileSize := 256.0

	// Center of screen in pixels from world origin
	centerX := (c.Lon + 180.0) / 360.0 * scale * tileSize
	latRad := c.Lat * math.Pi / 180.0
	centerY := (1.0 - math.Log(math.Tan(latRad)+1.0/math.Cos(latRad))/math.Pi) / 2.0 * scale * tileSize

	// Offset from center
	offsetX := screenX - float64(c.ViewportWidth)/2
	offsetY := screenY - float64(c.ViewportHeight)/2

	// World pixel position
	worldX := centerX + offsetX
	worldY := centerY + offsetY

	// Convert back to geo
	lon = worldX/(scale*tileSize)*360.0 - 180.0
	latRad = math.Atan(math.Sinh(math.Pi * (1 - 2*worldY/(scale*tileSize))))
	lat = latRad * 180.0 / math.Pi

	return lon, lat
}

// GeoToScreen converts geographic coordinates to screen coordinates
func (c *Camera) GeoToScreen(lon, lat float64) (screenX, screenY float64) {
	scale := math.Pow(2, float64(c.Zoom))
	tileSize := 256.0

	// Center of screen in pixels from world origin
	centerX := (c.Lon + 180.0) / 360.0 * scale * tileSize
	latRad := c.Lat * math.Pi / 180.0
	centerY := (1.0 - math.Log(math.Tan(latRad)+1.0/math.Cos(latRad))/math.Pi) / 2.0 * scale * tileSize

	// Target position in world pixels
	targetX := (lon + 180.0) / 360.0 * scale * tileSize
	targetLatRad := lat * math.Pi / 180.0
	targetY := (1.0 - math.Log(math.Tan(targetLatRad)+1.0/math.Cos(targetLatRad))/math.Pi) / 2.0 * scale * tileSize

	// Screen position
	screenX = targetX - centerX + float64(c.ViewportWidth)/2
	screenY = targetY - centerY + float64(c.ViewportHeight)/2

	return screenX, screenY
}

// StartDrag begins a drag operation
func (c *Camera) StartDrag(x, y float64) {
	c.isDragging = true
	c.lastDragX = x
	c.lastDragY = y
}

// Drag continues a drag operation
func (c *Camera) Drag(x, y float64) {
	if !c.isDragging {
		return
	}

	deltaX := x - c.lastDragX
	deltaY := y - c.lastDragY

	c.Pan(deltaX, deltaY)

	c.lastDragX = x
	c.lastDragY = y
}

// EndDrag ends a drag operation
func (c *Camera) EndDrag() {
	c.isDragging = false
}

// IsDragging returns whether a drag is in progress
func (c *Camera) IsDragging() bool {
	return c.isDragging
}

// clampPosition ensures the camera stays within valid bounds
func (c *Camera) clampPosition() {
	// Clamp longitude to -180 to 180
	for c.Lon > 180 {
		c.Lon -= 360
	}
	for c.Lon < -180 {
		c.Lon += 360
	}

	// Clamp latitude to valid Mercator range
	if c.Lat > 85.0511 {
		c.Lat = 85.0511
	}
	if c.Lat < -85.0511 {
		c.Lat = -85.0511
	}
}

// GetTileBounds returns the tile coordinates for the current viewport
func (c *Camera) GetTileBounds() (minX, minY, maxX, maxY int) {
	scale := math.Pow(2, float64(c.Zoom))
	tileSize := 256.0
	maxTile := int(scale) - 1

	// Center tile
	centerTileX := (c.Lon + 180.0) / 360.0 * scale
	latRad := c.Lat * math.Pi / 180.0
	centerTileY := (1.0 - math.Log(math.Tan(latRad)+1.0/math.Cos(latRad))/math.Pi) / 2.0 * scale

	// How many tiles fit in viewport
	tilesX := float64(c.ViewportWidth) / tileSize / 2
	tilesY := float64(c.ViewportHeight) / tileSize / 2

	minX = int(math.Floor(centerTileX - tilesX - 1))
	maxX = int(math.Ceil(centerTileX + tilesX + 1))
	minY = int(math.Floor(centerTileY - tilesY - 1))
	maxY = int(math.Ceil(centerTileY + tilesY + 1))

	// Clamp to valid range
	if minX < 0 {
		minX = 0
	}
	if minY < 0 {
		minY = 0
	}
	if maxX > maxTile {
		maxX = maxTile
	}
	if maxY > maxTile {
		maxY = maxTile
	}

	return minX, minY, maxX, maxY
}

// GetTileScreenPosition returns the screen position for a tile's top-left corner
func (c *Camera) GetTileScreenPosition(tileX, tileY int) (screenX, screenY float64) {
	scale := math.Pow(2, float64(c.Zoom))
	tileSize := 256.0

	// Center position in tile coordinates
	centerTileX := (c.Lon + 180.0) / 360.0 * scale
	latRad := c.Lat * math.Pi / 180.0
	centerTileY := (1.0 - math.Log(math.Tan(latRad)+1.0/math.Cos(latRad))/math.Pi) / 2.0 * scale

	// Offset from center in tiles
	offsetX := float64(tileX) - centerTileX
	offsetY := float64(tileY) - centerTileY

	// Convert to screen position
	screenX = float64(c.ViewportWidth)/2 + offsetX*tileSize
	screenY = float64(c.ViewportHeight)/2 + offsetY*tileSize

	return screenX, screenY
}
