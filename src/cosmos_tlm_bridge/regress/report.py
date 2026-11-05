"""JUnit XML and standalone HTML reporters for ``cosmos-regress``.

The HTML report is fully self-contained (inline CSS/SVG, no CDN fetches) so it
can be archived as a CI artifact and opened inside an air-gapped ground station.
"""

from __future__ import annotations

import html
import json
import time
import xml.etree.ElementTree as ET
from pathlib import Path
from typing import Iterable

from .runner import CaseResult, SuiteResult

__all__ = ["write_junit", "write_html", "write_json", "write_all"]

_STATUS_COLOR = {
    "passed": "#1f9d55",
    "failed": "#d64545",
    "error": "#c026a3",
    "skipped": "#8a94a6",
}


def write_junit(results: Iterable[SuiteResult], path: str | Path) -> Path:
    """Emit a JUnit XML file consumable by GitHub Actions, GitLab, and Jenkins."""
    results = list(results)
    suites_el = ET.Element("testsuites")
    total = failures = errors = skipped = 0
    total_time = 0.0

    for res in results:
        counts = res.counts
        total += res.total
        failures += counts.get("failed", 0)
        errors += counts.get("error", 0)
        skipped += counts.get("skipped", 0)
        total_time += res.duration_s

        suite_el = ET.SubElement(
            suites_el,
            "testsuite",
            {
                "name": res.suite,
                "tests": str(res.total),
                "failures": str(counts.get("failed", 0)),
                "errors": str(counts.get("error", 0) + len(res.errors)),
                "skipped": str(counts.get("skipped", 0)),
                "time": f"{res.duration_s:.3f}",
                "timestamp": time.strftime("%Y-%m-%dT%H:%M:%S", time.localtime(res.started_at)),
                "hostname": "cosmos-tlm-bridge",
            },
        )
        props = ET.SubElement(suite_el, "properties")
        for key, value in res.environment.items():
            ET.SubElement(props, "property", {"name": key, "value": str(value)})
        ET.SubElement(
            props, "property", {"name": "target", "value": res.target}
        )
        ET.SubElement(props, "property", {"name": "scope", "value": res.scope})

        for case in res.cases:
            case_el = ET.SubElement(
                suite_el,
                "testcase",
                {
                    "classname": f"{res.suite}.{case.kind}",
                    "name": case.name,
                    "time": f"{case.duration_s:.3f}",
                },
            )
            if case.status == "failed":
                ET.SubElement(case_el, "failure", {"message": case.message[:2000]}).text = (
                    case.stdout or case.message
                )
            elif case.status == "error":
                ET.SubElement(case_el, "error", {"message": case.message[:2000]}).text = case.message
            elif case.status == "skipped":
                ET.SubElement(case_el, "skipped", {"message": case.message})
            if case.details:
                ET.SubElement(case_el, "system-out").text = json.dumps(
                    case.details, indent=2, default=str
                )
        for err in res.errors:
            ET.SubElement(
                suite_el, "system-err"
            ).text = (ET.SubElement(suite_el, "system-err").text or "") + err

    suites_el.set(
        "name",
        "cosmos-tlm-bridge acceptance",
    )
    suites_el.set("tests", str(total))
    suites_el.set("failures", str(failures))
    suites_el.set("errors", str(errors))
    suites_el.set("skipped", str(skipped))
    suites_el.set("time", f"{total_time:.3f}")

    tree = ET.ElementTree(suites_el)
    ET.indent(tree, space="  ")
    out = Path(path)
    out.parent.mkdir(parents=True, exist_ok=True)
    tree.write(out, encoding="utf-8", xml_declaration=True)
    return out


def write_json(results: Iterable[SuiteResult], path: str | Path) -> Path:
    from dataclasses import asdict

    payload = []
    for res in results:
        doc = asdict(res)
        doc["passed"] = res.passed
        doc["counts"] = res.counts
        payload.append(doc)
    out = Path(path)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(payload, indent=2, default=str), encoding="utf-8")
    return out


def _bar(counts: dict[str, int], width: int = 260) -> str:
    total = max(sum(counts.values()), 1)
    x = 0.0
    parts: list[str] = []
    for status in ("passed", "failed", "error", "skipped"):
        n = counts.get(status, 0)
        if not n:
            continue
        w = width * n / total
        parts.append(
            f'<rect x="{x:.1f}" y="0" width="{w:.1f}" height="10" rx="5" '
            f'fill="{_STATUS_COLOR[status]}"/>'
        )
        x += w
    return (
        f'<svg width="{width}" height="10" viewBox="0 0 {width} 10" '
        f'role="img" aria-label="pass/fail distribution">{"".join(parts)}</svg>'
    )


def write_html(results: Iterable[SuiteResult], path: str | Path, *, title: str | None = None) -> Path:
    results = list(results)
    grand = {"passed": 0, "failed": 0, "error": 0, "skipped": 0}
    total_time = 0.0
    for r in results:
        for k, v in r.counts.items():
            grand[k] = grand.get(k, 0) + v
        total_time += r.duration_s
    ok = grand["failed"] + grand["error"] == 0 and all(r.passed for r in results)
    headline = "PASS" if ok else "FAIL"
    accent = "#1f9d55" if ok else "#d64545"

    rows: list[str] = []
    for res in results:
        cases_html = []
        for case in res.cases:
            color = _STATUS_COLOR.get(case.status, "#8a94a6")
            details = ""
            if case.details:
                details = (
                    '<details><summary>samples</summary><pre>'
                    + html.escape(json.dumps(case.details[-8:], indent=2, default=str))
                    + "</pre></details>"
                )
            cases_html.append(
                f"""<tr>
  <td><span class="pill" style="background:{color}">{html.escape(case.status.upper())}</span></td>
  <td class="name">{html.escape(case.name)}</td>
  <td class="kind">{html.escape(case.kind)}</td>
  <td class="num">{case.duration_s:.2f}s</td>
  <td class="msg">{html.escape(case.message)}{details}</td>
</tr>"""
            )
        env = " ".join(
            f'<span class="kv"><b>{html.escape(k)}</b>={html.escape(str(v))}</span>'
            for k, v in res.environment.items()
        )
        rows.append(
            f"""<section class="suite">
  <header>
    <h2>{html.escape(res.suite)}</h2>
    <div class="meta">{_bar(res.counts)} <b>{res.total}</b> cases &middot;
      {res.duration_s:.2f}s &middot; target <code>{html.escape(res.target)}</code>
      &middot; scope <code>{html.escape(res.scope)}</code></div>
    <div class="env">{env}</div>
  </header>
  <table>
    <thead><tr><th>Status</th><th>Case</th><th>Kind</th><th>Time</th><th>Detail</th></tr></thead>
    <tbody>{''.join(cases_html)}</tbody>
  </table>
</section>"""
        )

    doc = f"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{html.escape(title or 'cosmos-regress acceptance report')}</title>
<style>
  :root {{ color-scheme: light; }}
  * {{ box-sizing: border-box; }}
  body {{ margin:0; font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;
         color:#14181f; background:#f5f6f8; }}
  .wrap {{ max-width:1080px; margin:0 auto; padding:28px 22px 60px; }}
  .hero {{ background:#0e1420; color:#e8edf5; border-radius:14px; padding:22px 24px; margin-bottom:22px;
           display:flex; gap:22px; align-items:center; flex-wrap:wrap; }}
  .hero h1 {{ margin:0; font-size:19px; letter-spacing:.2px; font-weight:600; }}
  .hero .sub {{ color:#8f9bb0; font-size:12.5px; margin-top:3px; }}
  .verdict {{ margin-left:auto; font-weight:700; font-size:15px; letter-spacing:1.4px;
              border:2px solid {accent}; color:{accent}; padding:7px 16px; border-radius:999px; }}
  .kpis {{ display:flex; gap:12px; flex-wrap:wrap; margin-bottom:22px; }}
  .kpi {{ background:#fff; border:1px solid #e3e6ec; border-radius:11px; padding:12px 16px; min-width:104px; }}
  .kpi .v {{ font-size:21px; font-weight:700; }}
  .kpi .l {{ font-size:11px; text-transform:uppercase; letter-spacing:.7px; color:#69727f; }}
  .suite {{ background:#fff; border:1px solid #e3e6ec; border-radius:12px; margin-bottom:18px; overflow:hidden; }}
  .suite header {{ padding:15px 18px; border-bottom:1px solid #eef0f4; }}
  .suite h2 {{ margin:0 0 6px; font-size:15px; }}
  .meta {{ font-size:12.5px; color:#57606d; display:flex; align-items:center; gap:9px; flex-wrap:wrap; }}
  .env {{ margin-top:8px; font-size:11.5px; color:#7b8593; display:flex; gap:14px; flex-wrap:wrap; }}
  .kv b {{ color:#3d4552; }}
  table {{ width:100%; border-collapse:collapse; }}
  th {{ text-align:left; font-size:11px; letter-spacing:.6px; text-transform:uppercase; color:#7b8593;
        padding:9px 14px; background:#fafbfc; border-bottom:1px solid #eef0f4; }}
  td {{ padding:10px 14px; border-bottom:1px solid #f2f4f7; vertical-align:top; font-size:13px; }}
  tr:last-child td {{ border-bottom:none; }}
  .pill {{ color:#fff; font-size:10px; font-weight:700; letter-spacing:.5px; padding:3px 8px; border-radius:999px; }}
  .name {{ font-weight:550; max-width:330px; }}
  .kind {{ color:#69727f; font-size:12px; }}
  .num {{ color:#69727f; font-variant-numeric:tabular-nums; white-space:nowrap; }}
  .msg {{ color:#3d4552; }}
  code {{ background:#f1f3f6; padding:1px 5px; border-radius:4px; font-size:12px; }}
  pre {{ background:#0e1420; color:#d7dee9; padding:11px; border-radius:8px; overflow:auto;
         font-size:11.5px; margin:8px 0 2px; }}
  details summary {{ cursor:pointer; color:#3b6fd4; font-size:12px; margin-top:4px; }}
  footer {{ color:#8a94a6; font-size:11.5px; text-align:center; margin-top:26px; }}
</style>
</head>
<body>
<div class="wrap">
  <div class="hero">
    <div>
      <h1>{html.escape(title or 'cosmos-tlm-bridge &mdash; DROID acceptance report')}</h1>
      <div class="sub">OpenC3 COSMOS v5 &rarr; typed microservice bridge &middot; generated {time.strftime('%Y-%m-%d %H:%M:%S %Z')}</div>
    </div>
    <div class="verdict">{headline}</div>
  </div>

  <div class="kpis">
    <div class="kpi"><div class="v">{grand['passed']}</div><div class="l">Passed</div></div>
    <div class="kpi"><div class="v" style="color:#d64545">{grand['failed']}</div><div class="l">Failed</div></div>
    <div class="kpi"><div class="v" style="color:#c026a3">{grand['error']}</div><div class="l">Errors</div></div>
    <div class="kpi"><div class="v">{total_time:.1f}s</div><div class="l">Wall clock</div></div>
    <div class="kpi"><div class="v">{len(results)}</div><div class="l">Suites</div></div>
  </div>

  {''.join(rows)}

  <footer>cosmos-tlm-bridge &middot; keyless &middot; zero OpenC3 Script Runner scripts executed</footer>
</div>
</body>
</html>
"""
    out = Path(path)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(doc, encoding="utf-8")
    return out


def write_all(results: Iterable[SuiteResult], outdir: str | Path, stem: str = "droid_pass") -> dict[str, Path]:
    results = list(results)
    outdir = Path(outdir)
    return {
        "junit": write_junit(results, outdir / f"{stem}-junit.xml"),
        "html": write_html(results, outdir / f"{stem}-report.html"),
        "json": write_json(results, outdir / f"{stem}-results.json"),
    }
