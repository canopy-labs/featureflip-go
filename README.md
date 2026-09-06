# Featureflip Go SDK

Go SDK for [Featureflip](https://featureflip.io) - evaluate feature flags locally with near-zero latency.

## Installation

```bash
go get github.com/canopy-labs/featureflip-go/v2
```

## Quick Start

```go
package main

import (
	"fmt"
	"log"

	featureflip "github.com/canopy-labs/featureflip-go/v2"
)

func main() {
	client, err := featureflip.Get("your-sdk-key")
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
client, err := featureflip.Get("your-sdk-key",
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

// Record an identify event for analytics (does not affect flag evaluation)
client.Identify(featureflip.EvaluationContext{UserID: "123"})

// Force flush pending events
client.Flush()
```

## Reacting to Flag Changes

`OnUpdate` fires when flag configuration changes, with the keys that moved.

```go
unsubscribe := client.OnUpdate(func(flagKeys []string) {
	log.Printf("flags changed: %v", flagKeys)
})
defer unsubscribe()
```

The listener runs on the SDK's streaming or polling goroutine, so it must not
block: a slow listener delays flag delivery and, on the SSE path, can stall the
connection.

The initial load does not fire, and neither does a refresh that changed
nothing -- the SDK re-fetches the whole configuration on every poll tick and
every stream reconnect, so reporting those would mean waking you once per
interval rather than on changes.

The reported keys cover more than the flags whose own rows moved. Editing a
**segment** changes evaluated outcomes without bumping any flag's version, so
the flags referencing it are included; and a flag that depends on a changed
flag through a **prerequisite** is included too, because its value really does
flip -- to its off variation, with `ReasonPrerequisiteFailed` -- while its own
configuration is untouched.

A panic in a listener is logged and swallowed; it does not affect flag delivery
or the other listeners. Subscriptions are dropped when the client is closed, so
a caller that closes its client need not unsubscribe first.

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
- **Change notifications** - `OnUpdate` reports the flag keys that moved, including segment and prerequisite fan-out
- **Event tracking** - Automatic batching and background flushing
- **Test support** - `ForTesting()` factory for deterministic unit tests
- **Goroutine-safe** - Safe for concurrent access
- **Zero dependencies** - Uses only the Go standard library

## Requirements

- Go 1.21+

## License

Apache-2.0 — see [LICENSE](LICENSE) for details.
