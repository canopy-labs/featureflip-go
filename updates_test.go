package featureflip

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func updFlag(key string, version int, variation string) flagDTO {
	return flagDTO{
		Key:     key,
		Version: version,
		Type:    "Boolean",
		Enabled: true,
		Variations: []variationDTO{
			{Key: "true", Value: json.RawMessage(`true`)},
			{Key: "false", Value: json.RawMessage(`false`)},
		},
		Fallthrough:  serveConfig{Type: "Fixed", Variation: variation},
		OffVariation: "false",
	}
}

func updFlagMap(flags ...flagDTO) map[string]flagDTO {
	out := make(map[string]flagDTO, len(flags))
	for _, f := range flags {
		out[f.Key] = f
	}
	return out
}

// --- diffSnapshot ---------------------------------------------------------

func TestDiffSnapshot_ReportsAddedRemovedAndModified(t *testing.T) {
	previous := updFlagMap(updFlag("kept", 1, "true"), updFlag("edited", 1, "true"), updFlag("removed", 1, "true"))

	got := diffSnapshot(previous, nil, []flagDTO{
		updFlag("kept", 1, "true"),
		updFlag("edited", 2, "false"),
		updFlag("added", 1, "true"),
	}, nil)

	want := []string{"added", "edited", "removed"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("changed = %v, want %v", got, want)
	}
}

func TestDiffSnapshot_IdenticalSnapshotReportsNothing(t *testing.T) {
	// The store is handed a FULL snapshot on every poll tick and every SSE
	// reconnect. Reporting those would wake every listener once per interval.
	flags := []flagDTO{updFlag("a", 1, "true"), updFlag("b", 1, "false")}

	if got := diffSnapshot(updFlagMap(flags...), nil, flags, nil); got != nil {
		t.Errorf("changed = %v, want nil for an unchanged snapshot", got)
	}
}

func TestDiffSnapshot_DetectsChangeBeyondTheVersionField(t *testing.T) {
	// Comparison is on configuration, not on the version header: a server that
	// edits a flag without bumping its version still moves evaluated values.
	previous := updFlagMap(updFlag("a", 1, "true"))

	got := diffSnapshot(previous, nil, []flagDTO{updFlag("a", 1, "false")}, nil)

	if len(got) != 1 || got[0] != "a" {
		t.Errorf("changed = %v, want [a]", got)
	}
}

func TestDiffSnapshot_ChangedSegmentDragsInReferencingFlags(t *testing.T) {
	// A segment edit alters evaluation outcomes without bumping any flag's
	// version, so the referencing flags have to be pulled in by hand.
	referencing := updFlag("uses-segment", 1, "true")
	referencing.Rules = []ruleDTO{{ID: "r1", SegmentKey: "beta", Serve: serveConfig{Type: "Fixed", Variation: "true"}}}
	unrelated := updFlag("unrelated", 1, "true")

	previousSegments := map[string]segmentDTO{"beta": {Key: "beta", Version: 1, ConditionLogic: "And"}}
	nextSegments := []segmentDTO{{Key: "beta", Version: 2, ConditionLogic: "Or"}}

	got := diffSnapshot(updFlagMap(referencing, unrelated), previousSegments,
		[]flagDTO{referencing, unrelated}, nextSegments)

	if len(got) != 1 || got[0] != "uses-segment" {
		t.Errorf("changed = %v, want [uses-segment]", got)
	}
}

func TestDiffSnapshot_RemovedSegmentAlsoDragsInReferencingFlags(t *testing.T) {
	referencing := updFlag("uses-segment", 1, "true")
	referencing.Rules = []ruleDTO{{ID: "r1", SegmentKey: "beta", Serve: serveConfig{Type: "Fixed", Variation: "true"}}}

	got := diffSnapshot(updFlagMap(referencing),
		map[string]segmentDTO{"beta": {Key: "beta", Version: 1}},
		[]flagDTO{referencing}, nil)

	if len(got) != 1 || got[0] != "uses-segment" {
		t.Errorf("changed = %v, want [uses-segment]", got)
	}
}

func TestDiffSnapshot_PrerequisiteDependentIsReported(t *testing.T) {
	// The dependent's own config did not change, but its evaluated value moves
	// to its off variation with PrerequisiteFailed.
	parent := updFlag("parent", 1, "true")
	child := updFlag("child", 1, "true")
	child.Prerequisites = []prerequisite{{PrerequisiteFlagKey: "parent", ExpectedVariationKey: "true"}}

	got := diffSnapshot(updFlagMap(parent, child), nil,
		[]flagDTO{updFlag("parent", 2, "false"), child}, nil)

	want := []string{"child", "parent"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("changed = %v, want %v", got, want)
	}
}

func TestDiffSnapshot_PrerequisiteFanOutIsTransitive(t *testing.T) {
	parent := updFlag("parent", 1, "true")
	middle := updFlag("middle", 1, "true")
	middle.Prerequisites = []prerequisite{{PrerequisiteFlagKey: "parent", ExpectedVariationKey: "true"}}
	leaf := updFlag("leaf", 1, "true")
	leaf.Prerequisites = []prerequisite{{PrerequisiteFlagKey: "middle", ExpectedVariationKey: "true"}}

	got := diffSnapshot(updFlagMap(parent, middle, leaf), nil,
		[]flagDTO{updFlag("parent", 2, "false"), middle, leaf}, nil)

	want := []string{"leaf", "middle", "parent"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("changed = %v, want %v", got, want)
	}
}

func TestDiffSnapshot_SegmentFanOutReachesPrerequisiteDependents(t *testing.T) {
	// The prerequisite walk must run AFTER the segment scan, or a flag pulled
	// in only by the segment never reaches its own dependents.
	usesSegment := updFlag("uses-segment", 1, "true")
	usesSegment.Rules = []ruleDTO{{ID: "r1", SegmentKey: "beta", Serve: serveConfig{Type: "Fixed", Variation: "true"}}}
	dependent := updFlag("dependent", 1, "true")
	dependent.Prerequisites = []prerequisite{{PrerequisiteFlagKey: "uses-segment", ExpectedVariationKey: "true"}}

	got := diffSnapshot(updFlagMap(usesSegment, dependent),
		map[string]segmentDTO{"beta": {Key: "beta", Version: 1}},
		[]flagDTO{usesSegment, dependent},
		[]segmentDTO{{Key: "beta", Version: 2}})

	want := []string{"dependent", "uses-segment"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("changed = %v, want %v", got, want)
	}
}

func TestAddPrerequisiteDependents_CycleTerminates(t *testing.T) {
	// The server rejects cycles, but a malformed payload can carry one and a
	// walk that revisits would hang the streaming goroutine forever.
	a := updFlag("a", 1, "true")
	a.Prerequisites = []prerequisite{{PrerequisiteFlagKey: "b", ExpectedVariationKey: "true"}}
	b := updFlag("b", 1, "true")
	b.Prerequisites = []prerequisite{{PrerequisiteFlagKey: "a", ExpectedVariationKey: "true"}}

	done := make(chan []string, 1)
	go func() {
		changed := map[string]struct{}{"a": {}}
		addPrerequisiteDependents([]flagDTO{a, b}, changed)
		done <- sortedKeys(changed)
	}()

	select {
	case got := <-done:
		want := []string{"a", "b"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("changed = %v, want %v", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("addPrerequisiteDependents did not terminate on a prerequisite cycle")
	}
}

func TestAddPrerequisiteDependents_EmptyChangedSetStaysEmpty(t *testing.T) {
	child := updFlag("child", 1, "true")
	child.Prerequisites = []prerequisite{{PrerequisiteFlagKey: "parent", ExpectedVariationKey: "true"}}

	changed := map[string]struct{}{}
	addPrerequisiteDependents([]flagDTO{child}, changed)

	if len(changed) != 0 {
		t.Errorf("changed = %v, want empty", sortedKeys(changed))
	}
}

// --- store ----------------------------------------------------------------

func TestStore_SetFlagReportsTheChange(t *testing.T) {
	s := newStore()

	if got := s.setFlag(updFlag("a", 1, "true")); len(got) != 1 || got[0] != "a" {
		t.Errorf("setFlag = %v, want [a]", got)
	}
}

func TestStore_SetFlagReportsNothingForAnIdenticalDelta(t *testing.T) {
	s := newStore()
	flag := updFlag("a", 1, "true")
	s.setFlag(flag)

	if got := s.setFlag(flag); got != nil {
		t.Errorf("setFlag = %v, want nil for a redundant delta", got)
	}
}

func TestStore_SetFlagReportsPrerequisiteDependents(t *testing.T) {
	s := newStore()
	child := updFlag("child", 1, "true")
	child.Prerequisites = []prerequisite{{PrerequisiteFlagKey: "parent", ExpectedVariationKey: "true"}}
	s.setAll([]flagDTO{updFlag("parent", 1, "true"), child}, nil)

	got := s.setFlag(updFlag("parent", 2, "false"))

	want := []string{"child", "parent"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("setFlag = %v, want %v", got, want)
	}
}

func TestStore_RemoveFlagReportsDependentsFromWhatRemains(t *testing.T) {
	// The dependent still carries the now-dangling prerequisite row, so its
	// evaluated value moves even though nothing about it changed.
	s := newStore()
	child := updFlag("child", 1, "true")
	child.Prerequisites = []prerequisite{{PrerequisiteFlagKey: "parent", ExpectedVariationKey: "true"}}
	s.setAll([]flagDTO{updFlag("parent", 1, "true"), child}, nil)

	got := s.removeFlag("parent")

	want := []string{"child", "parent"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("removeFlag = %v, want %v", got, want)
	}
}

func TestStore_RemoveFlagReportsNothingForAnAbsentKey(t *testing.T) {
	s := newStore()

	if got := s.removeFlag("never-existed"); got != nil {
		t.Errorf("removeFlag = %v, want nil", got)
	}
}

// --- listener registry ----------------------------------------------------

func TestSharedCore_NotifiesEveryListener(t *testing.T) {
	sc := &sharedCore{}
	var mu sync.Mutex
	var seen [][]string
	for range 3 {
		sc.addUpdateListener(func(keys []string) {
			mu.Lock()
			seen = append(seen, keys)
			mu.Unlock()
		})
	}

	sc.notifyUpdateListeners([]string{"a"})

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 {
		t.Fatalf("listeners called = %d, want 3", len(seen))
	}
	for i, keys := range seen {
		if len(keys) != 1 || keys[0] != "a" {
			t.Errorf("listener %d saw %v, want [a]", i, keys)
		}
	}
}

func TestSharedCore_EachListenerGetsItsOwnCopy(t *testing.T) {
	// One listener sorting or truncating the slice must not corrupt the next.
	sc := &sharedCore{}
	sc.addUpdateListener(func(keys []string) { clear(keys) })
	var second []string
	sc.addUpdateListener(func(keys []string) { second = keys })

	sc.notifyUpdateListeners([]string{"a", "b"})

	if len(second) != 2 || second[0] != "a" || second[1] != "b" {
		t.Errorf("second listener saw %v, want [a b]", second)
	}
}

func TestSharedCore_UnsubscribeStopsDelivery(t *testing.T) {
	sc := &sharedCore{}
	var calls int32
	unsub := sc.addUpdateListener(func([]string) { atomic.AddInt32(&calls, 1) })

	sc.notifyUpdateListeners([]string{"a"})
	unsub()
	sc.notifyUpdateListeners([]string{"b"})

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1", got)
	}
}

func TestSharedCore_UnsubscribeIsIdempotent(t *testing.T) {
	sc := &sharedCore{}
	unsub := sc.addUpdateListener(func([]string) {})
	unsub()
	unsub() // must not panic or remove somebody else's later registration

	var calls int32
	sc.addUpdateListener(func([]string) { atomic.AddInt32(&calls, 1) })
	sc.notifyUpdateListeners([]string{"a"})

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1", got)
	}
}

func TestSharedCore_PanickingListenerIsContained(t *testing.T) {
	// A panic crossing back into the SSE read loop would kill the stream
	// goroutine and take flag delivery down with it.
	sc := &sharedCore{}
	sc.addUpdateListener(func([]string) { panic("listener blew up") })
	var reached int32
	sc.addUpdateListener(func([]string) { atomic.AddInt32(&reached, 1) })

	sc.notifyUpdateListeners([]string{"a"})

	if got := atomic.LoadInt32(&reached); got != 1 {
		t.Errorf("the listener after a panicking one ran %d times, want 1", got)
	}
}

func TestSharedCore_EmptyChangeSetNotifiesNobody(t *testing.T) {
	sc := &sharedCore{}
	var calls int32
	sc.addUpdateListener(func([]string) { atomic.AddInt32(&calls, 1) })

	sc.notifyUpdateListeners(nil)

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("calls = %d, want 0", got)
	}
}

// --- Client.OnUpdate ------------------------------------------------------

// mutableFlagServer serves whatever flag set the returned setter last stored.
func mutableFlagServer(initial []flagDTO) (*httptest.Server, func([]flagDTO)) {
	var mu sync.Mutex
	flags := initial

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		current := flags
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(getFlagsResponse{
			Environment: "test", Version: 1, Flags: current, Segments: []segmentDTO{},
		})
	}))

	return server, func(next []flagDTO) {
		mu.Lock()
		flags = next
		mu.Unlock()
	}
}

// Note on what this does NOT prove: the initial load cannot reach a listener,
// because a listener can only be registered through a Client and Get returns
// only after the initial fetch has already run. The `_ =` discard in doInit is
// therefore defensive rather than observable from here. What the quiet window
// below DOES prove is the property that actually bites: the poller re-fetches
// the whole config every tick, and an unchanged snapshot must wake nobody.
func TestClientOnUpdate_UnchangedPollsAreSilentThenAChangeFires(t *testing.T) {
	ResetForTesting()
	server, setFlags := mutableFlagServer([]flagDTO{updFlag("a", 1, "true")})
	defer server.Close()

	client, err := Get("on-update-key", WithBaseURL(server.URL), WithStreaming(false),
		WithPollInterval(50*time.Millisecond), WithInitTimeout(5*time.Second))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer client.Close()

	received := make(chan []string, 8)
	unsub := client.OnUpdate(func(keys []string) { received <- keys })
	defer unsub()

	// ~6 poll ticks land in this window, each replacing the store with an
	// identical snapshot. None of them is a change.
	select {
	case keys := <-received:
		t.Fatalf("listener fired for an unchanged poll with %v", keys)
	case <-time.After(300 * time.Millisecond):
	}

	setFlags([]flagDTO{updFlag("a", 2, "false"), updFlag("b", 1, "true")})

	select {
	case keys := <-received:
		want := []string{"a", "b"}
		if !reflect.DeepEqual(keys, want) {
			t.Errorf("keys = %v, want %v", keys, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener never fired after the config changed")
	}
}

func TestClientOnUpdate_UnsubscribeStopsDelivery(t *testing.T) {
	ResetForTesting()
	server, setFlags := mutableFlagServer([]flagDTO{updFlag("a", 1, "true")})
	defer server.Close()

	client, err := Get("on-update-unsub", WithBaseURL(server.URL), WithStreaming(false),
		WithPollInterval(50*time.Millisecond), WithInitTimeout(5*time.Second))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer client.Close()

	var calls int32
	unsub := client.OnUpdate(func([]string) { atomic.AddInt32(&calls, 1) })
	unsub()

	setFlags([]flagDTO{updFlag("a", 2, "false")})
	time.Sleep(400 * time.Millisecond)

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("calls after unsubscribe = %d, want 0", got)
	}
}

func TestClientOnUpdate_CloseDropsTheSubscription(t *testing.T) {
	// The core can outlive this handle, and its data sources keep running, so
	// a listener left registered would fire for a client already closed.
	ResetForTesting()
	server, setFlags := mutableFlagServer([]flagDTO{updFlag("a", 1, "true")})
	defer server.Close()

	opts := []Option{WithBaseURL(server.URL), WithStreaming(false),
		WithPollInterval(50 * time.Millisecond), WithInitTimeout(5 * time.Second)}

	closing, err := Get("on-update-close", opts...)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// A second handle keeps the shared core (and its poller) alive.
	survivor, err := Get("on-update-close", opts...)
	if err != nil {
		t.Fatalf("Get (second handle): %v", err)
	}
	defer survivor.Close()

	var closedHandleCalls int32
	closing.OnUpdate(func([]string) { atomic.AddInt32(&closedHandleCalls, 1) })

	survivorFired := make(chan struct{}, 4)
	survivor.OnUpdate(func([]string) { survivorFired <- struct{}{} })

	closing.Close()

	setFlags([]flagDTO{updFlag("a", 2, "false")})

	select {
	case <-survivorFired:
	case <-time.After(5 * time.Second):
		t.Fatal("the surviving handle's listener never fired, so this proves nothing")
	}

	if got := atomic.LoadInt32(&closedHandleCalls); got != 0 {
		t.Errorf("closed handle's listener fired %d times, want 0", got)
	}
}

func TestClientOnUpdate_NilListenerIsANoOp(t *testing.T) {
	client := ForTesting(map[string]any{"a": true})
	defer client.Close()

	unsub := client.OnUpdate(nil)
	unsub() // must not panic
}

func TestClientOnUpdate_ClosedHandleReturnsANoOp(t *testing.T) {
	client := ForTesting(map[string]any{"a": true})
	client.Close()

	unsub := client.OnUpdate(func([]string) { t.Error("listener on a closed handle fired") })
	unsub()
}
