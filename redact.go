package featureflip

import (
	"fmt"
	"log"
	"strings"
	"sync"
)

// redactedPlaceholder replaces the SDK key wherever it would reach the log.
const redactedPlaceholder = "[redacted]"

// minRedactableKeyLength is the shortest key treated as a credential. A real
// key is `sdk_server_`/`sdk_client_` plus 43 base64url characters (54 total,
// always). Redacting a far shorter value corrupts every line it appears inside
// (key "k" turns "skipped" into "s[redacted]ipped"); a string too short to be a
// Featureflip key cannot authenticate anyway.
const minRedactableKeyLength = 20

// redactKeys is the set of SDK keys any core in this process has used.
//
// The standard library logger is the only sink this SDK writes to, and a
// handful of call sites (flag-payload parsing, rollout warnings) have no core
// in scope, so the keys live in one process-wide registry that logf consults.
//
// Entries are never removed. shutdown does not join the stream and poll
// goroutines, so one can still log after Close; dropping the key then would
// leave that line unprotected. The set is bounded by the distinct keys a
// process ever uses, and redacting a closed client's key is harmless.
var (
	redactKeysMu sync.RWMutex
	redactKeys   = map[string]struct{}{}
)

// registerRedactedKey starts redacting key from every logf line. Keys shorter
// than minRedactableKeyLength are ignored.
func registerRedactedKey(key string) {
	if len(key) < minRedactableKeyLength {
		return
	}
	redactKeysMu.Lock()
	redactKeys[key] = struct{}{}
	redactKeysMu.Unlock()
}

// redact replaces every registered SDK key in s with "[redacted]".
func redact(s string) string {
	redactKeysMu.RLock()
	defer redactKeysMu.RUnlock()
	for key := range redactKeys {
		if strings.Contains(s, key) {
			s = strings.ReplaceAll(s, key, redactedPlaceholder)
		}
	}
	return s
}

// logf is the single place the SDK writes a log line. It formats first and
// redacts the result, so a key smuggled in through an argument (typically an
// error from the HTTP stack that quotes the Authorization header) cannot get
// past it. Every SDK-originated line must go through here, never log.Printf.
func logf(format string, args ...any) {
	log.Print(redact(fmt.Sprintf(format, args...)))
}
