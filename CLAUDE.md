# CLAUDE.md

## Project

Sumomo is a LibreTranslate-compatible translation API server: a single Go
binary that keeps a llama-server child process resident to run the
plamo-3-translate-0.8b GGUF model on CPU and serves LibreTranslate-style
endpoints (`POST /translate`, `GET /languages`, `POST /detect`,
`GET /frontend/settings`, `GET /health`). API only — there is no HTML UI.

Everything runs through Nix (`flake.nix`): the dev shell provides Go,
golangci-lint, llama.cpp, and Python. The Go server has no external
dependencies (stdlib only).

## Commands

The toolchain lives in the Nix dev shell, loaded via direnv (`use flake` in
`.envrc`) — assume it is active and run commands directly (never wrap them
in `nix develop`). CI loads the same shell with the SHA-pinned
`nicknovitski/nix-develop` action.

| Command | Description |
|---|---|
| `make lint` | golangci-lint (default linters; keep it at 0 issues) |
| `make vet` | gofmt check + go vet (fails on unformatted code) |
| `make fmt` | gofmt |
| `make test` | server unit tests (against an in-process mock llama-server) |
| `make build` | build the Go server binary into `bin/sumomo` |
| `make build-image` | build the native-architecture container image with Nix |
| `make test-e2e` | E2E: Mastodon on a kind cluster with sumomo as its translation backend |
| `make release VERSION=X.Y.Z` | create/push the `v`-prefixed tag and open the GitHub release |

Before finishing a change to `server/`, run `make lint` and `make test`.
The full E2E run needs Docker and is slow; run it only when touching the
deployment/Mastodon integration.

## Layout

- `server/` — the Go server (one package, stdlib only)
  - `main.go` — flags, llama-server startup, signal handling
  - `api.go` — LibreTranslate API handlers, routing (`server.routes()`), CORS
  - `llama.go` — llama-server child process management + OpenAI-compatible
    client; completions go through a bounded FIFO queue (`--queue-depth`)
    drained by a single worker (strictly sequential; a full queue fails fast
    with 503, cancelled waiters are skipped)
  - `languages.go` — language tables; maps LibreTranslate codes (`zh`/`zt`)
    to the model's (`zh-hans`/`zh-hant`)
  - `detect.go` — Unicode-script-based language detection
  - `html.go` — `format=html` translation (tags preserved, entities round-tripped)
  - `*_test.go` — unit tests; the mock llama-server helpers live in `main_test.go`
- `scripts/` — `download_model.py` (GGUF downloader), `translate.py` (one-shot
  CLI), `e2e.sh` (E2E test; run via `make test-e2e`)
- `k8s/` — manifests used by the E2E test (sumomo + postgres)
- `flake.nix` — dev shells, the `sumomo` / `sumomo-image` packages, and apps
  (`nix run .#sumomo`, `.#download-model`, `.#translate`)

## CI / Release

- `ci.yml`: flake check, `make lint`, `make test`, `make vet`, builds of the
  server package and the image
- `e2e.yml`: `make test-e2e` (separate workflow)
- `release.yml`: on `v*` tag push, builds the image per architecture natively
  (amd64 on `ubuntu-latest`, arm64 on `ubuntu-26.04-arm` runners — **no
  cross-compilation, no VMs/emulation**), pushes per-arch tags, then merges
  them into the final multi-arch tags atomically with
  `docker buildx imagetools create`. The image name/tag is evaluated from the
  flake (`ghcr.io/ushitora-anqou/sumomo`), so rename it there, not in the
  workflow.

- Dependabot (`.github/dependabot.yml`): weekly combined updates for Go
  modules, Actions workflows, and flake.lock — all ecosystems share one
  multi-ecosystem group, so updates arrive as a single PR.

The GGUF model is NOT baked into the image; mount the `models/` directory at
`/models` and pass `--model /models/<file>.gguf`.
