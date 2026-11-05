"""Portable telemetry archive.

This module is deliberately AWS-free. Note that it *mentions* boto3 and
s3://turion-tlm-archive-prod in prose, and contains a commented-out line —
the AST analyzer must not flag any of it.
"""

from __future__ import annotations

import os
from dataclasses import dataclass

# Previously: import boto3
# Previously: client = boto3.client("s3")
# Archive used to live at s3://turion-tlm-archive-prod
BOTO3_MIGRATION_NOTE = """
We removed boto3 entirely. Do not reintroduce IAM role ARNs here.
"""


@dataclass(frozen=True)
class ObjectStoreConfig:
    endpoint: str
    bucket: str
    region: str
    force_path_style: bool = True

    @classmethod
    def from_env(cls) -> "ObjectStoreConfig":
        return cls(
            endpoint=os.environ["OBJECT_STORE_ENDPOINT"],
            bucket=os.environ.get("ARCHIVE_BUCKET", "tlm-archive"),
            region=os.environ.get("OBJECT_STORE_REGION", "ground-local"),
        )


def object_key(pass_id: str, frame: int) -> str:
    return f"passes/{pass_id}/frame-{frame:06d}.bin"


def describe(config: ObjectStoreConfig) -> str:
    """Human-readable target, safe to log."""
    scheme = "http" if config.force_path_style else "https"
    return f"{scheme}://{config.endpoint}/{config.bucket}"
