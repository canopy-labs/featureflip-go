package featureflip

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// StartupSource says where a client's flag data came from at startup.
type StartupSource string

// Startup sources reported by [StartupInfo.Source].
const (
	// StartupLive means the flag data came from a successful fetch from the
	// Evaluation API.
	StartupLive StartupSource = "live"
	// StartupSnapshot means the client started from the snapshot store.
	StartupSnapshot StartupSource = "snapshot"
	// StartupNone means there is no flag data yet, so caller defaults are served.
	StartupNone StartupSource = "none"
)

// SnapshotReason says why a stored snapshot was used or ignored.
type SnapshotReason string

// Reasons reported by [StartupInfo.Reason].
const (
	// SnapshotReasonSnapshot means the client started from the snapshot store.
	SnapshotReasonSnapshot SnapshotReason = "snapshot"
	// SnapshotReasonExpired means a stored snapshot was older than the maximum
	// snapshot age and was ignored.
	SnapshotReasonExpired SnapshotReason = "snapshot-expired"
	// SnapshotReasonInvalid means stored text failed a check and was ignored.
	SnapshotReasonInvalid SnapshotReason = "snapshot-invalid"
)

const (
	// defaultMaxSnapshotAge is the maximum age of a snapshot the client will start
	// from when [WithMaxSnapshotAge] is not set.
	defaultMaxSnapshotAge = 24 * time.Hour

	// snapshotRefreshInterval is how often an unchanged live state is rewritten, so
	// the stored snapshot's fetchedAt does not age out while the client is
	// connected. Changed content is never throttled.
	snapshotRefreshInterval = 300 * time.Second

	// futureFetchedAtTolerance is how far in the future a stored fetchedAt may be
	// (clock skew between the writer and the reader) and still be accepted, with
	// its age clamped to 0.
	futureFetchedAtTolerance = 60 * time.Second
)

// StartupInfo reports where the client's flag data came from at startup.
type StartupInfo struct {
	// Source is [StartupLive] after a successful fetch from the Evaluation
	// API, [StartupSnapshot] when the client started from the snapshot
	// store, or [StartupNone] when there is no data yet.
	Source StartupSource
	// Reason is [SnapshotReasonSnapshot] for a snapshot start,
	// [SnapshotReasonExpired] or [SnapshotReasonInvalid] when a stored snapshot
	// was ignored, and empty once the client is live or when nothing was stored.
	Reason SnapshotReason
	// AgeSeconds is the age, in whole seconds, of the snapshot that was used or
	// rejected as expired. It is meaningful only when Reason is
	// [SnapshotReasonSnapshot] or [SnapshotReasonExpired].
	AgeSeconds int64
}

// SnapshotStore is a pluggable persistence layer for the last good flag
// configuration, used to start a process while the Evaluation API is down.
//
// The store is opaque text storage. The SDK owns the stored text end to end (the
// envelope, the environment check, the age check and every startup reason), so a
// store only has to hand back exactly what it was last given. One store may be
// shared by several SDK keys: the SDK rejects text written for a different SDK
// key or base URL.
//
// Get and Put may block, for example on a network call. The SDK calls them from
// its own goroutines, never from the goroutine that evaluates flags. A store that
// returns an error or panics is contained: a failing Get is a miss and a failing
// Put is a dropped write, each with a logged warning. The SDK bounds Get by the
// client's init timeout and abandons a late result. A Put that never returns
// blocks later writes for that client, which does not affect evaluation.
type SnapshotStore interface {
	// Get returns the text last stored. found is false when nothing is stored.
	Get() (text string, found bool, err error)
	// Put replaces the stored text.
	Put(text string) error
}

// FileSnapshotStore is a [SnapshotStore] that keeps the snapshot in one file.
//
// The store is opaque: it neither parses nor checks what it holds. A write goes
// to a uniquely named temp file in the same directory, created exclusively with
// owner-only (0600) permissions, and is renamed over the target, so a reader
// never sees a partial file and the final mode is 0600 even when the target
// already existed with broader permissions. The temp file is removed if the write
// fails. The parent directory must already exist: the store never creates it.
//
// There is deliberately no fsync before the rename, so a power loss can leave a
// zero-length or partial file. The SDK reads that as snapshot-invalid, logs a
// warning and serves defaults, and the next successful write replaces it. A
// process killed mid-write can leave a "<name>.<pid>.<hex>.tmp" file next to the
// snapshot. It is never read.
//
// The 0600 mode is POSIX. On Windows the permission bits are not enforced the
// same way, so protect the directory with an ACL instead.
type FileSnapshotStore struct {
	path string
}

// NewFileSnapshotStore returns a store backed by the file at path.
func NewFileSnapshotStore(path string) *FileSnapshotStore {
	return &FileSnapshotStore{path: path}
}

// Get returns the file's contents. A missing file is a plain miss. Any other read
// error is returned, and the SDK logs it and treats it as a miss.
func (f *FileSnapshotStore) Get() (string, bool, error) {
	b, err := os.ReadFile(f.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(b), true, nil
}

// Put atomically replaces the file's contents.
func (f *FileSnapshotStore) Put(text string) (err error) {
	var suffix [6]byte
	if _, rerr := rand.Read(suffix[:]); rerr != nil {
		return rerr
	}
	tmp := filepath.Join(filepath.Dir(f.path),
		fmt.Sprintf("%s.%d.%s.tmp", filepath.Base(f.path), os.Getpid(), hex.EncodeToString(suffix[:])))

	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = file.WriteString(text); err != nil {
		_ = file.Close()
		return err
	}
	// The umask can only remove bits from the create mode, never add them, but an
	// explicit chmod keeps the 0600 guarantee independent of it.
	if err = file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

// --- envelope ---

// snapshotKeyHash identifies the (base URL, SDK key) pair without storing the
// key: hex SHA-256 over the base URL (trailing slashes stripped), a newline and
// the SDK key.
func snapshotKeyHash(baseURL, sdkKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimRight(baseURL, "/") + "\n" + sdkKey))
	return hex.EncodeToString(sum[:])
}

// snapshotEnvelope is the stored text. Field order is the serialization order.
type snapshotEnvelope struct {
	V         int             `json:"v"`
	KeyHash   string          `json:"keyHash"`
	FetchedAt int64           `json:"fetchedAt"`
	Data      json.RawMessage `json:"data"`
}

// snapshotData is the persisted payload in the wire (GetFlagsResponse) shape.
// Flags and segments are sorted by key before serialization (see sortedState),
// because the unchanged-state comparison relies on a deterministic byte order.
type snapshotData struct {
	Flags    []flagDTO    `json:"flags"`
	Segments []segmentDTO `json:"segments"`
}

// serializeSnapshotData renders the in-memory state as the wire-shaped data
// object, sorted by key, with stable property order (struct field order).
func serializeSnapshotData(flags []flagDTO, segments []segmentDTO) ([]byte, error) {
	flags = append(make([]flagDTO, 0, len(flags)), flags...)
	segments = append(make([]segmentDTO, 0, len(segments)), segments...)
	sort.Slice(flags, func(i, j int) bool { return flags[i].Key < flags[j].Key })
	sort.Slice(segments, func(i, j int) bool { return segments[i].Key < segments[j].Key })
	return json.Marshal(snapshotData{Flags: flags, Segments: segments})
}

func encodeSnapshotEnvelope(keyHash string, fetchedAtMs int64, data []byte) (string, error) {
	b, err := json.Marshal(snapshotEnvelope{V: 1, KeyHash: keyHash, FetchedAt: fetchedAtMs, Data: data})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// snapshotInvalidError carries the name of the failed check. It never contains
// the stored contents or the SDK key.
type snapshotInvalidError struct{ why string }

func (e *snapshotInvalidError) Error() string { return e.why }

func invalidSnapshot(why string) error { return &snapshotInvalidError{why: why} }

// decodedSnapshot is a validated snapshot, already parsed.
type decodedSnapshot struct {
	resp        getFlagsResponse
	fetchedAtMs int64
}

// decodeSnapshotEnvelope parses and checks stored text in the pinned order. The
// age check is not done here. now is the reader's clock.
func decodeSnapshotEnvelope(text, keyHash string, now time.Time) (*decodedSnapshot, error) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		if text == "" {
			return nil, invalidSnapshot("empty text")
		}
		// A JSON value that is not an object (array, string, number) fails the
		// Unmarshal into a map too, so this one message covers both checks.
		return nil, invalidSnapshot("not valid JSON or not an object")
	}
	if env == nil { // the literal null
		return nil, invalidSnapshot("not a snapshot envelope")
	}

	var version float64
	if err := json.Unmarshal(env["v"], &version); err != nil || version != 1 {
		return nil, invalidSnapshot("unsupported envelope version")
	}
	var gotHash string
	if err := json.Unmarshal(env["keyHash"], &gotHash); err != nil || gotHash != keyHash {
		return nil, invalidSnapshot("written for a different SDK key or base URL")
	}
	var fetchedAt float64
	if err := json.Unmarshal(env["fetchedAt"], &fetchedAt); err != nil ||
		fetchedAt <= 0 || fetchedAt != math.Trunc(fetchedAt) || fetchedAt > 1<<53 {
		return nil, invalidSnapshot("fetchedAt is not a positive integer")
	}
	fetchedAtMs := int64(fetchedAt)
	if fetchedAtMs > now.UnixMilli()+futureFetchedAtTolerance.Milliseconds() {
		return nil, invalidSnapshot("fetchedAt is in the future")
	}

	rawData := bytes.TrimSpace(env["data"])
	if len(rawData) == 0 || rawData[0] != '{' {
		return nil, invalidSnapshot("missing data")
	}
	// flags and segments must both be present arrays: a null or absent one is a
	// wrong-shape payload, not an empty one.
	var parts struct {
		Flags    json.RawMessage `json:"flags"`
		Segments json.RawMessage `json:"segments"`
	}
	if err := json.Unmarshal(rawData, &parts); err != nil ||
		!isJSONArray(parts.Flags) || !isJSONArray(parts.Segments) {
		return nil, invalidSnapshot("data failed payload validation")
	}
	// The same wholesale-discard type check the live paths use: encoding/json
	// partially populates on a type error, so any error discards the whole payload.
	var resp getFlagsResponse
	if err := json.Unmarshal(rawData, &resp); err != nil {
		return nil, invalidSnapshot("data failed payload validation")
	}
	return &decodedSnapshot{resp: resp, fetchedAtMs: fetchedAtMs}, nil
}

func isJSONArray(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '['
}
