"""Edge cases the analyzer must still handle."""
import os
import boto3


def dynamic_service():
    # Service name from the environment: still an SDK client.
    return boto3.client(os.environ["AWS_SERVICE"])


def aliased():
    import boto3 as aws
    return aws.client("dynamodb")


def from_import():
    from boto3.dynamodb.conditions import Key
    return Key("pk").eq("x")


def resource_form():
    return boto3.resource("s3").Bucket("turion-droid-ephemeris")


def nested_call():
    return boto3.client("sqs").send_message(
        QueueUrl="https://sqs.us-west-2.amazonaws.com/123456789012/ground",
        MessageBody="x",
    )
