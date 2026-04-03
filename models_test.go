package featureflip

import (
	"encoding/json"
	"testing"
)

func TestGetFlagsResponse_Unmarshal(t *testing.T) {
	raw := `{
		"environment": "production",
		"version": 5,
		"flags": [
			{
				"key": "dark-mode",
				"version": 2,
				"type": "Boolean",
				"enabled": true,
				"variations": [
					{"key": "on", "value": true},
					{"key": "off", "value": false}
				],
				"rules": [
					{
						"id": "rule-1",
						"priority": 1,
						"conditionGroups": [
							{
								"operator": "and",
								"conditions": [
									{
										"attribute": "country",
										"operator": "in",
										"values": ["US", "CA"],
										"negate": false
									}
								]
							}
						],
						"serve": {
							"type": "Fixed",
							"variation": "on"
						},
						"segmentKey": "beta-users"
					}
				],
				"fallthrough": {
					"type": "Rollout",
					"bucketBy": "userId",
					"salt": "abc123",
					"variations": [
						{"key": "on", "weight": 70},
						{"key": "off", "weight": 30}
					]
				},
				"offVariation": "off"
			}
		],
		"segments": [
			{
				"key": "beta-users",
				"version": 1,
				"conditions": [
					{
						"attribute": "email",
						"operator": "endsWith",
						"values": ["@example.com"],
						"negate": false
					}
				],
				"conditionLogic": "and"
			}
		]
	}`

	var resp getFlagsResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Top-level fields
	if resp.Environment != "production" {
		t.Errorf("Environment = %q, want %q", resp.Environment, "production")
	}
	if resp.Version != 5 {
		t.Errorf("Version = %d, want %d", resp.Version, 5)
	}

	// Flags
	if len(resp.Flags) != 1 {
		t.Fatalf("len(Flags) = %d, want 1", len(resp.Flags))
	}
	flag := resp.Flags[0]
	if flag.Key != "dark-mode" {
		t.Errorf("Flag.Key = %q, want %q", flag.Key, "dark-mode")
	}
	if flag.Version != 2 {
		t.Errorf("Flag.Version = %d, want %d", flag.Version, 2)
	}
	if flag.Type != "Boolean" {
		t.Errorf("Flag.Type = %q, want %q", flag.Type, "Boolean")
	}
	if !flag.Enabled {
		t.Error("Flag.Enabled = false, want true")
	}

	// Variations
	if len(flag.Variations) != 2 {
		t.Fatalf("len(Variations) = %d, want 2", len(flag.Variations))
	}
	if flag.Variations[0].Key != "on" {
		t.Errorf("Variations[0].Key = %q, want %q", flag.Variations[0].Key, "on")
	}

	// Rules
	if len(flag.Rules) != 1 {
		t.Fatalf("len(Rules) = %d, want 1", len(flag.Rules))
	}
	rule := flag.Rules[0]
	if rule.ID != "rule-1" {
		t.Errorf("Rule.ID = %q, want %q", rule.ID, "rule-1")
	}
	if rule.Priority != 1 {
		t.Errorf("Rule.Priority = %d, want %d", rule.Priority, 1)
	}
	if rule.SegmentKey != "beta-users" {
		t.Errorf("Rule.SegmentKey = %q, want %q", rule.SegmentKey, "beta-users")
	}
	if len(rule.ConditionGroups) != 1 {
		t.Fatalf("len(ConditionGroups) = %d, want 1", len(rule.ConditionGroups))
	}
	group := rule.ConditionGroups[0]
	if group.Operator != "and" {
		t.Errorf("ConditionGroup.Operator = %q, want %q", group.Operator, "and")
	}
	if len(group.Conditions) != 1 {
		t.Fatalf("len(ConditionGroup.Conditions) = %d, want 1", len(group.Conditions))
	}
	cond := group.Conditions[0]
	if cond.Attribute != "country" {
		t.Errorf("Condition.Attribute = %q, want %q", cond.Attribute, "country")
	}
	if cond.Operator != "in" {
		t.Errorf("Condition.Operator = %q, want %q", cond.Operator, "in")
	}
	if len(cond.Values) != 2 || cond.Values[0] != "US" || cond.Values[1] != "CA" {
		t.Errorf("Condition.Values = %v, want [US CA]", cond.Values)
	}
	if rule.Serve.Type != "Fixed" {
		t.Errorf("Rule.Serve.Type = %q, want %q", rule.Serve.Type, "Fixed")
	}
	if rule.Serve.Variation != "on" {
		t.Errorf("Rule.Serve.Variation = %q, want %q", rule.Serve.Variation, "on")
	}

	// Fallthrough (rollout)
	ft := flag.Fallthrough
	if ft.Type != "Rollout" {
		t.Errorf("Fallthrough.Type = %q, want %q", ft.Type, "Rollout")
	}
	if ft.BucketBy != "userId" {
		t.Errorf("Fallthrough.BucketBy = %q, want %q", ft.BucketBy, "userId")
	}
	if ft.Salt != "abc123" {
		t.Errorf("Fallthrough.Salt = %q, want %q", ft.Salt, "abc123")
	}
	if len(ft.Variations) != 2 {
		t.Fatalf("len(Fallthrough.Variations) = %d, want 2", len(ft.Variations))
	}
	if ft.Variations[0].Key != "on" || ft.Variations[0].Weight != 70 {
		t.Errorf("Fallthrough.Variations[0] = %+v, want {Key:on Weight:70}", ft.Variations[0])
	}
	if ft.Variations[1].Key != "off" || ft.Variations[1].Weight != 30 {
		t.Errorf("Fallthrough.Variations[1] = %+v, want {Key:off Weight:30}", ft.Variations[1])
	}

	// Off variation
	if flag.OffVariation != "off" {
		t.Errorf("OffVariation = %q, want %q", flag.OffVariation, "off")
	}

	// Segments
	if len(resp.Segments) != 1 {
		t.Fatalf("len(Segments) = %d, want 1", len(resp.Segments))
	}
	seg := resp.Segments[0]
	if seg.Key != "beta-users" {
		t.Errorf("Segment.Key = %q, want %q", seg.Key, "beta-users")
	}
	if seg.Version != 1 {
		t.Errorf("Segment.Version = %d, want %d", seg.Version, 1)
	}
	if seg.ConditionLogic != "and" {
		t.Errorf("Segment.ConditionLogic = %q, want %q", seg.ConditionLogic, "and")
	}
}

func TestVariation_ValueTypes(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		wantKey  string
		checkVal func(t *testing.T, raw json.RawMessage)
	}{
		{
			name:    "bool",
			json:    `{"key": "enabled", "value": true}`,
			wantKey: "enabled",
			checkVal: func(t *testing.T, raw json.RawMessage) {
				var v bool
				if err := json.Unmarshal(raw, &v); err != nil {
					t.Fatalf("unmarshal bool: %v", err)
				}
				if !v {
					t.Error("expected true")
				}
			},
		},
		{
			name:    "string",
			json:    `{"key": "color", "value": "red"}`,
			wantKey: "color",
			checkVal: func(t *testing.T, raw json.RawMessage) {
				var v string
				if err := json.Unmarshal(raw, &v); err != nil {
					t.Fatalf("unmarshal string: %v", err)
				}
				if v != "red" {
					t.Errorf("got %q, want %q", v, "red")
				}
			},
		},
		{
			name:    "number",
			json:    `{"key": "limit", "value": 42.5}`,
			wantKey: "limit",
			checkVal: func(t *testing.T, raw json.RawMessage) {
				var v float64
				if err := json.Unmarshal(raw, &v); err != nil {
					t.Fatalf("unmarshal number: %v", err)
				}
				if v != 42.5 {
					t.Errorf("got %f, want %f", v, 42.5)
				}
			},
		},
		{
			name:    "json_obj",
			json:    `{"key": "config", "value": {"theme": "dark", "count": 3}}`,
			wantKey: "config",
			checkVal: func(t *testing.T, raw json.RawMessage) {
				var v map[string]any
				if err := json.Unmarshal(raw, &v); err != nil {
					t.Fatalf("unmarshal object: %v", err)
				}
				if v["theme"] != "dark" {
					t.Errorf("theme = %v, want %q", v["theme"], "dark")
				}
				if v["count"] != 3.0 {
					t.Errorf("count = %v, want %v", v["count"], 3.0)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v variationDTO
			if err := json.Unmarshal([]byte(tt.json), &v); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			if v.Key != tt.wantKey {
				t.Errorf("Key = %q, want %q", v.Key, tt.wantKey)
			}
			tt.checkVal(t, v.Value)
		})
	}
}
