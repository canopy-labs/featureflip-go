# Featureflip Go SDK

Go SDK for [Featureflip](https://featureflip.io) - evaluate feature flags locally with near-zero latency.

## Installation

```bash
go get github.com/canopy-labs/featureflip-go
```

## Quick Start

```go
package main

import (
	"fmt"
	"log"

	featureflip "github.com/canopy-labs/featureflip-go"
)

func main() {
	client, err := featureflip.NewClient("your-sdk-key")
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	ctx := featureflip.EvaluationContext{UserID: "user-123"}
	enabled := client.BoolVariation("my-feature", ctx, false)

	if enabled {
		fmt.Println("Feature is enabled!")
	}
}
```

## Configuration

```go
client, err := featureflip.NewClient("your-sdk-key",
	featureflip.WithBaseURL("https://eval.featureflip.io"),   // default
	featureflip.WithStreaming(true),                           // default
	featureflip.WithPollInterval(30 * time.Second),           // if streaming=false
	featureflip.WithFlushInterval(30 * time.Second),          // event flush interval
	featureflip.WithFlushBatchSize(100),                      // events per batch
	featureflip.WithInitTimeout(10 * time.Second),            // max wait for init
	featureflip.WithConnectTimeout(5 * time.Second),          // HTTP connect timeout
	featureflip.WithReadTimeout(10 * time.Second),            // HTTP read timeout
)
```

The SDK key can also be set via the `FEATUREFLIP_SDK_KEY` environment variable.

## Evaluation

```go
ctx := featureflip.EvaluationContext{UserID: "123"}

// Boolean flag
enabled := client.BoolVariation("feature-key", ctx, false)

// String flag
tier := client.StringVariation("pricing-tier", ctx, "free")

// Number flag
limit := client.Float64Variation("rate-limit", ctx, 100.0)

// JSON flag
config := client.JSONVariation("ui-config", ctx, map[string]any{"theme": "light"})
```

### Detailed Evaluation

```go
detail := client.VariationDetail("feature-key",
	featureflip.EvaluationContext{UserID: "123"}, false)

fmt.Println(detail.Value)     // The evaluated value
fmt.Println(detail.Reason)    // "RuleMatch", "Fallthrough", "FlagDisabled", etc.
fmt.Println(detail.RuleID)    // Rule ID if reason is "RuleMatch"
fmt.Println(detail.Variation) // Variation key
```

## Event Tracking

```go
// Track custom events
client.Track("checkout-completed",
	featureflip.EvaluationContext{UserID: "123"},
	map[string]any{"total": 99.99})

// Identify users for segment building
client.Identify(featureflip.EvaluationContext{UserID: "123"})

// Force flush pending events
client.Flush()
```

## Testing

Use `ForTesting()` to create a client with predetermined flag values -- no network calls.

```go
client := featureflip.ForTesting(map[string]any{
	"my-feature":  true,
	"pricing-tier": "pro",
})

client.BoolVariation("my-feature", featureflip.EvaluationContext{}, false)     // true
client.StringVariation("pricing-tier", featureflip.EvaluationContext{}, "free") // "pro"
client.BoolVariation("unknown", featureflip.EvaluationContext{}, false)         // false
```

## Features

- **Local evaluation** - Near-zero latency after initialization
- **Real-time updates** - SSE streaming with automatic polling fallback
- **Event tracking** - Automatic batching and background flushing
- **Test support** - `ForTesting()` factory for deterministic unit tests
- **Goroutine-safe** - Safe for concurrent access
- **Zero dependencies** - Uses only the Go standard library

## Requirements

- Go 1.21+

## License

Apache-2.0 — see [LICENSE](LICENSE) for details.
