#!/usr/bin/env python3
"""Render the probe-agent tray / menu bar icons into cmd/probe-agent/trayicons/.

  python3 scripts/probe-agent-icons.py          # fetch lucide-static via npm pack
  python3 scripts/probe-agent-icons.py DIR      # use an extracted package/icons dir

Needs rsvg-convert (brew install librsvg). Lucide icons: ISC License.
Windows gets multi-size .ico files (colored tile, white glyph); macOS gets
44px template PNGs (black + alpha, tinted by the menu bar).
"""
import pathlib
import re
import struct
import subprocess
import sys
import tarfile
import tempfile

OUT = pathlib.Path(__file__).resolve().parent.parent / "cmd/probe-agent/trayicons"

# state -> (glyph, Windows tile color, macOS glyph opacity)
STATES = {
    "ok": ("activity", "#16a34a", 1.0),
    "off": ("activity", "#6b7280", 0.4),
    "up": ("circle-arrow-up", "#0891b2", 1.0),
}
WIN_SIZES = [16, 20, 24, 32, 48]


def fetch():
    tmp = pathlib.Path(tempfile.mkdtemp())
    name = subprocess.check_output(["npm", "pack", "lucide-static@latest", "--silent"], cwd=tmp, text=True).strip().splitlines()[-1]
    with tarfile.open(tmp / name) as t:
        t.extractall(tmp, filter="data")
    return tmp / "package/icons"


def inner(path):
    svg = re.sub(r"<!--.*?-->", "", path.read_text(), flags=re.S)
    return re.search(r"<svg[^>]*>(.*)</svg>", svg, re.S).group(1)


def render(svg, size):
    return subprocess.run(["rsvg-convert", "-w", str(size), "-h", str(size), "-f", "png"],
                          input=svg.encode(), capture_output=True, check=True).stdout


def win_svg(glyph, color):
    return f"""<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
<rect x="0.5" y="0.5" width="23" height="23" rx="5.5" fill="{color}"/>
<g transform="translate(4 4) scale(0.6667)" fill="none" stroke="#fff" stroke-width="3"
   stroke-linecap="round" stroke-linejoin="round">{glyph}</g></svg>"""


def mac_svg(glyph, opacity):
    return f"""<svg xmlns="http://www.w3.org/2000/svg" viewBox="-1 -1 26 26">
<g fill="none" stroke="#000" stroke-opacity="{opacity}" stroke-width="2.2"
   stroke-linecap="round" stroke-linejoin="round">{glyph}</g></svg>"""


def ico(pngs):
    """ICO container with PNG-compressed entries (Vista+)."""
    head = struct.pack("<HHH", 0, 1, len(pngs))
    offset = 6 + 16 * len(pngs)
    dirs, data = b"", b""
    for size, png in pngs:
        dirs += struct.pack("<BBBBHHII", size % 256, size % 256, 0, 0, 1, 32, len(png), offset + len(data))
        data += png
    return head + dirs + data


def main():
    src = pathlib.Path(sys.argv[1]) if len(sys.argv) > 1 else fetch()
    OUT.mkdir(parents=True, exist_ok=True)
    for state, (name, color, opacity) in STATES.items():
        glyph = inner(src / f"{name}.svg")
        (OUT / f"win-{state}.ico").write_bytes(ico([(s, render(win_svg(glyph, color), s)) for s in WIN_SIZES]))
        (OUT / f"mac-{state}.png").write_bytes(render(mac_svg(glyph, opacity), 44))
    print(f"wrote {OUT}: {', '.join(sorted(p.name for p in OUT.iterdir()))}")


main()
