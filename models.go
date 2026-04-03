package featureflip

import "encoding/json"

type getFlagsResponse struct {
	Environment string       `json:"environment"`
	Version     int          `json:"version"`
	Flags       []flagDTO    `json:"flags"`
	Segments    []segmentDTO `json:"segments"`
}

type flagDTO struct {
	Key          string         `json:"key"`
	Version      int            `json:"version"`
	Type         string         `json:"type"`
	Enabled      bool           `json:"enabled"`
	Variations   []variationDTO `json:"variations"`
	Rules        []ruleDTO      `json:"rules"`
	Fallthrough  serveConfig    `json:"fallthrough"`
	OffVariation string         `json:"offVariation"`
}

type variationDTO struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

type ruleDTO struct {
	ID              string           `json:"id"`
	Priority        int              `json:"priority"`
	ConditionGroups []conditionGroup `json:"conditionGroups"`
	Serve           serveConfig      `json:"serve"`
	SegmentKey      string           `json:"segmentKey,omitempty"`
}

type conditionGroup struct {
	Operator   string      `json:"operator"`
	Conditions []condition `json:"conditions"`
}

type condition struct {
	Attribute string   `json:"attribute"`
	Operator  string   `json:"operator"`
	Values    []string `json:"values"`
	Negate    bool     `json:"negate"`
}

type serveConfig struct {
	Type       string              `json:"type"`
	Variation  string              `json:"variation,omitempty"`
	BucketBy   string              `json:"bucketBy,omitempty"`
	Salt       string              `json:"salt,omitempty"`
	Variations []weightedVariation `json:"variations,omitempty"`
}

type weightedVariation struct {
	Key    string `json:"key"`
	Weight int    `json:"weight"`
}

type segmentDTO struct {
	Key            string      `json:"key"`
	Version        int         `json:"version"`
	Conditions     []condition `json:"conditions"`
	ConditionLogic string      `json:"conditionLogic"`
}

// EvaluationContext provides attributes for flag evaluation.
type EvaluationContext struct {
	UserID     string
	Attributes map[string]any
}

// EvaluationReason describes why a particular flag value was returned.
type EvaluationReason string

const (
	ReasonRuleMatch    EvaluationReason = "RuleMatch"
	ReasonFallthrough  EvaluationReason = "Fallthrough"
	ReasonFlagDisabled EvaluationReason = "FlagDisabled"
	ReasonFlagNotFound EvaluationReason = "FlagNotFound"
	ReasonError        EvaluationReason = "Error"
)

// EvaluationDetail contains the result of a flag evaluation with metadata.
type EvaluationDetail struct {
	Value     any
	Variation string
	Reason    EvaluationReason
	RuleID    string
}

type sdkEvent struct {
	Type      string         `json:"type"`
	FlagKey   string         `json:"flagKey"`
	UserID    string         `json:"userId,omitempty"`
	Variation string         `json:"variation,omitempty"`
	Timestamp string         `json:"timestamp"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type recordEventsRequest struct {
	Events []sdkEvent `json:"events"`
}

type streamEvent struct {
	Key     string `json:"key"`
	Version int    `json:"version"`
}
