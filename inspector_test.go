package featureflip

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

// inspectorFlags builds the fixture flag set used across the inspector tests,
// mirroring packages/js-sdk/tests/inspector.test.ts.
//
//	flag-on     — enabled, no rules   -> Fallthrough / "on" / true
//	flag-off    — disabled            -> FlagDisabled / "off" / false
//	flag-rule   — rule on country=US  -> RuleMatch / rule-1 / "on"
//	flag-prereq — prereq on flag-off  -> PrerequisiteFailed / flag-off
//	flag-string — enabled, serves the string "hello"
//	flag-number — enabled, serves the number 42.5
//	err-0..err-11 — 12-deep prereq chain (maxPrerequisiteDepth is 10) -> Error
func inspectorFlags() []flagDTO {
	rule := boolFlag("flag-rule", true, "on", "off")
	rule.Fallthrough = serveConfig{Type: "Fixed", Variation: "off"}
	rule.Rules = []ruleDTO{
		{
			ID:       "rule-1",
			Priority: 1,
			ConditionGroups: []conditionGroup{
				{
					Operator: "And",
					Conditions: []condition{
						{Attribute: "country", Operator: "Equals", Values: []string{"US"}},
					},
				},
			},
			Serve: serveConfig{Type: "Fixed", Variation: "on"},
		},
	}

	// Malformed on purpose: the fallthrough serves a variation key the flag does
	// not define (e.g. a since-deleted variation) -> Error (#1989).
	missingVar := boolFlag("flag-missing-variation", true, "on", "off")
	missingVar.Fallthrough = serveConfig{Type: "Fixed", Variation: "ghost"}

	flags := []flagDTO{
		boolFlag("flag-on", true, "on", "off"),
		boolFlag("flag-off", false, "on", "off"),
		rule,
		prereqFlag("flag-prereq", true, []prerequisite{mkPrereq("flag-off", "on")}),
		valueFlag("flag-string", "hello"),
		valueFlag("flag-number", 42.5),
		missingVar,
	}

	for i := 0; i < 12; i++ {
		key := fmt.Sprintf("err-%d", i)
		var prereqs []prerequisite
		if i < 11 {
			prereqs = []prerequisite{mkPrereq(fmt.Sprintf("err-%d", i+1), "on")}
		}
		flags = append(flags, prereqFlag(key, true, prereqs))
	}

	return flags
}

// valueFlag builds an enabled flag whose fallthrough serves a single variation
// carrying an arbitrary JSON value — used to feed the typed accessors a value
// of the "wrong" type so their coercion is observable.
func valueFlag(key string, value any) flagDTO {
	return flagDTO{
		Key:     key,
		Version: 1,
		Type:    "String",
		Enabled: true,
		Variations: []variationDTO{
			{Key: "served", Value: mustJSON(value)},
			{Key: "off", Value: mustJSON(nil)},
		},
		Fallthrough:  serveConfig{Type: "Fixed", Variation: "served"},
		OffVariation: "off",
	}
}

// newInspectorClient builds a network-free client whose core is constructed
// through the public option path, so the test exercises WithInspectors exactly
// as a caller would. No background goroutines are started.
func newInspectorClient(inspectors ...EvaluationInspector) *Client {
	cfg := defaultConfig()
	WithInspectors(inspectors...)(&cfg)

	core := newSharedCore("inspector-test-key", cfg)
	core.store.setAll(inspectorFlags(), nil)
	core.initialized = true
	// Detach the event processor from HTTP: a nil client makes enqueue/flush
	// no-ops, so no evaluation touches the network.
	core.ep = newEventProcessor(nil, 100, time.Hour)

	return &Client{core: core}
}

// collector records every event an inspector receives.
type collector struct {
	mu     sync.Mutex
	events []EvaluationEvent
}

func (c *collector) inspect(e EvaluationEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *collector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func (c *collector) at(t *testing.T, i int) EvaluationEvent {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if i >= len(c.events) {
		t.Fatalf("no event at index %d (got %d events)", i, len(c.events))
	}
	return c.events[i]
}

// assertISO8601 asserts the timestamp is an ISO-8601 (RFC3339, sub-second
// precision) string — the cross-SDK contract's representation — naming a sane,
// recent instant. Parseability alone does NOT prove precision (RFC3339 parses a
// whole-second string happily), so
// TestInspector_TimestampCarriesSubSecondPrecision covers truncation.
func assertISO8601(t *testing.T, ts string) {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t.Fatalf("Timestamp %q is not ISO-8601/RFC3339: %v", ts, err)
	}
	if delta := time.Since(parsed); delta < -time.Minute || delta > time.Minute {
		t.Errorf("Timestamp %s is not close to now (delta %v)", ts, delta)
	}
}

func TestInspector_FiresOnceWithFullPayload_Fallthrough(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	ctx := EvaluationContext{UserID: "bob", Attributes: map[string]any{"plan": "pro"}}
	if got := client.BoolVariation("flag-on", ctx, false); got != true {
		t.Fatalf("BoolVariation = %v, want true", got)
	}

	if c.count() != 1 {
		t.Fatalf("inspector called %d times, want 1", c.count())
	}
	e := c.at(t, 0)
	if e.FlagKey != "flag-on" {
		t.Errorf("FlagKey = %q, want %q", e.FlagKey, "flag-on")
	}
	if e.Value != true {
		t.Errorf("Value = %v, want true", e.Value)
	}
	if e.VariationKey != "on" {
		t.Errorf("VariationKey = %q, want %q", e.VariationKey, "on")
	}
	if e.Reason != ReasonFallthrough {
		t.Errorf("Reason = %q, want %q", e.Reason, ReasonFallthrough)
	}
	if e.RuleID != "" {
		t.Errorf("RuleID = %q, want empty", e.RuleID)
	}
	if e.PrerequisiteKey != "" {
		t.Errorf("PrerequisiteKey = %q, want empty", e.PrerequisiteKey)
	}
	if e.Context.UserID != "bob" || e.Context.Attributes["plan"] != "pro" {
		t.Errorf("Context = %+v, want the caller's context", e.Context)
	}
	assertISO8601(t, e.Timestamp)
}

func TestInspector_ReportsRuleIDOnRuleMatch(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	ctx := EvaluationContext{UserID: "alice", Attributes: map[string]any{"country": "US"}}
	if got := client.BoolVariation("flag-rule", ctx, false); got != true {
		t.Fatalf("BoolVariation = %v, want true", got)
	}

	e := c.at(t, 0)
	if e.Reason != ReasonRuleMatch {
		t.Errorf("Reason = %q, want %q", e.Reason, ReasonRuleMatch)
	}
	if e.RuleID != "rule-1" {
		t.Errorf("RuleID = %q, want %q", e.RuleID, "rule-1")
	}
	if e.VariationKey != "on" {
		t.Errorf("VariationKey = %q, want %q", e.VariationKey, "on")
	}
}

func TestInspector_ReportsFlagDisabledWithOffValue(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	ctx := EvaluationContext{UserID: "bob"}
	if got := client.BoolVariation("flag-off", ctx, true); got != false {
		t.Fatalf("BoolVariation = %v, want false", got)
	}

	e := c.at(t, 0)
	if e.Reason != ReasonFlagDisabled {
		t.Errorf("Reason = %q, want %q", e.Reason, ReasonFlagDisabled)
	}
	if e.Value != false {
		t.Errorf("Value = %v, want false", e.Value)
	}
	if e.VariationKey != "off" {
		t.Errorf("VariationKey = %q, want %q", e.VariationKey, "off")
	}
}

func TestInspector_ReportsFlagNotFoundWithDefaultValue(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	ctx := EvaluationContext{UserID: "bob"}
	if got := client.BoolVariation("missing", ctx, true); got != true {
		t.Fatalf("BoolVariation = %v, want true (default)", got)
	}

	if c.count() != 1 {
		t.Fatalf("inspector called %d times, want 1", c.count())
	}
	e := c.at(t, 0)
	if e.FlagKey != "missing" {
		t.Errorf("FlagKey = %q, want %q", e.FlagKey, "missing")
	}
	if e.Reason != ReasonFlagNotFound {
		t.Errorf("Reason = %q, want %q", e.Reason, ReasonFlagNotFound)
	}
	if e.Value != true {
		t.Errorf("Value = %v, want true (the caller's default)", e.Value)
	}
	if e.VariationKey != "" {
		t.Errorf("VariationKey = %q, want empty", e.VariationKey)
	}
	assertISO8601(t, e.Timestamp)
}

func TestInspector_ReportsPrerequisiteFailedWithPrerequisiteKey(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	ctx := EvaluationContext{UserID: "bob"}
	if got := client.BoolVariation("flag-prereq", ctx, true); got != false {
		t.Fatalf("BoolVariation = %v, want false (off variation)", got)
	}

	e := c.at(t, 0)
	if e.Reason != ReasonPrerequisiteFailed {
		t.Errorf("Reason = %q, want %q", e.Reason, ReasonPrerequisiteFailed)
	}
	if e.PrerequisiteKey != "flag-off" {
		t.Errorf("PrerequisiteKey = %q, want %q", e.PrerequisiteKey, "flag-off")
	}
	if e.Value != false {
		t.Errorf("Value = %v, want false", e.Value)
	}
	if e.VariationKey != "off" {
		t.Errorf("VariationKey = %q, want %q", e.VariationKey, "off")
	}
}

func TestInspector_ReportsErrorReasonOnEvaluationError(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	// err-0 heads a 12-deep prerequisite chain, past maxPrerequisiteDepth — the
	// evaluator's error exit path.
	ctx := EvaluationContext{UserID: "bob"}
	detail := client.VariationDetail("err-0", ctx, "unused-default")

	if detail.Reason != ReasonError {
		t.Fatalf("Reason = %q, want %q", detail.Reason, ReasonError)
	}
	if c.count() != 1 {
		t.Fatalf("inspector called %d times, want 1", c.count())
	}
	e := c.at(t, 0)
	if e.FlagKey != "err-0" {
		t.Errorf("FlagKey = %q, want %q", e.FlagKey, "err-0")
	}
	if e.Reason != ReasonError {
		t.Errorf("Reason = %q, want %q", e.Reason, ReasonError)
	}
	// The value the caller actually receives on this path is the off variation,
	// not the caller default — the Go evaluator returns rather than throwing.
	if e.Value != detail.Value {
		t.Errorf("Value = %v, want %v (what the caller received)", e.Value, detail.Value)
	}
	assertISO8601(t, e.Timestamp)
}

func TestInspector_ReportsErrorWhenServedVariationNotDefined(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	// The evaluator resolves the fallthrough to "ghost", which the flag does not
	// define. The returned detail reports Error rather than the misleading
	// Fallthrough, keeping the served key for diagnostics.
	ctx := EvaluationContext{UserID: "bob"}
	detail := client.VariationDetail("flag-missing-variation", ctx, false)

	if detail.Reason != ReasonError {
		t.Fatalf("Reason = %q, want %q", detail.Reason, ReasonError)
	}
	if detail.Variation != "ghost" {
		t.Errorf("Variation = %q, want %q (kept for diagnostics)", detail.Variation, "ghost")
	}
	if c.count() != 1 {
		t.Fatalf("inspector called %d times, want 1", c.count())
	}
	if e := c.at(t, 0); e.Reason != ReasonError {
		t.Errorf("Reason = %q, want %q", e.Reason, ReasonError)
	}
}

func TestInspector_InvokesEveryRegisteredInspector(t *testing.T) {
	a := &collector{}
	b := &collector{}
	client := newInspectorClient(a.inspect, b.inspect)
	defer client.Close()

	client.BoolVariation("flag-on", EvaluationContext{UserID: "bob"}, false)

	if a.count() != 1 {
		t.Errorf("first inspector called %d times, want 1", a.count())
	}
	if b.count() != 1 {
		t.Errorf("second inspector called %d times, want 1", b.count())
	}
}

func TestInspector_PanicIsIsolated(t *testing.T) {
	var logBuf bytes.Buffer
	originalOutput := log.Writer()
	originalFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(originalOutput)
		log.SetFlags(originalFlags)
	}()

	after := &collector{}
	boom := func(EvaluationEvent) { panic("inspector boom") }
	client := newInspectorClient(boom, after.inspect)
	defer client.Close()

	// (a) evaluation still returns the correct value.
	if got := client.BoolVariation("flag-on", EvaluationContext{UserID: "bob"}, false); got != true {
		t.Errorf("BoolVariation = %v, want true", got)
	}
	// (b) the sibling inspector still fired.
	if after.count() != 1 {
		t.Errorf("sibling inspector called %d times, want 1", after.count())
	}
	// (c) a warning was logged.
	logged := logBuf.String()
	if !strings.Contains(logged, "[featureflip] evaluation inspector panicked: inspector boom") {
		t.Errorf("log = %q, want a warning naming the panic", logged)
	}
}

func TestInspector_NilEntriesAreFilteredOut(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(nil, c.inspect, nil)
	defer client.Close()

	if got := client.BoolVariation("flag-on", EvaluationContext{UserID: "bob"}, false); got != true {
		t.Errorf("BoolVariation = %v, want true", got)
	}
	if c.count() != 1 {
		t.Errorf("inspector called %d times, want 1", c.count())
	}
	if len(client.core.inspectors) != 1 {
		t.Errorf("core holds %d inspectors, want 1 (nils dropped)", len(client.core.inspectors))
	}
}

func TestInspector_NoInspectorsConfiguredIsNoOp(t *testing.T) {
	client := newInspectorClient()
	defer client.Close()

	if client.core.inspectors != nil {
		t.Errorf("core inspectors = %v, want nil", client.core.inspectors)
	}
	if got := client.BoolVariation("flag-on", EvaluationContext{UserID: "bob"}, false); got != true {
		t.Errorf("BoolVariation = %v, want true", got)
	}
	if got := client.BoolVariation("missing", EvaluationContext{UserID: "bob"}, true); got != true {
		t.Errorf("BoolVariation = %v, want true", got)
	}
}

func TestInspector_EventContextIsACopy(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	attrs := map[string]any{"plan": "pro"}
	ctx := EvaluationContext{UserID: "bob", Attributes: attrs}
	client.BoolVariation("flag-on", ctx, false)

	e := c.at(t, 0)

	// Mutating the event's context must not touch the caller's map or struct.
	e.Context.Attributes["plan"] = "mutated"
	e.Context.Attributes["injected"] = true
	e.Context.UserID = "someone-else"

	if attrs["plan"] != "pro" {
		t.Errorf("caller's attributes were mutated: plan = %v, want %q", attrs["plan"], "pro")
	}
	if _, ok := attrs["injected"]; ok {
		t.Error("caller's attributes gained an injected key")
	}
	if ctx.UserID != "bob" {
		t.Errorf("caller's UserID = %q, want %q", ctx.UserID, "bob")
	}
}

func TestInspector_NilAttributesContextIsCopiedSafely(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	client.BoolVariation("flag-on", EvaluationContext{UserID: "bob"}, false)

	e := c.at(t, 0)
	if e.Context.Attributes != nil {
		t.Errorf("Attributes = %v, want nil for a nil-attribute context", e.Context.Attributes)
	}
}

// --- Finding 1: the event's Value is what the caller actually receives ---

// A flag serving a string, read through BoolVariation, is the regression case:
// the accessor's type assertion fails and it substitutes the caller's default,
// so an inspector that reported the raw evaluated value would record a value the
// application never acted on.
func TestInspector_ValueIsPostCoercion_BoolVariationOnStringFlag(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	got := client.BoolVariation("flag-string", EvaluationContext{UserID: "bob"}, false)
	if got != false {
		t.Fatalf("BoolVariation = %v, want false (the default — the flag serves a string)", got)
	}

	if c.count() != 1 {
		t.Fatalf("inspector called %d times, want 1", c.count())
	}
	e := c.at(t, 0)
	if e.Value != false {
		t.Errorf("Value = %#v, want false (the value the caller received), not the raw served value", e.Value)
	}
	// The rest of the event still describes the real evaluation.
	if e.VariationKey != "served" {
		t.Errorf("VariationKey = %q, want %q", e.VariationKey, "served")
	}
	if e.Reason != ReasonFallthrough {
		t.Errorf("Reason = %q, want %q", e.Reason, ReasonFallthrough)
	}
}

func TestInspector_ValueIsPostCoercion_Float64VariationOnStringFlag(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	got := client.Float64Variation("flag-string", EvaluationContext{UserID: "bob"}, 1.5)
	if got != 1.5 {
		t.Fatalf("Float64Variation = %v, want 1.5 (the default — the flag serves a string)", got)
	}

	e := c.at(t, 0)
	if e.Value != 1.5 {
		t.Errorf("Value = %#v, want 1.5 (the value the caller received)", e.Value)
	}
}

func TestInspector_ValueIsPostCoercion_StringVariationOnNumberFlag(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	got := client.StringVariation("flag-number", EvaluationContext{UserID: "bob"}, "fallback")
	if got != "fallback" {
		t.Fatalf("StringVariation = %q, want %q (the default — the flag serves a number)", got, "fallback")
	}

	e := c.at(t, 0)
	if e.Value != "fallback" {
		t.Errorf("Value = %#v, want %q (the value the caller received)", e.Value, "fallback")
	}
}

// Positive control: when the served value DOES match the accessor's type, the
// coercion is a no-op and the real value still reaches the inspector.
func TestInspector_ValueIsTheServedValueWhenTypesMatch(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	if got := client.Float64Variation("flag-number", EvaluationContext{UserID: "bob"}, 0); got != 42.5 {
		t.Fatalf("Float64Variation = %v, want 42.5", got)
	}
	if got := c.at(t, 0).Value; got != 42.5 {
		t.Errorf("Value = %#v, want 42.5", got)
	}

	if got := client.JSONVariation("flag-string", EvaluationContext{UserID: "bob"}, nil); got != "hello" {
		t.Fatalf("JSONVariation = %#v, want %q", got, "hello")
	}
	if got := c.at(t, 1).Value; got != "hello" {
		t.Errorf("Value = %#v, want %q", got, "hello")
	}
}

// accessorCalls names every public variation method so the exactly-once
// guarantee can be asserted uniformly across the whole public surface. If one
// accessor ever delegates to another, the counts below catch the double-fire.
var accessorCalls = []struct {
	name string
	call func(*Client, string, EvaluationContext)
}{
	{"BoolVariation", func(c *Client, k string, ctx EvaluationContext) { c.BoolVariation(k, ctx, false) }},
	{"StringVariation", func(c *Client, k string, ctx EvaluationContext) { c.StringVariation(k, ctx, "d") }},
	{"Float64Variation", func(c *Client, k string, ctx EvaluationContext) { c.Float64Variation(k, ctx, 1) }},
	{"JSONVariation", func(c *Client, k string, ctx EvaluationContext) { c.JSONVariation(k, ctx, nil) }},
	{"VariationDetail", func(c *Client, k string, ctx EvaluationContext) { c.VariationDetail(k, ctx, nil) }},
}

func TestInspector_FiresExactlyOncePerCall_EveryAccessor(t *testing.T) {
	// One key per evaluator exit path: found + type match, found + type
	// mismatch (coercion), and not found.
	keys := []string{"flag-on", "flag-string", "missing"}

	for _, accessor := range accessorCalls {
		for _, key := range keys {
			t.Run(accessor.name+"/"+key, func(t *testing.T) {
				c := &collector{}
				client := newInspectorClient(c.inspect)
				defer client.Close()

				accessor.call(client, key, EvaluationContext{UserID: "bob"})

				if c.count() != 1 {
					t.Fatalf("inspector called %d times for one %s call, want exactly 1", c.count(), accessor.name)
				}
				if got := c.at(t, 0).FlagKey; got != key {
					t.Errorf("FlagKey = %q, want %q", got, key)
				}
			})
		}
	}
}

func TestInspector_FiresOncePerCallAcrossRepeatedCalls(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	ctx := EvaluationContext{UserID: "bob"}
	for _, accessor := range accessorCalls {
		accessor.call(client, "flag-on", ctx)
	}

	if c.count() != len(accessorCalls) {
		t.Errorf("inspector called %d times for %d accessor calls, want one event each", c.count(), len(accessorCalls))
	}
}

// --- Finding 2: timestamps carry sub-second precision ---

func TestInspector_TimestampCarriesSubSecondPrecision(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)
	defer client.Close()

	const calls = 10
	for i := 0; i < calls; i++ {
		client.BoolVariation("flag-on", EvaluationContext{UserID: "bob"}, false)
	}
	if c.count() != calls {
		t.Fatalf("inspector called %d times, want %d", c.count(), calls)
	}

	distinct := make(map[string]struct{}, calls)
	fractional := 0
	for i := 0; i < calls; i++ {
		ts := c.at(t, i).Timestamp
		if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
			t.Fatalf("Timestamp %q is not RFC3339Nano-parseable: %v", ts, err)
		}
		distinct[ts] = struct{}{}
		if strings.Contains(ts, ".") {
			fractional++
		}
	}

	// Whole-second truncation (time.RFC3339) would make an analytics sink that
	// de-duplicates on (flagKey, userId, timestamp) silently drop repeat
	// exposures within the same second — so assert on the precision itself, not
	// on mere parseability.
	if fractional == 0 {
		t.Errorf("no timestamp of %d carried a sub-second component (e.g. %q) — timestamps look truncated to whole seconds",
			calls, c.at(t, 0).Timestamp)
	}
	if len(distinct) < 2 {
		t.Errorf("all %d back-to-back evaluations shared one timestamp (%q) — the format cannot distinguish same-second exposures",
			calls, c.at(t, 0).Timestamp)
	}
}

// --- Finding 3: no notification once the client is closed ---

func TestInspector_NoEventsAfterClose(t *testing.T) {
	c := &collector{}
	client := newInspectorClient(c.inspect)

	ctx := EvaluationContext{UserID: "bob"}
	client.BoolVariation("flag-on", ctx, false)
	if c.count() != 1 {
		t.Fatalf("inspector called %d times before Close, want 1", c.count())
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
	if !client.core.isShutDown() {
		t.Fatal("core is not shut down after closing the last handle")
	}

	// Closing suppresses only the notification — the returned values are
	// unchanged.
	if got := client.BoolVariation("flag-on", ctx, false); got != true {
		t.Errorf("BoolVariation after Close = %v, want true (Close must not change returned values)", got)
	}
	if got := client.StringVariation("flag-string", ctx, "d"); got != "hello" {
		t.Errorf("StringVariation after Close = %q, want %q", got, "hello")
	}
	if got := client.Float64Variation("flag-number", ctx, 0); got != 42.5 {
		t.Errorf("Float64Variation after Close = %v, want 42.5", got)
	}
	if got := client.JSONVariation("flag-string", ctx, nil); got != "hello" {
		t.Errorf("JSONVariation after Close = %#v, want %q", got, "hello")
	}
	if got := client.VariationDetail("flag-on", ctx, nil); got.Value != true {
		t.Errorf("VariationDetail after Close = %#v, want true", got.Value)
	}
	if got := client.BoolVariation("missing", ctx, true); got != true {
		t.Errorf("BoolVariation(missing) after Close = %v, want true (the default)", got)
	}

	if c.count() != 1 {
		t.Errorf("inspector fired %d times, want 1 — no event may be emitted after Close", c.count())
	}
}

// A surviving handle keeps the core alive, so inspectors keep firing: the guard
// is on core shutdown, not on any per-handle disposal.
func TestInspector_StillFiresWhileAnotherHandleIsOpen(t *testing.T) {
	c := &collector{}
	first := newInspectorClient(c.inspect)

	second := &Client{core: first.core}
	if !first.core.tryAcquire() {
		t.Fatal("tryAcquire on a live core returned false")
	}
	defer second.Close()

	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
	if second.core.isShutDown() {
		t.Fatal("core shut down while a handle is still open")
	}

	second.BoolVariation("flag-on", EvaluationContext{UserID: "bob"}, false)
	if c.count() != 1 {
		t.Errorf("inspector called %d times, want 1 — the core is still alive", c.count())
	}
}

func TestInspector_ConfigsEqualIgnoresInspectors(t *testing.T) {
	a := defaultConfig()
	WithInspectors(func(EvaluationEvent) {})(&a)
	b := defaultConfig()

	if !configsEqual(a, b) {
		t.Error("configsEqual returned false for configs differing only by inspectors")
	}
}
