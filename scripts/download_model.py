#!/usr/bin/env python3
"""Download an official pfnet/plamo-3-translate-0.8b GGUF file from Hugging Face.

The quantized GGUF files are published by Preferred Networks at:
  https://huggingface.co/pfnet/plamo-3-translate-0.8b-GGUF

Q2_K is the default because sumomo defaults to it (fastest, ~10% lower
latency and ~27% less memory than Q4_K_M in our benchmarks); Q4_K_M is the
model card's recommendation for best accuracy. Set the HF_TOKEN environment
variable when a token is required.
"""

from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path

import requests

REPO_ID = "pfnet/plamo-3-translate-0.8b-GGUF"
BASE_NAME = "plamo-3-translate-0.8b"

# Quantization types shipped in the official GGUF repository.
QUANTIZATIONS = ["Q4_K_M", "Q4_0", "Q3_K_M", "Q2_K"]

CHUNK_SIZE = 1024 * 1024  # 1 MiB

# The flake copies this script into the Nix store before running it, so the
# default destination is resolved from the working directory at runtime.
DEFAULT_DIR = Path("models")


def download_url(quant: str) -> str:
    return f"https://huggingface.co/{REPO_ID}/resolve/main/{BASE_NAME}-{quant}.gguf"


def remote_size(url: str, token: str | None) -> int:
    headers = {"Authorization": f"Bearer {token}"} if token else {}
    resp = requests.head(url, headers=headers, allow_redirects=True, timeout=30)
    resp.raise_for_status()
    length = resp.headers.get("Content-Length")
    if length is None:
        raise RuntimeError(f"Content-Length is missing for {url}")
    return int(length)


def download(url: str, dest: Path, token: str | None) -> None:
    expected = remote_size(url, token)
    if dest.exists() and dest.stat().st_size == expected:
        print(f"Already downloaded: {dest} ({expected / 10**9:.2f} GB)")
        return

    dest.parent.mkdir(parents=True, exist_ok=True)
    part = dest.with_name(dest.name + ".part")
    headers = {"Authorization": f"Bearer {token}"} if token else {}

    # Resume from a partial download if one exists.
    offset = part.stat().st_size if part.exists() else 0
    if offset:
        headers["Range"] = f"bytes={offset}-"

    with requests.get(
        url, headers=headers, stream=True, allow_redirects=True, timeout=(10, 60)
    ) as resp:
        if resp.status_code == 416:  # stale .part larger than the file
            part.unlink(missing_ok=True)
            raise RuntimeError(
                "Stale partial download removed; please run the script again."
            )
        resp.raise_for_status()
        resuming = resp.status_code == 206
        if offset and not resuming:
            # Server ignored the Range header; start over.
            offset = 0
            del headers["Range"]
            part.unlink(missing_ok=True)

        mode = "ab" if resuming else "wb"
        downloaded = offset
        with open(part, mode) as f:
            for chunk in resp.iter_content(chunk_size=CHUNK_SIZE):
                f.write(chunk)
                downloaded += len(chunk)
                done = downloaded / expected
                sys.stdout.write(
                    f"\r{downloaded / 10**9:.2f} / {expected / 10**9:.2f} GB"
                    f" ({done * 100:5.1f}%)"
                )
                sys.stdout.flush()
    sys.stdout.write("\n")

    if downloaded != expected:
        part.unlink(missing_ok=True)
        raise RuntimeError(
            f"Download incomplete ({downloaded} / {expected} bytes); retry."
        )

    part.replace(dest)
    print(f"Saved: {dest}")


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Download the official PLaMo 3 Translate 0.8B GGUF."
    )
    parser.add_argument(
        "--quant",
        choices=QUANTIZATIONS,
        default="Q2_K",
        help="quantization type (default: Q2_K, sumomo's default; Q4_K_M has the best accuracy per the model card)",
    )
    parser.add_argument(
        "--dest",
        type=Path,
        default=None,
        help=f"destination directory (default: {DEFAULT_DIR})",
    )
    args = parser.parse_args()

    token = os.environ.get("HF_TOKEN")
    dest_dir = args.dest or DEFAULT_DIR
    dest = dest_dir / f"{BASE_NAME}-{args.quant}.gguf"

    print(f"Downloading {REPO_ID} ({args.quant}) -> {dest}")
    try:
        download(download_url(args.quant), dest, token)
    except requests.RequestException as e:
        print(f"error: download failed: {e}", file=sys.stderr)
        return 1
    except RuntimeError as e:
        print(f"error: {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
