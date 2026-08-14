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

バインドの既定は `127.0.0.1`（ループバック）です。コンテナ内のループバックはホストから
到達できないため、`.devcontainer/go/devcontainer.json` が `containerEnv` で
`SEEKX_BIND=0.0.0.0` を渡しています。**この値を変えたときはコンテナを作り直してください**
（`containerEnv` は作成時に適用されます）。

devcontainer は必須ではありません。外部依存が無くテンプレート・静的アセットは embed 済みなので、
ホスト上でバイナリを直接動かせます。既定がループバックなので追加の指定は要りません。
手順は README の「ホストで直接実行する」を参照してください。

```sh
CGO_ENABLED=0 go build -o seekx ./cmd/seekx && ./seekx
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

検証コマンドを増減するときは、この節と `ci.yml`・`release.yml` の 3 つを更新してください。

## リリース

`.github/workflows/release.yml` が `v*` のタグ push で走り、バイナリをビルドして
GitHub Release に添付します。

```sh
git tag v0.1.0
git push origin v0.1.0
```

- 対象は linux / darwin / windows × amd64 / arm64 の 6 つ。外部依存が無いので
  `CGO_ENABLED=0` で 1 ジョブから全部クロスコンパイルします。
- 実行ビットを保つため tar.gz（Windows は zip）に固めます。`checksums.txt` も添付します。
- タグ名は `-ldflags -X main.version=` で埋め込まれ、`seekx --version` と起動ログに出ます。
- `ci.yml` はタグ push では走りません。壊れたバイナリを配らないよう、`release.yml` 側でも
  同じ検証（build / vet / gofmt / test -race）を通してからビルドします。
- 第三者 action は使わず、Release の作成は runner 同梱の `gh` CLI で行います。
- **Release が既にある場合はアセットを添付するだけにします。** GitHub の UI から Release を
  作るとその時点でタグも作られ、それがこの workflow を起動します。「Release が先にあって
  タグ push が後から来る」順序は普通に起きるので、`gh release create` 一本だと必ず衝突します。
  既存の Release のタイトルと本文は書き換えません（UI で書いた内容を残すため）。
- 失敗したリリースは `workflow_dispatch`（タグ名を入力）でやり直せます。タグを打ち直す必要は
  ありません。`workflow_dispatch` は既定ブランチの workflow ファイルで動くため、
  `release.yml` を直せば過去のタグに対しても直した手順で再実行できます。

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
