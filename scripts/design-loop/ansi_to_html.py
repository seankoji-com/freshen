#!/usr/bin/env python3
"""Convert ANSI SGR text (from `tmux capture-pane -e`) to a standalone HTML page.

Only SGR sequences are interpreted; other CSI sequences are dropped. The page is
sized for a monospace terminal screenshot and uses the freshen palette defaults.
"""
import html
import re
import sys

DEFAULT_FG = "#DCE7E0"
DEFAULT_BG = "#101613"

SGR = re.compile(r"\x1b\[([0-9;:]*)m")
# Any CSI except SGR: keep SGR intact for the parser below.
CSI = re.compile(r"\x1b\[(?!([0-9;:]*)m)[0-9;?]*[ -/]*[@-~]")
OSC = re.compile(r"\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)")

BASE16 = [
    "#1a1a1a", "#e06c75", "#98c379", "#e5c07b",
    "#61afef", "#c678dd", "#56b6c2", "#dcdfe4",
]


def color_from_256(n: int) -> str:
    if n < 16:
        return BASE16[n % 8] if n < 8 else BASE16[n - 8]
    if n < 232:
        n -= 16
        r, g, b = n // 36, (n % 36) // 6, n % 6
        conv = lambda v: 0 if v == 0 else 55 + v * 40
        return f"#{conv(r):02x}{conv(g):02x}{conv(b):02x}"
    v = 8 + (n - 232) * 10
    return f"#{v:02x}{v:02x}{v:02x}"


def apply_codes(state: dict, params: str) -> None:
    if params == "":
        params = "0"
    parts = [p for p in params.replace(":", ";").split(";")]
    i = 0
    while i < len(parts):
        p = parts[i]
        try:
            code = int(p) if p != "" else 0
        except ValueError:
            i += 1
            continue
        if code == 0:
            state.update(fg=None, bg=None, bold=False, dim=False,
                         italic=False, underline=False, reverse=False)
        elif code == 1:
            state["bold"] = True
        elif code == 2:
            state["dim"] = True
        elif code == 3:
            state["italic"] = True
        elif code == 4:
            state["underline"] = True
        elif code == 7:
            state["reverse"] = True
        elif code == 22:
            state["bold"] = state["dim"] = False
        elif code == 23:
            state["italic"] = False
        elif code == 24:
            state["underline"] = False
        elif code == 27:
            state["reverse"] = False
        elif 30 <= code <= 37:
            state["fg"] = BASE16[code - 30]
        elif code == 39:
            state["fg"] = None
        elif 40 <= code <= 47:
            state["bg"] = BASE16[code - 40]
        elif code == 49:
            state["bg"] = None
        elif 90 <= code <= 97:
            state["fg"] = BASE16[code - 90]
        elif 100 <= code <= 107:
            state["bg"] = BASE16[code - 100]
        elif code in (38, 48):
            key = "fg" if code == 38 else "bg"
            if i + 1 < len(parts) and parts[i + 1] == "5" and i + 2 < len(parts):
                state[key] = color_from_256(int(parts[i + 2]))
                i += 2
            elif i + 1 < len(parts) and parts[i + 1] == "2" and i + 4 < len(parts):
                r, g, b = (int(x) for x in parts[i + 2:i + 5])
                state[key] = f"#{r:02x}{g:02x}{b:02x}"
                i += 4
        i += 1


def span(text: str, state: dict) -> str:
    if not text:
        return ""
    fg = state["fg"] or DEFAULT_FG
    bg = state["bg"] or DEFAULT_BG
    if state["reverse"]:
        fg, bg = bg, fg
    css = [f"color:{fg}", f"background:{bg}"]
    if state["bold"]:
        css.append("font-weight:700")
    if state["dim"]:
        css.append("opacity:0.62")
    if state["italic"]:
        css.append("font-style:italic")
    if state["underline"]:
        css.append("text-decoration:underline")
    return f'<span style="{";".join(css)}">{html.escape(text)}</span>'


def convert(text: str) -> tuple[str, int]:
    state = dict(fg=None, bg=None, bold=False, dim=False,
                 italic=False, underline=False, reverse=False)
    out: list[str] = []
    pos = 0
    for m in SGR.finditer(text):
        out.append(span(text[pos:m.start()], state))
        apply_codes(state, m.group(1))
        pos = m.end()
    out.append(span(text[pos:], state))
    body = "".join(out)
    width = max((len(line) for line in text.splitlines()), default=80)
    return body, width


def main() -> None:
    raw = sys.stdin.buffer.read().decode("utf-8", "replace")
    raw = OSC.sub("", CSI.sub("", raw))
    body, width = convert(raw)
    page = f"""<!doctype html>
<html><head><meta charset="utf-8"><style>
  html,body{{margin:0;padding:0;background:{DEFAULT_BG};}}
  pre{{margin:0;padding:10px 14px;display:inline-block;
       font-family:"DejaVu Sans Mono","Liberation Mono",monospace;
       font-size:15px;line-height:1.25;white-space:pre;
       color:{DEFAULT_FG};background:{DEFAULT_BG};
       font-variant-ligatures:none;}}
</style></head><body><pre>{body}</pre></body></html>"""
    sys.stdout.write(page)
    sys.stderr.write(f"{width}\n")


if __name__ == "__main__":
    main()
