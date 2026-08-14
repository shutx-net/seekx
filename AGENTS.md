# 「X」ツイート検索システム

## 概要
- APIを使い、「日時」や「アカウント」、「リプライ」、「画像付き」などの条件をパラメータとして検索・取得する
- 実行は、現状はローカル環境を想定
- 取得したツイートは、画像含めてわかりやすいように表示すること（具体的なUI/UXは未定）

## 実装上の注意点
- 関心の分離
- 環境変数などの設定値の一元管理

## 起動

設定ファイルが `.devcontainer/go/` 配下にあるため、devcontainer CLI の自動検出が曖昧になります。
**すべてのコマンドで `--config` を明示してください。**

```sh
# 初回のみ: コンテナを起動する
devcontainer up --workspace-folder . --config .devcontainer/go/devcontainer.json

# 課金ゼロのフィクスチャモードで UI を確認する
devcontainer exec --workspace-folder . --config .devcontainer/go/devcontainer.json \
  env SEEKX_FIXTURE_DIR=testdata go run ./cmd/seekx

# 実 API に接続して起動する（課金が発生します）
# 起動後 http://localhost:5050/settings で Bearer Token を入力します（.env には書きません）
devcontainer exec --workspace-folder . --config .devcontainer/go/devcontainer.json go run ./cmd/seekx
```

テストは実 API を叩きません。`httptest` の偽サーバと `testdata/` のフィクスチャだけで完結します。

```sh
devcontainer exec --workspace-folder . --config .devcontainer/go/devcontainer.json go build ./...
devcontainer exec --workspace-folder . --config .devcontainer/go/devcontainer.json go vet ./...
devcontainer exec --workspace-folder . --config .devcontainer/go/devcontainer.json go test ./...
```

## CI

`.github/workflows/ci.yml` が push（main）と pull request で同じ検証を回します。

- `go build ./...` / `go vet ./...` / `gofmt -l .`（出力があれば失敗）/ `go test -race ./...`
- Go は `1.22`（go.mod が宣言している下限）と `stable` の 2 つで実行します。
  手元の新しい Go でしか通らないコードを防ぐためです。
- `-race` は外せません。Bearer Token の差し替えなど実行時に可変な状態があり、
  競合はこれでしか検出できません。

検証コマンドを増減するときは、この節と `ci.yml` の両方を更新してください。

## パッケージ構成

エントリポイントは `cmd/seekx`、実装は `internal/` 配下です。依存の向きは一方向で、
`go build ./...` が循環の不在を保証します。

```
cmd/seekx  ──> config, query, cost, view, xapi   （main.go・guard.go・token.go・templates/・static/）
cost       ──> config, xapi
query      ──> xapi
view       ──> xapi                （query と cost を import しない）
config     ──> 標準ライブラリのみ
xapi       ──> 標準ライブラリのみ   （internal を一切 import しない葉）
```

- `internal/xapi` … X API についての事実（レスポンス型、`Endpoint`、`SearchParams`、公表単価、HTTP クライアント）
- `internal/view` … `includes` の結合と ViewModel の構築（純粋関数のみ）
- `internal/query` … 検索クエリの組み立てと全バリデーション（V01〜V14）
- `internal/cost` … 見積り、月次 cap ガード、セッション累計（方針の側）
- `internal/config` … `Config`、`.env` パーサ、`DefaultPort`、タイムゾーン解決

`cmd/seekx` 側の分割:

- `main.go` … 起動、ルーティング、ハンドラ、ViewModel
- `guard.go` … リクエストの境界チェック（cross-site 検証・Host 検証）
- `token.go` … 実行中の Bearer Token の保持と差し替え（メモリのみ・永続化しない）

Bearer Token は `.env` と設定画面の 2 経路から入り、設定画面が優先されます。実行時に
可変なのはこの値だけで、差し替えは `App.applyToken` の 1 箇所に集約しています
（`Client` への反映・usage キャッシュの破棄・セッション累計のリセットがばらけないように）。

**運用方針: トークンは `.env` に書かず、設定画面（`/settings`）から入力します。**
この運用ではトークンがディスクに一切残りません（代償は起動のたびの入力）。`.env` 経路は
その入力を避けたい場合のためにフォールバックとして残していますが、既定の案内ではありません。
画面・ログ・エラーの文言もこの方針に揃えてあるので、変更するときは併せて直してください。

`testdata/` はリポジトリ直下に置いたままです（`SEEKX_FIXTURE_DIR=testdata` は
リポジトリ直下を作業ディレクトリとして起動する前提のため）。各パッケージのテストは
`../../testdata` で参照します。
