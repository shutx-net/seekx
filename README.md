# seekx

「X」（旧 Twitter）の投稿を、**日時・アカウント・リプライ・画像付き**などの条件で検索して見やすく表示する、
ローカル専用のツールです。X API v2 を直接叩き、Go 標準ライブラリのみで実装しています。

## 使い方

1. フォームに条件を入力します。
2. **「件数を見積る（$0.005）」** で counts エンドポイントを 1 回叩き、総件数・時系列・予想コストを確認します。
3. 見積り画面の **「この条件で検索する」** で実際に投稿を取得します（ここで初めて post read が課金されます）。
4. 結果画面のページングリンクには必ず金額が表示されます。

条件だけでは検索できない組み合わせがあります（X API の仕様）。「画像付き」「言語」「返信のみ」などは
**単独では使えません**。キーワード・アカウント（`from:` など）・ハッシュタグのいずれかを 1 つ以上
指定してください。seekx は API に送る前にこれを検出してエラー表示します（無駄な 400 を避けます）。

既定でクエリ末尾に `-is:retweet` が付きます。付けないと結果の大半がリポストで埋まり、
同じ内容に対して post read を無駄に消費します（実測で 10 件中 7〜9 件がリポスト）。
「リポストを含める」で外せます。

## 表示

- 検索結果は 1 / 2 / 3 列で表示を切り替えられます。選択はブラウザに保存され、URL には含まれません
  （切り替えで再検索は起きません）。画面幅が狭いときは表示上の列数を自動的に減らします。
- 結果ページには取得済みの投稿に対する絞り込み（本文の文字数・メディア枚数・いいね数・種別）が
  あります。API を再度呼ばないため追加のコストは発生しませんが、**絞り込んでもコストは下がりません**
  （post read は取得した時点で課金済みです）。絞り込みは表示中のページ内だけに効きます
  （ページを移動すると解除されます）。
- 列数の切り替えと絞り込みは JavaScript で動作します。JS を無効にした場合、コントロールは
  表示されず、全件が 1 列で読めます。

## コストの注意

| 操作 | コスト |
| --- | --- |
| 投稿の取得 | $0.005 / 件（`max_results=10` なら $0.05、100 なら $0.50） |
| counts（直近 7 日） | $0.005 / リクエスト |
| counts（全期間） | $0.010 / リクエスト |
| HTTP 400 / 429 | 課金なし |

- **まず `SEEKX_FIXTURE_DIR=testdata` で UI を確認してください**（完全に無料です）。
- 実検索は `max_results=10`（$0.05）から始めてください。
- 条件を試行錯誤する段階では counts の見積り（$0.005）が最も安い偵察手段です。
- 同じ検索の再実行は、24 時間 UTC 窓のサーバ側重複排除により追加の金銭コストが発生しにくいですが、
  月次 cap とレートリミットは消費します。
- **全ページが GET です。ブラウザのリロードや「戻る」で再検索が走ります。** 画面ヘッダの
  セッション累計コストを常に確認してください。
- 月次 cap（既定 2,000,000 post reads）の 80% で警告、95% で実行停止します。
- セッション累計が `SEEKX_SESSION_BUDGET_USD`（既定 $5.00）を超えると実行を停止します。

## 環境変数

| 変数 | 既定値 | 説明 |
| --- | --- | --- |
| `BEARER_TOKEN` | （なし） | **必須**。`.env` から読みます。フィクスチャモードでは不要です。 |
| `PORT` | `5050` | 変更する場合は `devcontainer.json` の `appPort` と `forwardPorts` も同時に変更し、コンテナを作り直してください。 |
| `SEEKX_FIXTURE_DIR` | （空） | 設定すると API を叩かず、そのディレクトリの JSON を返します（課金ゼロ）。 |
| `SEEKX_FIXTURE_FILE` | （空） | 検索フィクスチャのファイル名を個別に上書きします。 |
| `SEEKX_API_BASE` | `https://api.x.com/2` | 偽サーバに向けるなど、テスト用。 |
| `SEEKX_RAW_DUMP_DIR` | （空） | 実レスポンスの生 JSON を保存します（フィクスチャ作成用。本文のみでヘッダは保存しません）。 |
| `SEEKX_SESSION_BUDGET_USD` | `5.00` | セッション累計コストの上限。超えると実行を停止します。 |
| `SEEKX_ALLOW_OVER_CAP` | （未設定） | `1` にすると月次 cap 95% 超でも実行できます。 |
| `SEEKX_MAX_RESULTS_CAP` | `100` | UI に出す取得件数の上限。 |
| `SEEKX_TZ` | `Asia/Tokyo` | 入力・表示のタイムゾーン。 |
| `SEEKX_ENV_FILE` | `.env` | `.env` の場所を変える場合。 |
| `SEEKX_LOG_LEVEL` | `info` | `debug` で詳細ログ（トークンは出力しません）。 |
| `SEEKX_ALLOWED_HOSTS` | （空） | `Host` ヘッダとして受け付けるホスト名をカンマ区切りで**追加**します。既定の `localhost` / `127.0.0.1` / `::1` は常に有効です（ポート番号は書いても無視されます）。 |

## ファイル構成

| パス | 役割 |
| --- | --- |
| `cmd/seekx/main.go` | エントリポイント、ルーティング、HTTP ハンドラ、ページの ViewModel、テンプレートの embed |
| `cmd/seekx/templates/` | `html/template`（`//go:embed`） |
| `cmd/seekx/static/` | CSS・X 公式ロゴ・JS（`//go:embed`） |
| `cmd/seekx/static/js/` | `boot.js`（`<head>` 同期）と `app.js`（`</body>` 直前） |
| `internal/xapi/` | X API v2 クライアントとレスポンス型。`Endpoint`・`SearchParams`・公表単価もここ（リトライ、エラー分類、ペーシング、フィクスチャモード） |
| `internal/view/` | `includes` の結合、ViewModel（`ViewPost` など）の構築、一覧の絞り込みキー（純粋関数のみ） |
| `internal/query/` | フォームから検索クエリを組み立て、全バリデーション（V01〜V14）を行う |
| `internal/cost/` | コスト見積り、月次 cap ガード、セッション累計 |
| `internal/config/` | `Config`、`.env` パーサ、`DefaultPort`、タイムゾーン解決 |
| `testdata/` | テスト・フィクスチャモード用の JSON（リポジトリ直下のまま） |

`static/` の配置規約: **同じ種類のファイルが 2 つ以上になったらサブディレクトリに分けます。**
JS は 2 ファイルあるため `static/js/` 配下です。CSS とロゴは 1 つずつなので直下に置いています。

依存の向きは一方向です（`go build ./...` が循環の不在を保証します）。

```
cmd/seekx  ──> config, query, cost, view, xapi
cost       ──> config, xapi
query      ──> xapi
view       ──> xapi                （query と cost を import しない）
config     ──> 標準ライブラリのみ
xapi       ──> 標準ライブラリのみ   （internal を一切 import しない葉）
```

## 起動

設定ファイルが `.devcontainer/go/` 配下にあるため、devcontainer CLI の自動検出が曖昧になります。
**すべてのコマンドで `--config` を明示してください。**

```sh
# 課金ゼロのフィクスチャモードで UI を確認する
devcontainer exec --workspace-folder . --config .devcontainer/go/devcontainer.json \
  env SEEKX_FIXTURE_DIR=testdata go run ./cmd/seekx

# 実 API に接続して起動する（課金が発生します）
devcontainer exec --workspace-folder . --config .devcontainer/go/devcontainer.json go run ./cmd/seekx
```

`.env` はプロセスの作業ディレクトリから解決します。上のようにリポジトリ直下から
`go run ./cmd/seekx` する限り、`.env` と `SEEKX_FIXTURE_DIR=testdata` の位置は変わりません。

## 表示要件（X の Display requirements）への対応

各投稿カードで以下を満たしています（`cmd/seekx/templates/_post.html`）。

- 投稿者の表示名・`@username`・プロフィール画像を表示し、**いずれもプロフィールへリンク**しています。
- プロフィール画像は表示名・`@username` の**左**に配置しています。
- 本文は未加工の全文です（切り詰め・`<br>` 置換・絵文字置換を行いません。改行は CSS の
  `white-space: pre-wrap` で表現します）。280 文字超の投稿は `note_post.text` の全文を表示します。
- X 公式ロゴを投稿カードの**右上**に置き、恒久リンク（`https://x.com/{username}/status/{id}`）にしています。
  ロゴは X 公式ブランドツールキット（`about.x.com/en/who-we-are/brand-toolkit` の `x-logo.zip`）の
  `logo.svg` を**改変せず**`cmd/seekx/static/x-logo.svg` として配置しています。白抜き版のため、
  ブランドガイドラインに沿って黒地の上に表示しています。
- タイムスタンプを表示し、恒久リンクにしています。返信・リポスト・いいねのアクションは実装しないため、
  代わりにタイムスタンプの隣に「X で表示」を置いています（表示要件が認めている代替手段）。
- `possibly_sensitive` が真の投稿はメディアを既定で隠します（本文は隠しません）。

**「X 上で削除されたコンテンツを 24 時間以内に削除する」要件について**: seekx は取得結果を
一切永続化しません（データベース・ファイルキャッシュ・検索履歴を持たず、レスポンスは
リクエストごとに組み立てて破棄します）。したがってこの要件は設計上自動的に満たされます。

### 未対応（既知の非準拠）

- **本文中のエンティティ（`@mention` / ハッシュタグ / URL）のリンク化**を行っていません。
  表示要件はこれを求めていますが、本バージョンでは「本文を未加工で出す」方針を優先しています。
  対応するには `post.fields` に `entities` を追加し、`entities.urls` の `display_url` / `url` を
  使って正確にリンク化する必要があります。
- 編集済み投稿（edit history）の表示に対応していません。
- リポストを表示する場合の「リポストした人の表示名 + リポストアイコン」の明示に対応していません
  （「リポスト」バッジのみ）。

第三者への再配布は行わない、ローカル単一ユーザー用途を前提としています。

## セキュリティ上の注意

- サーバは `0.0.0.0` にバインドします。コンテナ内のループバックはホストから到達できないためです。
  副作用として、ネットワーク構成次第では LAN の他の端末から UI に到達できる可能性があります。
  **公開ネットワークでは起動しないでください**（第三者が課金操作を実行できます）。
  ホストのファイアウォール設定に依存する前提です。
- Bearer Token はサーバプロセス内にのみ保持し、HTML・ログ・エラー画面・URL には一切出力しません
  （起動ログには先頭 4 文字と文字数のマスク表示のみ）。UI から設定する口は用意していません。
  トークンが HTTP 境界を一度も越えないことが、認証を持たないこのサーバの安全性の根拠です。

### リクエストの境界チェック

seekx は認証を持ちません。そのため、ブラウザを踏み台にした 2 つの経路を入口で塞いでいます
（`cmd/seekx/guard.go`）。いずれも該当したリクエストは本文を処理せず 403 を返します。

**1. 他サイト起点のリクエストを拒否します。**
課金が発生する経路は `GET /search?mode=execute` という GET リクエストなので、対策が無いと
他サイトに置かれた `<img src="http://localhost:5050/search?mode=execute&…">` を踏むだけで
課金が発火します（レスポンスは同一オリジンポリシーで読めませんが、課金は成立します）。

判定には `Sec-Fetch-Site` を使い、`same-origin`（アプリ内の遷移）と `none`（アドレスバー・
ブックマーク）だけを通します。ヘッダが無い場合は `Origin` にフォールバックし、どちらも
無い場合は通します（curl などブラウザ以外からの呼び出し。CSRF はブラウザを踏み台にする
攻撃なので、ヘッダを自由に作れる相手を止めても意味がありません）。

Fetch Metadata の一般的な方針は「トップレベル遷移かつ GET」なら他サイトからでも通しますが、
seekx は意図的にそこから外れています。課金経路がまさに GET のトップレベル遷移であり、
他サイトから正当にリンクされる場面が無いためです。

**2. 想定外の `Host` ヘッダを拒否します（DNS rebinding 対策）。**
攻撃者のドメインが `127.0.0.1` に解決するよう仕込まれると、攻撃者のページがこのアプリと
同一オリジンになりレスポンスまで読めてしまいます。ブラウザが送る `Host` は攻撃者の
ドメインのままなので、そこで落とします。既定の許可は `localhost` / `127.0.0.1` / `::1` で、
比較はホスト名のみ（ポート転送でポートが食い違い得るため）。別のホスト名で開く場合は
`SEEKX_ALLOWED_HOSTS` に追加してください。

この 2 つは LAN 上の第三者からの直接アクセスを防ぐものではありません（ブラウザを
経由しない攻撃者には効きません）。上に書いた「公開ネットワークでは起動しない」という
前提は変わりません。

### XSS 対策

seekx は X API から取得した信頼できない文字列（本文・表示名・alt テキスト・メディア URL）を描画します。

**一次防御は `html/template` の文脈依存自動エスケープです。** `template.HTML` / `template.JS` /
`template.URL` による迂回はコードベースに 1 箇所もありません。JS 側も `textContent` と
`setAttribute` のみで、`innerHTML` などのシンクを持ちません。

**二次防御として CSP ヘッダを付けています。** 全レスポンス（`/static/` を含む）に付与されます。

```
default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline';
img-src 'self' https://pbs.twimg.com; media-src 'self' https://video.twimg.com;
form-action 'self'; base-uri 'none'; frame-ancestors 'none'; connect-src 'none'
```

これは**「スクリプトについては厳格な CSP」であり、厳格な CSP ではありません。**
`style-src` に `'unsafe-inline'` が必要な理由は、項目ごとに値が変わるインライン `style` 属性が
2 箇所あるためです（`estimate.html` のバー幅、`_media.html` の `aspect-ratio`）。
どちらも CSS クラスには追い出せず、**nonce は `<style>` 要素にしか効かず `style` 属性には
適用されません**。スタイル注入はスクリプト注入より影響が小さいため、この妥協を受け入れています。

CSP が実際に守るのは、**将来の変更が API 由来の値を `template.HTML` に通してエスケープを
失った場合**です。そのとき実行そのものを止めます。

あわせて `X-Content-Type-Options: nosniff` と `Referrer-Policy: no-referrer` を設定しています。
後者は現時点の漏洩を止めるものではありません（最近のブラウザの既定
`strict-origin-when-cross-origin` でも、`pbs.twimg.com` への画像リクエストに検索語を含む
クエリ文字列は乗りません）。既定に依存しなくなるという意味での固めです。

**`nosniff` と `.js` の MIME タイプは連動しています。** `.js` が
`Content-Type: text/javascript` で配信されないと、`nosniff` によりブラウザがスクリプトの
実行を拒否します。`TestJavaScriptContentType` で固定しています。

JS は `static/js/boot.js`（`<head>` で同期読み込み。列数を最初のペイントより前に当てるため
`defer` / `async` は不可）と `static/js/app.js`（`</body>` 直前）の 2 ファイルです。
外部 CDN は使わず、`<script src>` は `/static/` 配下のみに限定しています。
