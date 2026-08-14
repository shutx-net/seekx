# seekx

「X」（旧 Twitter）の投稿を、**日時・アカウント・リプライ・画像付き**などの条件で検索して
見やすく表示する、ローカル専用のツールです。X API v2 を直接叩きます。

**X API は従量課金です**（投稿 1 件の取得で $0.005）。seekx は課金される操作すべてに金額を
表示し、実行前に件数を見積れるようにしています。取得したデータは永続化しません。

## インストール

macOS / Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/shutx-net/seekx/main/install.sh | sh
```

`$HOME/.local/bin` に置きます。**sudo は要りません。** checksum（`checksums.txt`）は
スクリプトが自動で検証します。

```sh
# 置き場所やバージョンを指定する
curl -fsSL https://raw.githubusercontent.com/shutx-net/seekx/main/install.sh | sh -s -- --dir /usr/local/bin
curl -fsSL https://raw.githubusercontent.com/shutx-net/seekx/main/install.sh | sh -s -- --version v0.1.0
```

Windows（PowerShell）:

```powershell
$version = (Invoke-RestMethod https://api.github.com/repos/shutx-net/seekx/releases/latest).tag_name
Invoke-WebRequest "https://github.com/shutx-net/seekx/releases/download/$version/seekx_${version}_windows_amd64.zip" -OutFile seekx.zip
Expand-Archive seekx.zip -DestinationPath . -Force
.\seekx.exe --version
```

手動ダウンロード：  

**→ [最新リリース](https://github.com/shutx-net/seekx/releases/latest)**

`checksums.txt` を同じリリースから落とせば手で検証できます。

```sh
file=seekx_v0.1.0_linux_amd64.tar.gz          # 落としたファイル名
grep "$file" checksums.txt | sha256sum -c -   # macOS は shasum -a 256 -c -
```

## 起動

```sh
./seekx        # http://localhost:5050
```

起動したら `http://localhost:5050/settings` で Bearer Token を入力してください。

**課金ゼロで画面だけ見る**場合はフィクスチャモードで起動します（API を叩きません）。

```sh
SEEKX_FIXTURE_DIR=testdata ./seekx
```

ソースから動かす場合（devcontainer / docker / ホスト）は [DEVELOPERS.md](DEVELOPERS.md) にあります。

## 使い方

1. フォームに検索条件を入力します。
2. **「件数を見積る（$0.005）」** で総件数・時系列・予想コストを確認します。
3. **「この条件で検索する」** で投稿を取得します（**ここで初めて post read が課金されます**）。
4. ページングリンクには必ず金額が表示されます。

### 留意点

- 既定でクエリに `-is:retweet` が付きます。外すと結果の 7〜9 割がリポストで埋まり、同じ内容に
  post read を浪費します（実測値）。必要なら「リポストを含める」で外せます。

## 画面

- 結果ページは 2 カラムです。左のサイドバーに**操作**（列数の切り替え・絞り込み）と情報、
  右は投稿カードだけを置いています。サイドバーは上から**「注意 → 操作 → 参照」**の順で、
  よく触るものほど上にあります。
- 表示は **1 / 2 / 3 列**を切り替えられます。選択はブラウザに保存され、**切り替えても再検索は
  起きません**。列数を変えてもサイドバーの位置は動きません。画面が狭いときは自動的に列数を減らします。
- 取得済みの投稿を**絞り込めます**（文字数・メディア数・いいね数・種別）。API を呼ばないので
  追加のコストはかかりませんが、**絞り込んでもコストは下がりません**（取得した時点で課金済みです）。
  効くのは表示中のページ内だけで、ページを移動すると解除されます。

## コスト

| 操作 | コスト |
| --- | --- |
| 投稿の取得 | $0.005 / 件（`max_results=10` なら $0.05、100 なら $0.50） |
| counts（直近 7 日） | $0.005 / リクエスト |
| counts（全期間） | $0.010 / リクエスト |
| HTTP 400 / 429 | 課金なし |

- **まずフィクスチャモードで画面を確認してください**（完全に無料です）。
- 実検索は `max_results=10`（$0.05）から始めてください。条件を試す段階では counts の見積り
  （$0.005）が最も安い偵察手段です。
- **全ページが GET です。ブラウザのリロードや「戻る」で再検索が走ります。** 画面ヘッダの
  セッション累計コストを常に確認してください。
- 同じ検索の再実行は、24 時間 UTC 窓のサーバ側重複排除により追加の金銭コストが発生しにくい
  ですが、月次 cap とレートリミットは消費します。
- 月次 cap（既定 2,000,000 post reads）の 80% で警告、95% で実行を停止します。
- セッション累計が `SEEKX_SESSION_BUDGET_USD`（既定 $5.00）を超えると実行を停止します。

## 設定

ポート・タイムゾーン・予算上限などは環境変数で変えられます（`.env` でも可）。
一覧は [DEVELOPERS.md](DEVELOPERS.md#設定) にあります。

## 安全に使うための注意

- **公開ネットワークでは起動しないでください。** seekx は認証を持ちません。到達できる相手は
  誰でも課金操作を実行できます。既定でループバックにバインドするのはこのためです。
- コンテナで動かす場合は `SEEKX_BIND=0.0.0.0` が必要で、ネットワーク構成次第では LAN の
  他の端末から画面に到達できます。
- **トークンは設定画面から入力すればディスクに一切残りません。** 画面・ログ・URL にも
  出力しません（マスク表示のみ）。
- 取得したデータは永続化しません（プロセスメモリのみ）。データベースもキャッシュも検索履歴も
  持ちません。

仕組みと判断の理由は [docs/security.md](docs/security.md) にあります。

## 制限

X の表示要件に対して、次の 3 点が未対応です。

- 本文中の `@mention` / ハッシュタグ / URL をリンク化していません（本文は未加工で表示します）。
- 編集済み投稿の編集履歴を表示しません。
- リポストを表示する際、リポストした人を明示しません（「リポスト」バッジのみ）。

対応状況の全体は [docs/display-requirements.md](docs/display-requirements.md) にあります。
