# 開発者向け

利用者向けの手順（GitHub Release のバイナリを落として動かす）は [README](README.md) にあります。
このファイルはソースから動かす場合の手順です。

外部依存はありません。Go 1.22 以上があれば devcontainer も docker も要りません。

## devcontainer CLI で起動する

設定ファイルが `.devcontainer/go/` 配下にあるため、devcontainer CLI の自動検出が曖昧になります。
**すべてのコマンドで `--config` を明示してください。**

```sh
# 初回のみ: コンテナを起動する
devcontainer up --workspace-folder . --config .devcontainer/go/devcontainer.json

# 課金ゼロのフィクスチャモードで起動する
devcontainer exec --workspace-folder . --config .devcontainer/go/devcontainer.json \
  env SEEKX_FIXTURE_DIR=testdata go run ./cmd/seekx

# 実 API に接続して起動する（課金が発生します）
devcontainer exec --workspace-folder . --config .devcontainer/go/devcontainer.json go run ./cmd/seekx
```

`http://localhost:5050` で開きます。実 API に接続する場合は `/settings` で Bearer Token を
入力してください（`.env` には書きません。→ [docs/security.md](docs/security.md)）。

## docker で起動する

devcontainer CLI を使わない場合は、公式の Go イメージでそのまま動きます。Dockerfile は要りません。

```sh
docker run --rm -it -p 5050:5050 -v "$PWD":/w -w /w \
  -e SEEKX_BIND=0.0.0.0 -e SEEKX_FIXTURE_DIR=testdata \
  golang:1.22-alpine go run ./cmd/seekx
```

外部依存が無いので alpine で足ります（`1.22` は `go.mod` が宣言している下限で、CI と同じです）。
実 API に接続する場合は `-e SEEKX_FIXTURE_DIR=testdata` を外してください。

**コンテナで動かす場合は `SEEKX_BIND=0.0.0.0` が必須です。** seekx の既定は `127.0.0.1` で、
コンテナ内のループバックはホストから到達できません（`devcontainer.json` は `containerEnv` で
同じ値を渡しています）。

## ホストで直接起動する

```sh
CGO_ENABLED=0 go build -o seekx ./cmd/seekx && ./seekx
```

既定がループバックなので追加の指定は要りません。テンプレートと静的アセットは `//go:embed` で
バイナリに入るため、**バイナリ 1 つで動きます**（リポジトリのファイルは不要です）。

## 検証

テストは実 API を叩きません。`httptest` の偽サーバと `testdata/` のフィクスチャだけで完結します。

```sh
go build ./...
go vet ./...
gofmt -l .          # 出力があれば失敗
go test -race ./...
```

devcontainer 経由で回す場合は各行の前に
`devcontainer exec --workspace-folder . --config .devcontainer/go/devcontainer.json` を付けます。

この 4 つは CI（`.github/workflows/ci.yml`）とリリース（`release.yml`）が回すものと同じです。
**増減させるときは 3 か所（このファイル・`ci.yml`・`release.yml`）を更新してください。**

## 設定

設定はすべて環境変数です。`.env` に書くこともできます（プロセスの作業ディレクトリから解決します）。

### よく使うもの

| 変数 | 既定値 | 説明 |
| --- | --- | --- |
| `PORT` | `5050` | 待ち受けポート。 |
| `SEEKX_FIXTURE_DIR` | （空） | 設定すると API を叩かず、そのディレクトリの JSON を返します（**課金ゼロ**）。 |
| `SEEKX_TZ` | `Asia/Tokyo` | 入力・表示のタイムゾーン。 |
| `SEEKX_MAX_RESULTS_CAP` | `100` | 画面に出す取得件数の上限。 |
| `SEEKX_ALLOW_OVER_CAP` | （未設定） | `1` にすると月次 cap 95% 超でも実行できます。 |

### ネットワークと認証

| 変数 | 既定値 | 説明 |
| --- | --- | --- |
| `BEARER_TOKEN` | （なし） | **通常は設定しません。**設定画面（`/settings`）から入力する方針です。起動のたびの入力を避けたい場合に限り書けます（設定画面の値が優先）。フィクスチャモードでは不要です。→ [docs/security.md](docs/security.md) |
| `SEEKX_BIND` | `127.0.0.1` | バインドするアドレス。既定はループバックのみで LAN からは到達できません。**コンテナで動かす場合は `0.0.0.0` が必要です。** |
| `SEEKX_ALLOWED_HOSTS` | （空） | `Host` として受け付けるホスト名をカンマ区切りで**追加**します。既定の `localhost` / `127.0.0.1` / `::1` は常に有効です（ポート番号は書いても無視されます）。 |

### その他

| 変数 | 既定値 | 説明 |
| --- | --- | --- |
| `SEEKX_ENV_FILE` | `.env` | `.env` の場所を変える場合。 |
| `SEEKX_LOG_LEVEL` | `info` | `debug` で詳細ログ（トークンは出力しません）。 |
| `SEEKX_API_BASE` | `https://api.x.com/2` | 偽サーバに向けるなど、テスト用。 |
| `SEEKX_FIXTURE_FILE` | （空） | 検索フィクスチャのファイル名を個別に上書きします。 |
| `SEEKX_RAW_DUMP_DIR` | （空） | 実レスポンスの生 JSON を保存します（フィクスチャ作成用。本文のみでヘッダは保存しません）。 |

### 覚えておくと迷わない点

- **`.env` とフィクスチャは作業ディレクトリからの相対解決です。** リポジトリ外から起動する場合は
  `SEEKX_ENV_FILE` とフィクスチャのパスを絶対パスで渡してください。トークンは設定画面から
  入れる方針なので、`.env` 自体が無くても起動できます。
- `testdata/` はリポジトリ直下に置いたままです（`SEEKX_FIXTURE_DIR=testdata` はリポジトリ直下を
  作業ディレクトリとして起動する前提のため）。各パッケージのテストは `../../testdata` で参照します。
- タイムゾーンのデータベースはバイナリに同梱してあるため、システム側に無い環境（Windows など）でも
  `SEEKX_TZ` は正しく解決されます。名前が誤っている場合は起動時に警告を出し、UTC で表示します。
- **`PORT` と `SEEKX_BIND` をコンテナで変えるときはコンテナを作り直してください。**
  `devcontainer.json` の `appPort` / `forwardPorts` / `containerEnv` は作成時に適用されます。

## 設計の記録

| ファイル | 内容 |
| --- | --- |
| [docs/security.md](docs/security.md) | バインド方針、Bearer Token の扱い、リクエストの境界チェック、XSS 対策 |
| [docs/ui.md](docs/ui.md) | 結果ページのレイアウト方針 |
| [docs/display-requirements.md](docs/display-requirements.md) | X の表示要件への対応と既知の非準拠 |
