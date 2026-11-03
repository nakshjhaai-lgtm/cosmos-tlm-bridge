# React ground-microservice example

`TelemetryPanel.tsx` is the typed consumer side of the bridge. It consumes the
webhook JSON emitted by `TelemetryBridge.to_webhook_payload()`; it never speaks
OpenC3 JSON-RPC and never runs Ruby Script Runner code. Point `loadFrame()` at
the ground service's `/api/tlm` endpoint (which can proxy the `telemetry-sink`
container's `/health` or last-frame endpoint).
