#!/usr/bin/env python3
"""Translate text with pfnet/plamo-3-translate-0.8b served by llama.cpp.

This script launches `llama-server` (the OpenAI-compatible HTTP server of
llama.cpp) as a child process, waits for it to become healthy, sends a
translation request to /v1/chat/completions, prints the result, and shuts
the server down.

The official GGUF file embeds Preferred Networks' chat template, which maps
a system message of the form

    [plamo-translate]
    target_lang=en
    source_lang=ja        (optional)
    form=plain            (optional, Japanese target only)
    ---
    (optional reference context)

followed by the source text as a user message, onto the PFML3 prompt the
model was trained on. Decoding settings recommended by the model card are
applied: temperature=0 with stop=["<|plamo:tag|>", "<|plamo:bos|>"].

Examples:
  nix run .#translate -- -t en "会議は来週の月曜日に変更されました。"
  nix run .#translate -- -s en -t ja --form plain "This system is convenient."
  cat input.txt | nix run .#translate -- -t en
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

LLAMA_SERVER_ENV = "LLAMA_SERVER_BIN"
DEFAULT_QUANT = "Q4_K_M"
STOP_SEQUENCES = ["<|plamo:tag|>", "<|plamo:bos|>"]
SERVER_START_TIMEOUT = 300.0  # seconds; first-time model load can be slow
REQUEST_TIMEOUT = 600.0  # seconds; long inputs take a while on CPU

LANG_HELP = (
    "lowercase language code such as ja, en, zh-hans, or ja(easy) "
    "(53 languages supported by the model)"
)


def default_model() -> Path:
    # The flake copies this script into the Nix store before running it, so
    # the default path is resolved from the working directory at runtime.
    return Path("models") / f"plamo-3-translate-0.8b-{DEFAULT_QUANT}.gguf"


def find_server_bin() -> str:
    candidate = os.environ.get(LLAMA_SERVER_ENV)
    if candidate:
        if not Path(candidate).exists():
            sys.exit(f"error: {LLAMA_SERVER_ENV}={candidate} does not exist")
        return candidate
    found = shutil.which("llama-server")
    if found:
        return found
    sys.exit(
        "error: llama-server not found; run via `nix run .#translate` or "
        "enter the development shell first (`nix develop`)"
    )


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return int(s.getsockname()[1])


def start_server(
    server_bin: str,
    model_path: Path,
    n_ctx: int,
    threads: int | None,
    verbose: bool,
) -> tuple[subprocess.Popen, int, Path]:
    port = free_port()
    log_file = tempfile.NamedTemporaryFile(
        prefix="llama-server-", suffix=".log", delete=False
    )
    cmd = [
        server_bin,
        "-m", str(model_path),
        "--host", "127.0.0.1",
        "--port", str(port),
        "-c", str(n_ctx),
        "--no-webui",
    ]
    if threads:
        cmd += ["-t", str(threads)]
    if verbose:
        print(f"starting: {' '.join(cmd)}", file=sys.stderr)
        print(f"server log: {log_file.name}", file=sys.stderr)
    proc = subprocess.Popen(cmd, stdout=log_file, stderr=subprocess.STDOUT)
    return proc, port, Path(log_file.name)


def wait_until_healthy(
    port: int, proc: subprocess.Popen, log_path: Path, timeout: float
) -> None:
    deadline = time.monotonic() + timeout
    url = f"http://127.0.0.1:{port}/health"
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            show_log_tail(log_path)
            sys.exit(f"error: llama-server exited with code {proc.returncode}")
        try:
            with urllib.request.urlopen(url, timeout=2) as resp:
                if resp.status == 200:
                    status = json.loads(resp.read()).get("status")
                    if status == "ok":
                        return
        except (urllib.error.URLError, TimeoutError, OSError, json.JSONDecodeError):
            pass
        time.sleep(0.5)
    show_log_tail(log_path)
    sys.exit(f"error: llama-server did not become healthy within {timeout:.0f}s")


def show_log_tail(log_path: Path, lines: int = 20) -> None:
    try:
        tail = log_path.read_text(errors="replace").splitlines()[-lines:]
    except OSError:
        return
    if tail:
        print(f"--- llama-server log ({log_path}) ---", file=sys.stderr)
        print("\n".join(tail), file=sys.stderr)


def stop_server(proc: subprocess.Popen) -> None:
    if proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()


def build_system_message(
    target_lang: str, source_lang: str | None, form: str | None
) -> str:
    """Build the [plamo-translate] settings block understood by the
    chat template embedded in the official GGUF file."""
    lines = ["[plamo-translate]", f"target_lang={target_lang}"]
    if source_lang:
        lines.append(f"source_lang={source_lang}")
    if form:
        lines.append(f"form={form}")
    lines.append("---")
    return "\n".join(lines)


def translate(
    port: int,
    text: str,
    target_lang: str,
    source_lang: str | None,
    form: str | None,
    max_tokens: int,
) -> dict:
    payload = {
        "model": "plamo-3-translate-0.8b",
        "messages": [
            {
                "role": "system",
                "content": build_system_message(target_lang, source_lang, form),
            },
            {"role": "user", "content": text},
        ],
        "temperature": 0.0,  # deterministic decoding, per the model card
        "max_tokens": max_tokens,
        "stop": STOP_SEQUENCES,
    }
    req = urllib.request.Request(
        f"http://127.0.0.1:{port}/v1/chat/completions",
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=REQUEST_TIMEOUT) as resp:
            return json.loads(resp.read())
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")
        sys.exit(f"error: server returned {e.code}: {body}")


def read_text(args_text: list[str]) -> str:
    if args_text:
        return " ".join(args_text)
    return sys.stdin.read()


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Translate text with plamo-3-translate-0.8b on llama.cpp.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "text",
        nargs="*",
        help='text to translate (reads stdin when omitted, e.g.: echo "..." | %(prog)s)',
    )
    parser.add_argument(
        "-t", "--target-lang", default="ja",
        help=f"target language (default: ja; {LANG_HELP})",
    )
    parser.add_argument(
        "-s", "--source-lang", default=None,
        help=f"source language (default: auto-detect; {LANG_HELP})",
    )
    parser.add_argument(
        "--form", choices=["polite", "plain"], default=None,
        help="Japanese output style; only valid with --target-lang ja",
    )
    parser.add_argument(
        "--model", type=Path, default=None,
        help=f"path to the GGUF file (default: {default_model()})",
    )
    parser.add_argument(
        "--n-ctx", type=int, default=4096,
        help="context window size in tokens (default: 4096)",
    )
    parser.add_argument(
        "--max-tokens", type=int, default=512,
        help="maximum tokens to generate (default: 512)",
    )
    parser.add_argument(
        "--threads", type=int, default=None,
        help="number of CPU threads (default: chosen by llama.cpp)",
    )
    parser.add_argument(
        "--verbose", action="store_true",
        help="show the underlying llama-server command and statistics on stderr",
    )
    args = parser.parse_args()

    if args.form and args.target_lang != "ja":
        parser.error("--form requires --target-lang ja")

    model_path = args.model or default_model()

    text = read_text(args.text).strip()
    if not text:
        parser.error("no input text given (pass it as an argument or via stdin)")

    if not model_path.exists():
        print(
            f"error: model file not found: {model_path}\n"
            f"Download it first, e.g.: nix run .#download-model",
            file=sys.stderr,
        )
        return 1

    server_bin = find_server_bin()
    load_start = time.perf_counter()
    proc, port, log_path = start_server(
        server_bin, model_path, args.n_ctx, args.threads, args.verbose
    )
    try:
        wait_until_healthy(port, proc, log_path, SERVER_START_TIMEOUT)
        if args.verbose:
            print(
                f"server ready on port {port} in "
                f"{time.perf_counter() - load_start:.1f}s",
                file=sys.stderr,
            )
        gen_start = time.perf_counter()
        response = translate(
            port, text, args.target_lang, args.source_lang, args.form,
            args.max_tokens,
        )
        elapsed = time.perf_counter() - gen_start
    finally:
        stop_server(proc)

    choice = response["choices"][0]
    print(choice["message"]["content"].strip())

    if args.verbose:
        usage = response.get("usage", {})
        completion_tokens = usage.get("completion_tokens", 0)
        print(
            f"generated {completion_tokens} tokens in {elapsed:.1f}s "
            f"({completion_tokens / max(elapsed, 1e-9):.1f} tokens/s), "
            f"finish_reason={choice.get('finish_reason')}",
            file=sys.stderr,
        )

    try:
        log_path.unlink()
    except OSError:
        pass
    return 0


if __name__ == "__main__":
    sys.exit(main())
