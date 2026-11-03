provider "aws" {
  region = "us-west-2"
}

resource "aws_s3_bucket" "archive" {
  bucket = "turion-tlm-archive-prod"
}

resource "aws_batch_compute_environment" "pass_compute" {
  compute_environment_name = "droid-pass-compute"
  type                     = "MANAGED"
}

resource "aws_lambda_function" "enrich" {
  function_name = "droid-telemetry-enrich"
  role          = "arn:aws:iam::123456789012:role/lambda-enrich"
  runtime       = "python3.11"
}

resource "aws_iam_role" "etl_runner" {
  name = "droid-etl-runner"
}
