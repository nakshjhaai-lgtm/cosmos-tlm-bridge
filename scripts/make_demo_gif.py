#!/usr/bin/env python3
"""Regenerate docs/demo.gif from docs/demo-replay.txt (10.0 s, 50 frames)."""
from __future__ import annotations

import json
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

ROOT = Path(__file__).resolve().parents[1]
REPLAY = ROOT / "docs" / "demo-replay.txt"
GIF = ROOT / "docs" / "demo.gif"
CAST = ROOT / "docs" / "demo.cast"
W, H = 960, 600
FPS_MS = 200
FRAMES = 50  # 50 * 200 ms = exactly 10.0 s
BG = (10, 14, 20)
FG = (214, 226, 236)
DIM = (110, 128, 145)
ACCENT = (80, 210, 255)
GREEN = (90, 220, 130)
YELLOW = (255, 205, 70)
RED = (255, 95, 95)


def font(size: int, bold: bool = False) -> ImageFont.FreeTypeFont | ImageFont.ImageFont:
    path = "/usr/share/fonts/truetype/dejavu/DejaVuSansMono%s.ttf" % ("-Bold" if bold else "")
    try:
        return ImageFont.truetype(path, size)
    except OSError:
        return ImageFont.load_default()


F_TITLE = font(16, True)
F_TEXT = font(14)
F_SMALL = font(12)


def colour(line: str):
    if line.startswith("$ ") or "POST /" in line or "GET /" in line:
        return ACCENT
    if "YELLOW_HIGH" in line or "YELLOW" in line and "alarm" in line:
        return YELLOW
    if "RED_HIGH" in line or "FAILED" in line:
        return RED
    if line.startswith("PASS") or " PASS " in line or "-> PASS" in line or "instant spin-up" in line:
        return GREEN
    if line.startswith("  junit") or line.startswith("  html") or "webhook" in line or "frames=" in line:
        return DIM
    return FG


def render(visible: str, frame: int) -> Image.Image:
    img = Image.new("RGB", (W, H), BG)
    draw = ImageDraw.Draw(img)
    draw.rectangle((0, 0, W, 34), fill=(18, 26, 36))
    draw.text((16, 9), "cosmos-tlm-bridge  |  DROID_SIM  |  OpenC3 JSON-RPC -> typed structs", font=F_TITLE, fill=ACCENT)
    draw.text((W - 128, 10), "10s replay", font=F_SMALL, fill=DIM)
    # Progress bar.
    progress = min(1.0, frame / (FRAMES - 1))
    draw.rectangle((0, 33, W, 36), fill=(30, 42, 55))
    draw.rectangle((0, 33, int(W * progress), 36), fill=ACCENT)
    y = 52
    for line in visible.split("\n"):
        draw.text((18, y), line, font=F_TEXT, fill=colour(line))
        y += 20
    # Blinking cursor.
    if frame % 4 < 2:
        last = visible.split("\n")[-1]
        x = 18 + draw.textlength(last, font=F_TEXT)
        draw.rectangle((x + 2, y - 18, x + 10, y - 2), fill=ACCENT)
    draw.text((18, H - 20), "uvx --from . cosmos-tlm-bridge demo  |  docker compose up  |  < 45 s", font=F_SMALL, fill=DIM)
    return img.convert("P", palette=Image.ADAPTIVE, colors=8)


def main() -> None:
    lines = REPLAY.read_text(encoding="utf-8").rstrip("\n").split("\n")
    full = "\n".join(lines)
    frames = []
    for i in range(FRAMES):
        if i < FRAMES - 5:
            n = int(len(full) * (i / (FRAMES - 6)))
            shown = full[:n]
        else:
            shown = full
        frames.append(render(shown, i))
    frames[0].save(GIF, save_all=True, append_images=frames[1:], duration=FPS_MS,
                   loop=0, optimize=True, disposal=2)
    header = {"version": 2, "width": 112, "height": len(lines) + 2, "timestamp": 0,
              "env": {"SHELL": "/bin/bash", "TERM": "xterm-256color"}}
    cast = [json.dumps(header)]
    for i, line in enumerate(lines):
        cast.append(json.dumps([round(i * 9.7 / max(1, len(lines) - 1), 3), "o",
                                line.replace("\t", "    ") + "\r\n"]))
    cast.append(json.dumps([9.9, "o", "\r\n"]))
    CAST.write_text("\n".join(cast) + "\n", encoding="utf-8")
    with Image.open(GIF) as im:
        total = sum(im.seek(n) or im.info.get("duration", 0) for n in range(im.n_frames))
        print(f"wrote {GIF} ({GIF.stat().st_size} bytes), {im.n_frames} frames, {total} ms")
        print(f"wrote {CAST}")


if __name__ == "__main__":
    main()
