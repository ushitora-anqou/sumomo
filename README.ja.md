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
$ docker run --rm -p 5000:5000 -v "$PWD/models:/models:ro" \
    ghcr.io/ushitora-anqou/sumomo \
    --model /models/plamo-3-translate-0.8b-Q4_K_M.gguf
```

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

### 主なオプション

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

```console
$ nix develop          # Go + Python + llama.cpp CLI
$ make vet             # gofmt チェック + go vet
$ make fmt             # gofmt
$ make build           # Go バイナリを bin/sumomo にビルド
$ make build-image     # 実行アーキテクチャのコンテナイメージをビルド
```

CI(GitHub Actions)のワークフローは次のとおりです。

- **CI**(`ci.yml`): flake check、gofmt/go vet、サーバーとイメージのビルド
- **E2E**(`e2e.yml`): `make test-e2e` を実行し Mastodon 連携を検証
- **Release image**(`release.yml`): `v*` タグの push で amd64 + arm64 の
  マルチアーキイメージをビルドして `ghcr.io/ushitora-anqou/sumomo` に公開
  (arm64 は Nix のクロスコンパイルによるビルド)

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
