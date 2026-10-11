package main

// followers_render_test.go はフォロワー一覧（followers.html）のマークアップを固定する。
// 結果ページの render_test.go（TestResultsSidebarLayout など）に当たる。

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shutx-net/seekx/internal/config"
	"github.com/shutx-net/seekx/internal/cost"
	"github.com/shutx-net/seekx/internal/view"
	"github.com/shutx-net/seekx/internal/xapi"
)

// followersFirstPage はフィクスチャの最初のページ（6 人・指標の欠けた 1 人の警告・次のページあり）の URL。
// 見積り画面の取得のリンクと同じ形（TestFollowersExecuteFixture が見積り画面からたどって確かめている）。
const followersFirstPage = "/followers?id=20000001&max=100&mode=execute&username=sample_star"

// csvDataOpen は CSV のデータブロックの開始タグ。
const csvDataOpen = `<script type="application/json" id="csv-data">`

// withoutCSVData は CSV のデータブロックを取り除いた HTML を返す。
//
// ブロックには表示名・自己紹介と CSV の見出し（「鍵アカウント」「表示名」など）が JSON の文字列として入るので、
// マークアップの検査（出現回数・エスケープ漏れ・並び順）はこれを除いてから行う。ブロックの中身は
// TestFollowersCSVDataBlock が別に見る。
//
// ブロックの中で < がエスケープされていないと、最初の </script> で切れて JSON の残りが本文の側に残る。
// その場合も本文の側の検査（TestFollowersNoRawHTML など）が落ちるので、取り除き方のせいで漏れを見逃すことはない。
func withoutCSVData(html string) string {
	for {
		i := strings.Index(html, csvDataOpen)
		if i < 0 {
			return html
		}
		j := strings.Index(html[i:], "</script>")
		if j < 0 {
			return html[:i] // 閉じていないのは想定外。中身の検査（csvDataBlock）が落とす
		}
		html = html[:i] + html[i+j+len("</script>"):]
	}
}

// csvDataBlock は CSV のデータブロックの中身（JSON）を返す。ブロックがちょうど 1 個であることも確かめる。
func csvDataBlock(t *testing.T, html string) string {
	t.Helper()
	if n := strings.Count(html, `id="csv-data"`); n != 1 {
		t.Fatalf("CSV のデータブロックはちょうど 1 個であるべき: got %d", n)
	}
	i := strings.Index(html, csvDataOpen)
	if i < 0 {
		t.Fatalf("CSV のデータブロックの開始タグは %s であるべき", csvDataOpen)
	}
	rest := html[i+len(csvDataOpen):]
	j := strings.Index(rest, "</script>")
	if j < 0 {
		t.Fatal("CSV のデータブロックが閉じていない")
	}
	return rest[:j]
}

// userCards は本文のユーザーカード（<article class="user-card"> から </article> まで）を出現順に返す。
func userCards(html string) []string {
	const open, end = `<article class="user-card">`, "</article>"
	var cards []string
	for {
		i := strings.Index(html, open)
		if i < 0 {
			return cards
		}
		j := strings.Index(html[i:], end)
		if j < 0 {
			return cards
		}
		cards = append(cards, html[i:i+j+len(end)])
		html = html[i+j+len(end):]
	}
}

// followersFixtureBody はフィクスチャモードで target（フォロワー一覧）を開き、200 の本文を返す。
func followersFixtureBody(t *testing.T, target string) string {
	t.Helper()
	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	rec := serveGet(t, newRouter(app), target)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s は 200 を返すべき: status=%d\n%s", target, rec.Code, truncate(rec.Body.String(), 400))
	}
	return rec.Body.String()
}

// TestFollowersSidebarLayout はフォロワー一覧の 2 カラム構造を固定する（結果ページの TestResultsSidebarLayout に当たる）。
// 何がサイドバーに入り、何が入らないかを見る。
func TestFollowersSidebarLayout(t *testing.T) {
	t.Run("サイドバーは注意・操作・参照、カード列はカードと金額付きのリンク", func(t *testing.T) {
		body := withoutCSVData(followersFixtureBody(t, followersFirstPage))

		// 結果ページと同じ CSS（2 カラム・サイドバーぶんの横幅）を当てる手掛かり。サーバが出すので JS に依らない。
		if !strings.Contains(body, `<html lang="ja" data-page="results">`) {
			t.Error(`<html> に data-page="results" が無い（結果ページと同じ 2 カラムの CSS が当たらない）`)
		}

		side := sliceDiv(t, body, `<div class="results-side"`)
		main := sliceDiv(t, body, `<div class="results-main"`)

		// サイドバーに入るもの（操作と参照）。
		for _, want := range []string{`id="list-controls"`, "<legend>表示列数</legend>", `id="csv-download"`, `class="meta-list"`, "今回の推定コスト"} {
			if !strings.Contains(side, want) {
				t.Errorf("サイドバーに %q が無い", want)
			}
		}
		// サイドバーに入れてはいけないもの。ページングと再見積りは金額付きのリンクで、常時見える位置に置くと
		// 誤クリックを招く（docs/ui.md）。カードもカード列だけに置く。
		for _, bad := range []string{`class="paging"`, "人を取得（+", "再見積り", "user-list", `<article class="user-card">`} {
			if strings.Contains(side, bad) {
				t.Errorf("サイドバーに %q を入れてはいけない", bad)
			}
		}
		// カード列に入るもの。
		for _, want := range []string{`id="user-list"`, `class="paging"`, "次の 100 人を取得（+$1.00）", "再見積り"} {
			if !strings.Contains(main, want) {
				t.Errorf("カード列に %q が無い", want)
			}
		}
		// 対照: 操作（列数・CSV 保存）はカード列に置かない。
		for _, bad := range []string{"list-controls", "csv-download", "表示列数"} {
			if strings.Contains(main, bad) {
				t.Errorf("カード列に %q を置いてはいけない（操作はサイドバー）", bad)
			}
		}

		// 【サイドバーそのものを hidden にしてはいけない】参照情報（対象・人数・コスト）は JS が無くても意味がある。
		openTag := body[strings.Index(body, `<div class="results-side"`):]
		openTag = openTag[:strings.Index(openTag, ">")]
		if strings.Contains(openTag, "hidden") {
			t.Error("results-side に hidden を付けてはいけない（JS 無効時に対象とコストが消える）")
		}
		// hidden を付けるのは JS が無いと使えない操作（操作のブロックと CSV のボタン）だけ。
		for _, want := range []string{
			`<div class="list-controls" id="list-controls" hidden>`,
			`<button type="button" id="csv-download" class="secondary" hidden aria-describedby="csv-note">このページを CSV で保存（6 人）</button>`,
			`<p class="hint" id="csv-note">追加のコストはかかりません（API を呼ばず、表示中の 6 人だけを保存します）。</p>`,
		} {
			if !strings.Contains(side, want) {
				t.Errorf("サイドバーに %q が無い", want)
			}
		}
		if n := strings.Count(body, " hidden"); n != 2 {
			t.Errorf("hidden は操作のブロックと CSV のボタンの 2 つだけに付けるべき: got %d", n)
		}

		// 並びは「注意 → 操作 → 参照」（結果ページと同じ）。
		iNotice := strings.Index(side, "notice-warn")
		iControls := strings.Index(side, `id="list-controls"`)
		iMeta := strings.Index(side, `class="meta-list"`)
		if iNotice < 0 {
			t.Fatal("警告が出ていない（フィクスチャの指標が欠けた 1 人が前提）")
		}
		if !(iNotice < iControls && iControls < iMeta) {
			t.Errorf("サイドバーの並びは 注意 → 操作 → 参照 であるべき: notice=%d controls=%d meta=%d", iNotice, iControls, iMeta)
		}

		// 読み上げ順・フォーカス順は「サイドバー → カード」。
		if strings.Index(body, `class="results-side"`) > strings.Index(body, `id="user-list"`) {
			t.Error("サイドバーはカードより前に置く（フォーカス順が変わる）")
		}

		// 操作のブロックにフォーム要素を置かない（D-47）。置くと送信でこの URL を読み直し、再取得（課金）になり得る。
		controls := sliceDiv(t, side, `<div class="list-controls"`)
		for _, bad := range []string{"<form", `type="submit"`, `type="reset"`} {
			if strings.Contains(controls, bad) {
				t.Errorf("list-controls の内側に %q を置いてはいけない", bad)
			}
		}

		// セッション累計はヘッダのバッジだけに出す（結果ページと同じ）。
		if strings.Contains(side, "セッション") {
			t.Error("セッション累計はヘッダのバッジと重複する（サイドバーには出さない）")
		}
		// style 属性を書かない（CSP の style 例外は estimate.html と _media.html の 2 箇所だけ）。
		if strings.Contains(body, " style=") {
			t.Error("フォロワー一覧に style 属性を書いてはいけない（CSP の style 例外を増やさない）")
		}
	})

	t.Run("部分エラーも操作より前に出す", func(t *testing.T) {
		body := withoutCSVData(followersFixtureBody(t, executeTarget("20000001", "sample_star", "100", "FIXTUREPAGE3ZZZZ")))
		side := sliceDiv(t, body, `<div class="results-side"`)
		iPartial := strings.Index(side, "一部のデータを取得できませんでした")
		iControls := strings.Index(side, `id="list-controls"`)
		if iPartial < 0 || iControls < 0 {
			t.Fatalf("部分エラーと操作のブロックがサイドバーに無い（フィクスチャが変わった可能性）: partial=%d controls=%d", iPartial, iControls)
		}
		if iPartial > iControls {
			t.Error("部分エラーは操作より前に置く")
		}
	})
}

// TestFollowersCSVDataBlock は CSV のデータブロックを固定する。html/template が JS の文脈で JSON にして埋めるので、
// 表示名や自己紹介に </script> があっても要素から抜け出せず、ブラウザは JSON として元の CSV を取り出せる。
func TestFollowersCSVDataBlock(t *testing.T) {
	block := csvDataBlock(t, followersFixtureBody(t, followersFirstPage))

	// 【ブロック内に生の < を出してはいけない】出ると自己紹介の </script> で要素が閉じ、続きが HTML として解釈される。
	if strings.Contains(block, "<") {
		t.Errorf("データブロックに生の < がある（エスケープされていない）:\n%s", truncate(block, 400))
	}
	// 対照: 落としたのではなく、JSON の Unicode エスケープ（バックスラッシュ・u003c の 6 文字）の形で入っている。
	if !strings.Contains(block, "\x5cu003c") {
		t.Error("< はエスケープした形でデータブロックに残っているべき（落としてはいけない）")
	}

	// JSON のキーは csv.js が読む名前（filename / csv）で、ほかのキーは無い。
	var data map[string]string
	if err := json.Unmarshal([]byte(block), &data); err != nil {
		t.Fatalf("データブロックを JSON として読めない: %v\n%s", err, truncate(block, 400))
	}
	if len(data) != 2 {
		t.Errorf("データブロックのキーは filename と csv の 2 つであるべき: got %d 個", len(data))
	}
	filename, okName := data["filename"]
	text, okCSV := data["csv"]
	if !okName || !okCSV {
		t.Fatalf("データブロックに filename / csv のキーが無い: %v", data)
	}

	// ファイル名は followers_<ユーザー名>_<表示タイムゾーンの日時>.csv（testApp の現在時刻は 2026-08-13 09:00 JST）。
	if want := "followers_sample_star_20260813-090000.csv"; filename != want {
		t.Errorf("ファイル名: got %q, want %q", filename, want)
	}

	// CSV は BOM と見出し行で始まる（Excel が UTF-8 と判定するため）。
	const bom = "\xef\xbb\xbf"
	if !strings.HasPrefix(text, bom+"ユーザーID,ユーザー名,表示名,") {
		t.Errorf("CSV は BOM と見出し行で始まるべき: %q", truncate(text, 40))
	}
	// 数式になり得るセルには ' が付いている（CSV インジェクション対策）。' の無い =HYPERLINK は無い。
	if !strings.Contains(text, "'=HYPERLINK(") {
		t.Error("数式になり得る表示名は ' を付けて無害化されているべき")
	}
	if strings.Count(text, "=HYPERLINK(") != strings.Count(text, "'=HYPERLINK(") {
		t.Error("' の付いていない =HYPERLINK が CSV に残っている")
	}
	// < や </script> は JSON を読めば元の文字列に戻る（エスケープは HTML に埋めるためだけのもの）。
	for _, want := range []string{"<img src=x onerror=alert(1)>", "</script><script>alert(1)</script>"} {
		if !strings.Contains(text, want) {
			t.Errorf("CSV には元の文字列 %q が入っているべき", want)
		}
	}
	// 行の区切りは CRLF のまま届く（data 属性に入れると、ブラウザが CR を LF に直してしまう）。
	if !strings.Contains(text, "\r\n") {
		t.Error("CSV の CRLF が保たれていない")
	}

	// 中身はサーバの純粋関数（view.FollowersCSVFile）の出力そのもの。カードと同じ 6 人が同じ順に並ぶ。
	var resp xapi.FollowersResponse
	loadFixture(t, "users_followers.json", &resp)
	if want := view.FollowersCSVFile(view.BuildFollowersView(&resp, view.ViewOptions{Loc: jst(t)}).Users); text != want {
		t.Errorf("CSV は表示中のフォロワーから view.FollowersCSVFile で作った内容と一致するべき:\n got  %q\n want %q", truncate(text, 200), truncate(want, 200))
	}
	recs, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(text, bom))).ReadAll()
	if err != nil {
		t.Fatalf("CSV として読めない: %v", err)
	}
	if len(recs) != 1+6 {
		t.Errorf("CSV は見出し + 表示中の 6 人であるべき: got %d 行", len(recs))
	}
}

// TestFollowersNoRawHTML はユーザーカードの表示名・自己紹介の HTML がエスケープされることを確認する（D-24）。
func TestFollowersNoRawHTML(t *testing.T) {
	// 前提: フィクスチャに HTML を含む表示名と自己紹介がある。
	var resp xapi.FollowersResponse
	loadFixture(t, "users_followers.json", &resp)
	var hasName, hasBio bool
	for _, u := range resp.Data {
		hasName = hasName || strings.Contains(u.Name, "<img src=x onerror")
		hasBio = hasBio || strings.Contains(u.Description, "</script>")
	}
	if !hasName || !hasBio {
		t.Fatal("フィクスチャに <img …> を含む表示名と </script> を含む自己紹介が必要")
	}

	body := withoutCSVData(followersFixtureBody(t, followersFirstPage))
	for _, bad := range []string{"<img src=x onerror", "</script><script>alert(1)", "<script>alert(1)", "ZgotmplZ", "<br>"} {
		if strings.Contains(body, bad) {
			t.Errorf("フォロワー一覧に %q を出してはいけない（エスケープ漏れ・未加工でない）", bad)
		}
	}
	for _, want := range []string{
		`<a class="author-name" href="https://x.com/sample_fan05" rel="noreferrer">&lt;img src=x onerror=alert(1)&gt;</a>`,
		`alt="&lt;img src=x onerror=alert(1)&gt; のプロフィール画像"`,
		`aria-label="&lt;img src=x onerror=alert(1)&gt; のプロフィール"`,
		// 自己紹介は改行を残したまま（改行は CSS の pre-wrap で表す）、エスケープして出す。
		"<div class=\"user-bio\">&lt;/script&gt;&lt;script&gt;alert(1)&lt;/script&gt;\n2 行目</div>",
		// 数式のような表示名も、画面ではそのまま出す（' を付けて無害化するのは CSV だけ）。
		`<a class="author-name" href="https://x.com/sample_fan04" rel="noreferrer">=HYPERLINK(&#34;https://example.com&#34;)</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("エスケープした %q が見つからない（描画していない）", want)
		}
	}
}

// TestFollowersDisplayRequirements はユーザーカードが X の表示要件を満たすことを確認する。
// プロフィール画像・表示名・@username はいずれもプロフィール（https://x.com/<username>）へリンクし、
// アバターは表示名と @username の左に置き、X ロゴを添える。鍵アカウントと認証済みの印も出す。
func TestFollowersDisplayRequirements(t *testing.T) {
	body := withoutCSVData(followersFixtureBody(t, followersFirstPage))
	users := []string{"sample_fan01", "sample_fan02", "sample_fan03", "sample_fan04", "sample_fan05", "sample_gov"}
	cards := userCards(body)
	if len(cards) != len(users) {
		t.Fatalf("カードは API が返した順に %d 枚であるべき: got %d", len(users), len(cards))
	}

	// 印のあるユーザー（それ以外のカードには ✔ も鍵の印も出さない）。
	marks := map[string]string{
		"sample_fan02": `<span class="verified" title="認証済み（blue）">✔</span>`,
		"sample_fan03": `<span class="flag">鍵アカウント</span>`,
		"sample_gov":   `<span class="verified" title="認証済み（government）">✔</span>`,
	}
	for i, card := range cards {
		u := users[i]
		profile := "https://x.com/" + u
		for _, want := range []string{
			`<a href="` + profile + `" rel="noreferrer" aria-label=`, // プロフィール画像のリンク
			`<img class="avatar" src="https://`,
			`<a class="author-name" href="` + profile + `" rel="noreferrer">`,
			`<a class="author-handle" href="` + profile + `" rel="noreferrer">@` + u + `</a>`,
			`<a class="x-logo" href="` + profile + `" title="X で表示" rel="noreferrer">`,
			`<img src="/static/x-logo.svg" alt="X"`,
		} {
			if !strings.Contains(card, want) {
				t.Errorf("%s のカードに %q が無い", u, want)
			}
		}
		iAvatar := strings.Index(card, `class="avatar"`)
		iName := strings.Index(card, `class="author-name"`)
		iHandle := strings.Index(card, `class="author-handle"`)
		iLogo := strings.Index(card, `class="x-logo"`)
		if !(0 <= iAvatar && iAvatar < iName && iName < iHandle && iHandle < iLogo) {
			t.Errorf("%s: 並びはアバター → 表示名 → @username → X ロゴであるべき: %d %d %d %d", u, iAvatar, iName, iHandle, iLogo)
		}

		if mark, ok := marks[u]; ok {
			if !strings.Contains(card, mark) {
				t.Errorf("%s のカードに %q が無い", u, mark)
			}
		} else if strings.Contains(card, "✔") || strings.Contains(card, "鍵アカウント") {
			t.Errorf("%s は認証済みでも鍵アカウントでもないので、印を出してはいけない", u)
		}
	}

	// アイコンを設定していないユーザーは既定のアイコン（abs.twimg.com・CSP の img-src で許可済み）を 200x200 で出す。
	if !strings.Contains(cards[4], `src="https://abs.twimg.com/sticky/default_profile_images/default_profile_200x200.png"`) {
		t.Error("既定のプロフィール画像も 200x200 に置き換えて出すべき")
	}
}

// TestFollowersZeroHasNoControls は、フォロワーが 0 人のページに操作（列数・CSV 保存）とデータブロックを
// 出さないことを確認する（結果ページの TestZeroResultsHasNoControls に当たる）。保存するものが無い。
func TestFollowersZeroHasNoControls(t *testing.T) {
	body := followersFixtureBody(t, executeTarget("20000003", "fx_empty", "100", ""))
	for _, bad := range []string{"list-controls", "表示列数", "csv-download", "CSV で保存", "csv-data", "application/json", "user-list"} {
		if strings.Contains(body, bad) {
			t.Errorf("0 人のときに %q を出力してはいけない", bad)
		}
	}
	// 対照: 0 人の通知と参照情報は残る。
	for _, want := range []string{
		"フォロワーは 0 人でした。",
		"課金は発生していません",
		`<div class="results-side">`,
		`表示中 <span class="strong">0 人</span>（1 ページ 100 人）`,
		`今回の推定コスト: <span class="strong">$0.000</span>（0 人 × $0.010）`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("0 人のページに %q が無い", want)
		}
	}
}

// TestFollowersPagingBlocked は、ゲートが閉じたときに前後のページのリンクを無効なボタンにすることを確認する。
// ユーザーの読み取りはいまは月次 cap で止めない（常に許可）ので、FollowersPage を直接描画して確かめる。
func TestFollowersPagingBlocked(t *testing.T) {
	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	var resp xapi.FollowersResponse
	loadFixture(t, "users_followers.json", &resp)
	const (
		next = "/followers?id=1&max=100&mode=execute&token=NEXTNEXTNEXTNEXT&username=sample"
		prev = "/followers?id=1&max=100&mode=execute&token=PREVPREVPREVPREV&username=sample"
	)
	p := FollowersPage{
		Page:           app.page("フォロワー一覧", cost.CapStatus{}, xapi.EndpointRecent),
		Result:         view.BuildFollowersView(&resp, view.ViewOptions{Loc: jst(t), SpentUSD: cost.FollowersCostFor(len(resp.Data))}),
		TargetUsername: "sample",
		TargetID:       "1",
		PageSize:       100,
		UnitCost:       cost.FollowersCostFor(1),
		NextCost:       cost.FollowersCostFor(100),
		LookupCost:     cost.UserLookupCost(),
		NextURL:        next,
		PrevURL:        prev,
		PagingBlocked:  true,
		PagingReason:   "テスト用の停止理由です。",
		FormURL:        "/followers?max=100",
		EstimateURL:    "/followers?max=100&mode=estimate&username=sample",
	}
	out := withoutCSVData(mustRender(t, app, "followers.html", p))
	for _, want := range []string{
		`<span class="btn secondary" aria-disabled="true">前の 100 人を取得（実行不可）</span>`,
		`<span class="btn" aria-disabled="true">次の 100 人を取得（実行不可）</span>`,
		`<div class="notice notice-error"><p>テスト用の停止理由です。</p></div>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ゲートが閉じた一覧に %q が無い", want)
		}
	}
	for _, bad := range []string{"token=NEXTNEXTNEXTNEXT", "token=PREVPREVPREVPREV", "人を取得（+"} {
		if strings.Contains(out, bad) {
			t.Errorf("ゲートが閉じているときに %q（取得のリンク・金額）を出してはいけない", bad)
		}
	}

	// 対照: 開いていればリンクになって金額を出し、無効なボタンと理由は出ない。
	p.PagingBlocked, p.PagingReason = false, ""
	out = withoutCSVData(mustRender(t, app, "followers.html", p))
	for _, want := range []string{
		`<a class="btn secondary" href="/followers?id=1&amp;max=100&amp;mode=execute&amp;token=PREVPREVPREVPREV&amp;username=sample">前の 100 人を取得（+$1.00）</a>`,
		`<a class="btn" href="/followers?id=1&amp;max=100&amp;mode=execute&amp;token=NEXTNEXTNEXTNEXT&amp;username=sample">次の 100 人を取得（+$1.00）</a>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("許可されているときは %q を出すべき", want)
		}
	}
	for _, bad := range []string{`aria-disabled="true"`, "実行不可", "notice-error"} {
		if strings.Contains(out, bad) {
			t.Errorf("許可されているときに %q を出してはいけない", bad)
		}
	}
}
