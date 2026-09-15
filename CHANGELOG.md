# Changelog

## 2.8.1 — 2026-09-14

### Changed

- No changes to the library itself. The public repository, `canopy-labs/featureflip-go`, now runs its own CI on every push and pull request (gofmt, `go vet`, and `go test -race` with coverage on Go 1.25 and 1.26) and publishes a coverage report. Upgrading from 2.8.0 changes only the version the SDK reports in its `User-Agent`.

## 2.8.0 — 2026-09-06

### Added

- `Client.OnUpdate` subscribes to flag-configuration changes. The listener is called with the flag keys whose configuration changed, batched into one call per update, and the returned func unsubscribes; subscriptions are also dropped when the handle that registered them is closed. This is what an OpenFeature provider needs in order to emit `PROVIDER_CONFIGURATION_CHANGED`, which the Go provider shipped without. ([#2767](https://github.com/canopy-labs/featureflip/issues/2767))

- The reported key set covers more than the flags whose own rows moved, matching the js and Python SDKs. A **segment** edit changes evaluated outcomes without bumping any flag's version, so the flags referencing that segment are pulled in; and a flag's version covers its own prerequisite rows but not the flags they point at, so every transitive **prerequisite dependent** is pulled in too — its value really does flip, to its off variation with `PrerequisiteFailed`, while its own configuration is untouched.

- The initial flag load does not fire, and neither does a snapshot that changed nothing. The store is handed a full snapshot on every poll tick and every SSE `sync` reconnect, so notifying on each would report "everything changed" once per poll interval rather than reporting changes.

### Fixed

- The store now compares incoming configuration by value rather than trusting the `version` header, so a flag edited server-side without a version bump is no longer treated as unchanged. This was invisible before because nothing consumed change information.

## 2.7.0 — 2026-09-01

### Fixed

- A condition operator is now recognised however it is spelled: `NotEquals`, `notequals`, `NOTEQUALS` and `not_equals` all resolve to the same operator. This SDK folded case but left underscores in place, so it resolved `notequals` and `NOTEQUALS` but treated the snake_case `not_equals` as unrecognised — and an unrecognised operator fails closed (#2262), so such a rule matched nobody rather than erroring. php had the mirror-image rule (it inserted underscores ahead of PascalCase runs, resolving `not_equals` and rejecting `notequals`), and js and ruby refused both. Each accepted a form the others refused, so one saved rule could serve different variations to two users purely by which SDK their service ran. All four now normalise by removing underscores and folding case — the one rule that is a superset of all four previous ones, so no SDK gets stricter and no configuration that evaluated before stops doing so. ([#2374](https://github.com/canopy-labs/featureflip/issues/2374))

- The operator is resolved **once** per condition and every subsequent lookup keys off that value, not just the dispatch arm. The operator name also selects the case-sensitivity set and the numeric-coercion set, so normalising at the dispatch alone would have made a mis-cased `MatchesRegex` match case-insensitively where its canonical spelling does not, and dropped a mis-cased `NotEquals` onto the string path, comparing `"1"` against `"1.0"` lexically. ([#2374](https://github.com/canopy-labs/featureflip/issues/2374))

- The fail-closed guarantee is unchanged and re-asserted: this resolves *spellings*, it does not invent operators. A name that is not an operator is still unrecognised and still matches nothing before `negate` can invert it. ([#2262](https://github.com/canopy-labs/featureflip/issues/2262))

## 2.6.2 — 2026-08-26

### Fixed

- An explicit `Client.Flush` no longer opens a second drain loop while one is already running. The in-flight latch added for [#2456](https://github.com/canopy-labs/featureflip/issues/2456) guarded only the batch-size trigger, so the periodic flush, an explicit `Client.Flush` and a size-triggered flush could enter the loop together — two request streams against an endpoint the backoff gate exists to protect, and worse, a success in one cleared the gate a failure in the other had just armed, re-opening the one-request-per-evaluation behaviour outright. A caller arriving while a drain is running now waits for it and returns, matching the js and node SDKs. Shutdown still bypasses coalescing, because it is the last drain there will ever be. ([#2477](https://github.com/canopy-labs/featureflip/issues/2477))

- A `Before`/`After` date operand that resolves outside the representable date range now matches nothing, where it previously resolved to a real instant. The evaluation engine parses with `DateTimeOffset.TryParse`, so its accepted range is 0001-01-01T00:00:00Z to 9999-12-31T23:59:59.999Z and it matches nothing outside that; this SDK resolved past **both** ends, so a single saved rule served different variations to two users purely by which SDK their service ran. ([#2500](https://github.com/canopy-labs/featureflip/issues/2500))

- The two reachable shapes are a **year-zero** operand and an operand carried out of range **by its offset**. `0000-01-01` is inside the ISO grammar and is a real proleptic date (`0000-02-29` exists — year 0 is divisible by 400), so neither #2480's grammar guard nor #2491's calendar-day check excluded it. Separately, `[0-9]{4}` constrains only the **written** year while a timezone offset moves the resolved instant, so `0001-01-01T00:00:00+05:00` fell below the floor and `9999-12-31T23:59:59-05:00` rose above the ceiling from years the grammar allows. The check therefore runs on the **resolved** instant — deliberately unlike #2491's, which runs on the written date. ([#2500](https://github.com/canopy-labs/featureflip/issues/2500))

- The exact boundaries remain accepted: `0001-01-01`, `0001-01-01T05:00:00+05:00`, `9999-12-31T23:59:59Z` and `9999-12-31T18:59:59-05:00` all still resolve. ([#2500](https://github.com/canopy-labs/featureflip/issues/2500))

**If you have a targeting rule using one of these operands**, rewrite it as the date you meant. The Management API has rejected them on write since #2480 (`PortableDateOperand` round-trips every grammar-matched operand through the engine's own parser, so it inherits the range bound), meaning only rules saved before that release can carry one.

- The first SSE reconnect after a healthy stream drops is now jittered to `[d/2, d]`, like every other backoff level. The drops this absorbs are fleet-wide — a single edge event severs every stream at once — so every client re-entered the backoff together and waited an identical delay, republishing the drop's own synchronisation as a reconnect spike one backoff later. Measured in production: a drop spread across 2.5–3.0 ms produced a reconnect spread of 26–46 ms. The delay never exceeds the previous one and stays strictly positive, so a stream that fails immediately still cannot busy-loop. ([#2508](https://github.com/canopy-labs/featureflip/issues/2508))

## 2.6.1 — 2026-08-24

### Fixed

- A date operand is now trimmed of exactly the whitespace the evaluation engine trims (tab, newline, vertical tab, form feed, carriage return and space), and is rejected outright if it still carries a NUL, another control character, or a non-ASCII whitespace character. Each SDK had been relying on its own language's `trim`, and no two of those cover the same set, so the same operand could match on one SDK and match nothing on another. ([#2468](https://github.com/canopy-labs/featureflip/issues/2468))
- A date operand written with a space separator (`2024-01-01 00:00:00`), without seconds (`2024-01-01T00:00`), or with a basic offset (`+0500`) now parses. All three are accepted by the engine and were previously matching nothing here. ([#2468](https://github.com/canopy-labs/featureflip/issues/2468))

## 2.6.0 — 2026-08-24

### Fixed

- Analytics events now survive a transient failure of the events endpoint. The buffer is swapped out before the batch is sent and the send error was explicitly discarded, so any non-2xx or network error dropped that batch outright — and the public edge answers this endpoint with a 503 at a low but constant rate, so evaluation analytics were being lost steadily. A retryable failure (5xx, 429, transport fault, timeout) returns the batch to the front of the buffer for the next flush; a permanent one (401/403/400, or a batch that cannot be encoded) still drops it, because retrying a rejected SDK key forever would starve every later event. ([#2456](https://github.com/canopy-labs/featureflip/issues/2456))
- Every failed flush is now logged. The send error was discarded without a word, so a rejected batch was indistinguishable from a delivered one — no log, no counter, nothing to diagnose the loss from. ([#2456](https://github.com/canopy-labs/featureflip/issues/2456))
- `Close()` no longer risks hanging while the events endpoint is down: shutdown makes one final flush attempt and discards whatever it cannot deliver, rather than holding a re-queued batch nothing will drain. ([#2456](https://github.com/canopy-labs/featureflip/issues/2456))

### Changed

- The event buffer is bounded at 10,000 events, shedding the oldest and logging how many were dropped. Only reachable during a sustained outage, when re-queued batches would otherwise accumulate without limit; shedding oldest-first keeps the freshest analytics and drops the stale re-queued batches rather than starving new events. ([#2456](https://github.com/canopy-labs/featureflip/issues/2456))
- A batch-size-triggered flush now stands down for one flush interval after a retryable failure, and never runs concurrently with itself. A re-queued batch leaves the buffer at or above the batch size, so without this every subsequent tracked event would start another flush — one request per event against an endpoint already failing. Explicit `Flush()` calls and the periodic flush are unaffected, and the periodic flush remains the retry vehicle. ([#2456](https://github.com/canopy-labs/featureflip/issues/2456))
- A flush now sends at most one batch per request, looping until the buffer is empty, instead of posting the whole buffer at once. That only became reachable once failed batches started being re-queued: before, a failure emptied the buffer, so it never grew far past the batch size. After a sustained outage it can sit at the 10,000-event bound, and a body carrying all of that risks a 413 — which is non-retryable, so the entire backlog would have been dropped by the very path added to preserve it. A batch the server rejects permanently is dropped and the loop moves on, so one bad batch cannot block the backlog behind it. ([#2456](https://github.com/canopy-labs/featureflip/issues/2456))

## 2.5.1 — 2026-08-23

### Fixed

- An unrecognised condition operator now fails closed instead of matching every user. The default arm returned `false`, which a negated condition then inverted to `true` — so a config naming an operator the SDK did not know could silently target everyone. ([#2262](https://github.com/canopy-labs/featureflip/issues/2262))
- `identify()` and `track()` put the same payload on the wire as every other server SDK. The field set and shapes had drifted per language, so the same call produced different events depending on which SDK sent it. ([#2359](https://github.com/canopy-labs/featureflip/issues/2359))
- A malformed config payload is announced on every fetch path, not just the streaming `sync` frame, so a bad payload is diagnosable however it arrived. ([#2400](https://github.com/canopy-labs/featureflip/issues/2400))

## 2.5.0 — 2026-08-20

### Fixed

- A closed handle serves the caller's default from every accessor and reports not-initialized. `Close()` releases the shared core — stopping streaming and polling, shutting down the event processor — but the in-memory store stayed readable, so a closed client kept evaluating against a frozen snapshot that could never update again while still reporting itself initialized. ([#2289](https://github.com/canopy-labs/featureflip/issues/2289))

- The `sync`-snapshot unmarshal error check is pinned by a regression test. Go's `encoding/json` partially populates on a type error rather than aborting, so that `if err != nil { return }` is load-bearing: without it a malformed frame would apply a half-decoded snapshot. ([#2288](https://github.com/canopy-labs/featureflip/issues/2288))
### Changed

- A type-mismatched read returns the caller's default and reports `ReasonError`, instead of returning it under the evaluator's *success* reason, which left callers no signal at all. Reading a String flag through a number accessor, say, is now detectable rather than silent. Matching reads and the generic/JSON accessors are unchanged. ([#2281](https://github.com/canopy-labs/featureflip/issues/2281))

## 2.4.4 — 2026-08-05

### Fixed

- `LICENSE` is now the verbatim Apache-2.0 text. Three phrases in the operative sections had been reworded and the appendix dropped, which left automated license scanners unable to identify it — pkg.go.dev reported `License: UNKNOWN` and withheld the entire package documentation. The license itself is unchanged; the file now says what it always claimed to.

## 2.4.3 — 2026-08-05

### Changed

- The package documentation on pkg.go.dev now links out to featureflip.io and the Go SDK guide, and says up front that evaluation happens in-process.

## 2.4.2 — 2026-08-02

### Fixed

- The `User-Agent` reports the SDK's real version. It had been pinned to `0.1.0` since the first release, so every request from a 2.x client identified itself as pre-1.0 (#2141).

## 2.4.1 — 2026-08-02

### Fixed

- The module path carries its major-version suffix: `github.com/canopy-labs/featureflip-go/v2`. Go requires this of any v2+ module, and without it the proxy rejected every v2 tag — `go get` kept resolving to v1.0.1 and nothing released since 2.0.0 was installable. Update imports to `featureflip "github.com/canopy-labs/featureflip-go/v2"`; the v1 line is unaffected and keeps resolving as before (#2138).

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
  ``go
  client, err := featureflip.NewClient("your-sdk-key")
  ``

  After:
  ``go
  client, err := featureflip.Get("your-sdk-key")
  ``

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
