// Typed downstream React consumer of a cosmos-tlm-bridge webhook frame.
// The bridge sends the same JSON shape to Go, Python and React services.
export interface TelemetryPoint {
  channel: string;       // DROID_SIM__THERMAL__RW_TEMP
  dn: number;            // raw Data Number
  eu: number;            // converted Engineering Unit
  units: string;
  limits_state: "GREEN" | "BLUE" | "YELLOW_LOW" | "YELLOW_HIGH" | "RED_LOW" | "RED_HIGH";
}

export interface TelemetryFrame {
  target: string;
  packet: "ADCS" | "EPS" | "THERMAL";
  points: Record<string, TelemetryPoint>;
  limits_overall: "GREEN" | "BLUE" | "YELLOW" | "RED";
}

export async function loadFrame(url = "/api/tlm"): Promise<TelemetryFrame> {
  const response = await fetch(url);
  if (!response.ok) throw new Error(`telemetry webhook returned ${response.status}`);
  return response.json() as Promise<TelemetryFrame>;
}

export function ThermalAlarm({ frame }: { frame: TelemetryFrame }) {
  const wheel = frame.points["DROID_SIM__THERMAL__RW_TEMP"];
  const hot = wheel?.limits_state === "YELLOW_HIGH" || wheel?.limits_state === "RED_HIGH";
  return (
    <output aria-live="polite" className={hot ? "alarm" : "nominal"}>
      {wheel.channel}: DN {wheel.dn} / EU {wheel.eu.toFixed(2)} {wheel.units} — {wheel.limits_state}
    </output>
  );
}
