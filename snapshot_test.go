package featureflip

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- helpers ---

// syncBuffer is a log sink the test can read while SDK goroutines still write.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureSyncLog(t *testing.T) *syncBuffer {
	t.Helper()
	b := &syncBuffer{}
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(b)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldW)
		log.SetFlags(oldF)
	})
	return b
}

func snapWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// memStore is an in-memory SnapshotStore with hooks for blocking and failing.
type memStore struct {
	mu       sync.Mutex
	text     string
	found    bool
	gets     int
	puts     int // Put calls that have started
	putTexts []string

	getErr   error
	getPanic bool
	getGate  chan struct{} // when non-nil, Get blocks until closed
	putGate  chan struct{} // when non-nil, the FIRST Put blocks until closed
	putErr   error
	putPanic int         // number of leading Puts that panic
	onPut    func(n int) // called inside Put, before it returns
}

func (m *memStore) Get() (string, bool, error) {
	m.mu.Lock()
	m.gets++
	gate, text, found, err, doPanic := m.getGate, m.text, m.found, m.getErr, m.getPanic
	m.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if doPanic {
		panic("boom in Get")
	}
	return text, found, err
}

func (m *memStore) Put(text string) error {
	m.mu.Lock()
	m.puts++
	n := m.puts
	m.putTexts = append(m.putTexts, text)
	gate, perr, hook := m.putGate, m.putErr, m.onPut
	doPanic := n <= m.putPanic
	m.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	if gate != nil && n == 1 {
		<-gate
	}
	if doPanic {
		panic("boom in Put")
	}
	if perr != nil {
		return perr
	}
	m.mu.Lock()
	m.text, m.found = text, true
	m.mu.Unlock()
	return nil
}

func (m *memStore) putCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.puts
}

func (m *memStore) getCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gets
}

func (m *memStore) lastPut() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.putTexts) == 0 {
		return ""
	}
	return m.putTexts[len(m.putTexts)-1]
}

// fakeClock is a controllable clock for the manager.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

const snapTestBase = "https://snap.test"

// settle waits until no write is running or pending.
func settle(t *testing.T, m *snapshotManager) {
	t.Helper()
	snapWait(t, "writer to go idle", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !m.writing && !m.pending
	})
}

func snapFlag(key, value string) flagDTO {
	return flagDTO{
		Key: key, Version: 1, Type: "String", Enabled: true,
		Variations:   []variationDTO{{Key: "v", Value: json.RawMessage(fmt.Sprintf("%q", value))}},
		Fallthrough:  serveConfig{Type: "Fixed", Variation: "v"},
		OffVariation: "v",
	}
}

// newSnapCore builds an uninitialized core wired to the given store and clock.
func newSnapCore(t *testing.T, ms SnapshotStore, clock *fakeClock, opts ...func(*config)) *sharedCore {
	t.Helper()
	cfg := defaultConfig()
	cfg.baseURL = snapTestBase
	cfg.snapshotStore = ms
	cfg.initTimeout = 200 * time.Millisecond
	for _, o := range opts {
		o(&cfg)
	}
	sc := newSharedCore("sdk-key-A", cfg)
	if clock != nil {
		sc.snap.now = clock.Now
	}
	t.Cleanup(sc.release)
	return sc
}

// envelopeFor builds valid stored text for the given key hash and time.
func envelopeFor(t *testing.T, keyHash string, fetchedAt time.Time, flags []flagDTO, segments []segmentDTO) string {
	t.Helper()
	data, err := serializeSnapshotData(flags, segments)
	if err != nil {
		t.Fatal(err)
	}
	text, err := encodeSnapshotEnvelope(keyHash, fetchedAt.UnixMilli(), data)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// --- envelope and validation ---

func TestSnapshotKeyHash_NormalizesTrailingSlashesAndSeparatesKeys(t *testing.T) {
	a := snapshotKeyHash("https://x.test/", "k")
	if a != snapshotKeyHash("https://x.test", "k") {
		t.Fatal("trailing slash must not change the hash")
	}
	if a == snapshotKeyHash("https://x.test", "k2") || a == snapshotKeyHash("https://y.test", "k") {
		t.Fatal("different key or base URL must change the hash")
	}
	if len(a) != 64 {
		t.Fatal("hash must be 64 hex chars")
	}
}

func TestDecodeSnapshotEnvelope_ValidationOrder(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	hash := snapshotKeyHash(snapTestBase, "sdk-key-A")
	good := func(fetchedAt time.Time) string {
		return envelopeFor(t, hash, fetchedAt, []flagDTO{snapFlag("f", "x")}, nil)
	}
	fa := fmt.Sprintf("%d", now.UnixMilli())
	dataOK := `{"flags":[],"segments":[]}`
	env := func(v, kh, fetched, data string) string {
		parts := []string{}
		if v != "" {
			parts = append(parts, `"v":`+v)
		}
		if kh != "" {
			parts = append(parts, `"keyHash":`+kh)
		}
		if fetched != "" {
			parts = append(parts, `"fetchedAt":`+fetched)
		}
		if data != "" {
			parts = append(parts, `"data":`+data)
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	qh := fmt.Sprintf("%q", hash)

	const (
		wEmpty   = "empty text"
		wJSON    = "not valid JSON or not an object"
		wNull    = "not a snapshot envelope"
		wVersion = "unsupported envelope version"
		wKey     = "written for a different SDK key or base URL"
		wFetched = "fetchedAt is not a positive integer"
		wFuture  = "fetchedAt is in the future"
		wMissing = "missing data"
		wPayload = "data failed payload validation"
	)
	otherKey := fmt.Sprintf("%q", snapshotKeyHash(snapTestBase, "other"))
	cases := []struct {
		name string
		text string
		why  string // the failed check, "" for a valid snapshot
	}{
		{"valid", good(now), ""},
		{"fetchedAt 60s ahead is accepted", good(now.Add(60 * time.Second)), ""},
		{"fetchedAt 61s ahead is invalid", good(now.Add(61 * time.Second)), wFuture},
		{"empty text", "", wEmpty},
		{"garbage", "not json at all", wJSON},
		{"truncated", good(now)[:40], wJSON},
		{"array", "[]", wJSON},
		{"null", "null", wNull},
		{"string", `"x"`, wJSON},
		{"v 2", env("2", qh, fa, dataOK), wVersion},
		{"v missing", env("", qh, fa, dataOK), wVersion},
		{"v string", env(`"1"`, qh, fa, dataOK), wVersion},
		{"other key", env("1", otherKey, fa, dataOK), wKey},
		{"other base URL", env("1", fmt.Sprintf("%q", snapshotKeyHash("https://other.test", "sdk-key-A")), fa, dataOK), wKey},
		{"keyHash missing", env("1", "", fa, dataOK), wKey},
		{"fetchedAt missing", env("1", qh, "", dataOK), wFetched},
		{"fetchedAt zero", env("1", qh, "0", dataOK), wFetched},
		{"fetchedAt negative", env("1", qh, "-5", dataOK), wFetched},
		{"fetchedAt fractional", env("1", qh, "1.5", dataOK), wFetched},
		{"fetchedAt string", env("1", qh, `"123"`, dataOK), wFetched},
		{"data missing", env("1", qh, fa, ""), wMissing},
		{"data null", env("1", qh, fa, "null"), wMissing},
		{"data array", env("1", qh, fa, "[]"), wMissing},
		{"flags missing", env("1", qh, fa, `{"segments":[]}`), wPayload},
		{"segments missing", env("1", qh, fa, `{"flags":[]}`), wPayload},
		{"flags null", env("1", qh, fa, `{"flags":null,"segments":[]}`), wPayload},
		{"flags not an array", env("1", qh, fa, `{"flags":{},"segments":[]}`), wPayload},
		// An integer enum is the #2288 shape: encoding/json partially populates on a
		// type error, so any error must discard the whole payload.
		{"wrong-type field", env("1", qh, fa, `{"flags":[{"key":"f","type":3}],"segments":[]}`), wPayload},
		// Several failures at once: the FIRST check in the pinned order names it, so
		// reordering the checks fails these.
		{"version beats key", env("2", otherKey, "0", ""), wVersion},
		{"key beats fetchedAt", env("1", otherKey, "0", ""), wKey},
		{"fetchedAt beats future/data", env("1", qh, "-1", ""), wFetched},
		{"future beats data", env("1", qh, fmt.Sprintf("%d", now.UnixMilli()+3_600_000), ""), wFuture},
		{"missing data beats payload", env("1", qh, fa, ""), wMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := decodeSnapshotEnvelope(tc.text, hash, now)
			if tc.why == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			var inv *snapshotInvalidError
			if !errors.As(err, &inv) {
				t.Fatalf("expected snapshotInvalidError, got %v (%v)", err, d)
			}
			if err.Error() != tc.why {
				t.Fatalf("failed check = %q, want %q", err.Error(), tc.why)
			}
		})
	}
}

func TestSnapshotInvalidErrors_NeverContainContentsOrKey(t *testing.T) {
	hash := snapshotKeyHash(snapTestBase, "sdk-key-A")
	secret := "attr-value-secret"
	text := fmt.Sprintf(`{"v":1,"keyHash":"nope","fetchedAt":1,"data":{"flags":[{"key":"%s"}],"segments":[]}} sdk-key-A`, secret)
	_, err := decodeSnapshotEnvelope(text, hash, time.Now())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "sdk-key-A") {
		t.Fatalf("error leaks contents: %v", err)
	}
}

// --- wire shape and determinism ---

func richFlags() ([]flagDTO, []segmentDTO) {
	flags := []flagDTO{
		{
			Key: "zeta", Version: 3, Type: "Boolean", Enabled: true,
			Variations: []variationDTO{{Key: "on", Value: json.RawMessage(`true`)}, {Key: "off", Value: json.RawMessage(`false`)}},
			Rules: []ruleDTO{{
				ID: "r1", Priority: 1, SegmentKey: "seg-b",
				ConditionGroups: []conditionGroup{{Operator: "Or", Conditions: []condition{
					{Attribute: "plan", Operator: "In", Values: []string{"pro", "team"}, Negate: true},
				}}, {Operator: "And", Conditions: []condition{{Attribute: "n", Operator: "GreaterThan", Values: []string{"3"}}}}},
				Serve: serveConfig{Type: "Rollout", BucketBy: "userId", Salt: "s",
					Variations: []weightedVariation{{Key: "on", Weight: 30}, {Key: "off", Weight: 70}}},
			}},
			Fallthrough:  serveConfig{Type: "Fixed", Variation: "off"},
			OffVariation: "off",
			Prerequisites: []prerequisite{
				{PrerequisiteFlagKey: "alpha", ExpectedVariationKey: "on"},
			},
		},
		{
			Key: "alpha", Version: 1, Type: "SomeFutureType", Enabled: false,
			Variations:   []variationDTO{{Key: "on", Value: json.RawMessage(`{"a":[1,2.50,"x"]}`)}},
			Fallthrough:  serveConfig{Type: "Fixed", Variation: "on"},
			OffVariation: "on",
		},
	}
	segments := []segmentDTO{
		{Key: "seg-b", Version: 2, ConditionLogic: "Or", Conditions: []condition{{Attribute: "email", Operator: "EndsWith", Values: []string{"@x.io"}}}},
		{Key: "seg-a", Version: 1, ConditionLogic: "And", Conditions: []condition{{Attribute: "c", Operator: "Equals", Values: []string{"US"}}}},
	}
	return flags, segments
}

func TestSnapshotData_WireSpellingAndRoundTrip(t *testing.T) {
	flags, segments := richFlags()
	data, err := serializeSnapshotData(flags, segments)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"type":"Boolean"`, `"type":"Fixed"`, `"type":"Rollout"`, `"operator":"Or"`,
		`"operator":"And"`, `"conditionLogic":"Or"`, `"type":"SomeFutureType"`, `"prerequisiteFlagKey":"alpha"`} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("serialized data lacks real wire spelling %s:\n%s", want, data)
		}
	}

	hash := snapshotKeyHash(snapTestBase, "k")
	now := time.Now()
	text, _ := encodeSnapshotEnvelope(hash, now.UnixMilli(), data)
	got, err := decodeSnapshotEnvelope(text, hash, now)
	if err != nil {
		t.Fatal(err)
	}
	// Sorted by key on the way out.
	wantFlags := []flagDTO{flags[1], flags[0]}
	wantSegs := []segmentDTO{segments[1], segments[0]}
	if !reflect.DeepEqual(got.resp.Flags, wantFlags) {
		t.Errorf("flags did not round-trip through the SDK parser:\n got %+v\nwant %+v", got.resp.Flags, wantFlags)
	}
	if !reflect.DeepEqual(got.resp.Segments, wantSegs) {
		t.Errorf("segments did not round-trip:\n got %+v\nwant %+v", got.resp.Segments, wantSegs)
	}
}

func TestSnapshotData_DeterministicRegardlessOfInsertionOrder(t *testing.T) {
	var first []byte
	for i := 0; i < 40; i++ {
		s := newStore()
		flags := make([]flagDTO, 0, 30)
		segs := make([]segmentDTO, 0, 30)
		for j := 0; j < 30; j++ {
			flags = append(flags, snapFlag(fmt.Sprintf("flag-%02d", j), "x"))
			segs = append(segs, segmentDTO{Key: fmt.Sprintf("seg-%02d", j), ConditionLogic: "And"})
		}
		// Rotate the input order. The store's map then iterates randomly on top.
		rot := i % 30
		flags = append(flags[rot:], flags[:rot]...)
		segs = append(segs[rot:], segs[:rot]...)
		s.setAll(flags, segs)
		f, sg := s.sortedSnapshot()
		data, err := serializeSnapshotData(f, sg)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = data
		} else if !bytes.Equal(first, data) {
			t.Fatalf("serialization differs on iteration %d", i)
		}
	}
	var parsed snapshotData
	if err := json.Unmarshal(first, &parsed); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, len(parsed.Flags))
	for i, f := range parsed.Flags {
		keys[i] = f.Key
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatal("flags are not sorted by key")
	}
}

func TestSnapshotData_EmptyStateSerializesArraysNotNull(t *testing.T) {
	data, err := serializeSnapshotData(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"flags":[],"segments":[]}` {
		t.Fatalf("got %s", data)
	}
}

// --- file store ---

func TestFileSnapshotStore_RoundTripMissingAndNoMkdir(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileSnapshotStore(filepath.Join(dir, "snap.json"))

	text, found, err := fs.Get()
	if err != nil || found || text != "" {
		t.Fatalf("missing file must be a silent miss, got %q %v %v", text, found, err)
	}
	if err := fs.Put("hello ✓"); err != nil {
		t.Fatal(err)
	}
	text, found, err = fs.Get()
	if err != nil || !found || text != "hello ✓" {
		t.Fatalf("round trip failed: %q %v %v", text, found, err)
	}

	missingDir := filepath.Join(dir, "nope", "snap.json")
	if err := NewFileSnapshotStore(missingDir).Put("x"); err == nil {
		t.Fatal("Put into a missing directory must fail")
	}
	if _, err := os.Stat(filepath.Join(dir, "nope")); !os.IsNotExist(err) {
		t.Fatal("the store must not create parent directories")
	}
}

func TestFileSnapshotStore_Mode0600IncludingPreExistingTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	path := filepath.Join(t.TempDir(), "snap.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	fs := NewFileSnapshotStore(path)
	if err := fs.Put("new"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	if got, _, _ := fs.Get(); got != "new" {
		t.Fatalf("content = %q", got)
	}
}

func TestFileSnapshotStore_NoLeftoverTempFilesEvenConcurrently(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")
	fs := NewFileSnapshotStore(path)

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := fs.Put(fmt.Sprintf("writer-%d", i)); err != nil {
				t.Errorf("Put: %v", err)
			}
		}(i)
	}
	wg.Wait()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "snap.json" {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected only snap.json, found %v", names)
	}
	got, _, _ := fs.Get()
	if !strings.HasPrefix(got, "writer-") {
		t.Fatalf("torn content %q", got)
	}

	// A failed write removes its temp file too.
	if err := NewFileSnapshotStore(filepath.Join(dir, "no-such-dir", "x")).Put("x"); err == nil {
		t.Fatal("expected failure")
	}
	entries, _ = os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover after failed write: %v", entries)
	}
}

func TestFileSnapshotStore_DirectoryAsFileIsAReadErrorTreatedAsMissWithWarning(t *testing.T) {
	logs := captureSyncLog(t)
	dir := t.TempDir() // reading a directory as a file fails
	sc := newSnapCore(t, NewFileSnapshotStore(dir), nil)
	sc.snap.loadFallback("test", time.Second)

	info := sc.snap.startup()
	if info.Source != StartupNone || info.Reason != "" {
		t.Fatalf("read error must be a plain miss, got %+v", info)
	}
	if !strings.Contains(logs.String(), "snapshot store Get failed") {
		t.Fatalf("expected a warning, got %q", logs.String())
	}
}

// --- startup through the core ---

func TestSnapshotStartup_ReasonsThroughRealFileStore(t *testing.T) {
	hashA := snapshotKeyHash(snapTestBase, "sdk-key-A")
	clock := newFakeClock()
	flags := []flagDTO{snapFlag("f", "x")}
	valid := envelopeFor(t, hashA, clock.Now().Add(-90*time.Second), flags, nil)

	cases := []struct {
		name       string
		text       string // "" with write=false means no file
		write      bool
		wantSource StartupSource
		wantReason SnapshotReason
		wantAge    int64
	}{
		{"no file is a plain miss", "", false, StartupNone, "", 0},
		{"valid", valid, true, StartupSnapshot, SnapshotReasonSnapshot, 90},
		{"expired", envelopeFor(t, hashA, clock.Now().Add(-25*time.Hour), flags, nil), true, StartupNone, SnapshotReasonExpired, 25 * 3600},
		{"garbage", "}{ garbage", true, StartupNone, SnapshotReasonInvalid, 0},
		{"truncated", valid[:len(valid)/2], true, StartupNone, SnapshotReasonInvalid, 0},
		{"empty file", "", true, StartupNone, SnapshotReasonInvalid, 0},
		{"other key", envelopeFor(t, snapshotKeyHash(snapTestBase, "other"), clock.Now(), flags, nil), true, StartupNone, SnapshotReasonInvalid, 0},
		{"other base URL", envelopeFor(t, snapshotKeyHash("https://other.test", "sdk-key-A"), clock.Now(), flags, nil), true, StartupNone, SnapshotReasonInvalid, 0},
		{"v 2", strings.Replace(valid, `"v":1`, `"v":2`, 1), true, StartupNone, SnapshotReasonInvalid, 0},
		{"future", envelopeFor(t, hashA, clock.Now().Add(5*time.Minute), flags, nil), true, StartupNone, SnapshotReasonInvalid, 0},
		{"slightly future clamps to age 0", envelopeFor(t, hashA, clock.Now().Add(30*time.Second), flags, nil), true, StartupSnapshot, SnapshotReasonSnapshot, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureSyncLog(t)
			path := filepath.Join(t.TempDir(), "snap.json")
			if tc.write {
				if err := os.WriteFile(path, []byte(tc.text), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			sc := newSnapCore(t, NewFileSnapshotStore(path), clock)
			sc.snap.loadFallback("test", time.Second)

			info := sc.snap.startup()
			if info.Source != tc.wantSource || info.Reason != tc.wantReason || info.AgeSeconds != tc.wantAge {
				t.Fatalf("got %+v, want source=%s reason=%q age=%d", info, tc.wantSource, tc.wantReason, tc.wantAge)
			}
			_, have := sc.store.getFlag("f")
			if have != (tc.wantSource == StartupSnapshot) {
				t.Fatalf("flag served = %v for source %s", have, tc.wantSource)
			}
			// Loading (or rejecting) a snapshot never writes.
			if _, err := os.Stat(path); tc.write {
				if err != nil {
					t.Fatal(err)
				}
				if b, _ := os.ReadFile(path); string(b) != tc.text {
					t.Fatal("file was modified by a load")
				}
			}
		})
	}
}

func TestSnapshotStartup_MaxAgeBoundary(t *testing.T) {
	clock := newFakeClock()
	hash := snapshotKeyHash(snapTestBase, "sdk-key-A")
	for _, tc := range []struct {
		age  time.Duration
		want StartupSource
	}{{3600 * time.Second, StartupSnapshot}, {3601 * time.Second, StartupNone}} {
		ms := &memStore{found: true, text: envelopeFor(t, hash, clock.Now().Add(-tc.age), []flagDTO{snapFlag("f", "x")}, nil)}
		sc := newSnapCore(t, ms, clock, func(c *config) { c.maxSnapshotAge = time.Hour })
		sc.snap.loadFallback("test", time.Second)
		if got := sc.snap.startup().Source; got != tc.want {
			t.Errorf("age %v: source %s, want %s", tc.age, got, tc.want)
		}
	}
}

func TestSnapshotStartup_SharedStoreAcrossSDKKeysNeverLeaks(t *testing.T) {
	captureSyncLog(t)
	path := filepath.Join(t.TempDir(), "snap.json")
	fs := NewFileSnapshotStore(path)
	clock := newFakeClock()

	cfgA := defaultConfig()
	cfgA.baseURL = snapTestBase
	cfgA.snapshotStore = fs
	scA := newSharedCore("key-A", cfgA)
	defer scA.release()
	scA.snap.now = clock.Now
	scA.store.setAll([]flagDTO{snapFlag("only-a", "x")}, nil)
	scA.snap.fullApplied()
	snapWait(t, "A's snapshot file", func() bool { _, found, _ := fs.Get(); return found })
	settle(t, scA.snap)

	cfgB := cfgA
	scB := newSharedCore("key-B", cfgB)
	defer scB.release()
	scB.snap.now = clock.Now
	scB.snap.loadFallback("test", time.Second)
	if info := scB.snap.startup(); info.Source != StartupNone || info.Reason != SnapshotReasonInvalid {
		t.Fatalf("key B must reject A's snapshot, got %+v", info)
	}
	if _, ok := scB.store.getFlag("only-a"); ok {
		t.Fatal("A's flag leaked to B")
	}

	scA2 := newSharedCore("key-A", cfgA)
	defer scA2.release()
	scA2.snap.now = clock.Now
	scA2.snap.loadFallback("test", time.Second)
	if scA2.snap.startup().Source != StartupSnapshot {
		t.Fatal("key A must read its own snapshot")
	}
}

func TestSnapshotStartup_CustomStoreAndFailingStores(t *testing.T) {
	hash := snapshotKeyHash(snapTestBase, "sdk-key-A")
	clock := newFakeClock()

	t.Run("custom in-memory store", func(t *testing.T) {
		captureSyncLog(t)
		ms := &memStore{found: true, text: envelopeFor(t, hash, clock.Now(), []flagDTO{snapFlag("f", "x")}, nil)}
		sc := newSnapCore(t, ms, clock)
		sc.snap.loadFallback("test", time.Second)
		if sc.snap.startup().Source != StartupSnapshot {
			t.Fatal("expected snapshot start")
		}
	})
	t.Run("Get error is a miss with a warning", func(t *testing.T) {
		logs := captureSyncLog(t)
		sc := newSnapCore(t, &memStore{getErr: errors.New("redis down")}, clock)
		sc.snap.loadFallback("test", time.Second)
		if info := sc.snap.startup(); info.Source != StartupNone || info.Reason != "" {
			t.Fatalf("got %+v", info)
		}
		if !strings.Contains(logs.String(), "redis down") {
			t.Fatalf("warning missing: %q", logs.String())
		}
	})
	t.Run("Get panic is a miss with a warning", func(t *testing.T) {
		logs := captureSyncLog(t)
		sc := newSnapCore(t, &memStore{getPanic: true}, clock)
		sc.snap.loadFallback("test", time.Second)
		if info := sc.snap.startup(); info.Source != StartupNone || info.Reason != "" {
			t.Fatalf("got %+v", info)
		}
		if !strings.Contains(logs.String(), "panicked") {
			t.Fatalf("warning missing: %q", logs.String())
		}
	})
	t.Run("hung Get is bounded and a late result is ignored", func(t *testing.T) {
		logs := captureSyncLog(t)
		gate := make(chan struct{})
		ms := &memStore{getGate: gate, found: true, text: envelopeFor(t, hash, clock.Now(), []flagDTO{snapFlag("late", "x")}, nil)}
		sc := newSnapCore(t, ms, clock)
		started := time.Now()
		sc.snap.loadFallback("test", 80*time.Millisecond)
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("hung Get was not bounded: %v", elapsed)
		}
		close(gate) // the late result now arrives on a buffered channel nobody reads
		time.Sleep(30 * time.Millisecond)
		if info := sc.snap.startup(); info.Source != StartupNone || info.Reason != "" {
			t.Fatalf("got %+v", info)
		}
		if _, ok := sc.store.getFlag("late"); ok {
			t.Fatal("late Get result must be ignored")
		}
		if !strings.Contains(logs.String(), "did not return") {
			t.Fatalf("expected a timeout warning: %q", logs.String())
		}
	})
}

func TestSnapshotStartup_WarningNamesCauseNeverKey(t *testing.T) {
	logs := captureSyncLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	ms := &memStore{}
	cfg := defaultConfig()
	cfg.baseURL = srv.URL
	cfg.streaming = false
	cfg.pollInterval = time.Hour
	cfg.snapshotStore = ms
	sc := newSharedCore("super-secret-sdk-key", cfg)
	defer sc.release()
	if err := sc.initializeOnce(); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	if !strings.Contains(out, "unexpected status 401") {
		t.Fatalf("log must name the HTTP status: %q", out)
	}
	if strings.Contains(out, "super-secret-sdk-key") {
		t.Fatal("the SDK key must never be logged")
	}
}

// --- init paths over HTTP ---

func flagsServer(t *testing.T, flags func() []flagDTO) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/sdk/flags":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(getFlagsResponse{Environment: "t", Version: 1, Flags: flags(), Segments: []segmentDTO{}})
		case strings.HasPrefix(r.URL.Path, "/v1/sdk/flags/"):
			key := strings.TrimPrefix(r.URL.Path, "/v1/sdk/flags/")
			for _, f := range flags() {
				if f.Key == key {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(f)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/v1/sdk/events":
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSnapshotInit_HealthyStartWritesAndNeverReads(t *testing.T) {
	captureSyncLog(t)
	srv := flagsServer(t, func() []flagDTO { return []flagDTO{snapFlag("f", "live")} })
	ms := &memStore{}
	cfg := defaultConfig()
	cfg.baseURL = srv.URL
	cfg.streaming = false
	cfg.pollInterval = time.Hour
	cfg.snapshotStore = ms
	sc := newSharedCore("sdk-key-A", cfg)
	defer sc.release()
	if err := sc.initializeOnce(); err != nil {
		t.Fatal(err)
	}
	snapWait(t, "initial write", func() bool { return ms.putCount() >= 1 })
	settle(t, sc.snap)
	if ms.getCount() != 0 {
		t.Fatal("a healthy start must never read the store")
	}
	if info := sc.snap.startup(); info.Source != StartupLive || info.Reason != "" {
		t.Fatalf("got %+v", info)
	}
	hash := snapshotKeyHash(srv.URL, "sdk-key-A")
	d, err := decodeSnapshotEnvelope(ms.lastPut(), hash, time.Now())
	if err != nil || len(d.resp.Flags) != 1 || d.resp.Flags[0].Key != "f" {
		t.Fatalf("persisted text is not a valid snapshot of the live state: %v", err)
	}
	if strings.Contains(ms.lastPut(), "sdk-key-A") {
		t.Fatal("the SDK key must never be stored")
	}
}

func TestSnapshotInit_FetchErrorAndTimeoutBothFallBack(t *testing.T) {
	hashFor := func(url string) string { return snapshotKeyHash(url, "sdk-key-A") }

	t.Run("fetch error", func(t *testing.T) {
		captureSyncLog(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
		defer srv.Close()
		ms := &memStore{found: true, text: envelopeFor(t, hashFor(srv.URL), time.Now().Add(-time.Minute), []flagDTO{snapFlag("f", "from-snapshot")}, nil)}
		cfg := defaultConfig()
		cfg.baseURL, cfg.streaming, cfg.pollInterval, cfg.snapshotStore = srv.URL, false, time.Hour, ms
		sc := newSharedCore("sdk-key-A", cfg)
		defer sc.release()
		if err := sc.initializeOnce(); err != nil {
			t.Fatal(err)
		}
		c := &Client{core: sc}
		if got := c.StringVariation("f", EvaluationContext{}, "default"); got != "from-snapshot" {
			t.Fatalf("served %q", got)
		}
		if !c.Initialized() {
			t.Fatal("a snapshot start counts as initialized")
		}
		if info := c.StartupInfo(); info.Source != StartupSnapshot || info.Reason != SnapshotReasonSnapshot {
			t.Fatalf("got %+v", info)
		}
		// The data source still started: it polled (and failed) without disturbing
		// the snapshot, and nothing was written.
		if ms.putCount() != 0 {
			t.Fatal("loading a snapshot must not write")
		}
	})

	t.Run("init timeout", func(t *testing.T) {
		captureSyncLog(t)
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-release
			w.WriteHeader(500)
		}))
		defer srv.Close()
		defer close(release)
		ms := &memStore{found: true, text: envelopeFor(t, hashFor(srv.URL), time.Now().Add(-time.Minute), []flagDTO{snapFlag("f", "from-snapshot")}, nil)}
		cfg := defaultConfig()
		cfg.baseURL, cfg.streaming, cfg.pollInterval, cfg.snapshotStore = srv.URL, false, time.Hour, ms
		cfg.initTimeout = 100 * time.Millisecond
		sc := newSharedCore("sdk-key-A", cfg)
		defer sc.release()
		started := time.Now()
		if err := sc.initializeOnce(); err != nil {
			t.Fatal(err)
		}
		if time.Since(started) > 3*time.Second {
			t.Fatal("init took far longer than the timeout")
		}
		if info := sc.snap.startup(); info.Source != StartupSnapshot {
			t.Fatalf("got %+v", info)
		}
	})
}

func TestSnapshotInit_NoStoreKeepsExistingBehavior(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	cfg := defaultConfig()
	cfg.baseURL, cfg.streaming, cfg.pollInterval = srv.URL, false, time.Hour
	sc := newSharedCore("sdk-key-A", cfg)
	defer sc.release()
	if err := sc.initializeOnce(); err != nil {
		t.Fatalf("init with no store must still return nil (defaults-serving), got %v", err)
	}
	c := &Client{core: sc}
	if !c.Initialized() || c.StringVariation("f", EvaluationContext{}, "d") != "d" {
		t.Fatal("expected an initialized, defaults-serving client")
	}
	if info := c.StartupInfo(); info.Source != StartupNone || info.Reason != "" {
		t.Fatalf("got %+v", info)
	}
}

func TestSnapshotInit_FailedInitWithStoreButNoSnapshotServesDefaults(t *testing.T) {
	captureSyncLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	ms := &memStore{}
	cfg := defaultConfig()
	cfg.baseURL, cfg.streaming, cfg.pollInterval, cfg.snapshotStore = srv.URL, false, time.Hour, ms
	sc := newSharedCore("sdk-key-A", cfg)
	defer sc.release()
	if err := sc.initializeOnce(); err != nil {
		t.Fatalf("got %v", err)
	}
	c := &Client{core: sc}
	if !c.Initialized() || c.StartupInfo().Source != StartupNone {
		t.Fatalf("got %+v", c.StartupInfo())
	}
}

func TestForTesting_ExposesStartupInfo(t *testing.T) {
	c := ForTesting(map[string]any{"f": true})
	defer c.Close()
	if info := c.StartupInfo(); info.Source != StartupLive || info.Reason != "" {
		t.Fatalf("got %+v", info)
	}
}

// --- parity: write, stop the API, restart from the snapshot ---

func TestSnapshot_SharedParityScenario_StartsFromSnapshotWhenAPIIsGone(t *testing.T) {
	captureSyncLog(t)
	ResetForTesting()
	t.Cleanup(ResetForTesting)

	srv := flagsServer(t, func() []flagDTO { return []flagDTO{snapFlag("parity-flag", "real-value")} })
	path := filepath.Join(t.TempDir(), "snap.json")

	a, err := Get("parity-key", WithBaseURL(srv.URL), WithStreaming(false), WithPollInterval(time.Hour),
		WithSnapshotStore(NewFileSnapshotStore(path)))
	if err != nil {
		t.Fatal(err)
	}
	if a.StartupInfo().Source != StartupLive {
		t.Fatalf("A: %+v", a.StartupInfo())
	}
	snapWait(t, "A's snapshot file", func() bool { b, err := os.ReadFile(path); return err == nil && len(b) > 0 })
	settle(t, a.core.snap)
	_ = a.Close()
	srv.Close() // the API is now gone

	b, err := Get("parity-key", WithBaseURL(srv.URL), WithStreaming(false), WithPollInterval(time.Hour),
		WithInitTimeout(300*time.Millisecond), WithSnapshotStore(NewFileSnapshotStore(path)))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !b.Initialized() {
		t.Fatal("B must be initialized")
	}
	if got := b.StringVariation("parity-flag", EvaluationContext{UserID: "u"}, "default"); got != "real-value" {
		t.Fatalf("B served %q, want the snapshot's real value", got)
	}
	if info := b.StartupInfo(); info.Source != StartupSnapshot || info.Reason != SnapshotReasonSnapshot {
		t.Fatalf("B: %+v", info)
	}
}

// --- snapshot to live recovery (core level, data source stubbed) ---

func TestSnapshotRecovery_LiveApplyFiresListenersForDiffAndMovesInfoToLive(t *testing.T) {
	captureSyncLog(t)
	srv := flagsServer(t, func() []flagDTO {
		return []flagDTO{snapFlag("f", "v2"), snapFlag("g", "new"), {
			Key: "bad", Version: 1, Type: "Boolean", Enabled: true,
			Fallthrough: serveConfig{Type: "Xor", Variation: "v"}, OffVariation: "v",
		}}
	})
	clock := newFakeClock()
	hash := snapshotKeyHash(snapTestBase, "sdk-key-A")
	ms := &memStore{found: true, text: envelopeFor(t, hash, clock.Now().Add(-time.Minute), []flagDTO{snapFlag("f", "v1")}, nil)}
	sc := newSnapCore(t, ms, clock)

	var mu sync.Mutex
	var fired [][]string
	sc.addUpdateListener(func(keys []string) {
		mu.Lock()
		fired = append(fired, keys)
		mu.Unlock()
	})

	sc.snap.loadFallback("test", time.Second)
	mu.Lock()
	if len(fired) != 0 {
		t.Fatal("loading a snapshot must fire no listeners")
	}
	mu.Unlock()
	if info := sc.snap.startup(); info.Source != StartupSnapshot || info.AgeSeconds != 60 {
		t.Fatalf("got %+v", info)
	}

	cfg := defaultConfig()
	cfg.baseURL = srv.URL
	ps := newPollSource(newHTTPClient("sdk-key-A", cfg), sc.store, time.Hour, sc.notifyUpdateListeners)
	ps.snap = sc.snap
	ps.poll() // deterministic: synchronous, no data source goroutine involved

	mu.Lock()
	if len(fired) != 1 {
		t.Fatalf("expected exactly one listener call, got %v", fired)
	}
	got := append([]string(nil), fired[0]...)
	mu.Unlock()
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"f", "g"}) {
		t.Fatalf("listener diff = %v, want [f g] (the dropped unevaluable flag is not a change)", got)
	}
	if info := sc.snap.startup(); info.Source != StartupLive || info.Reason != "" || info.AgeSeconds != 0 {
		t.Fatalf("startup info must move to live and clear reason/age, got %+v", info)
	}

	snapWait(t, "first live write", func() bool { return ms.putCount() == 1 })
	settle(t, sc.snap)
	d, err := decodeSnapshotEnvelope(ms.lastPut(), hash, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, f := range d.resp.Flags {
		keys = append(keys, f.Key)
	}
	if !reflect.DeepEqual(keys, []string{"f", "g"}) {
		t.Fatalf("persisted flags = %v: unevaluable entities must not be persisted", keys)
	}
}

func TestSnapshotRecovery_FirstLiveApplyAlwaysWritesEvenIfIdentical(t *testing.T) {
	captureSyncLog(t)
	srv := flagsServer(t, func() []flagDTO { return []flagDTO{snapFlag("f", "v1")} })
	clock := newFakeClock()
	hash := snapshotKeyHash(snapTestBase, "sdk-key-A")
	ms := &memStore{found: true, text: envelopeFor(t, hash, clock.Now(), []flagDTO{snapFlag("f", "v1")}, nil)}
	sc := newSnapCore(t, ms, clock)
	sc.snap.loadFallback("test", time.Second)

	cfg := defaultConfig()
	cfg.baseURL = srv.URL
	ps := newPollSource(newHTTPClient("sdk-key-A", cfg), sc.store, time.Hour, nil)
	ps.snap = sc.snap
	ps.poll()
	snapWait(t, "write", func() bool { return ms.putCount() == 1 })
}

// --- persist rules ---

func TestSnapshotPersist_DeltasWriteOnlyAfterLiveStart(t *testing.T) {
	captureSyncLog(t)
	srv := flagsServer(t, func() []flagDTO { return []flagDTO{snapFlag("f", "changed")} })
	cfg := defaultConfig()
	cfg.baseURL = srv.URL
	hc := newHTTPClient("sdk-key-A", cfg)
	clock := newFakeClock()
	hash := snapshotKeyHash(snapTestBase, "sdk-key-A")

	delta := func(sc *sharedCore) {
		ss := newStreamSource(hc, sc.store, nil)
		ss.snap = sc.snap
		ss.handleEvent("flag.updated", `{"key":"f","version":2}`)
	}

	t.Run("after a snapshot start, no write", func(t *testing.T) {
		ms := &memStore{found: true, text: envelopeFor(t, hash, clock.Now(), []flagDTO{snapFlag("f", "old")}, nil)}
		sc := newSnapCore(t, ms, clock)
		sc.snap.loadFallback("test", time.Second)
		delta(sc)
		settle(t, sc.snap)
		if ms.putCount() != 0 {
			t.Fatal("a delta right after a snapshot start must not re-stamp stale data")
		}
		if v, _ := sc.store.getFlag("f"); string(v.Variations[0].Value) != `"changed"` {
			t.Fatal("the delta must still apply in memory")
		}
	})
	t.Run("after a none start, no write", func(t *testing.T) {
		ms := &memStore{}
		sc := newSnapCore(t, ms, clock)
		delta(sc)
		settle(t, sc.snap)
		if ms.putCount() != 0 {
			t.Fatal("a delta after a none start must not write")
		}
	})
	t.Run("after a live start, update and delete write", func(t *testing.T) {
		ms := &memStore{}
		sc := newSnapCore(t, ms, clock)
		sc.store.setAll([]flagDTO{snapFlag("f", "old")}, nil)
		sc.snap.fullApplied()
		snapWait(t, "initial write", func() bool { return ms.putCount() == 1 })
		settle(t, sc.snap)

		delta(sc)
		snapWait(t, "delta write", func() bool { return ms.putCount() == 2 })
		settle(t, sc.snap)
		if !strings.Contains(ms.lastPut(), `\"changed\"`) && !strings.Contains(ms.lastPut(), `"changed"`) {
			t.Fatalf("delta write lacks the new value: %s", ms.lastPut())
		}

		ss := newStreamSource(hc, sc.store, nil)
		ss.snap = sc.snap
		ss.handleEvent("flag.deleted", `{"key":"f"}`)
		snapWait(t, "delete write", func() bool { return ms.putCount() == 3 })
	})
}

func TestSnapshotPersist_WritesAreCoalescedAndTrailingWriteReadsStateWhenItRuns(t *testing.T) {
	captureSyncLog(t)
	gate := make(chan struct{})
	ms := &memStore{putGate: gate}
	sc := newSnapCore(t, ms, newFakeClock())

	sc.store.setAll([]flagDTO{snapFlag("f", "first")}, nil)
	sc.snap.fullApplied()
	snapWait(t, "first put to start", func() bool { return ms.putCount() == 1 })

	for i := 0; i < 10; i++ { // while the first put is held
		sc.store.setAll([]flagDTO{snapFlag("f", fmt.Sprintf("held-%d", i))}, nil)
		sc.snap.fullApplied()
	}
	sc.store.setAll([]flagDTO{snapFlag("f", "latest")}, nil)
	sc.snap.fullApplied()

	if ms.putCount() != 1 {
		t.Fatal("no second write may start while the first is in flight")
	}
	close(gate)
	snapWait(t, "trailing write", func() bool { return ms.putCount() == 2 })
	settle(t, sc.snap)
	if ms.putCount() != 2 {
		t.Fatalf("expected exactly one trailing write, got %d puts", ms.putCount())
	}
	if !strings.Contains(ms.lastPut(), "latest") {
		t.Fatalf("the trailing write must read the state when it runs: %s", ms.lastPut())
	}
}

func TestSnapshotPersist_CloseNeitherWaitsNorStartsATrailingWrite(t *testing.T) {
	captureSyncLog(t)
	gate := make(chan struct{})
	ms := &memStore{putGate: gate}
	cfg := defaultConfig()
	cfg.baseURL, cfg.streaming, cfg.pollInterval, cfg.snapshotStore = snapTestBase, false, time.Hour, ms
	cfg.initTimeout = 50 * time.Millisecond
	sc := newSharedCore("sdk-key-A", cfg)
	sc.snap.now = newFakeClock().Now

	sc.store.setAll([]flagDTO{snapFlag("f", "x")}, nil)
	sc.snap.fullApplied()
	snapWait(t, "put to start", func() bool { return ms.putCount() == 1 })
	sc.store.setAll([]flagDTO{snapFlag("f", "y")}, nil)
	sc.snap.fullApplied() // marks a trailing write

	done := make(chan struct{})
	go func() { sc.shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown waited on a hung Put")
	}
	close(gate)
	settle(t, sc.snap)
	if ms.putCount() != 1 {
		t.Fatalf("no trailing write may start after close, got %d puts", ms.putCount())
	}
	sc.store.setAll([]flagDTO{snapFlag("f", "z")}, nil)
	sc.snap.fullApplied()
	sc.snap.deltaApplied()
	sc.snap.ping()
	time.Sleep(20 * time.Millisecond)
	if ms.putCount() != 1 {
		t.Fatal("a write was requested after close")
	}
}

func TestSnapshotPersist_FailingAndPanickingPutDoNotWedgeOrEscape(t *testing.T) {
	logs := captureSyncLog(t)
	ms := &memStore{putPanic: 1, putErr: errors.New("disk full")}
	sc := newSnapCore(t, ms, newFakeClock())

	sc.store.setAll([]flagDTO{snapFlag("f", "a")}, nil)
	sc.snap.fullApplied()
	snapWait(t, "panicking put", func() bool { return ms.putCount() == 1 })
	settle(t, sc.snap) // the panic was contained: the writer went idle

	sc.store.setAll([]flagDTO{snapFlag("f", "b")}, nil)
	sc.snap.fullApplied() // changed content writes again, so the writer is not wedged
	snapWait(t, "second put", func() bool { return ms.putCount() == 2 })
	settle(t, sc.snap)
	out := logs.String()
	if !strings.Contains(out, "panicked") || !strings.Contains(out, "disk full") {
		t.Fatalf("both failures must be logged as warnings: %q", out)
	}
}

// --- refresh rule (fake clock, no sleeping minutes) ---

func TestSnapshotRefresh_UnchangedStateRewrittenAtMostEveryFiveMinutes(t *testing.T) {
	captureSyncLog(t)
	clock := newFakeClock()
	ms := &memStore{}
	sc := newSnapCore(t, ms, clock)
	sc.store.setAll([]flagDTO{snapFlag("f", "x")}, nil)

	sc.snap.fullApplied() // first live apply: writes
	snapWait(t, "write 1", func() bool { return ms.putCount() == 1 })
	settle(t, sc.snap)

	clock.Advance(299 * time.Second)
	sc.snap.fullApplied() // identical state inside the window: skipped
	sc.snap.ping()        // heartbeat inside the window: skipped
	settle(t, sc.snap)
	if ms.putCount() != 1 {
		t.Fatalf("an unchanged state inside 300s must not be rewritten, puts=%d", ms.putCount())
	}

	clock.Advance(2 * time.Second) // 301s since the START of the last write
	sc.snap.ping()
	snapWait(t, "refresh by ping", func() bool { return ms.putCount() == 2 })
	settle(t, sc.snap)
	if !strings.Contains(ms.lastPut(), fmt.Sprintf(`"fetchedAt":%d`, clock.Now().UnixMilli())) {
		t.Fatalf("fetchedAt must be the write time in epoch ms: %s", ms.lastPut())
	}

	clock.Advance(301 * time.Second)
	sc.snap.fullApplied() // a polling client refreshes the same way
	snapWait(t, "refresh by full apply", func() bool { return ms.putCount() == 3 })
	settle(t, sc.snap)

	clock.Advance(time.Second)
	sc.store.setAll([]flagDTO{snapFlag("f", "changed")}, nil)
	sc.snap.fullApplied() // changed content is never throttled
	snapWait(t, "changed content write", func() bool { return ms.putCount() == 4 })
}

func TestSnapshotRefresh_SnapshotAndNoneStartsNeverRefreshUntilFirstLiveApply(t *testing.T) {
	captureSyncLog(t)
	clock := newFakeClock()
	hash := snapshotKeyHash(snapTestBase, "sdk-key-A")

	for _, name := range []string{"snapshot start", "none start"} {
		t.Run(name, func(t *testing.T) {
			ms := &memStore{}
			if name == "snapshot start" {
				ms.found, ms.text = true, envelopeFor(t, hash, clock.Now(), []flagDTO{snapFlag("f", "x")}, nil)
			}
			sc := newSnapCore(t, ms, clock)
			sc.snap.loadFallback("test", time.Second)

			clock.Advance(time.Hour)
			sc.snap.ping()
			sc.snap.deltaApplied()
			settle(t, sc.snap)
			if ms.putCount() != 0 {
				t.Fatal("must not write before the first live full apply")
			}
			sc.store.setAll([]flagDTO{snapFlag("f", "x")}, nil)
			sc.snap.fullApplied()
			snapWait(t, "first live write", func() bool { return ms.putCount() == 1 })
			if sc.snap.startup().Source != StartupLive {
				t.Fatal("expected live")
			}
		})
	}
}

// --- ping through the stream handler ---

func TestSnapshotRefresh_PingEventReachesTheManager(t *testing.T) {
	captureSyncLog(t)
	clock := newFakeClock()
	ms := &memStore{}
	sc := newSnapCore(t, ms, clock)
	sc.store.setAll([]flagDTO{snapFlag("f", "x")}, nil)
	sc.snap.fullApplied()
	snapWait(t, "write 1", func() bool { return ms.putCount() == 1 })
	settle(t, sc.snap)

	ss := newStreamSource(newHTTPClient("k", defaultConfig()), sc.store, nil)
	ss.snap = sc.snap
	clock.Advance(10 * time.Minute)
	ss.handleEvent("ping", `{"timestamp":"2026-11-06T12:10:00Z"}`)
	snapWait(t, "ping refresh", func() bool { return ms.putCount() == 2 })
}

// --- options and factory ---

func TestWithMaxSnapshotAge_RejectsNonPositive(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	for _, d := range []time.Duration{0, -time.Second, 500 * time.Millisecond} {
		if _, err := Get("k-bad", WithMaxSnapshotAge(d)); err == nil {
			t.Errorf("WithMaxSnapshotAge(%v) must be a config error", d)
		}
	}
	srv := flagsServer(t, func() []flagDTO { return nil })
	c, err := Get("k-ok", WithBaseURL(srv.URL), WithStreaming(false), WithPollInterval(time.Hour), WithMaxSnapshotAge(time.Second))
	if err != nil {
		t.Fatalf("one second is valid: %v", err)
	}
	_ = c.Close()
}

func TestFactory_SnapshotOptionIgnoredWarning(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	srv := flagsServer(t, func() []flagDTO { return nil })
	base := []Option{WithBaseURL(srv.URL), WithStreaming(false), WithPollInterval(time.Hour)}
	s1, s2 := &memStore{}, &memStore{}

	first, err := Get("k-factory", append([]Option{WithSnapshotStore(s1), WithMaxSnapshotAge(2 * time.Hour)}, base...)...)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	const warning = "different options"
	try := func(opts ...Option) bool {
		logs := captureSyncLog(t)
		c, err := Get("k-factory", append(opts, base...)...)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
		return strings.Contains(logs.String(), warning)
	}

	if !try(WithSnapshotStore(s2), WithMaxSnapshotAge(2*time.Hour)) {
		t.Error("a different store instance must warn")
	}
	if !try(WithSnapshotStore(s1), WithMaxSnapshotAge(time.Hour)) {
		t.Error("a different max age must warn")
	}
	if try(WithSnapshotStore(s1), WithMaxSnapshotAge(2*time.Hour)) {
		t.Error("the same store and max age must stay silent")
	}
	if try() {
		t.Error("omitting the snapshot options must not warn (no spurious warning)")
	}
}

// --- race exercise ---

func TestSnapshotManager_ConcurrentTriggersAreRaceFree(t *testing.T) {
	captureSyncLog(t)
	ms := &memStore{}
	sc := newSnapCore(t, ms, nil)
	var wg sync.WaitGroup
	var n atomic.Int64
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				sc.store.setAll([]flagDTO{snapFlag("f", fmt.Sprintf("v%d", n.Add(1)))}, nil)
				sc.snap.fullApplied()
				sc.snap.deltaApplied()
				sc.snap.ping()
				_ = sc.snap.startup()
			}
		}()
	}
	wg.Wait()
	settle(t, sc.snap)
	if ms.putCount() == 0 {
		t.Fatal("expected at least one write")
	}
	last := ms.lastPut()
	if _, err := decodeSnapshotEnvelope(last, sc.snap.keyHash, time.Now()); err != nil {
		t.Fatalf("last write is not valid: %v", err)
	}
}

// --- fix wave: default streaming paths, shutdown ordering, double write, logging ---

func TestSnapshotStreaming_SyncAfterSnapshotStartPersistsGoesLiveAndFiresDiff(t *testing.T) {
	captureSyncLog(t)
	clock := newFakeClock()
	hash := snapshotKeyHash(snapTestBase, "sdk-key-A")
	ms := &memStore{found: true, text: envelopeFor(t, hash, clock.Now().Add(-time.Minute), []flagDTO{snapFlag("f", "v1")}, nil)}
	sc := newSnapCore(t, ms, clock)

	var mu sync.Mutex
	var fired [][]string
	sc.addUpdateListener(func(keys []string) {
		mu.Lock()
		fired = append(fired, keys)
		mu.Unlock()
	})
	sc.snap.loadFallback("test", time.Second)

	payload, err := json.Marshal(getFlagsResponse{Environment: "t", Version: 2,
		Flags: []flagDTO{snapFlag("f", "v2")}, Segments: []segmentDTO{}})
	if err != nil {
		t.Fatal(err)
	}
	ss := newStreamSource(newHTTPClient("sdk-key-A", defaultConfig()), sc.store, sc.notifyUpdateListeners)
	ss.snap = sc.snap
	ss.handleEvent("sync", string(payload))

	snapWait(t, "sync write", func() bool { return ms.putCount() == 1 })
	if info := sc.snap.startup(); info.Source != StartupLive || info.Reason != "" {
		t.Fatalf("got %+v", info)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 1 || !reflect.DeepEqual(fired[0], []string{"f"}) {
		t.Fatalf("listener diff = %v, want [[f]]", fired)
	}
	if !strings.Contains(ms.lastPut(), "v2") {
		t.Fatalf("the sync payload was not persisted: %s", ms.lastPut())
	}
}

func TestSnapshotStreaming_SegmentUpdatedAfterLiveStartPersists(t *testing.T) {
	captureSyncLog(t)
	var current atomic.Value
	current.Store("v1")
	srv := flagsServer(t, func() []flagDTO { return []flagDTO{snapFlag("f", current.Load().(string))} })
	cfg := defaultConfig()
	cfg.baseURL = srv.URL
	ms := &memStore{}
	sc := newSnapCore(t, ms, newFakeClock())
	sc.store.setAll([]flagDTO{snapFlag("f", "v1")}, nil)
	sc.snap.fullApplied()
	snapWait(t, "initial write", func() bool { return ms.putCount() == 1 })
	settle(t, sc.snap)

	current.Store("v2") // the segment edit moved a flag's evaluated value
	ss := newStreamSource(newHTTPClient("sdk-key-A", cfg), sc.store, nil)
	ss.snap = sc.snap
	ss.handleEvent("segment.updated", `{"key":"seg"}`)
	snapWait(t, "segment.updated write", func() bool { return ms.putCount() == 2 })
	if !strings.Contains(ms.lastPut(), "v2") {
		t.Fatalf("segment refetch was not persisted: %s", ms.lastPut())
	}
}

func TestSnapshotShutdown_InFlightPollFinishingDuringShutdownStartsNoWrite(t *testing.T) {
	captureSyncLog(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		_ = json.NewEncoder(w).Encode(getFlagsResponse{Flags: []flagDTO{snapFlag("f", "x")}, Segments: []segmentDTO{}})
	}))
	defer srv.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	cfg := defaultConfig()
	cfg.baseURL = srv.URL
	ms := &memStore{}
	sc := newSnapCore(t, ms, newFakeClock())
	ps := newPollSource(newHTTPClient("sdk-key-A", cfg), sc.store, time.Hour, nil)
	ps.snap = sc.snap
	pollDone := make(chan struct{})
	go func() { ps.poll(); close(pollDone) }()
	<-entered

	// Stopping the poll source lets the in-flight poll finish, as it can in real
	// life. The snapshot writer must already be closed by then.
	var stopOnce sync.Once
	sc.stopPoll = func() {
		stopOnce.Do(func() {
			close(release)
			<-pollDone
		})
	}
	sc.shutdown()
	settle(t, sc.snap)
	if ms.putCount() != 0 {
		t.Fatalf("an in-flight poll finishing during shutdown started a write (%d puts)", ms.putCount())
	}
}

func TestSnapshotInit_HealthyPollingStartWritesExactlyOnce(t *testing.T) {
	captureSyncLog(t)
	var requests atomic.Int64
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sdk/flags" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(getFlagsResponse{Flags: []flagDTO{snapFlag("f", "x")}, Segments: []segmentDTO{}})
	}))
	defer counting.Close()

	ms := &memStore{}
	cfg := defaultConfig()
	cfg.baseURL, cfg.streaming, cfg.pollInterval, cfg.snapshotStore = counting.URL, false, time.Hour, ms
	sc := newSharedCore("sdk-key-A", cfg)
	defer sc.release()
	if err := sc.initializeOnce(); err != nil {
		t.Fatal(err)
	}
	// The initial fetch plus the poll source's immediate first poll.
	snapWait(t, "initial fetch and first poll", func() bool { return requests.Load() >= 2 })
	time.Sleep(50 * time.Millisecond) // let the poll's apply run
	settle(t, sc.snap)
	if ms.putCount() != 1 {
		t.Fatalf("a healthy polling start must write once, got %d puts", ms.putCount())
	}
}

func TestSnapshotInit_NoStoreLogsFailureCauseButStaysQuietWhenHealthy(t *testing.T) {
	t.Run("failure logs the cause, never the key", func(t *testing.T) {
		logs := captureSyncLog(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
		defer srv.Close()
		cfg := defaultConfig()
		cfg.baseURL, cfg.streaming, cfg.pollInterval = srv.URL, false, time.Hour
		sc := newSharedCore("very-secret-sdk-key", cfg)
		defer sc.release()
		if err := sc.initializeOnce(); err != nil {
			t.Fatal(err)
		}
		out := logs.String()
		if !strings.Contains(out, "initial fetch failed") || !strings.Contains(out, "unexpected status 401") {
			t.Fatalf("expected the cause to be logged: %q", out)
		}
		if strings.Contains(out, "very-secret-sdk-key") {
			t.Fatal("the SDK key must never be logged")
		}
	})
	t.Run("healthy start is quiet", func(t *testing.T) {
		logs := captureSyncLog(t)
		srv := flagsServer(t, func() []flagDTO { return []flagDTO{snapFlag("f", "x")} })
		cfg := defaultConfig()
		cfg.baseURL, cfg.streaming, cfg.pollInterval = srv.URL, false, time.Hour
		sc := newSharedCore("sdk-key-A", cfg)
		defer sc.release()
		if err := sc.initializeOnce(); err != nil {
			t.Fatal(err)
		}
		if out := logs.String(); out != "" {
			t.Fatalf("a healthy start must log nothing, got %q", out)
		}
	})
}

func TestWithMaxSnapshotAge_TruncatesToWholeSeconds(t *testing.T) {
	cfg := defaultConfig()
	WithMaxSnapshotAge(1500 * time.Millisecond)(&cfg)
	if cfg.optionErr != nil {
		t.Fatal(cfg.optionErr)
	}
	sc := newSharedCore("sdk-key-A", cfg)
	defer sc.release()
	if sc.snap.maxAgeSeconds != 1 {
		t.Fatalf("1.5s must truncate to 1s, got %d", sc.snap.maxAgeSeconds)
	}

	// Behavior, not just the field: a 1.2 s old snapshot is expired under a 1 s max.
	clock := newFakeClock()
	hash := snapshotKeyHash(snapTestBase, "sdk-key-A")
	ms := &memStore{found: true, text: envelopeFor(t, hash, clock.Now().Add(-1200*time.Millisecond), []flagDTO{snapFlag("f", "x")}, nil)}
	sc2 := newSnapCore(t, ms, clock, func(c *config) { c.maxSnapshotAge = 1500 * time.Millisecond })
	captureSyncLog(t)
	sc2.snap.loadFallback("test", time.Second)
	if info := sc2.snap.startup(); info.Reason != SnapshotReasonExpired || info.AgeSeconds != 1 {
		t.Fatalf("got %+v", info)
	}
}

func TestSnapshotRefresh_WindowIsMeasuredFromTheStartOfTheWrite(t *testing.T) {
	captureSyncLog(t)
	clock := newFakeClock()
	ms := &memStore{}
	ms.onPut = func(n int) {
		if n == 1 {
			clock.Advance(200 * time.Second) // the first Put takes 200 s
		}
	}
	sc := newSnapCore(t, ms, clock)
	sc.store.setAll([]flagDTO{snapFlag("f", "x")}, nil)
	sc.snap.fullApplied() // write 1 starts at t0 and ends at t0+200s
	snapWait(t, "write 1", func() bool { return ms.putCount() == 1 })
	settle(t, sc.snap)

	clock.Advance(150 * time.Second) // t0+350s: past 300s from the START, only 150s from the end
	sc.snap.ping()
	snapWait(t, "refresh measured from the start", func() bool { return ms.putCount() == 2 })
}

func TestSnapshotPersist_FailedPutIsRetriedByTheNextUnchangedFullApply(t *testing.T) {
	captureSyncLog(t)
	clock := newFakeClock()
	ms := &memStore{putErr: errors.New("disk full")}
	sc := newSnapCore(t, ms, clock)
	sc.store.setAll([]flagDTO{snapFlag("f", "x")}, nil)

	sc.snap.fullApplied()
	snapWait(t, "failing put", func() bool { return ms.putCount() == 1 })
	settle(t, sc.snap)

	ms.mu.Lock()
	ms.putErr = nil
	ms.mu.Unlock()
	clock.Advance(time.Second) // well inside the 5-minute window, state unchanged
	sc.snap.fullApplied()
	snapWait(t, "retry after a failed write", func() bool { return ms.putCount() == 2 })
	settle(t, sc.snap)

	sc.snap.fullApplied() // the retry succeeded, so now an unchanged apply is skipped
	settle(t, sc.snap)
	if ms.putCount() != 2 {
		t.Fatalf("a successful write must restore the throttle, puts=%d", ms.putCount())
	}
}
