#!/usr/bin/env python3
"""Prepare daily OHLCV CSV data for backtest symbols.

For each requested symbol:
  1. Look it up in topNFixed.parquet (F:/pitrading). If present, export the
     ticker's daily bars sorted by date.
  2. Otherwise download from yfinance.

The symbol CSV cache lives under the cache directory as <SYMBOL>.csv with a
sidecar <SYMBOL>.meta.json recording the source.

Usage:
    python dataprep.py --cache F:/pitrading/_bt_cache \
                       --parquet F:/pitrading/topNFixed.parquet \
                       --symbols AAPL,BTC-USD
"""
import argparse
import json
import os
import sys

import pandas as pd


def load_parquet_symbol(parquet_path: str, symbol: str):
    """Return daily bars for symbol from the parquet, or None if not found."""
    try:
        import pyarrow.parquet as pq
    except ImportError:
        return None
    pf = pq.ParquetFile(parquet_path)
    for rg in range(pf.metadata.num_row_groups):
        table = pf.read_row_group(rg, columns=["Ticker", "Date", "Open", "High", "Low", "Close", "Vol", "Openint"])
        df = table.to_pandas()
        sub = df[df["Ticker"] == symbol]
        if len(sub):
            sub = sub.sort_values("Date")
            sub = sub.drop_duplicates(subset=["Date"], keep="last")
            return sub
    return None


def load_yfinance_symbol(symbol: str):
    try:
        import yfinance as yf
    except ImportError:
        raise SystemExit("yfinance not installed: pip install yfinance")
    df = yf.download(symbol, period="max", interval="1d", auto_adjust=False, progress=False, threads=False)
    if df is None or df.empty:
        return None
    if isinstance(df.columns, pd.MultiIndex):
        df.columns = df.columns.get_level_values(0)
    if "Adj Close" in df.columns:
        df = df.drop(columns=["Adj Close"])
    df = df.reset_index()
    df = df.rename(columns={"Volume": "Vol"})
    df["Openint"] = 0
    df["Ticker"] = symbol
    cols = ["Ticker", "Date", "Open", "High", "Low", "Close", "Vol", "Openint"]
    for c in cols:
        if c not in df.columns:
            df[c] = 0
    return df[cols]


def save(cache: str, symbol: str, df, source: str):
    os.makedirs(cache, exist_ok=True)
    out = os.path.join(cache, symbol + ".csv")
    df = df.copy()
    df["Date"] = pd.to_datetime(df["Date"]).dt.strftime("%Y-%m-%d")
    df.to_csv(out, index=False)
    with open(os.path.join(cache, symbol + ".meta.json"), "w", encoding="utf-8") as fh:
        json.dump({"symbol": symbol, "source": source, "rows": int(len(df)),
                   "first": df["Date"].iloc[0] if len(df) else None,
                   "last": df["Date"].iloc[-1] if len(df) else None}, fh)
    return len(df)


def ensure_symbol(cache: str, parquet_path: str, symbol: str, force: bool = False):
    meta_path = os.path.join(cache, symbol + ".meta.json")
    if os.path.exists(meta_path) and not force:
        with open(meta_path, encoding="utf-8") as fh:
            meta = json.load(fh)
        return {"symbol": symbol, "source": meta.get("source"), "rows": meta.get("rows"),
                "cached": True, "ok": True}

    df = load_parquet_symbol(parquet_path, symbol)
    if df is not None and len(df):
        # Only use parquet rows that look like real daily bars.
        df["Date"] = pd.to_datetime(df["Date"])
        df = df.dropna(subset=["Open", "High", "Low", "Close"])
        df = df.sort_values("Date").drop_duplicates(subset=["Date"], keep="last")
        if len(df):
            n = save(cache, symbol, df, "parquet")
            return {"symbol": symbol, "source": "parquet", "rows": n, "cached": False, "ok": True}

    df = load_yfinance_symbol(symbol)
    if df is not None and len(df):
        n = save(cache, symbol, df, "yfinance")
        return {"symbol": symbol, "source": "yfinance", "rows": n, "cached": False, "ok": True}

    return {"symbol": symbol, "source": None, "rows": 0, "cached": False, "ok": False}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--cache", required=True)
    ap.add_argument("--parquet", required=True)
    ap.add_argument("--symbols", required=True, help="comma-separated symbols")
    ap.add_argument("--force", action="store_true")
    args = ap.parse_args()

    results = []
    for sym in [s.strip() for s in args.symbols.split(",") if s.strip()]:
        r = ensure_symbol(args.cache, args.parquet, sym, force=args.force)
        results.append(r)
        print(f"{sym}: source={r.get('source')} rows={r.get('rows')} ok={r.get('ok')} cached={r.get('cached')}")
    bad = [r for r in results if not r["ok"]]
    if bad:
        print("FAILED:", [b["symbol"] for b in bad], file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())