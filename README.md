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
$ docker run --rm -p 5000:5000 -v "$PWD/models:/models:ro" \
    ghcr.io/ushitora-anqou/sumomo \
    --model /models/plamo-3-translate-0.8b-Q4_K_M.gguf
```

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

### Options

The current CLI help is printed in Japanese:

```console
$ sumomo --help
  --listen string     待ち受けアドレス (default "127.0.0.1:5000")
  --model string      GGUF モデルのパス (default "models/plamo-3-translate-0.8b-Q4_K_M.gguf")
  --api-key string    API キーを要求する(空なら無認証)
  --char-limit int    1 テキストあたりの文字数上限 (default 5000, <=0 で無制限)
  --n-ctx int         llama-server のコンテキスト長 (default 4096)
  --threads int       CPU スレッド数 (0 で自動)
  --max-tokens int    生成トークン数の基本値(入力長に応じて自動拡張) (default 512)
```

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

```console
$ nix develop          # Go + Python + llama.cpp CLI
$ make lint            # golangci-lint
$ make vet             # gofmt check + go vet
$ make fmt             # gofmt
$ make test            # server unit tests
$ make build           # build the Go server binary into bin/sumomo
$ make build-image     # build the native-architecture container image
```

CI (GitHub Actions) workflows:

- **CI** (`ci.yml`): flake check, `make lint` (golangci-lint), `make test`
  (unit tests), `make vet`, and builds of the server package and the image
- **E2E** (`e2e.yml`): runs `make test-e2e` to verify the Mastodon integration
- **Release image** (`release.yml`): on pushing a `v*` tag, builds and
  publishes the amd64 + arm64 multi-arch image to
  `ghcr.io/ushitora-anqou/sumomo`; each architecture is built natively on
  its own GitHub Actions runner (arm64 runners; no cross-compilation)

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
