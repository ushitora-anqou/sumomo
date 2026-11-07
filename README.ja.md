# Sumomo

[LibreTranslate](https://libretranslate.com/) 互換 API サーバー。
バックエンドに翻訳特化モデル
[pfnet/plamo-3-translate-0.8b](https://huggingface.co/pfnet/plamo-3-translate-0.8b)
を [llama.cpp](https://github.com/ggml-org/llama.cpp) で実行し、
CPU オンリーのマシンで 53 言語の翻訳を提供します。
Go 製の単一バイナリで、llama-server を子プロセスとして常駐管理します。
[Mastodon](https://joinmastodon.org/) の翻訳バックエンドとしてそのまま使えます。

## クイックスタート

Nix で実行します。

```console
$ nix run .#download-model          # 公式 GGUF(Q4_K_M, 0.57GB)を models/ に保存
$ nix run .#sumomo                  # http://127.0.0.1:5000 で起動
```

Docker(GHCR のマルチアーキイメージ)でも動きます。

```console
$ docker run --rm -p 5000:5000 -v "$PWD/models:/models" \
    ghcr.io/ushitora-anqou/sumomo \
    --listen "0.0.0.0:5000" \
    --model /models/plamo-3-translate-0.8b-Q4_K_M.gguf
```

`--listen "0.0.0.0:5000"` は必須です(デフォルトの 127.0.0.1 では
コンテナ外からアクセスできません)。モデルファイルが無い場合は
マウントしたディレクトリに自動的にダウンロードします。

翻訳リクエストの例です。

```console
$ curl -s http://127.0.0.1:5000/translate -H 'Content-Type: application/json' \
    -d '{"q":"会議は来週の月曜日に変更されました。","source":"ja","target":"en"}'
{"translatedText":"The meeting has been rescheduled for next Monday."}
```

## API

LibreTranslate の API と互換性があります。

| エンドポイント | 説明 |
|---|---|
| `POST /translate` | 翻訳。`q` は文字列または文字列配列、`source` は `auto` 可、`format` は `text` / `html`(タグ保持) |
| `GET /languages` | 対応言語一覧(53 言語) |
| `POST /detect` | 言語検出(Unicode 文字種による簡易判定) |
| `GET /frontend/settings` | フロントエンド設定 |
| `GET /health` | ヘルスチェック |

LibreTranslate と同じ挙動: `source=auto` 時の `detectedLanguage`、`alternatives`、
バッチ翻訳、数字・記号のみのテキストのパススルー、form エンコードリクエスト、
CORS ヘッダ、同一形式のエラーとステータスコード。
言語コードは `zh`(簡体字)/ `zt`(繁体字)をモデルの `zh-hans` / `zh-hant`
に変換します。

翻訳は常駐モデル上で必ず 1 件ずつ実行されます。並列リクエストは
上限付きの FIFO キュー(`--queue-depth`)で順番待ちし、逐次処理されるため、
高負荷時もリソース消費は一定に保たれます。キューが満杯のリクエストには
溜め込む代わりに即座に 503 を返します。

### 主なオプション

ヘルプ(`sumomo --help`)は英語で表示されます。

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
  -llama-server-extra value
    	extra argument forwarded to llama-server (repeatable, e.g. --llama-server-extra=-fa)
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
    	CPU threads for llama-server (0 = half of logical CPUs, usually the physical core count)
```

モデルファイルが存在しない場合、ファイル名が公式リリースのものと
一致すれば Hugging Face から自動的にダウンロードします。

## Mastodon との連携

Mastodon は `LIBRE_TRANSLATE_ENDPOINT` で LibreTranslate 互換サーバーを翻訳
バックエンドにできます。sumomo をエンドポイントに指定するだけで、投稿の
「翻訳」ボタンが動作します(投稿本文は `format=html` で送られるため、
sumomo はタグを保持したままテキスト部分のみ翻訳します)。

kind 上に [magout](https://github.com/ushitora-anqou/magout) で Mastodon を
立ち上げ、sumomo を連携させる構成が `k8s/` と `scripts/e2e.sh` に
完全に自動化されています。

```console
$ make test-e2e
[e2e] creating kind cluster 'sumomo-e2e'
...
[e2e] translated: <p>会議は来週の月曜日に日程変更となりました。…</p>
[e2e] PASS: Mastodon translated a post through sumomo
```

## 開発

ツールチェーンは Nix の dev shell にあり、[direnv](https://direnv.net/)
(`.envrc` の `use flake`)が自動的にロードします。Make ターゲットに
必要なもの(E2E 用の kind なども)がすべて揃っています。

```console
$ direnv allow         # dev shell をロード(Go、Python、llama.cpp、kind など)
$ make lint            # golangci-lint
$ make fmt             # gofmt
$ make test            # サーバーのユニットテスト
$ make build           # Go バイナリを bin/sumomo にビルド
$ make build-image     # 実行アーキテクチャのコンテナイメージをビルド
$ make bench           # 翻訳負荷時の CPU・メモリ消費を計測
```

`make bench` は sumomo + llama-server の CPU 時間、レイテンシ、ピーク
RSS を報告します。フラグは `SUMOMO_ARGS` 経由(例:
`make bench SUMOMO_ARGS="--threads 2"`)。4 コア / 8 スレッドの環境での
実測では、llama-server が全ハイパースレッドを使うと CPU が約 2 倍に
なる一方でレイテンシは変わらなかったため(1 リクエストあたり 1.51 秒 vs
0.73 秒)、`--threads 0`(デフォルト)は論理 CPU の半分(通常は物理
コア数)に解決します。

CI(GitHub Actions)のワークフローは次のとおりです。

- **CI**(`ci.yml`): flake check、`make lint`(golangci-lint)、`make test`
  (ユニットテスト)、サーバーパッケージとイメージのビルド。
  dev shell は [nix-develop](https://github.com/nicknovitski/nix-develop)
  (コミット SHA でピン)でロードし、ローカルの direnv と同じ形で
  Make ターゲットを実行します
- **E2E**(`e2e.yml`): `make test-e2e` を実行し Mastodon 連携を検証
- **Release image**(`release.yml`): `v*` タグの push で amd64 + arm64 の
  マルチアーキイメージをビルドして `ghcr.io/ushitora-anqou/sumomo` に公開
  (各アーキテクチャを GitHub Actions のネイティブ runner でビルド。
  クロスコンパイルは不使用)

依存関係の更新は Dependabot(`.github/dependabot.yml`)が行います。
Go module・GitHub Actions ワークフロー・`flake.lock` を週次でチェックし、
更新を 1 本の PR にまとめて通知します。

## リリース

```console
$ make release VERSION=0.1.0
```

先頭の `v` は省略でき、必要に応じて補われます。Git タグ(`v0.1.0`)の
作成・push と GitHub Release の作成を行います。タグの push に連動して
CI がマルチアーキ Docker イメージを公開します。

## 補助スクリプト

サーバー本体(sumomo)とは別に、以下の補助スクリプトがあります。

| スクリプト | 説明 |
|---|---|
| `scripts/download_model.py` | 公式 GGUF のダウンローダー(冪等・レジューム対応) |
| `scripts/translate.py` | CLI で 1 回だけ翻訳するスクリプト(llama-server を都度起動) |
| `scripts/e2e.sh` | E2E テスト(`make test-e2e` から実行) |

スクリプトは Nix 経由で実行できます。

```console
$ nix run .#download-model
$ nix run .#translate -- -t en "こんにちは"
```
