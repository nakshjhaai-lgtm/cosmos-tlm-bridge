# React ground-service example

`TelemetryPanel.tsx` shows the consumer side of the bridge. It reads the webhook JSON emitted by `TelemetryBridge.to_webhook_payload()`. It does not call OpenC3 JSON-RPC and does not run Ruby Script Runner code.

Point `loadFrame()` at the ground service's `/api/tlm` endpoint. That endpoint can proxy the `telemetry-sink` container's `/health` or last-frame route.
