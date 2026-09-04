#!/usr/bin/env python3
"""Extract Pine Script strategies from a directory of strategy markdown files.

Scans every *.md in the strategies directory, pulls the primary Pine Script
code block, classifies each file (strategy / indicator / skipped), detects a
preferred backtest symbol, and writes a JSON manifest consumed by the Go
backtest harness.

Usage:
    python extract.py --strategies F:/dev/test/fmzquant-strategies \
                      --manifest F:/pitrading/_bt_cache/manifest.json
"""
import argparse
import json
import os
import re
import sys

# ---------------------------------------------------------------------------
# Symbol hint detection. Order matters: first match wins.
# Each entry: (pattern, yfinance symbol, source label)
# Sources: "parquet" means the ticker exists in topNFixed.parquet; anything
# else falls back to yfinance.
SYMBOL_HINTS = [
    (re.compile(r"btcusdt|btcusd|btc\b|bitcoin|比特币|大饼"), "BTC-USD", "yfinance"),
    (re.compile(r"ethusdt|ethusd|eth\b|ethereum|以太坊|以太"), "ETH-USD", "yfinance"),
    (re.compile(r"xau|gold|黄金|金价|k金"), "GC=F", "yfinance"),
    (re.compile(r"xag|silver|白银"), "SI=F", "yfinance"),
    (re.compile(r"wti|crude|原油|oil\b"), "CL=F", "yfinance"),
    (re.compile(r"nasdaq|\bndx\b|纳指|纳斯达克"), "^NDX", "yfinance"),
    (re.compile(r"sp500|spx\b|标普|s&p"), "^GSPC", "yfinance"),
    (re.compile(r"djia|\bdow\b|道琼斯|道指"), "^DJI", "yfinance"),
    (re.compile(r"\bhsi\b|恒生|恒指"), "^HSI", "yfinance"),
]

DEFAULT_SYMBOL = "AAPL"
DEFAULT_SYMBOL_SOURCE = "parquet"

FENCE_RE = re.compile(r"```[ \t]*([\w+-]*)[ \t]*\n(.*?)```", re.S)

# Strategy() declaration argument parsing (Pine v1..v6).
DECLARATION_RE = re.compile(
    r"(?:strategy|study|indicator)\s*\(\s*(.*?)\)\s*\n", re.S
)
STRING_RE = re.compile(r'"([^"\\]|\\.)*"')

COLOR_NAMES = [
    "aqua", "black", "blue", "fuchsia", "gray", "green", "lime", "maroon",
    "navy", "olive", "orange", "purple", "red", "silver", "teal", "white",
    "yellow",
]
COLOR_RE = re.compile(r"\b(" + "|".join(COLOR_NAMES) + r")\b")


def parse_declaration_args(code: str) -> dict:
    """Best-effort parse of the strategy() declaration into a dict of args."""
    m = DECLARATION_RE.search(code)
    if not m:
        return {}
    inner = m.group(1)
    # Split top-level commas (respecting quotes, parens, brackets).
    args = []
    depth = 0
    cur = ""
    in_str = False
    for ch in inner:
        if ch == '"':
            in_str = not in_str
            cur += ch
            continue
        if not in_str:
            if ch in "([{":
                depth += 1
            elif ch in ")]}":
                depth -= 1
            elif ch == "," and depth == 0:
                args.append(cur.strip())
                cur = ""
                continue
        cur += ch
    if cur.strip():
        args.append(cur.strip())

    out = {}
    for a in args:
        if not a:
            continue
        if "=" in a:
            k, v = a.split("=", 1)
            k = k.strip()
            v = v.strip()
            out[k] = parse_value(v)
    return out


def parse_value(v: str):
    v = v.strip()
    if v.startswith('"') and v.endswith('"'):
        return v[1:-1]
    low = v.lower()
    if low in ("true", "false"):
        return low == "true"
    try:
        return float(v) if ("." in v or "e" in low) else int(v)
    except ValueError:
        return v


def detect_version(code: str):
    m = re.search(r"//@version=(\d+)", code)
    return int(m.group(1)) if m else None


def extract_pine_block(text: str):
    """Return the best Pine code block from the file text, or None."""
    fences = FENCE_RE.findall(text)
    candidates = []
    for lang, code in fences:
        lang = lang.strip().lower()
        is_pine = (
            lang in ("pine", "pinescript", "pine-script", "tv4", "tv", "js5")
            or "//@version=" in code
            or re.search(r"\b(strategy|indicator|study)\s*\(", code) is not None
        )
        if not is_pine:
            continue
        candidates.append((lang, code))

    if not candidates:
        # No fence blocks: if the body itself is a versioned Pine script, use it.
        # Guard against markdown prose being mistaken for code (FMZ docs often
        # mention "strategy(" or "//@version=" inside the description).
        if "//@version=" in text and re.search(r"\b(strategy|indicator|study)\s*\(", text):
            if not looks_like_markdown(text):
                return text
        return None

    # Prefer blocks containing a strategy/indicator/study declaration.
    decl = [c for c in candidates if re.search(r"\b(strategy|indicator|study)\s*\(", c[1])]
    pool = decl if decl else candidates
    # Prefer the block with a version header; tie-break on length.
    with_ver = [c for c in pool if "//@version=" in c[1]]
    pool = with_ver if with_ver else pool
    pool.sort(key=lambda c: len(c[1]), reverse=True)
    return pool[0][1]


MARKDOWN_NOISE_RE = re.compile(
    r"(?m)^\s*(?:>\s*|!\[|##+\s*|#{1,3}\s|[-*+]\s+|\d+\.\s+|\|\s+)"
)
# Interleaved markdown structure markers.
CODE_NOISE_RE = re.compile(r"\[\/?trans\]|!\[\w+\]\(http")


def looks_like_markdown(text):
    """Heuristic: the text contains markdown/prose artifacts that a real Pine
    script would not have at line starts or inline."""
    if MARKDOWN_NOISE_RE.search(text):
        return True
    if CODE_NOISE_RE.search(text):
        return True
    return False


def classify(code: str):
    if not code:
        return "skipped"
    # Non-Pine sources: a few FMZ strategy docs carry Python bot code in the
    # fenced block ("import json" / "class X:" with "def __init__"). They can
    # never compile as Pine; mark them skipped with a reason instead of
    # surfacing a perpetual compile_error.
    head = code[:400]
    if re.search(r"(?m)^\s*import\s+(json|time|pandas|numpy)\b", head) or re.search(r"(?m)^\s*class\s+\w+.*:\s*$", head) and re.search(r"\bdef\s+__init__\b", head):
        return "skipped"
    if re.search(r"(?m)^\s*def\s+\w+\s*\(.*\)\s*:\s*$", head) and "strategy(" not in code and "study(" not in code and "indicator(" not in code:
        return "skipped"
    if re.search(r"\bstrategy\s*\(", code):
        return "strategy"
    if re.search(r"\b(indicator|study)\s*\(", code):
        return "indicator"
    return "skipped"


def detect_symbol(text: str, title: str):
    haystack = (title + "\n" + text).lower()
    for pat, sym, src in SYMBOL_HINTS:
        if pat.search(haystack):
            return sym, src
    return DEFAULT_SYMBOL, DEFAULT_SYMBOL_SOURCE


def slugify(name: str):
    s = name
    s = re.sub(r"[\\/:*?\"<>|\x00-\x1f]", "_", s)
    s = s.strip().strip(".")
    return s[:180] or "untitled"


def read_meta(text: str, key: str):
    m = re.search(r">\s*" + key + r"\s*\n\s*\n\s*([^\n]+)", text)
    return m.group(1).strip() if m else ""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--strategies", required=True)
    ap.add_argument("--manifest", required=True)
    args = ap.parse_args()

    sdir = args.strategies
    entries = []
    needed = {}
    skipped = 0
    n_pine = 0

    for name in sorted(os.listdir(sdir)):
        if not name.lower().endswith(".md"):
            continue
        path = os.path.join(sdir, name)
        try:
            with open(path, encoding="utf-8", errors="replace") as fh:
                text = fh.read()
        except OSError as exc:
            entries.append({
                "file": name, "slug": slugify(name[:-3]), "kind": "skipped",
                "skip_reason": f"read error: {exc}",
            })
            continue

        code = extract_pine_block(text)
        if code is None:
            entries.append({
                "file": name, "slug": slugify(name[:-3]), "kind": "skipped",
                "skip_reason": "no pine code block found",
            })
            skipped += 1
            continue

        kind = classify(code)
        if kind == "skipped":
            entries.append({
                "file": name, "slug": slugify(name[:-3]), "kind": "skipped",
                "skip_reason": "no strategy/indicator declaration",
            })
            skipped += 1
            continue

        title = read_meta(text, "Name") or slugify(name[:-3])
        author = read_meta(text, "Author")
        sym, sym_src = detect_symbol(text, title)
        needed.setdefault(sym, sym_src)
        n_pine += 1

        entries.append({
            "file": name,
            "slug": slugify(name[:-3]),
            "title": title,
            "author": author,
            "kind": kind,
            "version": detect_version(code),
            "symbol": sym,
            "symbol_source": sym_src,
            "decl": parse_declaration_args(code),
            "code": code,
        })

    os.makedirs(os.path.dirname(args.manifest), exist_ok=True)
    with open(args.manifest, "w", encoding="utf-8") as fh:
        json.dump({
            "strategies_dir": sdir,
            "entries": entries,
            "needed_symbols": {k: v for k, v in sorted(needed.items())},
            "counts": {"total": len(entries), "pine": n_pine, "skipped": skipped},
        }, fh, ensure_ascii=False)

    print(json.dumps({
        "total": len(entries),
        "pine": n_pine,
        "skipped": skipped,
        "needed_symbols": sorted(needed.items()),
    }, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    sys.exit(main())