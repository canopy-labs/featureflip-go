package featureflip

import (
	"log"
	"sync"
)

// maxUnbucketableKeys bounds the dedup set so a hostile flag set can't grow it
// without limit. Once full, unseen keys are dropped silently.
const maxUnbucketableKeys = 1000

var (
	unbucketableMu   sync.Mutex
	unbucketableSeen = make(map[string]struct{})
)

// warnUnbucketable logs, once per (flagKey, rule id or "fallthrough", bucketBy)
// per process, that a percentage rollout was skipped because the context had no
// value to bucket on. An empty ruleID means the flag's fallthrough. It never
// logs context values.
func warnUnbucketable(flagKey, ruleID, bucketBy string) {
	where := "fallthrough"
	if ruleID != "" {
		where = "rule " + ruleID
	}
	key := flagKey + "\x00" + where + "\x00" + bucketBy

	unbucketableMu.Lock()
	if _, dup := unbucketableSeen[key]; dup || len(unbucketableSeen) >= maxUnbucketableKeys {
		unbucketableMu.Unlock()
		return
	}
	unbucketableSeen[key] = struct{}{}
	unbucketableMu.Unlock()

	log.Printf("[featureflip] Skipped the percentage rollout in flag %q (%s): the context has no value for %q. Pass a stable id in that attribute to include this context in the rollout.", flagKey, where, bucketBy)
}
