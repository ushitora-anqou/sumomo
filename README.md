# Sumomo

A LibreTranslate-compatible API server. It runs the translation-focused model
[pfnet/plamo-3-translate-0.8b](https://huggingface.co/pfnet/plamo-3-translate-0.8b)
with [llama.cpp](https://github.com/ggml-org/llama.cpp) and serves
translations in 53 languages on CPU-only machines. It is a single Go binary
that keeps a llama-server child process resident to manage the model, and it
works out of the box as the translation backend for
[Mastodon](https://joinmastodon.org/).

## Quick start

Run with Nix:

```console
$ nix run .#download-model          # save the official GGUF (Q4_K_M, 0.57 GB) into models/
$ nix run .#sumomo                  # start the server on http://127.0.0.1:5000
```

Or with Docker (multi-arch image from GHCR):

```console
$ docker run --rm -p 5000:5000 -v "$PWD/models:/models" \
    ghcr.io/ushitora-anqou/sumomo \
    --listen "0.0.0.0:5000" \
    --model /models/plamo-3-translate-0.8b-Q4_K_M.gguf
```

`--listen "0.0.0.0:5000"` is required so the server accepts connections
from outside the container (the default binds to 127.0.0.1). A missing
model file is downloaded into the mounted directory automatically.

Example translation request:

```console
$ curl -s http://127.0.0.1:5000/translate -H 'Content-Type: application/json' \
    -d '{"q":"会議は来週の月曜日に変更されました。","source":"ja","target":"en"}'
{"translatedText":"The meeting has been rescheduled for next Monday."}
```

## API

The API is compatible with LibreTranslate's.

| Endpoint | Description |
|---|---|
| `POST /translate` | Translate. `q` is a string or an array of strings, `source` may be `auto`, `format` is `text` / `html` (tags preserved) |
| `GET /languages` | List of supported languages (53 languages) |
| `POST /detect` | Language detection (lightweight, script-based) |
| `GET /frontend/settings` | Frontend settings |
| `GET /health` | Health check |

Behaviors shared with LibreTranslate: `detectedLanguage` for `source=auto`,
`alternatives`, batch translation (`q` as an array), pass-through of texts
made only of digits and symbols, form-encoded requests, CORS headers, and
identical error bodies and status codes. The language codes `zh` (Simplified)
and `zt` (Traditional) are mapped to the model's `zh-hans` / `zh-hant`.

Translations run strictly one at a time on the resident model: concurrent
requests are queued (bounded FIFO, `--queue-depth`) and processed
sequentially, which keeps resource usage flat under load; once the queue is
full, further requests fail fast with 503 instead of piling up.

### Options

```console
$ sumomo --help
  -api-key string
    	require this API key for /translate and /detect (empty = open)
  -char-limit int
    	per-text character limit (<=0 = unlimited) (default 1500)
  -listen string
    	address to listen on (default "127.0.0.1:5000")
  -llama-server-bin string
    	path to llama-server (default: $LLAMA_SERVER_BIN or PATH)
  -max-tokens int
    	base generation limit; grows with input length (default 512)
  -model string
    	path to the GGUF model file (default "models/plamo-3-translate-0.8b-Q4_K_M.gguf")
  -n-ctx int
    	llama-server context window in tokens (default 4096)
  -queue-depth int
    	completions to queue while one runs; excess requests get 503 (default 32)
  -request-timeout duration
    	per-request translation timeout (default 10m0s)
  -start-timeout duration
    	llama-server startup timeout (default 5m0s)
  -threads int
    	CPU threads for llama-server (0 = auto)
```

A missing model file is downloaded automatically from Hugging Face when
its name matches an official release.

## Mastodon integration

Mastodon can use any LibreTranslate-compatible server as its translation
backend through `LIBRE_TRANSLATE_ENDPOINT`. Point it at sumomo and the
"Translate" button on posts starts working (Mastodon sends post bodies with
`format=html`; sumomo translates only the text runs while keeping the markup).

The setup — Mastodon on a kind cluster via
[magout](https://github.com/ushitora-anqou/magout), wired to sumomo — is
fully automated in `k8s/` and `scripts/e2e.sh`:

```console
$ make test-e2e
[e2e] creating kind cluster 'sumomo-e2e'
...
[e2e] translated: <p>会議は来週の月曜日に日程変更となりました。…</p>
[e2e] PASS: Mastodon translated a post through sumomo
```

## Development

The toolchain lives in the Nix dev shell, loaded automatically by
[direnv](https://direnv.net/) (`use flake` in `.envrc`). It contains
everything the Make targets need, including the E2E tooling:

```console
$ direnv allow         # load the dev shell (Go, Python, llama.cpp, kind, ...)
$ make lint            # golangci-lint
$ make fmt             # gofmt
$ make test            # server unit tests
$ make build           # build the Go server binary into bin/sumomo
$ make build-image     # build the native-architecture container image
```

CI (GitHub Actions) workflows:

- **CI** (`ci.yml`): flake check, `make lint` (golangci-lint), `make test`
  (unit tests), and builds of the server package and the image;
  the dev shell is loaded with
  [nix-develop](https://github.com/nicknovitski/nix-develop) (pinned by
  commit SHA) so the Make targets run the same way as with direnv locally
- **E2E** (`e2e.yml`): runs `make test-e2e` to verify the Mastodon integration
- **Release image** (`release.yml`): on pushing a `v*` tag, builds and
  publishes the amd64 + arm64 multi-arch image to
  `ghcr.io/ushitora-anqou/sumomo`; each architecture is built natively on
  its own GitHub Actions runner (arm64 runners; no cross-compilation)

Dependencies are kept current by Dependabot (`.github/dependabot.yml`): Go
modules, GitHub Actions workflows, and the Nix flake.lock are checked weekly
and combined into a single update pull request.

## Release

```console
$ make release VERSION=0.1.0
```

A leading `v` is optional and added if missing. This creates and pushes a
Git tag (`v0.1.0`) and opens the GitHub release. Publishing the multi-arch
Docker image is triggered by the tag push.

## Helper scripts

Besides the server itself (sumomo), the following helper scripts exist.

| Script | Description |
|---|---|
| `scripts/download_model.py` | Downloader for the official GGUF (idempotent, resumable) |
| `scripts/translate.py` | One-shot CLI translator (starts a temporary llama-server) |
| `scripts/e2e.sh` | E2E test (run via `make test-e2e`) |

The scripts run through Nix:

```console
$ nix run .#download-model
$ nix run .#translate -- -t en "こんにちは"
```
