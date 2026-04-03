package featureflip

import (
	"encoding/json"
	"sync"
	"time"
)

// ForTesting creates a Client pre-populated with the given flag overrides.
// No network calls or background goroutines are started.
//
// The overrides map keys are flag keys and values are the flag values to return.
// Supported value types: bool, string, float64/int, and any JSON-serializable type.
//
// Usage:
//
//	client := featureflip.ForTesting(map[string]any{
//	    "my-bool-flag":   true,
//	    "my-string-flag": "variant-a",
//	})
//	defer client.Close()
func ForTesting(overrides map[string]any) *Client {
	s := newStore()

	flags := make([]flagDTO, 0, len(overrides))
	for key, val := range overrides {
		raw, err := json.Marshal(val)
		if err != nil {
			continue
		}

		flags = append(flags, flagDTO{
			Key:     key,
			Version: 1,
			Type:    inferType(val),
			Enabled: true,
			Variations: []variationDTO{
				{Key: "value", Value: json.RawMessage(raw)},
			},
			Fallthrough:  serveConfig{Type: "Fixed", Variation: "value"},
			OffVariation: "value",
		})
	}

	s.setAll(flags, nil)

	// Create a no-op event processor (nil httpClient causes enqueue/flush to be no-ops).
	ep := newEventProcessor(nil, 100, 30*time.Second)

	return &Client{
		store:       s,
		ep:          ep,
		initialized: true,
		closeOnce:   sync.Once{},
	}
}

// inferType returns the Featureflip flag type string for a Go value.
func inferType(v any) string {
	switch v.(type) {
	case bool:
		return "Boolean"
	case string:
		return "String"
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return "Number"
	default:
		return "Json"
	}
}
