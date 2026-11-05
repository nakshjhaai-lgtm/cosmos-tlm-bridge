// Frontend BFF calls — invokes Lambda directly from the ground UI.
import { LambdaClient, InvokeCommand } from "@aws-sdk/client-lambda";
import { S3Client, GetObjectCommand } from "@aws-sdk/client-s3";

const ARCHIVE_BUCKET = "turion-tlm-archive-prod";
const ENRICH_FN = "arn:aws:lambda:us-west-2:123456789012:function:droid-telemetry-enrich";

const lambda = new LambdaClient({ region: "us-west-2" });
const s3 = new S3Client({ region: "us-west-2" });

export async function enrichPacket(packet: unknown): Promise<unknown> {
  const res = await lambda.send(
    new InvokeCommand({
      FunctionName: ENRICH_FN,
      Payload: new TextEncoder().encode(JSON.stringify(packet)),
    })
  );
  return res.Payload;
}

export async function fetchArchive(passId: string) {
  return s3.send(
    new GetObjectCommand({
      Bucket: ARCHIVE_BUCKET,
      Key: `passes/${passId}/manifest.json`,
    })
  );
}
