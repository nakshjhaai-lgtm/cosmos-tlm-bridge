// Portable ground UI client: plain HTTP, no @aws-sdk imports.
const GROUND_API = "/api";

export async function fetchFrames(passId: string) {
  const res = await fetch(`${GROUND_API}/passes/${passId}/frames`);
  return res.json();
}
