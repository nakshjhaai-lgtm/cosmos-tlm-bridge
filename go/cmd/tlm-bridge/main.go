// Command tlm-bridge is the typed Go microservice half of cosmos-tlm-bridge.
//
// It decommutates an OpenC3 COSMOS v5 telemetry stream into Go structs and
// forwards frames to downstream microservice webhooks.
//
//	tlm-bridge -cosmos http://localhost:2900 -packets ADCS,EPS,THERMAL \
//	           -webhook http://localhost:8080/tlm -duration 10s
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	cosmostlm "github.com/turion-space/cosmos-tlm-bridge/go"
)

func main() {
	var (
		cosmos  = flag.String("cosmos", envOr("COSMOS_URL", "http://localhost:2900"), "COSMOS base URL")
		scope   = flag.String("scope", envOr("COSMOS_SCOPE", "DEFAULT"), "COSMOS scope")
		target  = flag.String("target", "DROID_SIM", "telemetry target")
		packets = flag.String("packets", "ADCS,EPS,THERMAL", "comma-separated packets")
		webhook = flag.String("webhook", "", "downstream microservice webhook URL")
		pollHz  = flag.Float64("poll-hz", 4, "polling rate")
		dur     = flag.Duration("duration", 10*time.Second, "how long to bridge (0 = forever)")
		verify  = flag.String("verify", "", "one-shot DN/EU verification: TARGET__PACKET__POINT[,MODE]")
		jsn     = flag.Bool("json", false, "emit raw JSON frames")
		dryRun  = flag.Bool("dry-run", false, "do not actually POST to webhooks")
	)
	flag.Parse()

	client := cosmostlm.NewClient(cosmostlm.Config{
		BaseURL: *cosmos, Scope: *scope,
		AuthMode: envOr("COSMOS_AUTH_MODE", "keyless"),
	})
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := client.Login(ctx); err != nil {
		fail("login: %v", err)
	}

	// One-shot verification mode.
	if *verify != "" {
		os.Exit(runVerify(ctx, client, *target, *verify))
	}

	bridge := cosmostlm.NewBridge()
	if *webhook != "" {
		bridge.Sinks = append(bridge.Sinks, &cosmostlm.WebhookSink{
			URL: *webhook, Retries: 3, Timeout: 5 * time.Second,
			DryRun: *dryRun, Client: http.DefaultClient,
		})
	}
	bridge.OnFrame = func(f cosmostlm.TelemetryFrame) {
		if *jsn {
			b, _ := json.Marshal(f)
			fmt.Println(string(b))
			return
		}
		fmt.Printf("%-8s %-6s %s\n", f.Packet, f.LimitsOverall, f.Summary().Cosmos)
	}
	bridge.OnAlarm = func(f cosmostlm.TelemetryFrame, s cosmostlm.LimitsSummary) {
		fmt.Fprintf(os.Stderr, "!! %s limits: %s\n", f.Packet, strings.Join(s.Violations, ", "))
	}

	list := strings.Split(*packets, ",")
	for i := range list {
		list[i] = strings.TrimSpace(list[i])
	}
	fmt.Printf("bridging %s %v via %s (keyless, %d sink(s))\n",
		*target, list, client.BaseURL(), len(bridge.Sinks))

	if err := bridge.Run(ctx, client, list, *pollHz, *dur); err != nil && err != context.Canceled {
		fail("bridge: %v", err)
	}
	errs := bridge.Stats.Snapshot()
	fmt.Printf("frames=%d points=%d webhook_ok=%d webhook_failed=%d violations=%d\n",
		bridge.Stats.Frames, bridge.Stats.Points, bridge.Stats.WebhookOK,
		bridge.Stats.WebhookFailed, bridge.Stats.Violations)
	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "warnings: %v\n", errs[:min(len(errs), 3)])
	}
	if bridge.Stats.Frames == 0 {
		os.Exit(1)
	}
}

func runVerify(ctx context.Context, c *cosmostlm.Client, target, spec string) int {
	mode := cosmostlm.ValueConverted
	if i := strings.LastIndex(spec, ","); i >= 0 {
		v := strings.ToUpper(strings.TrimSpace(spec[i+1:]))
		if v == "DN" || v == "RAW" {
			mode = cosmostlm.ValueRaw
		}
		spec = spec[:i]
	}
	tgt, packet, point, err := cosmostlm.SplitChannel(spec, target)
	if err != nil {
		fail("%v", err)
	}
	dn, err := c.TLMFloat(ctx, tgt, packet, point, cosmostlm.ValueRaw)
	if err != nil {
		fail("dn read: %v", err)
	}
	eu, err := c.TLMFloat(ctx, tgt, packet, point, cosmostlm.ValueConverted)
	if err != nil {
		fail("eu read: %v", err)
	}
	frame, err := cosmostlm.ReadFrame(ctx, c, tgt, packet)
	if err != nil {
		fail("frame read: %v", err)
	}
	p, err := frame.Point(point)
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("%s  DN=%v  EU=%v  limits_state=%s  color=%s  (asserted in %s mode)\n",
		p.Channel, dn, eu, p.LimitsState, p.LimitsColor, string(mode))
	return 0
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "tlm-bridge: "+format+"\n", args...)
	os.Exit(2)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
