// Package cosmostlm is the Go half of cosmos-tlm-bridge: strongly typed
// structs for OpenC3 COSMOS v5 telemetry, a keyless JSON-RPC client, and a
// bridge that forwards decommutated frames to downstream microservices.
//
// The structs mirror the Pydantic models in cosmos_tlm_bridge.models exactly,
// so Python and Go services consume the identical wire contract.
package cosmostlm

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ValueType is an OpenC3 telemetry value type. ingenium-lib-cosmos maps the
// DN/EU vocabulary onto these: DN -> RAW, EU -> CONVERTED.
type ValueType string

const (
	ValueRaw       ValueType = "RAW"
	ValueConverted ValueType = "CONVERTED"
	ValueFormatted ValueType = "FORMATTED"
)

// LimitsColor is the roll-up colour, matching get_overall_limits_state.
type LimitsColor string

const (
	ColorGreen  LimitsColor = "GREEN"
	ColorYellow LimitsColor = "YELLOW"
	ColorRed    LimitsColor = "RED"
	ColorBlue   LimitsColor = "BLUE"
)

// LimitsState is a granular COSMOS limits state.
type LimitsState string

const (
	StateRedLow     LimitsState = "RED_LOW"
	StateYellowLow  LimitsState = "YELLOW_LOW"
	StateGreen      LimitsState = "GREEN"
	StateBlue       LimitsState = "BLUE"
	StateYellowHigh LimitsState = "YELLOW_HIGH"
	StateRedHigh    LimitsState = "RED_HIGH"
	StateUnknown    LimitsState = "UNKNOWN"
)

// Color collapses a granular state to its COSMOS colour.
func (s LimitsState) Color() LimitsColor {
	switch s {
	case StateRedLow, StateRedHigh:
		return ColorRed
	case StateYellowLow, StateYellowHigh:
		return ColorYellow
	case StateBlue:
		return ColorBlue
	case StateGreen, StateUnknown:
		return ColorGreen
	}
	return ColorGreen
}

// IsViolation reports whether the state is a yellow or red limit violation.
func (s LimitsState) IsViolation() bool {
	switch s {
	case StateRedLow, StateRedHigh, StateYellowLow, StateYellowHigh:
		return true
	}
	return false
}

// ParseLimitsState accepts "YELLOW_HIGH", "yellow high", "YELLOW", "RED", ...
func ParseLimitsState(token string) LimitsState {
	key := strings.ToUpper(strings.TrimSpace(token))
	key = strings.NewReplacer(" ", "_", "-", "_").Replace(key)
	switch key {
	case "YELLOW", "WARN", "WARNING":
		return StateYellowHigh
	case "RED", "ALARM", "CRITICAL":
		return StateRedHigh
	case "GREEN", "NOMINAL", "OK":
		return StateGreen
	case "BLUE", "OPERATIONAL":
		return StateBlue
	case "UNKNOWN", "NONE", "DISABLED":
		return StateUnknown
	}
	return LimitsState(key)
}

// Limits holds a COSMOS LIMITS declaration expressed in EU.
type Limits struct {
	LimitsSet   string   `json:"limits_set"`
	Persistence int      `json:"persistence"`
	Enabled     bool     `json:"enabled"`
	RedLow      *float64 `json:"red_low"`
	YellowLow   *float64 `json:"yellow_low"`
	YellowHigh  *float64 `json:"yellow_high"`
	RedHigh     *float64 `json:"red_high"`
	GreenLow    *float64 `json:"green_low"`
	GreenHigh   *float64 `json:"green_high"`
}

// Evaluate applies COSMOS limits precedence to an EU value.
func (l *Limits) Evaluate(eu float64) LimitsState {
	if !l.Enabled {
		return StateUnknown
	}
	if l.RedHigh != nil && eu >= *l.RedHigh {
		return StateRedHigh
	}
	if l.RedLow != nil && eu <= *l.RedLow {
		return StateRedLow
	}
	if l.YellowHigh != nil && eu >= *l.YellowHigh {
		return StateYellowHigh
	}
	if l.YellowLow != nil && eu <= *l.YellowLow {
		return StateYellowLow
	}
	if l.GreenLow != nil && l.GreenHigh != nil && eu >= *l.GreenLow && eu <= *l.GreenHigh {
		return StateBlue
	}
	return StateGreen
}

// TelemetryPoint is one decommutated point in both DN and EU representation.
type TelemetryPoint struct {
	Channel     string      `json:"channel"`
	Target      string      `json:"target"`
	Packet      string      `json:"packet"`
	Name        string      `json:"name"`
	DN          float64     `json:"dn"`
	EU          float64     `json:"eu"`
	Formatted   string      `json:"formatted"`
	Units       string      `json:"units,omitempty"`
	LimitsState LimitsState `json:"limits_state"`
	LimitsColor LimitsColor `json:"limits_color"`
	LimitsSet   string      `json:"limits_set,omitempty"`
	OutOfLimits bool        `json:"out_of_limits"`
	Limits      *Limits     `json:"limits,omitempty"`
}

// Value returns the point in the requested representation.
func (p TelemetryPoint) Value(vt ValueType) (float64, error) {
	switch vt {
	case ValueRaw:
		return p.DN, nil
	case ValueConverted:
		return p.EU, nil
	default:
		return 0, fmt.Errorf("cosmostlm: %s has no numeric value; use Formatted", vt)
	}
}

// TelemetryFrame is a fully typed, decommutated packet.
type TelemetryFrame struct {
	SchemaVersion string                    `json:"schema_version"`
	Scope         string                    `json:"scope"`
	Target        string                    `json:"target"`
	Packet        string                    `json:"packet"`
	Topic         string                    `json:"topic"`
	PacketTime    string                    `json:"packet_time"`
	ReceivedCount int                       `json:"received_count"`
	LimitsOverall LimitsColor               `json:"limits_overall"`
	CosmosOverall string                    `json:"cosmos_overall_limits_state"`
	LimitsSet     string                    `json:"limits_set"`
	Points        map[string]TelemetryPoint `json:"points"`
	LocalReceived time.Time                 `json:"-"`
}

// Point looks up a point by name, "PACKET__POINT", or "TARGET__PACKET__POINT".
func (f TelemetryFrame) Point(name string) (TelemetryPoint, error) {
	key := strings.ToUpper(name)
	if p, ok := f.Points[key]; ok {
		return p, nil
	}
	if parts := strings.Split(key, "__"); len(parts) > 1 {
		key = parts[len(parts)-1]
		if p, ok := f.Points[key]; ok {
			return p, nil
		}
	}
	return TelemetryPoint{}, fmt.Errorf(
		"cosmostlm: packet %s %s has no point %q", f.Target, f.Packet, name)
}

// LimitsSummary aggregates limits states across the frame, mirroring COSMOS
// get_overall_limits_state (which never returns BLUE).
type LimitsSummary struct {
	Overall    LimitsColor    `json:"overall"`
	Cosmos     string         `json:"cosmos_overall"`
	Counts     map[string]int `json:"counts"`
	Violations []string       `json:"violations"`
}

// Summary computes the limits roll-up for the frame.
func (f TelemetryFrame) Summary() LimitsSummary {
	order := map[LimitsColor]int{ColorGreen: 0, ColorBlue: 1, ColorYellow: 2, ColorRed: 3}
	s := LimitsSummary{Overall: ColorGreen, Counts: map[string]int{}, Violations: []string{}}
	for _, p := range f.Points {
		s.Counts[string(p.LimitsState)]++
		if order[p.LimitsColor] > order[s.Overall] {
			s.Overall = p.LimitsColor
		}
		if p.OutOfLimits {
			s.Violations = append(s.Violations, p.Channel+"="+string(p.LimitsState))
		}
	}
	s.Cosmos = "GREEN"
	if s.Overall != ColorBlue {
		s.Cosmos = string(s.Overall)
	}
	return s
}

// DiscoveredItem is the StreamingApi subscription key for one point, matching
// the OpenC3 format MODE__CMD|TLM__TARGET__PACKET__ITEM__VALUETYPE__REDUCEDTYPE.
func DiscoveredItem(target, packet, item string, vt ValueType) string {
	return fmt.Sprintf("DECOM__TLM__%s__%s__%s__%s__",
		strings.ToUpper(target), strings.ToUpper(packet), strings.ToUpper(item), vt)
}

// Channel builds the canonical TARGET__PACKET__POINT channel name.
func Channel(target, packet, point string) string {
	return strings.ToUpper(target + "__" + packet + "__" + point)
}

// SplitChannel parses TARGET__PACKET__POINT (exactly two __ delimiters, as
// ing_lib_cosmos.split_channel_name requires). A two-part PACKET__POINT form
// is accepted when defaultTarget is non-empty.
func SplitChannel(channel, defaultTarget string) (target, packet, point string, err error) {
	parts := strings.Split(strings.ToUpper(strings.TrimSpace(channel)), "__")
	switch {
	case len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != "":
		return parts[0], parts[1], parts[2], nil
	case len(parts) == 2 && parts[0] != "" && parts[1] != "" && defaultTarget != "":
		return strings.ToUpper(defaultTarget), parts[0], parts[1], nil
	}
	return "", "", "", fmt.Errorf(
		"cosmostlm: invalid channel %q: expected TARGET__PACKET__POINT", channel)
}

// streamEnvelope is one NDJSON line from /openc3-api/stream.
type streamEnvelope struct {
	Topic  string          `json:"topic"`
	Packet string          `json:"packet"`
	Time   int64           `json:"time"`
	Frame  json.RawMessage `json:"frame"`
}
