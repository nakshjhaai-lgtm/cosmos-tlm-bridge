"""Telemetry archive writer — S3 primitives bound to one AWS account."""

import boto3
from botocore.config import Config

ARCHIVE_BUCKET = "turion-tlm-archive-prod"
RAW_BUCKET = "turion-droid-downlink-raw"


def _client():
    return boto3.client("s3", config=Config(retries={"max_attempts": 10}))


def write_frame(pass_id: str, frame_index: int, payload: bytes) -> str:
    key = f"passes/{pass_id}/frame-{frame_index:06d}.bin"
    _client().put_object(Bucket=ARCHIVE_BUCKET, Key=key, Body=payload)
    return f"s3://{ARCHIVE_BUCKET}/{key}"


def read_raw(pass_id: str, frame_index: int) -> bytes:
    key = f"raw/{pass_id}/frame-{frame_index:06d}.bin"
    obj = _client().get_object(Bucket=RAW_BUCKET, Key=key)
    return obj["Body"].read()


def list_pass_frames(pass_id: str) -> list[str]:
    response = _client().list_objects_v2(
        Bucket=ARCHIVE_BUCKET, Prefix=f"passes/{pass_id}/"
    )
    return [item["Key"] for item in response.get("Contents", [])]


def download_link(pass_id: str) -> str:
    return _client().generate_presigned_url(
        "get_object",
        Params={"Bucket": ARCHIVE_BUCKET, "Key": f"passes/{pass_id}/manifest.json"},
        ExpiresIn=3600,
    )


def store_telemetry_point(channel: str, value: float) -> None:
    """DynamoDB for the live telemetry index."""
    table = boto3.resource("dynamodb").Table("droid-telemetry-index")
    table.put_item(Item={"channel": channel, "value": str(value)})


def read_pass_secret(pass_id: str) -> str:
    """Decryption key from Secrets Manager."""
    client = boto3.client("secretsmanager")
    return client.get_secret_value(SecretId=f"ground/passes/{pass_id}")["SecretString"]


def queue_for_reprocessing(pass_id: str) -> None:
    boto3.client("sqs").send_message(
        QueueUrl="https://sqs.us-west-2.amazonaws.com/123456789012/droid-reprocess",
        MessageBody=pass_id,
    )
