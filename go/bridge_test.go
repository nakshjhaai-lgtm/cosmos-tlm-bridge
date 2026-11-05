package cosmostlm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebhookSinkDelivers(t *testing.T) {
	var got int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewWebhookSink(srv.URL)
	sink.Client = srv.Client()
	if err := sink.Send(context.Background(), map[string]any{"packet": "EPS"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got != 1 {
		t.Fatalf("server saw %d requests, want 1", got)
	}
}

func TestWebhookSinkRetriesThenFails(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sink := NewWebhookSink(srv.URL)
	sink.Client = srv.Client()
	sink.Retries = 3
	if err := sink.Send(context.Background(), map[string]any{}); err == nil {
		t.Fatal("expected an error after retries")
	}
	if hits != 3 {
		t.Fatalf("hits = %d, want 3", hits)
	}
}

func TestBridgeEmitCounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewWebhookSink(srv.URL)
	sink.Client = srv.Client()
	b := NewBridge(sink)

	var alarmed bool
	b.OnAlarm = func(TelemetryFrame, LimitsSummary) { alarmed = true }
	frame := TelemetryFrame{
		Target: "DROID_SIM", Packet: "THERMAL",
		Points: map[string]TelemetryPoint{
			"RW_TEMP": {Channel: "DROID_SIM__THERMAL__RW_TEMP", LimitsState: StateYellowHigh,
				LimitsColor: ColorYellow, OutOfLimits: true},
		},
	}
	b.Emit(context.Background(), frame)
	if b.Stats.Frames != 1 || b.Stats.WebhookOK != 1 || b.Stats.Violations != 1 {
		t.Fatalf("stats = %+v", b.Stats)
	}
	if !alarmed {
		t.Fatal("OnAlarm was not invoked")
	}
}

func TestClientRPCAgainstFakeServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openc3-api/api":
			w.Header().Set("Content-Type", "application/json")
			w.Write(req200)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := NewClient(Config{BaseURL: srv.URL, Scope: "DEFAULT", AuthMode: "keyless", HTTPClient: srv.Client()})
	v, err := c.TLMFloat(context.Background(), "DROID_SIM", "EPS", "BATT_VOLTAGE", ValueConverted)
	if err != nil {
		t.Fatalf("TLMFloat: %v", err)
	}
	if v != 15.6 {
		t.Fatalf("got %v, want 15.6", v)
	}
}

var req200 = []byte(`{"jsonrpc":"2.0","result":15.6,"id":1}`)
