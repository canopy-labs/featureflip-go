package featureflip

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// A String flag whose matching rule serves it, read through the bool accessor,
// is a type mismatch: the event must say "error" and must NOT carry the rule id,
// because that rule did not serve the traffic (#4268).
func TestEvaluationEvent_TypeMismatchIsErrorWithoutRuleID(t *testing.T) {
	flags := json.RawMessage(`[{
		"key": "str", "version": 1, "type": "String", "enabled": true,
		"variations": [{"key": "a", "value": "hello"}, {"key": "b", "value": "bye"}],
		"rules": [{
			"id": "rule-1", "priority": 0,
			"conditionGroups": [],
			"serve": {"type": "Fixed", "variation": "a"},
			"segmentKey": null
		}],
		"fallthrough": {"type": "Fixed", "variation": "b"},
		"offVariation": "b"
	}]`)

	var mu sync.Mutex
	var body []byte
	server := eventCapturingServerWithFlags(t, &body, &mu, flags, nil)
	defer server.Close()

	client, err := Get("events-type-mismatch",
		WithBaseURL(server.URL), WithStreaming(false), WithInitTimeout(5*time.Second))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	client.BoolVariation("str", EvaluationContext{UserID: "u1"}, false)
	client.Flush()
	client.Close()

	mu.Lock()
	captured := append([]byte(nil), body...)
	mu.Unlock()

	var sent struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(captured, &sent); err != nil {
		t.Fatalf("decode %q: %v", captured, err)
	}
	if len(sent.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(sent.Events))
	}
	ev := sent.Events[0]
	if ev["reason"] != "error" {
		t.Errorf("reason = %#v, want \"error\"", ev["reason"])
	}
	if _, present := ev["ruleId"]; present {
		t.Errorf("ruleId = %#v, want the key absent on an error read", ev["ruleId"])
	}
}
