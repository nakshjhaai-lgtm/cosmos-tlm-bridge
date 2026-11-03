package cosmostlm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// WebhookSink POSTs typed frames to a downstream microservice. It retries and
// never panics the stream.
type WebhookSink struct {
	URL     string
	Headers map[string]string
	Retries int
	Timeout time.Duration
	DryRun  bool
	Client  *http.Client
}

// NewWebhookSink builds a sink with sane defaults.
func NewWebhookSink(url string) *WebhookSink {
	return &WebhookSink{
		URL:     url,
		Retries: 3,
		Timeout: 5 * time.Second,
		Client:  &http.Client{Timeout: 5 * time.Second},
	}
}

// Send delivers one frame payload.
func (s *WebhookSink) Send(ctx context.Context, payload map[string]any) error {
	if s.DryRun {
		return nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	retries := s.Retries
	if retries <= 0 {
		retries = 1
	}
	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range s.Headers {
			req.Header.Set(k, v)
		}
		resp, err := s.Client.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("webhook %s returned HTTP %d", s.URL, resp.StatusCode)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
		}
	}
	return lastErr
}

// Stats are bridge runtime counters.
type Stats struct {
	Frames        int64
	Points        int64
	WebhookOK     int64
	WebhookFailed int64
	Violations    int64
	mu            sync.Mutex
	Errors        []string
}

// AddError records a non-fatal error.
func (s *Stats) AddError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Errors) < 50 {
		s.Errors = append(s.Errors, err.Error())
	}
}

// Snapshot returns a copy of the recorded errors.
func (s *Stats) Snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.Errors...)
}

// Bridge fans decommutated frames out to sinks and callbacks.
type Bridge struct {
	Sinks   []*WebhookSink
	OnFrame func(TelemetryFrame)
	OnAlarm func(TelemetryFrame, LimitsSummary)
	Stats   *Stats
}

// NewBridge constructs a bridge with the given sinks.
func NewBridge(sinks ...*WebhookSink) *Bridge {
	return &Bridge{Sinks: sinks, Stats: &Stats{}}
}

// Emit forwards one frame to every sink and invokes callbacks.
func (b *Bridge) Emit(ctx context.Context, frame TelemetryFrame) {
	b.Stats.Frames++
	b.Stats.Points += int64(len(frame.Points))
	summary := frame.Summary()
	if len(summary.Violations) > 0 {
		b.Stats.Violations++
	}
	payload := frameToPayload(frame)
	for _, sink := range b.Sinks {
		if err := sink.Send(ctx, payload); err != nil {
			b.Stats.WebhookFailed++
			b.Stats.AddError(err)
		} else {
			b.Stats.WebhookOK++
		}
	}
	if b.OnFrame != nil {
		b.OnFrame(frame)
	}
	if len(summary.Violations) > 0 && b.OnAlarm != nil {
		b.OnAlarm(frame, summary)
	}
}

func frameToPayload(frame TelemetryFrame) map[string]any {
	points := make(map[string]any, len(frame.Points))
	for name, p := range frame.Points {
		points[name] = map[string]any{
			"channel":      p.Channel,
			"dn":           p.DN,
			"eu":           p.EU,
			"units":        p.Units,
			"formatted":    p.Formatted,
			"limits_state": string(p.LimitsState),
			"limits_color": string(p.LimitsColor),
		}
	}
	return map[string]any{
		"schema_version":              frame.SchemaVersion,
		"scope":                       frame.Scope,
		"target":                      frame.Target,
		"packet":                      frame.Packet,
		"topic":                       frame.Topic,
		"packet_time":                 frame.PacketTime,
		"received_count":              frame.ReceivedCount,
		"limits_overall":              string(frame.LimitsOverall),
		"cosmos_overall_limits_state": frame.CosmosOverall,
		"limits_set":                  frame.LimitsSet,
		"points":                      points,
	}
}

// Run polls the requested packets at pollHz until duration elapses (0 = until
// ctx is cancelled) and emits typed frames.
func (b *Bridge) Run(ctx context.Context, c *Client, packets []string, pollHz float64, duration time.Duration) error {
	if pollHz <= 0 {
		pollHz = 4
	}
	ticker := time.NewTicker(time.Duration(float64(time.Second) / pollHz))
	defer ticker.Stop()

	var deadline <-chan time.Time
	if duration > 0 {
		t := time.NewTimer(duration)
		defer t.Stop()
		deadline = t.C
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return nil
		case <-ticker.C:
		}
		for _, packet := range packets {
			frame, err := ReadFrame(ctx, c, "", packet)
			if err != nil {
				b.Stats.AddError(err)
				continue
			}
			b.Emit(ctx, frame)
		}
	}
}

// ReadFrame reads one packet as a typed frame: RAW points come off the wire,
// EU is converted here, and the limits state is COSMOS' own evaluation.
func ReadFrame(ctx context.Context, c *Client, target, packet string) (TelemetryFrame, error) {
	if target == "" {
		target = "DROID_SIM"
	}
	// Two reads: RAW gives DN off the wire, CONVERTED gives the EU values and
	// COSMOS' own limits evaluation.
	euRows, err := c.GetTLMPacket(ctx, target, packet, ValueConverted)
	if err != nil {
		return TelemetryFrame{}, err
	}
	dnRows, err := c.GetTLMPacket(ctx, target, packet, ValueRaw)
	if err != nil {
		return TelemetryFrame{}, err
	}
	dn := make(map[string]float64, len(dnRows))
	for _, r := range dnRows {
		dn[r.Item] = r.Value
	}
	frame := TelemetryFrame{
		SchemaVersion: "1.0",
		Scope:         c.Scope(),
		Target:        target,
		Packet:        packet,
		Topic:         c.StreamTopic(target, packet),
		PacketTime:    time.Now().UTC().Format(time.RFC3339),
		Points:        make(map[string]TelemetryPoint, len(euRows)),
		LimitsSet:     "DEFAULT",
	}
	for _, row := range euRows {
		frame.Points[row.Item] = TelemetryPoint{
			Channel:     Channel(target, packet, row.Item),
			Target:      target,
			Packet:      packet,
			Name:        row.Item,
			EU:          row.Value,
			DN:          dn[row.Item],
			LimitsState: row.State,
			LimitsColor: row.State.Color(),
			OutOfLimits: row.State.IsViolation(),
		}
	}
	summary := frame.Summary()
	frame.LimitsOverall = summary.Overall
	frame.CosmosOverall = summary.Cosmos
	if n, err := c.GetTLMCnt(ctx, target, packet); err == nil {
		frame.ReceivedCount = int(n)
	}
	frame.LocalReceived = time.Now()
	return frame, nil
}
