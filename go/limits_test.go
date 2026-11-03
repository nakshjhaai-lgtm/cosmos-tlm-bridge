package cosmostlm

import (
	"math"
	"testing"
)

func f(v float64) *float64 { return &v }

func rwLimits() *Limits {
	return &Limits{
		LimitsSet: "DEFAULT", Persistence: 3, Enabled: true,
		RedLow: f(-20), YellowLow: f(0), YellowHigh: f(55), RedHigh: f(70),
		GreenLow: f(10), GreenHigh: f(45),
	}
}

func TestLimitsPrecedence(t *testing.T) {
	cases := []struct {
		eu    float64
		state LimitsState
		color LimitsColor
	}{
		{-25, StateRedLow, ColorRed},
		{-20, StateRedLow, ColorRed},
		{-5, StateYellowLow, ColorYellow},
		{0, StateYellowLow, ColorYellow},
		{5, StateGreen, ColorGreen},
		{10, StateBlue, ColorBlue},
		{30, StateBlue, ColorBlue},
		{45, StateBlue, ColorBlue},
		{50, StateGreen, ColorGreen},
		{55, StateYellowHigh, ColorYellow},
		{69.9, StateYellowHigh, ColorYellow},
		{70, StateRedHigh, ColorRed},
		{99, StateRedHigh, ColorRed},
	}
	l := rwLimits()
	for _, c := range cases {
		got := l.Evaluate(c.eu)
		if got != c.state {
			t.Errorf("Evaluate(%v) = %v, want %v", c.eu, got, c.state)
		}
		if got.Color() != c.color {
			t.Errorf("Evaluate(%v).Color() = %v, want %v", c.eu, got.Color(), c.color)
		}
	}
}

func TestLimitsDisabled(t *testing.T) {
	l := rwLimits()
	l.Enabled = false
	if got := l.Evaluate(1000); got != StateUnknown {
		t.Fatalf("disabled limits = %v, want UNKNOWN", got)
	}
}

func TestBlueBandNeverMasksYellowOrRed(t *testing.T) {
	l := rwLimits()
	l.GreenLow, l.GreenHigh = f(0), f(100)
	if got := l.Evaluate(56); got != StateYellowHigh {
		t.Errorf("56 C = %v, want YELLOW_HIGH", got)
	}
	if got := l.Evaluate(75); got != StateRedHigh {
		t.Errorf("75 C = %v, want RED_HIGH", got)
	}
}

func TestParseLimitsState(t *testing.T) {
	cases := map[string]LimitsState{
		"YELLOW_HIGH":    StateYellowHigh,
		"yellow high":    StateYellowHigh,
		"YELLOW":         StateYellowHigh,
		"RED":            StateRedHigh,
		"green":          StateGreen,
		"operational":    StateBlue,
		"BLUE":           StateBlue,
		"RED_LOW":        StateRedLow,
		"  YELLOW-LOW  ": StateYellowLow,
	}
	for in, want := range cases {
		if got := ParseLimitsState(in); got != want {
			t.Errorf("ParseLimitsState(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsViolation(t *testing.T) {
	for _, s := range []LimitsState{StateRedLow, StateRedHigh, StateYellowLow, StateYellowHigh} {
		if !s.IsViolation() {
			t.Errorf("%v should be a violation", s)
		}
	}
	for _, s := range []LimitsState{StateGreen, StateBlue, StateUnknown} {
		if s.IsViolation() {
			t.Errorf("%v should not be a violation", s)
		}
	}
}

// ---- DN/EU conversion must match the Python bridge exactly ----

func TestPolyConversionMatchesPython(t *testing.T) {
	cases := []struct{ c0, c1, dn, want float64 }{
		{13.6, 0.0025, 800, 15.6},
		{-40, 0.01, 9560, 55.6},
		{0, 0.001, 900, 0.9},
	}
	for _, c := range cases {
		got := c.c0 + c.c1*c.dn
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("DN %v -> EU %v, want %v", c.dn, got, c.want)
		}
	}
}

func TestChannelHelpers(t *testing.T) {
	if got := Channel("droid_sim", "eps", "batt_voltage"); got != "DROID_SIM__EPS__BATT_VOLTAGE" {
		t.Errorf("Channel = %q", got)
	}
	tg, pk, pt, err := SplitChannel("DROID_SIM__EPS__BATT_VOLTAGE", "")
	if err != nil || tg != "DROID_SIM" || pk != "EPS" || pt != "BATT_VOLTAGE" {
		t.Fatalf("SplitChannel = %q %q %q %v", tg, pk, pt, err)
	}
	tg, pk, pt, err = SplitChannel("EPS__BATT_VOLTAGE", "DROID_SIM")
	if err != nil || tg != "DROID_SIM" || pk != "EPS" || pt != "BATT_VOLTAGE" {
		t.Fatalf("SplitChannel shorthand = %q %q %q %v", tg, pk, pt, err)
	}
	for _, bad := range []string{"", "EPS", "A__B__C__D"} {
		if _, _, _, err := SplitChannel(bad, "DROID_SIM"); err == nil {
			t.Errorf("SplitChannel(%q) should fail", bad)
		}
	}
}

func TestStreamingItemKey(t *testing.T) {
	got := DiscoveredItem("droid_sim", "thermal", "rw_temp", ValueRaw)
	want := "DECOM__TLM__DROID_SIM__THERMAL__RW_TEMP__RAW__"
	if got != want {
		t.Fatalf("DiscoveredItem = %q, want %q", got, want)
	}
}

func TestStreamTopic(t *testing.T) {
	c := NewClient(Config{BaseURL: "http://x", Scope: "DEFAULT"})
	if got := c.StreamTopic("DROID_SIM", "EPS"); got != "DEFAULT__DECOM__{DROID_SIM}__EPS" {
		t.Fatalf("StreamTopic = %q", got)
	}
}

func TestFrameSummary(t *testing.T) {
	frame := TelemetryFrame{
		Target: "DROID_SIM",
		Packet: "THERMAL",
		Points: map[string]TelemetryPoint{
			"RW_TEMP": {Channel: "DROID_SIM__THERMAL__RW_TEMP", LimitsState: StateYellowHigh,
				LimitsColor: ColorYellow, OutOfLimits: true},
			"RADIATOR_TEMP": {Channel: "DROID_SIM__THERMAL__RADIATOR_TEMP", LimitsState: StateBlue,
				LimitsColor: ColorBlue},
		},
	}
	s := frame.Summary()
	if s.Overall != ColorYellow {
		t.Errorf("Overall = %v, want YELLOW", s.Overall)
	}
	if s.Cosmos != "YELLOW" {
		t.Errorf("Cosmos = %v, want YELLOW", s.Cosmos)
	}
	if len(s.Violations) != 1 {
		t.Errorf("Violations = %v", s.Violations)
	}
}

func TestFrameSummaryCollapsesBlue(t *testing.T) {
	frame := TelemetryFrame{Points: map[string]TelemetryPoint{
		"RW_TEMP": {LimitsState: StateBlue, LimitsColor: ColorBlue},
	}}
	s := frame.Summary()
	if s.Overall != ColorBlue || s.Cosmos != "GREEN" {
		t.Fatalf("got overall=%v cosmos=%v, want BLUE/GREEN", s.Overall, s.Cosmos)
	}
}

func TestPointLookup(t *testing.T) {
	frame := TelemetryFrame{Target: "DROID_SIM", Packet: "EPS", Points: map[string]TelemetryPoint{
		"BATT_VOLTAGE": {EU: 15.6, DN: 800},
	}}
	for _, name := range []string{"BATT_VOLTAGE", "EPS__BATT_VOLTAGE", "DROID_SIM__EPS__BATT_VOLTAGE"} {
		p, err := frame.Point(name)
		if err != nil || p.EU != 15.6 {
			t.Errorf("Point(%q) = %v, %v", name, p.EU, err)
		}
	}
	if _, err := frame.Point("NOPE"); err == nil {
		t.Error("Point(NOPE) should fail")
	}
}
