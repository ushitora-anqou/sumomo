#!/usr/bin/env python3
"""Benchmark sumomo's resource usage (server + llama-server).

Starts a sumomo server with the given flags, waits for /health, then
issues sequential translation requests and reports:

- total CPU seconds consumed by the sumomo + llama-server process tree
  during the run (utime + stime, from /proc),
- CPU seconds per request,
- wall-clock latency statistics (avg / p50 / p95),
- peak RSS of each process (VmHWM).

Usage: make bench   (or: python scripts/bench.py [--threads N ...])

Common sumomo flags have direct options (--threads); anything else can be
forwarded with repeated --sumomo-arg, e.g.:
  python scripts/bench.py --sumomo-arg=--no-webui
"""

from __future__ import annotations

import argparse
import json
import os
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_MODEL = os.path.join(ROOT, "models", "plamo-3-translate-0.8b-Q4_K_M.gguf")
BIN = os.path.join(ROOT, "bin", "sumomo")

# Fixed request mix: short Mastodon-like posts plus one longer paragraph.
TEXTS = [
    "The meeting has been rescheduled for next Monday.",
    "I really enjoyed the concert last night!",
    "Could you send me the report by Friday afternoon?",
    "Thanks, that helps a lot.",
    (
        "The new release includes several improvements to the translation "
        "pipeline, faster startup, and a number of bug fixes reported by "
        "users over the past month. Upgrading is recommended for everyone "
        "running an older version."
    ),
]

CLK_TCK = os.sysconf("SC_CLK_TCK")


def cpu_seconds(pid: int) -> float:
    with open(f"/proc/{pid}/stat") as f:
        fields = f.read().rsplit(")", 1)[1].split()
    utime, stime = int(fields[11]), int(fields[12])
    return (utime + stime) / CLK_TCK


def rss_hwm_kb(pid: int) -> int:
    with open(f"/proc/{pid}/status") as f:
        for line in f:
            if line.startswith("VmHWM:"):
                return int(line.split()[1])
    return 0


def child_pids(pid: int) -> list[int]:
    pids = []
    for tid in os.listdir(f"/proc/{pid}/task"):
        try:
            with open(f"/proc/{pid}/task/{tid}/children") as f:
                pids.extend(int(x) for x in f.read().split())
        except FileNotFoundError:
            continue
    return pids


def wait_for_health(url: str, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(url + "/health", timeout=2) as resp:
                if resp.status == 200:
                    return
        except (urllib.error.URLError, TimeoutError, OSError):
            pass
        time.sleep(0.5)
    raise RuntimeError(f"server did not become healthy within {timeout}s")


def translate(url: str, text: str, timeout: float) -> str:
    body = json.dumps({"q": text, "source": "en", "target": "ja"}).encode()
    req = urllib.request.Request(
        url + "/translate", data=body, headers={"Content-Type": "application/json"}
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        out = json.load(resp)
    return out["translatedText"]


def percentile(sorted_values: list[float], p: float) -> float:
    if not sorted_values:
        return float("nan")
    idx = min(len(sorted_values) - 1, int(round(p / 100 * (len(sorted_values) - 1))))
    return sorted_values[idx]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model", default=DEFAULT_MODEL)
    parser.add_argument("--requests", type=int, default=20,
                        help="number of translation requests (default 20)")
    parser.add_argument("--port", type=int, default=5999)
    parser.add_argument("--threads", type=int, default=None,
                        help="forward --threads N to sumomo (CPU threads for llama-server)")
    parser.add_argument("--sumomo-arg", action="append", default=[],
                        help="forward any other argument to sumomo (repeatable)")
    args = parser.parse_args()

    if not os.path.exists(BIN):
        print(f"error: {BIN} not found; run `make build` first", file=sys.stderr)
        return 2
    if not os.path.exists(args.model):
        print(f"error: model not found: {args.model} "
              "(run `nix run .#download-model`)", file=sys.stderr)
        return 2

    port = args.port
    cmd = [BIN, "--listen", f"127.0.0.1:{port}", "--model", args.model]
    if args.threads is not None:
        cmd += ["--threads", str(args.threads)]
    cmd += args.sumomo_arg
    print(f"[bench] starting: {' '.join(cmd)}", flush=True)
    server = subprocess.Popen(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    base_url = f"http://127.0.0.1:{port}"

    try:
        wait_for_health(base_url, timeout=300)

        # Measure the whole process tree: sumomo plus its llama-server child.
        pids = [server.pid] + child_pids(server.pid)
        names = {p: "sumomo" if p == server.pid else f"llama-server({p})" for p in pids}
        print(f"[bench] pids: {names}", flush=True)

        # Warm-up request so first-token costs do not skew the run.
        warm = translate(base_url, TEXTS[0], timeout=600)
        print(f"[bench] warm-up translated: {warm[:60]}...", flush=True)

        cpu_before = {p: cpu_seconds(p) for p in pids}
        wall_before = time.monotonic()

        latencies: list[float] = []
        for i in range(args.requests):
            text = TEXTS[i % len(TEXTS)]
            t0 = time.monotonic()
            out = translate(base_url, text, timeout=600)
            latencies.append(time.monotonic() - t0)
            print(f"[bench] {i + 1}/{args.requests} {time.monotonic() - t0:6.2f}s -> {out[:50]}...",
                  flush=True)

        wall = time.monotonic() - wall_before
        cpu_after = {p: cpu_seconds(p) for p in pids}

        total_cpu = sum(cpu_after[p] - cpu_before[p] for p in pids)
        lat_sorted = sorted(latencies)
        print()
        print("=== results ===")
        print(f"requests          : {args.requests}")
        print(f"wall time         : {wall:.2f} s  ({args.requests / wall:.3f} req/s)")
        print(f"cpu time (tree)   : {total_cpu:.2f} s  ({total_cpu / args.requests:.3f} s/req)")
        for p in pids:
            delta = cpu_after[p] - cpu_before[p]
            print(f"  {names[p]:<18}: cpu {delta:7.2f} s   peak RSS {rss_hwm_kb(p) / 1024:8.1f} MiB")
        print(f"latency avg       : {sum(latencies) / len(latencies):.2f} s")
        print(f"latency p50 / p95 : {percentile(lat_sorted, 50):.2f} / {percentile(lat_sorted, 95):.2f} s")
        return 0
    finally:
        server.send_signal(signal.SIGTERM)
        try:
            server.wait(timeout=15)
        except subprocess.TimeoutExpired:
            server.kill()


if __name__ == "__main__":
    sys.exit(main())
