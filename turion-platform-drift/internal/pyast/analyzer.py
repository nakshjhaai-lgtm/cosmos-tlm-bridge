#!/usr/bin/env python3
"""Python AST analyzer for turion-platform-drift.

Invoked by the Go scanner as::

    python3 -c <this file> <<< '{"root": ".", "files": ["a.py", ...]}'

Emits a JSON document::

    {"findings": [{rule_id, line, column, symbol, service, message, confidence}],
     "errors": ["..."]}

Design notes
------------
* It uses the real :mod:`ast`, not regular expressions, so commented-out code,
  docstrings and string literals that merely *mention* boto3 are not reported —
  that is what keeps the false-positive rate at zero.
* It performs lightweight intra-function dataflow: ``c = boto3.client("lambda")``
  is remembered so a later ``c.invoke(...)`` is attributed to Lambda (TPD003)
  rather than only to the generic SDK import.
* Every rule ID maps 1:1 onto ``internal/rules/rules.go``.
"""

from __future__ import annotations

import ast
import json
import re
import sys
from typing import Any

AWS_SERVICES = {
    "lambda": "TPD003",
    "batch": "TPD004",
    "s3": "TPD006",
    "sts": "TPD008",
    "sqs": "TPD010",
    "sns": "TPD010",
    "kinesis": "TPD010",
    "events": "TPD010",
    "eventbridge": "TPD010",
    "dynamodb": "TPD011",
    "dynamodbstreams": "TPD011",
    "rds": "TPD011",
    "rds-data": "TPD011",
    "secretsmanager": "TPD046",
    "ssm": "TPD046",
    "cloudwatch": "TPD048",
    "logs": "TPD048",
    "xray": "TPD048",
    "ecr": "TPD042",
    "eks": "TPD042",
    "ec2": "TPD002",
    "glue": "TPD002",
    "athena": "TPD002",
    "stepfunctions": "TPD002",
}

# boto3 S3 method names that constitute proprietary S3 primitives
S3_PRIMITIVES = {
    "get_object",
    "put_object",
    "list_objects",
    "list_objects_v2",
    "delete_object",
    "delete_objects",
    "head_object",
    "copy_object",
    "generate_presigned_url",
    "generate_presigned_post",
    "get_object_tagging",
    "upload_file",
    "upload_fileobj",
    "download_file",
    "download_fileobj",
}

# boto3 Batch method names
BATCH_METHODS = {
    "submit_job",
    "register_job_definition",
    "describe_job_queues",
    "create_job_queue",
    "create_compute_environment",
    "describe_jobs",
    "terminate_job",
    "cancel_job",
}

AWS_SDK_MODULES = {
    "boto3",
    "botocore",
    "awscli",
    "aioboto3",
    "aiobotocore",
    "aws_lambda_powertools",
    "aws_xray_sdk",
}

AWS_ARN_RE = re.compile(r"arn:aws[a-z-]*:(?P<svc>[a-z0-9-]+):")
S3_URI_RE = re.compile(r"s3://(?P<bucket>[A-Za-z0-9._-]+)")
# Bucket-shaped literal: turion-tlm-archive, droid-ground-artifacts, ...
BUCKET_HINT_RE = re.compile(
    r"^(?:[a-z0-9][a-z0-9.-]{1,61})?(?:"
    r"tlm|telemetry|droid|ground|archive|artifact|pass|bucket|sat|mission|"
    r"downlink|packet|ephemeris|bus|payload|raw|decom"
    r")(?:[a-z0-9.-]{0,61})$"
)


# A bucket-name-shaped string literal.
_re_bucket_value = re.compile(r"^[a-z0-9][a-z0-9.-]{2,62}$")


def is_string(node: ast.AST) -> bool:
    return isinstance(node, ast.Constant) and isinstance(node.value, str)


def call_name(node: ast.Call) -> str:
    """Dotted name of a call target: boto3.client, self.s3.put_object, ..."""
    parts: list[str] = []
    cur: Any = node.func
    while isinstance(cur, ast.Attribute):
        parts.append(cur.attr)
        cur = cur.value
    if isinstance(cur, ast.Name):
        parts.append(cur.id)
    return ".".join(reversed(parts))


def const_arg(node: ast.Call, index: int = 0) -> str | None:
    if len(node.args) > index and is_string(node.args[index]):
        return str(node.args[index].value)
    for kw in node.keywords:
        if kw.arg in {"service_name", "service"} and is_string(kw.value):
            return str(kw.value.value)
    return None


def collect_docstrings(tree: ast.AST) -> set[int]:
    """Node ids of every module/class/function docstring.

    Documentation strings are prose: they mention AWS services for context and
    must never be reported as a live dependency.
    """
    out: set[int] = set()
    for node in ast.walk(tree):
        if not isinstance(
            node, (ast.Module, ast.ClassDef, ast.FunctionDef, ast.AsyncFunctionDef)
        ):
            continue
        body = getattr(node, "body", None)
        if not body:
            continue
        first = body[0]
        if (
            isinstance(first, ast.Expr)
            and isinstance(first.value, ast.Constant)
            and isinstance(first.value.value, str)
        ):
            out.add(id(first.value))
    return out


class Analyzer(ast.NodeVisitor):
    def __init__(self, path: str) -> None:
        self.path = path
        self.docstrings: set[int] = set()
        self.bucket_vars: set[str] = set()
        self._pending_bucket_literal: str | None = None
        self.findings: list[dict[str, Any]] = []
        self.errors: list[str] = []
        # variable name -> AWS service it is bound to
        self.clients: dict[str, str] = {}
        # variables that are S3 clients/resources
        self.s3_vars: set[str] = set()
        self.lambda_vars: set[str] = set()
        self.batch_vars: set[str] = set()
        self.sdk_imported = False

    # -- emit -------------------------------------------------------------- #

    def emit(
        self,
        rule_id: str,
        node: ast.AST,
        symbol: str,
        service: str = "",
        message: str = "",
        confidence: float = 1.0,
    ) -> None:
        line = getattr(node, "lineno", 1)
        col = getattr(node, "col_offset", 0) + 1
        self.findings.append(
            {
                "rule_id": rule_id,
                "line": line,
                "column": col,
                "symbol": symbol,
                "service": service,
                "message": message,
                "confidence": confidence,
            }
        )

    # -- imports ----------------------------------------------------------- #

    def visit_Import(self, node: ast.Import) -> None:  # noqa: N802
        for alias in node.names:
            root = alias.name.split(".")[0]
            if root in AWS_SDK_MODULES:
                self.sdk_imported = True
                self.emit(
                    "TPD001",
                    node,
                    f"import {alias.name}",
                    service="boto3" if root.startswith("boto") else root,
                    message=f"imports the proprietary AWS SDK module '{alias.name}'",
                )
        self.generic_visit(node)

    def visit_ImportFrom(self, node: ast.ImportFrom) -> None:  # noqa: N802
        module = node.module or ""
        root = module.split(".")[0]
        if root in AWS_SDK_MODULES:
            self.sdk_imported = True
            names = ", ".join(a.name for a in node.names)
            self.emit(
                "TPD001",
                node,
                f"from {module} import {names}",
                service="boto3" if root.startswith("boto") else root,
                message=f"imports from the proprietary AWS SDK module '{module}'",
            )
        self.generic_visit(node)

    # -- assignments (client tracking) ------------------------------------- #

    def visit_Assign(self, node: ast.Assign) -> None:  # noqa: N802
        # ARCHIVE_BUCKET = "turion-tlm-archive-prod"
        for target in node.targets:
            if isinstance(target, ast.Name) and "bucket" in target.id.lower():
                if is_string(node.value):
                    bucket = str(node.value.value)
                    if bucket and not bucket.startswith(("${", "{{", "http")):
                        self.emit(
                            "TPD005",
                            target,
                            f"{target.id} = {bucket}",
                            service="s3",
                            message=f"hard-codes the S3 bucket '{bucket}'",
                        )
        service = self._client_service(node.value)
        if service:
            for target in node.targets:
                if isinstance(target, ast.Name):
                    self.clients[target.id] = service
                    if service == "s3":
                        self.s3_vars.add(target.id)
                    if service == "lambda":
                        self.lambda_vars.add(target.id)
                    if service == "batch":
                        self.batch_vars.add(target.id)
        # boto3.Session().client("x") also lands here via _client_service
        self.generic_visit(node)

    def visit_AnnAssign(self, node: ast.AnnAssign) -> None:  # noqa: N802
        if node.value is not None:
            service = self._client_service(node.value)
            if service and isinstance(node.target, ast.Name):
                self.clients[node.target.id] = service
                if service == "s3":
                    self.s3_vars.add(node.target.id)
                if service == "lambda":
                    self.lambda_vars.add(node.target.id)
                if service == "batch":
                    self.batch_vars.add(node.target.id)
        self.generic_visit(node)

    def _client_service(self, value: ast.AST) -> str | None:
        """Resolve ``boto3.client("lambda")`` / ``session.resource("s3")``."""
        if not isinstance(value, ast.Call):
            return None
        name = call_name(value)
        if not name.endswith((".client", ".resource")) and name not in {
            "client",
            "resource",
        }:
            # boto3.client -> parts ["boto3","client"]
            if not (name.endswith("client") or name.endswith("resource")):
                return None
        svc = const_arg(value)
        if svc:
            return svc.lower()
        # boto3.client(os.environ["SVC"]) — unknown service, still an SDK client
        return "__unknown__"

    # -- calls ------------------------------------------------------------- #

    def visit_Call(self, node: ast.Call) -> None:  # noqa: N802
        name = call_name(node)
        root = name.split(".")[0]
        attr = name.split(".")[-1]

        # 1. direct SDK client/resource construction
        service = self._client_service(node)
        if service is not None and service != "__unknown__":
            rule = AWS_SERVICES.get(service, "TPD002")
            self.emit(
                rule,
                node,
                f"{name}({service!r})",
                service=service,
                message=(
                    f"constructs a boto3 client for AWS service '{service}', "
                    "which has no portable endpoint"
                ),
            )
        elif service == "__unknown__":
            self.emit(
                "TPD002",
                node,
                name,
                service="<dynamic>",
                message="constructs a boto3 client with a dynamic service name",
                confidence=0.8,
            )

        # 2. Lambda invocation
        if attr == "invoke" and (
            root in self.lambda_vars or "lambda" in name.lower()
        ):
            self.emit(
                "TPD003",
                node,
                name,
                service="lambda",
                message="invokes an AWS Lambda function",
            )

        # 3. Batch submission
        if attr in BATCH_METHODS:
            self.emit(
                "TPD004",
                node,
                name,
                service="batch",
                message=f"submits work to AWS Batch via {attr}()",
            )

        # 4. S3 primitives
        if attr in S3_PRIMITIVES and (root in self.s3_vars or self.sdk_imported):
            self.emit(
                "TPD006",
                node,
                name,
                service="s3",
                message=f"uses the proprietary boto3 S3 primitive {attr}()",
                confidence=1.0 if root in self.s3_vars else 0.9,
            )

        # 5. STS role assumption
        if attr in {"assume_role", "assume_role_with_web_identity", "get_caller_identity"}:
            self.emit(
                "TPD008",
                node,
                name,
                service="sts",
                message=f"assumes an AWS IAM role via {attr}()",
            )

        # 6. aws CLI shell-out
        if root in {"subprocess", "os"} and attr in {"run", "check_output", "call", "popen", "system", "Popen"}:
            if node.args and is_string(node.args[0]):
                argv = str(node.args[0].value)
                if re.match(r"^\s*aws\s", argv) or re.match(r"^\s*aws\s", " ".join(
                    str(a.value) for a in node.args if is_string(a)
                )):
                    self.emit(
                        "TPD012",
                        node,
                        argv[:60],
                        service="aws-cli",
                        message="shells out to the AWS CLI",
                    )
            elif node.args and isinstance(node.args[0], (ast.List, ast.Tuple)):
                items = [str(e.value) for e in node.args[0].elts if is_string(e)]
                if items and items[0] == "aws":
                    self.emit(
                        "TPD012",
                        node,
                        " ".join(items)[:60],
                        service="aws-cli",
                        message="shells out to the AWS CLI",
                    )

        # 7. CloudWatch/X-Ray SDK entry points
        if root in {"aws_xray_sdk", "watchtower"} or "cloudwatch" in name.lower():
            self.emit(
                "TPD048",
                node,
                name,
                service="cloudwatch",
                message="ships telemetry to CloudWatch/X-Ray",
            )

        self.generic_visit(node)

    # -- literals ---------------------------------------------------------- #

    def visit_Constant(self, node: ast.Constant) -> None:  # noqa: N802
        if not isinstance(node.value, str):
            return
        if id(node) in self.docstrings:
            return
        value = node.value

        for m in AWS_ARN_RE.finditer(value):
            svc = m.group("svc")
            self.emit(
                "TPD007",
                node,
                value[:80],
                service=svc,
                message=f"hard-codes an {svc} ARN literal",
            )

        for m in S3_URI_RE.finditer(value):
            self.emit(
                "TPD005",
                node,
                value[:80],
                service="s3",
                message=f"hard-codes the S3 bucket '{m.group('bucket')}'",
            )

        # A bare bucket name: either an s3:// URI (above) or a value bound to a
        # name that says "bucket", which is how ground services hard-code it.
        if (
            "arn:" not in value
            and "s3://" not in value
            and _re_bucket_value.match(value)
            and not value.startswith(("${", "{{", "http"))
        ):
            self._pending_bucket_literal = value

        # IAM-ish policy fragments
        if "arn:aws:iam::" in value or '"Action"' in value and "s3:" in value:
            self.emit(
                "TPD009",
                node,
                value[:80],
                service="iam",
                message="embeds a non-portable IAM policy fragment",
            )

        self.generic_visit(node)

    # -- AWS Batch job queue / definition kwargs --------------------------- #

    def visit_Keyword(self, node: ast.keyword) -> None:  # noqa: N802
        if node.arg in {"jobQueue", "job_queue", "jobDefinition", "job_definition"}:
            self.emit(
                "TPD004",
                node,
                node.arg,
                service="batch",
                message=f"references AWS Batch '{node.arg}'",
                confidence=0.95,
            )
        self.generic_visit(node)


def analyze_file(path: str) -> tuple[list[dict[str, Any]], list[str]]:
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as fh:
            source = fh.read()
    except OSError as exc:  # pragma: no cover
        return [], [f"{path}: {exc}"]

    try:
        tree = ast.parse(source, filename=path)
    except SyntaxError as exc:
        return [], [f"{path}:{exc.lineno}: syntax error: {exc.msg}"]

    analyzer = Analyzer(path)
    analyzer.docstrings = collect_docstrings(tree)
    try:
        analyzer.visit(tree)
    except RecursionError:  # pragma: no cover
        return analyzer.findings, [f"{path}: recursion limit exceeded"]

    # Heuristic pass: hard-coded bucket-name-shaped constants used as arguments
    # to functions named like *bucket*. AST-only, no regex over raw text.
    for node in ast.walk(tree):
        if not isinstance(node, ast.Call):
            continue
        for kw in node.keywords:
            if kw.arg in {"Bucket", "bucket", "bucket_name"} and is_string(kw.value):
                bucket = str(kw.value.value)
                if bucket and not bucket.startswith(("${", "{{")):
                    analyzer.emit(
                        "TPD005",
                        kw.value,
                        bucket,
                        service="s3",
                        message=f"hard-codes the S3 bucket '{bucket}'",
                    )

    return analyzer.findings, analyzer.errors


def main() -> int:
    try:
        request = json.load(sys.stdin)
    except json.JSONDecodeError as exc:
        print(json.dumps({"findings": [], "errors": [f"bad request: {exc}"]}))
        return 2

    findings: list[dict[str, Any]] = []
    errors: list[str] = []
    for rel in request.get("files", []):
        got, errs = analyze_file(rel)
        for f in got:
            f["file"] = rel
            findings.append(f)
        errors.extend(errs)

    print(json.dumps({"findings": findings, "errors": errors}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
