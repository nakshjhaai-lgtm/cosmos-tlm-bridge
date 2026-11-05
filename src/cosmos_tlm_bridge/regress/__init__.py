"""Declarative YAML acceptance suites and CI reporting."""

from .report import write_all, write_html, write_junit, write_json
from .runner import CaseResult, RegressionRunner, SuiteResult
from .schema import AssertionSpec, SuiteSpec, load_suite, load_suite_text

__all__ = [
    "RegressionRunner",
    "SuiteResult",
    "CaseResult",
    "SuiteSpec",
    "AssertionSpec",
    "load_suite",
    "load_suite_text",
    "write_all",
    "write_junit",
    "write_html",
    "write_json",
]
