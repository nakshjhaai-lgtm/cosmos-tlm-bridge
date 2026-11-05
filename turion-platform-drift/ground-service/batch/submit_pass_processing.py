"""Submit a DROID pass-processing job.

NOTE (turion-platform-drift): this module is the canonical example of the
AWS Batch coupling the linter targets. It must be ported before a ground
station can run off-AWS.
"""

from __future__ import annotations

import json
import os
import subprocess

import boto3
from botocore.exceptions import ClientError

JOB_QUEUE = "arn:aws:batch:us-west-2:123456789012:job-queue/droid-pass-processing"
JOB_DEFINITION = "arn:aws:batch:us-west-2:123456789012:job-definition/pass-processor:7"
ARCHIVE_BUCKET = "turion-tlm-archive-prod"
EPHEMERIS_BUCKET = "turion-droid-ephemeris"


def batch_client():
    """Build the Batch control-plane client (proprietary, AWS-only)."""
    session = boto3.Session(region_name="us-west-2")
    return session.client("batch")


def s3_client():
    return boto3.client("s3")


def assume_etl_role():
    sts = boto3.client("sts")
    return sts.assume_role(
        RoleArn="arn:aws:iam::123456789012:role/droid-etl-runner",
        RoleSessionName="pass-processing",
    )


def submit_pass_job(pass_id: str, packet_count: int) -> str:
    client = batch_client()
    response = client.submit_job(
        jobName=f"pass-{pass_id}",
        jobQueue=JOB_QUEUE,
        jobDefinition=JOB_DEFINITION,
        containerOverrides={
            "environment": [
                {"name": "PASS_ID", "value": pass_id},
                {"name": "PACKET_COUNT", "value": str(packet_count)},
                {"name": "OUTPUT_BUCKET", "value": ARCHIVE_BUCKET},
            ]
        },
    )
    return response["jobId"]


def stage_ephemeris(pass_id: str, local_path: str) -> None:
    """Upload an ephemeris file straight into a hard-coded S3 bucket."""
    client = s3_client()
    key = f"ephemeris/{pass_id}.oem"
    client.put_object(Bucket=EPHEMERIS_BUCKET, Key=key, Body=open(local_path, "rb"))
    print(f"staged s3://{EPHEMERIS_BUCKET}/{key}")


def describe_job(job_id: str) -> dict:
    try:
        return batch_client().describe_jobs(jobs=[job_id])["jobs"][0]
    except ClientError as exc:
        raise RuntimeError(f"batch describe failed: {exc}") from exc


def deploy_notification_rule() -> None:
    """A deploy-time AWS CLI shell-out."""
    subprocess.check_call(
        ["aws", "events", "put-rule", "--name", "droid-pass-complete",
         "--schedule-expression", "rate(5 minutes)"]
    )


if __name__ == "__main__":
    print(json.dumps({"submitted": submit_pass_job(os.environ["PASS_ID"], 42)}))
