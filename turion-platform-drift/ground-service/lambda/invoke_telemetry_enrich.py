"""Invoke the telemetry-enrichment Lambda from the ground ETL."""

import json
import boto3

LAMBDA_FUNCTION = "arn:aws:lambda:us-west-2:123456789012:function:droid-telemetry-enrich"


def invoke_enrich(packet: dict) -> dict:
    client = boto3.client("lambda")
    response = client.invoke(
        FunctionName=LAMBDA_FUNCTION,
        InvocationType="RequestResponse",
        Payload=json.dumps(packet).encode(),
    )
    return json.loads(response["Payload"].read())


def publish_pass_event(document: dict) -> None:
    """Fan out to EventBridge + SNS after enrichment."""
    boto3.client("events").put_events(
        Entries=[{"Source": "turion.ground", "DetailType": "PassComplete",
                  "Detail": json.dumps(document)}]
    )
    boto3.client("sns").publish(
        TopicArn="arn:aws:sns:us-west-2:123456789012:droid-ground-events",
        Message=json.dumps(document),
    )
