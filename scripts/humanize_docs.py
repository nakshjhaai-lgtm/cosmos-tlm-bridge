#!/usr/bin/env python3
"""Apply the installed human-writing and humanizer guidance to repository prose.

The rewrite keeps commands, paths, inline code, fenced code, link targets, YAML,
and data files alone. It removes the formatting and vocabulary patterns that
Wikipedia's "Signs of AI writing" and the installed skills flag as mechanical.

Run from the repository root:

    .venv/bin/python scripts/humanize_docs.py
"""

from __future__ import annotations

import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

# Files rewritten by hand or kept exactly as part of the required replay.
SKIP_FILES = {
    "README.md",
    "docs/DELIVERY-VERIFICATION.md",
    "docs/DEFINITION-OF-DONE.md",
    "docs/demo-replay.txt",
    "docs/demo.cast",
    "docs/demo.gif",
}

SKIP_PARTS = {
    ".git",
    ".venv",
    ".claude",
    ".agents",
    "node_modules",
    "docs/evidence",
    "docs/original-delivery/evidence",
}

WORD_REPLACEMENTS: list[tuple[str, str]] = [
    (r"\butilize\b", "use"),
    (r"\bleverage\b", "use"),
    (r"\bin order to\b", "to"),
    (r"\ba number of\b", "several"),
    (r"\bthe table below\b", "the table"),
    (r"\bIt is important to note that\b", ""),
    (r"\bIt should be noted that\b", ""),
    (r"\bPlease note that\b", ""),
    (r"\bIn conclusion,?\s*", ""),
    (r"\bserves as\b", "is"),
    (r"\bstands as\b", "is"),
    (r"\bfunctions as\b", "is"),
    (r"\boperates as\b", "is"),
    (r"\brefers to\b", "means"),
    (r"\boffers\b", "has"),
    (r"\bboasts\b", "has"),
    (r"\bshowcases\b", "shows"),
    (r"\bshowcase\b", "show"),
    (r"\bshowcasing\b", "showing"),
    (r"\bdelves\b", "looks"),
    (r"\bdelve\b", "look"),
    (r"\bcrucial\b", "important"),
    (r"\bpivotal\b", "important"),
    (r"\brobust\b", "reliable"),
    (r"\bseamlessly\b", "directly"),
    (r"\bcomprehensive\b", "full"),
    (r"\bmeticulous\b", "careful"),
    (r"\bvibrant\b", "active"),
    (r"\btapestry\b", "mix"),
    (r"\btestament\b", "sign"),
    (r"\bintricate\b", "detailed"),
    (r"\balign with\b", "match"),
    (r"\bbolstered\b", "supported"),
    (r"\bgarner\b", "get"),
    (r"\binterplay\b", "interaction"),
    (r"\bvaluable\b", "useful"),
    (r"\bkey role\b", "important part"),
    (r"\bhighlighting\b", "showing"),
    (r"\bunderscoring\b", "showing"),
    (r"\bemphasizing\b", "stressing"),
    (r"\bensuring\b", "making sure"),
    (r"\breflecting\b", "showing"),
    (r"\bsymbolizing\b", "representing"),
    (r"\bcontributing to\b", "adding to"),
    (r"\bcultivating\b", "building"),
    (r"\bfostering\b", "supporting"),
    (r"\bencompassing\b", "including"),
    (r"\bunderscores\b", "shows"),
    (r"\bhighlights\b", "shows"),
    (r"\blandscape\b", "system"),
    (r"\bin the heart of\b", "in"),
    (r"\bgroundbreaking\b", "new"),
    (r"\brenowned\b", "known"),
    (r"\bdiverse array\b", "range"),
    (r"\bbreathtaking\b", "impressive"),
    (r"\bmust-visit\b", "worth seeing"),
    (r"\bstunning\b", "impressive"),
    (r"\bprofound\b", "strong"),
    (r"\bexemplifies\b", "shows"),
    (r"\bcommitment to\b", "focus on"),
    (r"\bnatural beauty\b", "scenery"),
    (r"\bnestled\b", "located"),
    (r"\bstands as a\b", "is a"),
    (r"\bserves as a\b", "is a"),
    (r"\bAdditionally,\b", "Also,"),
    (r"\bActually,\b", ""),
    (r"\bactually\b", ""),
    (r"\bnot just\b", "also"),
    (r"\bnot only\b", "also"),
    (r"\bnot merely\b", "also"),
]

INLINE_CODE = re.compile(r"(`[^`]*`)")


def preserve_case(original: str, replacement: str) -> str:
    if not replacement:
        return replacement
    if original[:1].isupper():
        return replacement[:1].upper() + replacement[1:]
    return replacement


def apply_words(text: str) -> str:
    for pattern, replacement in WORD_REPLACEMENTS:
        def sub(match: re.Match[str]) -> str:
            return preserve_case(match.group(0), replacement)
        text = re.sub(pattern, sub, text, flags=re.IGNORECASE)
    return text


def transform_segment(text: str, *, heading: bool) -> str:
    # Punctuation and decoration.
    text = text.replace("“", '"').replace("”", '"')
    text = text.replace("‘", "'").replace("’", "'")
    text = text.replace("**", "")
    text = text.replace("→", "to")
    text = text.replace("·", "and")
    text = text.replace("–", "-")
    if heading:
        text = re.sub(r"\s+—\s+", ": ", text)
    else:
        text = re.sub(r"\s+—\s+", ". ", text)
    text = re.sub(r"\s+—", ".", text)
    text = text.replace("—", ". ")
    text = apply_words(text)
    # Tidy the spaces left by phrase deletions.
    text = re.sub(r"[ \t]{2,}", " ", text)
    text = re.sub(r"\s+([.,;:])", r"\1", text)
    text = re.sub(r"\.\s*\.", ".", text)
    text = re.sub(r",\s*,", ",", text)
    return text


def transform_line(line: str, in_fence: bool) -> str:
    if in_fence:
        return line
    stripped = line.lstrip()
    heading = stripped.startswith("#")
    parts = INLINE_CODE.split(line)
    for i in range(0, len(parts), 2):
        parts[i] = transform_segment(parts[i], heading=heading)
    return "".join(parts)


def should_skip(path: Path) -> bool:
    rel = path.relative_to(ROOT).as_posix()
    if rel in SKIP_FILES:
        return True
    if any(part in SKIP_PARTS for part in path.relative_to(ROOT).parts):
        return True
    if path.suffix not in {".md", ".txt"}:
        return True
    return False


def main() -> None:
    changed: list[str] = []
    for path in sorted(ROOT.rglob("*")):
        if not path.is_file() or should_skip(path):
            continue
        try:
            original = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue
        lines = original.splitlines(keepends=True)
        out: list[str] = []
        in_fence = False
        for line in lines:
            bare = line.rstrip("\n")
            if re.match(r"^\s*(```|~~~)", bare):
                in_fence = not in_fence
                out.append(line)
                continue
            newline = "\n" if line.endswith("\n") else ""
            transformed = transform_line(bare, in_fence)
            out.append(transformed + newline)
        updated = "".join(out)
        if updated != original:
            path.write_text(updated, encoding="utf-8")
            changed.append(path.relative_to(ROOT).as_posix())
    print(f"rewrote {len(changed)} prose files")
    for name in changed:
        print(f"  {name}")


if __name__ == "__main__":
    main()
