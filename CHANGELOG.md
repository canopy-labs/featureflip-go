# Changelog

## 2.4.0 — 2026-07-29

### Added

- **`onEvaluation` inspector callback.** `Config.Inspectors` registers in-process observers fired on every evaluation (#1914).

### Fixed

- A served variation key the flag does not define now reports `ReasonError` with the caller's default, instead of a misleading success reason (#1989).

## 2.3.0 — 2026-07-13

### Fixed

- Outage-recovery hardening: never give up reconnecting, and re-sync on recovery (#1857, #1896).
- The SSE scanner buffer cap is lifted so a `sync` snapshot larger than 64 KiB no longer freezes the stream (#1890).

## 2.2.0 — 2026-06-19

### Added

- **Semantic-version condition operators** (`SemverEquals`, `SemverGreaterThan`, `SemverGreaterThanOrEqual`, `SemverLessThan`, `SemverLessThanOrEqual`) for local rule evaluation, comparing per semver precedence rather than as decimals (#1431).

### Fixed

- Per-flag rollout salt aligns bucketing with the engine and every other SDK; the previous `flagKey` fallback re-bucketed users (#1452).
- Relational operators match against **any** supplied condition value (#1443).
- `MatchesRegex` is case-sensitive — the pattern is no longer compiled with the `(?i)` flag (#1453).
- Semver prerelease comparison is case-sensitive in ASCII order per semver §11 (#1447).
- `Before`/`After` date operators aligned with the engine (#1455).
- Type-aware numeric coercion for `Equals`/`In` (#1458).
- Keyless rollouts serve the control variation deterministically (#1457).
- A present-but-nil attribute is treated as missing rather than an empty string (#1484).
- The `bucketBy` `userId`/`user_id` alias is matched case-sensitively, aligning with the engine (#1460).
- Environment-level percentage rollouts with no variations no longer panic (#1469).

## 2.1.0 — 2026-05-27

### Added

- Prerequisite flag support. Flag evaluation now resolves prerequisites before applying rules: a flag whose prerequisite is missing, disabled, or serves an unexpected variation short-circuits to its off variation with `ReasonPrerequisiteFailed`, and `EvaluationDetail.PrerequisiteKey` carries the failing prerequisite's flag key. The resolution depth is capped at 10 (returning `ReasonError` beyond that). Mirrors the algorithm in the .NET evaluator and the JS, Python, C#, and Java SDKs (#1111).

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
