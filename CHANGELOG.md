# Changelog

## Unreleased

### Added

- Prerequisite flag support. Flag evaluation now resolves prerequisites before applying rules: a flag whose prerequisite is missing, disabled, or serves an unexpected variation short-circuits to its off variation with `ReasonPrerequisiteFailed`, and `EvaluationDetail.PrerequisiteKey` carries the failing prerequisite's flag key. The resolution depth is capped at 10 (returning `ReasonError` beyond that). Mirrors the algorithm in the .NET evaluator and the JS, Python, C#, and Java SDKs.

## 2.0.0 — 2026-04-09

### BREAKING

- **`featureflip.NewClient()` removed.** The only way to obtain a client is now the package-level factory `featureflip.Get(sdkKey, opts...)`. The factory dedupes by SDK key: repeated calls with the same key return handles pointing at a single shared underlying client, making package-level singletons and per-handler construction safe by construction.

  **Migration:**

  Before:
  ```go
  client, err := featureflip.NewClient("your-sdk-key")
  ```

  After:
  ```go
  client, err := featureflip.Get("your-sdk-key")
  ```

- **`Close()` is now refcounted.** When multiple handles share one cached core, closing one handle does not shut down the core — the SSE connection and event processor stay alive until the last handle is closed. Double-close on the same handle is a no-op.

### Added

- `featureflip.Get(sdkKey, opts...)` — static factory, the new primary entry point.
- Internal `sharedCore` type separating expensive resources (HTTP client, flag store, event processor, SSE/polling goroutines) from the public handle. Refcounted via `sync/atomic` CAS loop. Initialization is exactly-once via `sync.Once`.
- `featureflip.DebugLiveCoreCount()` and `featureflip.DebugRefCount(sdkKey)` — diagnostic helpers.
- `featureflip.ResetForTesting()` — test isolation helper.

### Changed

- `Client` is now a thin handle over `sharedCore`. All evaluation, tracking, and lifecycle methods delegate to the core.

### Removed

- `featureflip.NewClient()`.

## 1.0.0

Initial release.
