package config

import (
	"encoding/json"
	"os"
	"sync"
)

// Config holds application configuration and feature flags
type Config struct {
	// Feature flags
	Features Features `json:"features"`

	// Rendering parameters
	Rendering Rendering `json:"rendering"`
}

// Features contains feature flags for development
type Features struct {
	// ShowDevUI enables development UI controls (sliders, etc.)
	ShowDevUI bool `json:"show_dev_ui"`

	// EnableCityMask enables the city radius masking feature
	EnableCityMask bool `json:"enable_city_mask"`

	// EnableRoadWeights enables Voronoi-like road weight influence on city mask
	EnableRoadWeights bool `json:"enable_road_weights"`

	// EnableVectorOverlay enables rendering vector data on top of raster tiles
	EnableVectorOverlay bool `json:"enable_vector_overlay"`
}

// Rendering contains rendering parameters
type Rendering struct {
	// CityRadiusPercent controls the city mask radius (0-100)
	// 0 = cities invisible, 100 = cities fully visible
	CityRadiusPercent float64 `json:"city_radius_percent"`

	// RoadWeightInfluence controls how much roads extend the city mask (0-1)
	// 0 = no influence, 1 = full influence
	RoadWeightInfluence float64 `json:"road_weight_influence"`

	// RoadWeightDecay controls how quickly road influence decays with distance
	RoadWeightDecay float64 `json:"road_weight_decay"`
}

var (
	instance *Config
	once     sync.Once
	mu       sync.RWMutex
)

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	return &Config{
		Features: Features{
			ShowDevUI:           true,  // On by default for development
			EnableCityMask:      true,  // On by default for development
			EnableRoadWeights:   false, // Off until implemented
			EnableVectorOverlay: true,  // On by default
		},
		Rendering: Rendering{
			CityRadiusPercent:   100.0, // Full size by default
			RoadWeightInfluence: 0.3,
			RoadWeightDecay:     0.5,
		},
	}
}

// Get returns the global configuration instance
func Get() *Config {
	once.Do(func() {
		instance = DefaultConfig()
		// Try to load from file
		if data, err := os.ReadFile("config.json"); err == nil {
			json.Unmarshal(data, instance)
		}
	})
	return instance
}

// Load loads configuration from a file
func Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()

	if instance == nil {
		instance = DefaultConfig()
	}

	return json.Unmarshal(data, instance)
}

// Save saves configuration to a file
func Save(path string) error {
	mu.RLock()
	defer mu.RUnlock()

	if instance == nil {
		instance = DefaultConfig()
	}

	data, err := json.MarshalIndent(instance, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// SetCityRadius sets the city mask radius percentage
func SetCityRadius(percent float64) {
	mu.Lock()
	defer mu.Unlock()

	if instance == nil {
		instance = DefaultConfig()
	}

	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	instance.Rendering.CityRadiusPercent = percent
}

// GetCityRadius returns the current city radius percentage
func GetCityRadius() float64 {
	mu.RLock()
	defer mu.RUnlock()

	if instance == nil {
		return 100.0
	}
	return instance.Rendering.CityRadiusPercent
}

// AdjustCityRadius adjusts the city radius by a delta
func AdjustCityRadius(delta float64) float64 {
	mu.Lock()
	defer mu.Unlock()

	if instance == nil {
		instance = DefaultConfig()
	}

	instance.Rendering.CityRadiusPercent += delta
	if instance.Rendering.CityRadiusPercent < 0 {
		instance.Rendering.CityRadiusPercent = 0
	}
	if instance.Rendering.CityRadiusPercent > 100 {
		instance.Rendering.CityRadiusPercent = 100
	}

	return instance.Rendering.CityRadiusPercent
}
