#!/usr/bin/env python3
"""Supervisor for the full backtest run.

The Go harness persists a compact record per completed strategy, but a single
strategy can crash the whole Go process (e.g. a recursive Pine script makes
the engine blow the goroutine stack, which is a Go fatal error that cannot be
recovered in-process). This loop:

  1. re-invokes the harness with --skip-existing so finished strategies are
     never redone, and
  2. after any crash, probes the first still-pending strategy with `-single`;
     if it crashes it is blacklisted, otherwise it records itself and batches
     resume.

Usage:
    python backtest/supervise.py [--workers 4]

State is tracked in F:/pitrading/_bt_cache:
  results.jsonl  - one compact record per completed strategy
  blacklist.txt  - slugs that crash the engine and are skipped
  run_full2.log  - appended harness output
"""
import argparse
import json
import os
import subprocess
import sys
import time

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")
if hasattr(sys.stderr, "reconfigure"):
    sys.stderr.reconfigure(encoding="utf-8")

MODULE_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CACHE = "F:/pitrading/_bt_cache"
RESULTS = os.path.join(CACHE, "results.jsonl")
BLACKLIST = os.path.join(CACHE, "blacklist.txt")
MANIFEST = os.path.join(CACHE, "manifest.json")


def read_records():
    slugs = set()
    if os.path.exists(RESULTS):
        with open(RESULTS, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    slugs.add(json.loads(line)["slug"])
                except Exception:
                    pass
    return slugs


def read_blacklist():
    if not os.path.exists(BLACKLIST):
        return set()
    with open(BLACKLIST, encoding="utf-8") as f:
        return {ln.strip() for ln in f if ln.strip()}


def write_blacklist(slugs):
    with open(BLACKLIST, "w", encoding="utf-8") as f:
        f.write("\n".join(sorted(slugs)) + "\n")


def ordered_targets():
    with open(MANIFEST, encoding="utf-8") as f:
        man = json.load(f)
    return [e["slug"] for e in man["entries"] if e.get("kind") != "skipped"]


def pending_slugs(done, black):
    return [s for s in ordered_targets() if s not in done and s not in black]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--workers", type=int, default=4)
    ap.add_argument("--log", default=os.path.join(CACHE, "run_full2.log"))
    args = ap.parse_args()

    exe = os.path.join(CACHE, "backtest.exe")
    print("building harness...", flush=True)
    subprocess.run(["go", "build", "-o", exe, "./backtest"], cwd=MODULE_ROOT, check=True)

    total = len(ordered_targets())
    black = read_blacklist()
    logf = open(args.log, "a", encoding="utf-8")
    probing = False
    started = time.time()

    while True:
        done = read_records()

        if probing:
            # probe the first few pending strategies in parallel: healthy ones
            # record themselves, crashers get blacklisted. Repeat until a
            # batch shows meaningful progress again.
            pending = pending_slugs(done, black)
            if not pending:
                probing = False
                continue
            batch = pending[:6]
            procs = []
            for slug in batch:
                p = subprocess.Popen([exe, "-single", slug, "-workers", "1"],
                                     cwd=MODULE_ROOT, stdout=logf, stderr=logf)
                procs.append((slug, p))
            crashed = 0
            for slug, p in procs:
                rc = p.wait()
                if rc != 0:
                    crashed += 1
                    black.add(slug)
                    write_blacklist(black)
                    print(f"[probe] blacklisted crasher: {slug}", flush=True)
            print(f"[probe] checked {len(batch)} pending, {crashed} crashed", flush=True)
            probing = False
            continue

        pending = pending_slugs(done, black)
        if not pending:
            break
        t0 = time.time()
        print(f"[run] {len(done)}/{total} done, {len(pending)} pending, "
              f"workers={args.workers}, blacklist={len(black)}", flush=True)
        r = subprocess.run([exe, "-skip-existing", "-workers", str(args.workers),
                            "-blacklist", BLACKLIST],
                           cwd=MODULE_ROOT, stdout=logf, stderr=logf)
        new_done = read_records()
        added = len(new_done) - len(done)
        print(f"[run] exit={r.returncode}, +{added} records in "
              f"{time.time()-t0:.0f}s", flush=True)
        if r.returncode != 0:
            # engine crash: isolate the offending strategy before resuming
            probing = True

    done = read_records()
    print(f"complete: {len(done)}/{total} recorded, {len(black)} blacklisted "
          f"in {time.time()-started:.0f}s", flush=True)
    logf.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())