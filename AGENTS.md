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
devcontainer exec --workspace-folder . --config .devcontainer/go/devcontainer.json go run ./cmd/seekx
```

テストは実 API を叩きません。`httptest` の偽サーバと `testdata/` のフィクスチャだけで完結します。

```sh
devcontainer exec --workspace-folder . --config .devcontainer/go/devcontainer.json go build ./...
devcontainer exec --workspace-folder . --config .devcontainer/go/devcontainer.json go vet ./...
devcontainer exec --workspace-folder . --config .devcontainer/go/devcontainer.json go test ./...
```

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

`testdata/` はリポジトリ直下に置いたままです（`SEEKX_FIXTURE_DIR=testdata` は
リポジトリ直下を作業ディレクトリとして起動する前提のため）。各パッケージのテストは
`../../testdata` で参照します。
