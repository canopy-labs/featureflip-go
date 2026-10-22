package featureflip

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
)

// captureLog redirects the standard logger into a buffer for the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldW)
		log.SetFlags(oldF)
	})
	return &buf
}

func keylessRolloutFlag(key string, enabled bool) flagDTO {
	flag := boolFlag(key, enabled, "on", "off")
	flag.Rules = []ruleDTO{{
		ID:       "r1",
		Priority: 1,
		Serve: serveConfig{
			Type:     "Rollout",
			BucketBy: "userId",
			Salt:     "s",
			Variations: []weightedVariation{
				{Key: "on", Weight: 1},
				{Key: "off", Weight: 99},
			},
		},
	}}
	flag.Fallthrough = serveConfig{Type: "Fixed", Variation: "off"}
	return flag
}

func TestWarnUnbucketable_OncePerFlagAndRule_NoContextValues(t *testing.T) {
	buf := captureLog(t)
	flag := keylessRolloutFlag("warn-once-flag", true)
	ctx := EvaluationContext{Attributes: map[string]any{"email": "secret@example.com"}}

	evaluate(flag, ctx, nil, nil)
	evaluate(flag, ctx, nil, nil)

	out := buf.String()
	if n := strings.Count(out, "Skipped the percentage rollout"); n != 1 {
		t.Fatalf("warnings = %d, want 1; log=%q", n, out)
	}
	for _, want := range []string{`"warn-once-flag"`, "rule r1", `"userId"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q missing %q", out, want)
		}
	}
	if strings.Contains(out, "secret@example.com") {
		t.Errorf("log leaked a context value: %q", out)
	}
}

func TestWarnUnbucketable_DisabledFlag_NoWarning(t *testing.T) {
	buf := captureLog(t)
	evaluate(keylessRolloutFlag("warn-disabled-flag", false), EvaluationContext{}, nil, nil)
	if buf.Len() != 0 {
		t.Fatalf("expected no log output, got %q", buf.String())
	}
}

func TestWarnUnbucketable_FallthroughWording(t *testing.T) {
	buf := captureLog(t)
	flag := boolFlag("warn-fallthrough-flag", true, "on", "off")
	flag.Fallthrough = serveConfig{
		Type: "Rollout", BucketBy: "anonymous_id", Salt: "s", Variation: "off",
		Variations: []weightedVariation{{Key: "on", Weight: 50}, {Key: "off", Weight: 50}},
	}
	evaluate(flag, EvaluationContext{UserID: "u"}, nil, nil)
	want := `[featureflip] Skipped the percentage rollout in flag "warn-fallthrough-flag" (fallthrough): the context has no value for "anonymous_id". Pass a stable id in that attribute to include this context in the rollout.`
	if got := strings.TrimSpace(buf.String()); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWarnUnbucketable_CapsAtMaxKeys(t *testing.T) {
	buf := captureLog(t)
	unbucketableMu.Lock()
	saved := unbucketableSeen
	unbucketableSeen = make(map[string]struct{})
	unbucketableMu.Unlock()
	t.Cleanup(func() {
		unbucketableMu.Lock()
		unbucketableSeen = saved
		unbucketableMu.Unlock()
	})

	for i := 0; i < maxUnbucketableKeys; i++ {
		warnUnbucketable(fmt.Sprintf("cap-flag-%d", i), "r1", "userId")
	}
	if n := strings.Count(buf.String(), "Skipped the percentage rollout"); n != maxUnbucketableKeys {
		t.Fatalf("warnings = %d, want %d", n, maxUnbucketableKeys)
	}
	buf.Reset()
	warnUnbucketable("cap-flag-overflow", "r1", "userId")
	if buf.Len() != 0 {
		t.Fatalf("1001st key warned: %q", buf.String())
	}
	unbucketableMu.Lock()
	size := len(unbucketableSeen)
	unbucketableMu.Unlock()
	if size != maxUnbucketableKeys {
		t.Fatalf("set size = %d, want %d", size, maxUnbucketableKeys)
	}
}

func TestWarnUnbucketable_ConcurrentSameFlag_ExactlyOneWarning(t *testing.T) {
	buf := captureLog(t)
	flag := keylessRolloutFlag("warn-concurrent-flag", true)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			evaluate(flag, EvaluationContext{}, nil, nil)
		}()
	}
	close(start)
	wg.Wait()

	if n := strings.Count(buf.String(), "Skipped the percentage rollout"); n != 1 {
		t.Fatalf("warnings = %d, want 1", n)
	}
}
