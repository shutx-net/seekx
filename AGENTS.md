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
  env SEEKX_FIXTURE_DIR=testdata go run .

# 実 API に接続して起動する（課金が発生します）
devcontainer exec --workspace-folder . --config .devcontainer/go/devcontainer.json go run .
```

テストは実 API を叩きません。`httptest` の偽サーバと `testdata/` のフィクスチャだけで完結します。
