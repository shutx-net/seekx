package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shutx-net/seekx/internal/config"
	"github.com/shutx-net/seekx/internal/cost"
	"github.com/shutx-net/seekx/internal/query"
	"github.com/shutx-net/seekx/internal/view"
	"github.com/shutx-net/seekx/internal/xapi"
)

// serveGet は h に GET を 1 回送って応答を返す。guard を通すヘッダは newTestRequest が付ける。
func serveGet(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newTestRequest(http.MethodGet, target, nil))
	return rec
}

// pageSizeSelect は 1 ページの人数の <select> を切り出す。selected をページ全体で数えると、
// 将来ほかの <select> が増えたときに無関係な理由で落ちるため。
func pageSizeSelect(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `<select id="max" name="max">`)
	if i < 0 {
		t.Fatalf("1 ページの人数の <select> が無い:\n%s", truncate(body, 400))
	}
	j := strings.Index(body[i:], "</select>")
	if j < 0 {
		t.Fatal("1 ページの人数の </select> が無い")
	}
	return body[i : i+j]
}

// usageOnlyServer は /usage/tweets（ヘッダの cap 表示に使う）だけに答える偽の X API。
// 課金経路（/users/ 配下のユーザー検索・フォロワー取得）への到達回数を *users に数える。
func usageOnlyServer(t *testing.T, users *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/users/") {
			*users++
		}
		if strings.HasPrefix(r.URL.Path, "/usage/tweets") {
			_, _ = w.Write([]byte(`{"data":{"project_cap":"2000000","project_usage":"0","cap_reset_day":12}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// estimateTarget はフォロワーの見積り（mode=estimate）の URL を作る。
func estimateTarget(username, max string) string {
	return followersPath + "?" + url.Values{"username": {username}, "max": {max}, "mode": {"estimate"}}.Encode()
}

// lookupServer はユーザー検索（/users/by/username/）に status・header・body で答える偽の X API。
// /users/ 配下に届いたリクエストの URL を *got に記録する（課金経路への到達とリクエストの形を確かめる）。
// /usage/tweets にはヘッダの cap 表示のための値を返す（usageOnlyServer と同じ）。
func lookupServer(t *testing.T, status int, header http.Header, body string, got *[]*url.URL) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/users/"):
			*got = append(*got, r.URL)
			for k, vs := range header {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		case strings.HasPrefix(r.URL.Path, "/usage/tweets"):
			_, _ = w.Write([]byte(`{"data":{"project_cap":"2000000","project_usage":"0","cap_reset_day":12}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// between は open から始まり close で終わる最初の範囲を切り出す（入れ子の無い要素用。<div> は sliceDiv）。
func between(t *testing.T, html, open, close string) string {
	t.Helper()
	i := strings.Index(html, open)
	if i < 0 {
		t.Fatalf("%s が無い:\n%s", open, truncate(html, 400))
	}
	j := strings.Index(html[i:], close)
	if j < 0 {
		t.Fatalf("%s が閉じていない", open)
	}
	return html[i : i+j+len(close)]
}

// assertNoCharge はセッション累計が 0 のままであることを確かめる（金額と回数の両方）。
func assertNoCharge(t *testing.T, app *App, why string) {
	t.Helper()
	if got := app.session.Total(); got != 0 {
		t.Errorf("%s: セッション累計に計上してはいけない: got $%v", why, got)
	}
	if got := app.session.CallCount(); got != 0 {
		t.Errorf("%s: 課金の回数に数えてはいけない: got %d", why, got)
	}
}

// TestFollowersForm はフォロワー取得のフォーム（GET /followers）と、全ページのヘッダの導線を確認する。
func TestFollowersForm(t *testing.T) {
	t.Run("フィクスチャモードで GET /followers がフォームを出す", func(t *testing.T) {
		app := testApp(t, config.Config{FixtureDir: fixtureDir})
		rec := serveGet(t, newRouter(app), followersPath)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s は 200 を返すべき: status=%d", followersPath, rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{
			`<h1>フォロワーを取得する</h1>`,
			`<form action="/followers" method="get">`,
			`<legend>対象アカウント</legend>`,
			`<label for="username">`, `name="username"`,
			`placeholder="nasa（@ は省略可）"`, `autocapitalize="off"`, `spellcheck="false"`,
			`<label for="max">`, `name="max"`,
			// 見積り（ユーザー検索 1 回）の金額をボタンに出す（D-17）。
			`<button type="submit" name="mode" value="estimate">フォロワー数を見積る（$0.010）</button>`,
			"一括取得はありません",
			// フィクスチャモードでは状態ごとのユーザー名を例示する。
			"フィクスチャモードでは次のユーザー名で各状態を確認できます",
			"<code>fx_protected</code>", "<code>fx_empty</code>", "<code>fx_notfound</code>",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("フォームに %q が含まれていない", want)
			}
		}
		if strings.Contains(body, "ZgotmplZ") {
			t.Error("テンプレートが値を安全でないと判断した（ZgotmplZ）")
		}
		// 取得はユーザー ID が要るので、見積り画面からだけ行う。フォームから直接取得させない。
		if strings.Contains(body, `value="execute"`) {
			t.Error("フォームに取得（mode=execute）のボタンを置いてはいけない（見積り画面から 1 ページずつ取得する）")
		}
		// 入力の誤りが無いときはエラーの枠を出さない（下のエラー表示の対照）。
		if strings.Contains(body, "notice-error") {
			t.Error("入力の誤りが無いのにエラーを出してはいけない")
		}
	})

	t.Run("既定の 100 人だけが選ばれ、1,000 人まで選べる", func(t *testing.T) {
		// 投稿検索の件数上限（SEEKX_MAX_RESULTS_CAP）を最小にしても、選択肢は変わらないこと。
		app := testApp(t, config.Config{FixtureDir: fixtureDir, MaxResultsCap: 10})
		sel := pageSizeSelect(t, serveGet(t, newRouter(app), followersPath).Body.String())

		if !strings.Contains(sel, `<option value="100" selected>100 人（最大 $1.00）・既定</option>`) {
			t.Errorf("既定の 100 人が選ばれているべき:\n%s", sel)
		}
		if n := strings.Count(sel, "selected"); n != 1 {
			t.Errorf("selected はちょうど 1 つであるべき: got %d", n)
		}
		if n := strings.Count(sel, "・既定"); n != 1 {
			t.Errorf("既定の印はちょうど 1 つであるべき: got %d", n)
		}
		for _, want := range []string{
			`<option value="10">10 人（最大 $0.10）</option>`,
			`<option value="500">500 人（最大 $5.00）</option>`,
			// API の上限（1 ページ 1,000 人）。SEEKX_MAX_RESULTS_CAP に連動させてはいけない。
			`<option value="1000">1,000 人（最大 $10.00）</option>`,
		} {
			if !strings.Contains(sel, want) {
				t.Errorf("1 ページの人数の選択肢に %q が無い（SEEKX_MAX_RESULTS_CAP に連動させてはいけない）", want)
			}
		}
	})

	t.Run("URL のユーザー名と人数を描き戻す", func(t *testing.T) {
		app := testApp(t, config.Config{FixtureDir: fixtureDir})
		body := serveGet(t, newRouter(app), followersPath+"?username=%20%40nasa%20&max=500").Body.String()

		// 前後の空白は落とし、先頭の @ は入力どおりに残す（@ を落とすのは query の役目）。
		if !strings.Contains(body, `name="username" value="@nasa"`) {
			t.Errorf("ユーザー名を描き戻していない:\n%s", truncate(body, 400))
		}
		sel := pageSizeSelect(t, body)
		if !strings.Contains(sel, `<option value="500" selected>`) {
			t.Errorf("URL の人数（500）が選ばれているべき:\n%s", sel)
		}
		if strings.Contains(sel, `<option value="100" selected>`) || strings.Count(sel, "selected") != 1 {
			t.Errorf("selected は URL の人数だけに付くべき（既定の 100 人に付けてはいけない）:\n%s", sel)
		}
	})

	t.Run("読めない人数は既定を選んだ状態で出す", func(t *testing.T) {
		app := testApp(t, config.Config{FixtureDir: fixtureDir})
		for _, raw := range []string{"abc", "0", "-1"} {
			sel := pageSizeSelect(t, serveGet(t, newRouter(app), followersPath+"?max="+raw).Body.String())
			if !strings.Contains(sel, `<option value="100" selected>`) || strings.Count(sel, "selected") != 1 {
				t.Errorf("max=%s: 既定の 100 人だけが選ばれているべき（どれも選ばないとブラウザが先頭を選ぶ）:\n%s", raw, sel)
			}
		}
	})

	t.Run("ユーザー名は属性値としてエスケープして描き戻す", func(t *testing.T) {
		app := testApp(t, config.Config{FixtureDir: fixtureDir})
		target := followersPath + "?" + url.Values{"username": {`"><script>alert(1)</script>`}}.Encode()
		body := serveGet(t, newRouter(app), target).Body.String()
		if strings.Contains(body, "<script>alert(1)") {
			t.Error("URL のユーザー名がエスケープされずに描画されている（XSS）")
		}
		if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Errorf("エスケープしたユーザー名が見つからない（描き戻していない）:\n%s", truncate(body, 400))
		}
	})

	t.Run("入力の誤りは API を呼んでいないことと併せて出す", func(t *testing.T) {
		app := testApp(t, config.Config{FixtureDir: fixtureDir})
		p := FollowersFormPage{
			Page:            app.page("フォロワー取得", cost.CapStatus{}, xapi.EndpointRecent),
			Form:            query.FollowersForm{Username: "a-b", MaxResults: query.FollowersPageSizeDefault},
			Errors:          []string{"[U02] ユーザー名「a-b」が不正です。"},
			PageSizeOptions: followersPageSizeOptions(),
			LookupCost:      cost.UserLookupCost(),
		}
		out := mustRender(t, app, "followers_form.html", p)
		for _, want := range []string{"notice-error", "取得できません（API は呼び出していません）", "[U02]"} {
			if !strings.Contains(out, want) {
				t.Errorf("エラー表示に %q が含まれていない", want)
			}
		}
		// 例示はハンドラがフィクスチャモードのときだけ入れる。空なら出さない。
		if strings.Contains(out, "フィクスチャモードでは次のユーザー名") {
			t.Error("FixtureExamples が空なのに例示を出してはいけない")
		}
	})

	t.Run("フィクスチャモードでなければ例示を出さず、API も呼ばない", func(t *testing.T) {
		users := 0
		srv := usageOnlyServer(t, &users)
		app := testApp(t, config.Config{BearerToken: "t", APIBaseURL: srv.URL})
		rec := serveGet(t, newRouter(app), followersPath)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s は 200 を返すべき: status=%d", followersPath, rec.Code)
		}
		body := rec.Body.String()
		// 対照: フォームそのものは出ている（例示が無いのは画面が壊れたからではない）。
		if !strings.Contains(body, `<form action="/followers" method="get">`) {
			t.Fatalf("フォームが出ていない:\n%s", truncate(body, 400))
		}
		for _, bad := range []string{"フィクスチャモードでは次のユーザー名", "fx_protected", "fx_empty", "fx_notfound"} {
			if strings.Contains(body, bad) {
				t.Errorf("実 API に向いているときに %q を出してはいけない（存在しないかもしれない名前を勧める）", bad)
			}
		}
		if users != 0 {
			t.Errorf("フォームの表示でユーザー検索・フォロワー取得（課金経路）を呼んではいけない: calls=%d", users)
		}
	})

	t.Run("全ページのヘッダから投稿検索とフォロワー取得を行き来できる", func(t *testing.T) {
		app := testApp(t, config.Config{FixtureDir: fixtureDir})
		router := newRouter(app)
		links := []string{
			`<a class="badge" href="/">投稿検索</a>`,
			`<a class="badge" href="/followers">フォロワー取得</a>`,
			`<a class="badge" href="/settings">設定</a>`,
		}
		for _, tc := range []struct{ name, target string }{
			{"投稿検索のフォーム", "/"},
			{"フォロワー取得のフォーム", followersPath},
			{"設定", settingsPath},
			{"検索結果", "/search?kw=%E7%8C%AB&max=10&mode=execute"},
			{"見つからないページ", "/no-such-page"},
		} {
			body := serveGet(t, router, tc.target).Body.String()
			i := strings.Index(body, `<header class="site-header">`)
			j := strings.Index(body, "</header>")
			if i < 0 || j < i {
				t.Errorf("%s: ヘッダが無い", tc.name)
				continue
			}
			header := body[i:j]
			prev := -1
			for _, link := range links {
				k := strings.Index(header, link)
				if k < 0 {
					t.Errorf("%s: ヘッダに %q が無い", tc.name, link)
					continue
				}
				if k < prev {
					t.Errorf("%s: ヘッダの並びは 投稿検索 → フォロワー取得 → 設定 であるべき（%q の位置）", tc.name, link)
				}
				prev = k
			}
		}
	})
}

// TestLayoutBannersCoverFollowers は、全ページに出るバナーとフッタがフォロワー取得も踏まえた
// 文言になっていることを確認する。
func TestLayoutBannersCoverFollowers(t *testing.T) {
	t.Run("トークン未設定のバナーはフォロワー取得も実行できないと伝える", func(t *testing.T) {
		app := testApp(t, config.Config{APIBaseURL: "http://127.0.0.1:1/unused"}) // BearerToken 空
		body := serveGet(t, newRouter(app), followersPath).Body.String()
		if !strings.Contains(body, "Bearer Token が設定されていません") {
			t.Fatalf("トークン未設定のバナーが出ていない:\n%s", truncate(body, 400))
		}
		if !strings.Contains(body, "検索・フォロワー取得は実行できません（課金は発生しません）。") {
			t.Error("バナーはフォロワー取得も実行できないこと（と課金が無いこと）を伝えるべき")
		}
	})

	t.Run("トークンがあればバナーを出さない（対照）", func(t *testing.T) {
		users := 0
		srv := usageOnlyServer(t, &users)
		app := testApp(t, config.Config{BearerToken: "t", APIBaseURL: srv.URL})
		body := serveGet(t, newRouter(app), followersPath).Body.String()
		if strings.Contains(body, "Bearer Token が設定されていません") {
			t.Error("トークンが設定済みなのに未設定のバナーを出してはいけない")
		}
	})

	t.Run("月次 cap のバナーはフォロワー取得を止めないと伝える", func(t *testing.T) {
		app := testApp(t, config.Config{FixtureDir: fixtureDir})
		block := cost.CapStatus{Usage: 1960000, Cap: 2000000, Ratio: 0.98, Level: cost.CapBlock, Known: true, ResetDay: 12}
		out := mustRender(t, app, "followers_form.html", FollowersFormPage{
			Page:            app.page("フォロワー取得", block, xapi.EndpointRecent),
			Form:            query.FollowersForm{MaxResults: query.FollowersPageSizeDefault},
			PageSizeOptions: followersPageSizeOptions(),
			LookupCost:      cost.UserLookupCost(),
		})
		if !strings.Contains(out, "banner-block") {
			t.Fatal("cap 超過のバナーが出ていない")
		}
		if !strings.Contains(out, "投稿の検索は停止されます（フォロワー取得は post reads を消費しないため止めません）。") {
			t.Error("cap 超過のバナーは、止まるのが投稿の検索だけであることを伝えるべき")
		}
		// バナーの文言はゲートの方針（ユーザーの読み取りは月次 cap で止めない）と一致しているべき。
		if ok, _ := cost.GateAllowsFor(cost.ReadUsers, block, config.Config{}); !ok {
			t.Error("GateAllowsFor(ReadUsers) が cap 超過で止めるなら、バナーの「フォロワー取得は止めません」を直すべき")
		}
		if ok, _ := cost.GateAllowsFor(cost.ReadPosts, block, config.Config{}); ok {
			t.Error("GateAllowsFor(ReadPosts) が cap 超過で止めないなら、バナーの「投稿の検索は停止されます」を直すべき")
		}
	})

	t.Run("フッタはユーザー 1 人の単価と再取得になることを伝える", func(t *testing.T) {
		app := testApp(t, config.Config{FixtureDir: fixtureDir})
		body := serveGet(t, newRouter(app), "/").Body.String()
		for _, want := range []string{"X API は従量課金です", "ユーザー 1 人 $0.010", "ページの再読み込みは再取得になります。"} {
			if !strings.Contains(body, want) {
				t.Errorf("フッタに %q が無い", want)
			}
		}
	})
}

// TestFollowersFormRoundTrip はフォーム → URL → フォームの往復が壊れないことを確認する（D-23）。
func TestFollowersFormRoundTrip(t *testing.T) {
	t.Run("URL を経ても値が変わらない", func(t *testing.T) {
		in := query.FollowersForm{Username: "@nasa", UserID: "11348282", MaxResults: 500, PaginationToken: "FIXTUREPAGE2ZZZZ"}
		u := followersURL(in, "execute", in.PaginationToken)
		if !strings.HasPrefix(u, followersPath+"?") {
			t.Fatalf("URL は %s? で始まるべき: %q", followersPath, u)
		}
		q, err := parseQueryOf(u)
		if err != nil {
			t.Fatal(err)
		}
		if got := parseFollowersForm(q); got != in {
			t.Errorf("往復で値が変わった:\n got  %+v\n want %+v", got, in)
		}
		if q.Get("mode") != "execute" || q.Get("token") != in.PaginationToken {
			t.Errorf("mode / token が載っていない: %v", q)
		}
		// 先頭の @ は URL でもそのまま（落とすのは query.NormalizeFollowersUsername の役目）。
		if q.Get("username") != "@nasa" {
			t.Errorf("ユーザー名の @ を URL で落としてはいけない: %q", q.Get("username"))
		}
	})

	t.Run("mode とトークンは渡したときだけ載る", func(t *testing.T) {
		q, err := parseQueryOf(followersURL(query.FollowersForm{Username: "nasa", MaxResults: 100}, "", ""))
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"mode", "token", "id"} {
			if q.Has(key) {
				t.Errorf("空の %s を URL に載せてはいけない: %v", key, q)
			}
		}
		// 対照: 値のあるものは載る。
		if q.Get("username") != "nasa" || q.Get("max") != "100" {
			t.Errorf("ユーザー名と人数が載っていない: %v", q)
		}
	})

	t.Run("フォームに戻る URL は ID・mode・トークンを落とす", func(t *testing.T) {
		in := query.FollowersForm{Username: "@nasa", UserID: "11348282", MaxResults: 500, PaginationToken: "FIXTUREPAGE2ZZZZ"}
		u := followersFormURL(in)
		if !strings.HasPrefix(u, followersPath+"?") {
			t.Fatalf("フォームに戻る URL は %s? で始まるべき: %q", followersPath, u)
		}
		q, err := parseQueryOf(u)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"id", "mode", "token"} {
			if q.Has(key) {
				t.Errorf("フォームに戻る URL に %s を載せてはいけない: %q", key, u)
			}
		}
		// 対照: ユーザー名と人数は保つ（入力し直させない）。
		want := query.FollowersForm{Username: "@nasa", MaxResults: 500}
		if got := parseFollowersForm(q); got != want {
			t.Errorf("フォームに戻ったときの入力:\n got  %+v\n want %+v", got, want)
		}
	})

	t.Run("1 ページの人数の読み取り", func(t *testing.T) {
		def := query.FollowersPageSizeDefault
		cases := []struct {
			name string
			q    url.Values
			want int
		}{
			{"未指定は既定値", url.Values{}, def},
			{"空は既定値", url.Values{"max": {""}}, def},
			{"空白だけは既定値", url.Values{"max": {"  "}}, def},
			{"数字はそのまま", url.Values{"max": {"500"}}, 500},
			{"前後の空白は落として読む", url.Values{"max": {" 500 "}}, 500},
			{"読めない値は -1（既定値にしてはいけない）", url.Values{"max": {"abc"}}, -1},
			{"小数は読めない", url.Values{"max": {"100.0"}}, -1},
			{"全角数字は読めない", url.Values{"max": {"１００"}}, -1},
			{"桁あふれは読めない", url.Values{"max": {"99999999999999999999"}}, -1},
			{"0 はそのまま（検証の U04 で弾く）", url.Values{"max": {"0"}}, 0},
			{"負の値はそのまま（検証の U04 で弾く）", url.Values{"max": {"-5"}}, -5},
		}
		for _, tc := range cases {
			if got := parseFollowersForm(tc.q).MaxResults; got != tc.want {
				t.Errorf("%s: MaxResults は %d になるべき（実際: %d）", tc.name, tc.want, got)
			}
		}
	})

	t.Run("弾くべき人数がリンクを経て既定値に化けない", func(t *testing.T) {
		for _, n := range []int{-1, 0, -5, xapi.FollowersMaxResultsMax + 1} {
			f := query.FollowersForm{Username: "nasa", MaxResults: n}
			for _, u := range []string{followersURL(f, "estimate", ""), followersFormURL(f)} {
				q, err := parseQueryOf(u)
				if err != nil {
					t.Fatal(err)
				}
				if got := parseFollowersForm(q).MaxResults; got != n {
					t.Errorf("%q: 人数 %d は往復で変わってはいけない（実際: %d）", u, n, got)
				}
			}
		}
	})

	t.Run("前後の空白を落とす", func(t *testing.T) {
		got := parseFollowersForm(url.Values{
			"username": {"　@nasa　"}, // 全角空白も落とす
			"id":       {" 20000001 "},
			"max":      {"100"},
			"token":    {" FIXTUREPAGE2ZZZZ\n"},
		})
		want := query.FollowersForm{Username: "@nasa", UserID: "20000001", MaxResults: 100, PaginationToken: "FIXTUREPAGE2ZZZZ"}
		if got != want {
			t.Fatalf("前後の空白を落とすべき:\n got  %+v\n want %+v", got, want)
		}
		// 落とした値は検証を通る。対照として、落とさない値は U03 / U05 で弾かれることも固定する
		// （query は ID とトークンを補正しないので、空白の除去はパーサの責務）。
		if errs := query.ValidateFollowersFetch(got); len(errs) > 0 {
			t.Errorf("空白を落とした ID とトークンは検証を通るべき: %v", query.ValidationErrors(errs).Messages())
		}
		raw := query.FollowersForm{Username: "@nasa", UserID: " 20000001 ", MaxResults: 100, PaginationToken: " FIXTUREPAGE2ZZZZ\n"}
		var rules []string
		for _, e := range query.ValidateFollowersFetch(raw) {
			rules = append(rules, e.RuleID)
		}
		if strings.Join(rules, ",") != "U03,U05" {
			t.Errorf("対照: 空白付きの ID とトークンは U03 / U05 で弾かれるべき（パーサが空白を落とす理由）: got %v", rules)
		}
	})
}

// TestFollowersFixtureExamplesExist は、フォームで例示するユーザー名にそれぞれ専用のフィクスチャが
// あることを確認する。無いと通常のアカウント（users_by_username.json）に解決され、例示した状態
// （鍵アカウント・0 人・存在しないユーザー）を確かめられなくなる。
func TestFollowersFixtureExamplesExist(t *testing.T) {
	if len(fixtureFollowerExamples) == 0 {
		t.Fatal("フィクスチャの例示が空（各状態を課金なしで確かめる手段が無くなる）")
	}
	for _, name := range fixtureFollowerExamples {
		file := filepath.Join(fixtureDir, "users_by_username_"+name+".json")
		if _, err := os.Stat(file); err != nil {
			t.Errorf("例示 %q に対応する %s が無い: %v", name, file, err)
		}
		// xapi はファイル名を小文字にしたユーザー名で引く。大文字を含む例示はファイルと一致しない。
		if name != strings.ToLower(name) {
			t.Errorf("例示 %q は小文字で書くべき（フィクスチャは小文字のユーザー名で解決される）", name)
		}
		// 例示はそのままフォームに入力できる名前であるべき（U02 で弾かれると見積りまで進めない）。
		f := query.FollowersForm{Username: name, MaxResults: query.FollowersPageSizeDefault}
		if errs := query.ValidateFollowersLookup(f); len(errs) > 0 {
			t.Errorf("例示 %q は入力の検証を通るべき: %v", name, query.ValidationErrors(errs).Messages())
		}
	}
}

// TestFollowersEstimateFixture はフィクスチャモードの見積り（mode=estimate）を状態ごとに確認する。
// どの状態でも API は呼ばれないので、セッション累計は 0 のまま。
func TestFollowersEstimateFixture(t *testing.T) {
	t.Run("任意の名前は通常のアカウント（フォロワー 12,345 人）の見積りになる", func(t *testing.T) {
		app := testApp(t, config.Config{FixtureDir: fixtureDir})
		rec := serveGet(t, newRouter(app), estimateTarget("@Anyone", "100"))
		if rec.Code != http.StatusOK {
			t.Fatalf("見積りは 200 を返すべき: status=%d\n%s", rec.Code, truncate(rec.Body.String(), 400))
		}
		body := rec.Body.String()
		for _, want := range []string{
			"<h1>フォロワー取得の見積り</h1>",
			`<div class="user-target"><article class="user-card">`,
			`フォロワー数: <span class="strong">12,345 人</span>`,
			// 12,345 人 × $0.010。端数のページも 1 ページに数える（12,345 / 100 → 124 ページ）。
			`全件の予想コスト: <span class="strong">$123.45</span>（1 ページ 100 人で約 124 ページ）`,
			`今回取得する人数の見込み: <span class="strong">100 人・$1.00</span>`,
			"この見積り自体のコスト: $0.010（ユーザー 1 人・すでに発生しています）",
			// 取得のリンクは API が返したユーザー名と ID から作る（入力した "@Anyone" ではない）。
			`<a class="btn" href="/followers?id=20000001&amp;max=100&amp;mode=execute&amp;username=sample_star">最初の 100 人を取得する（最大 $1.00）</a>`,
			`<a class="btn secondary" href="/followers?max=100&amp;mode=estimate&amp;username=sample_star">再見積り（+$0.010）</a>`,
			// 「条件を修正する」は入力したとおりの値でフォームに戻す。
			`<a class="btn secondary" href="/followers?max=100&amp;username=%40Anyone">条件を修正する</a>`,
			// 対象ユーザーのカード（表示名・@username・自己紹介・指標）。
			`<a class="author-handle" href="https://x.com/sample_star" rel="noreferrer">@sample_star</a>`,
			"サンプル 星子", "フィクスチャの人気アカウントです。", "フォロワー 12,345",
			// 月次 cap は post read の上限で、フォロワー取得は対象外であることを添える。
			"月次 cap: 42 / 2,000,000", "フォロワー取得は post reads を消費しないため、この cap の対象外です。",
			"重複排除", "Owned Reads",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("見積り画面に %q が含まれていない", want)
			}
		}
		// 対照: 通常のアカウントでは警告も鍵アカウントの表示も出ない（下の鍵アカウントの検査の対照）。
		for _, bad := range []string{"notice-warn", "鍵アカウント", "フォロワーは 0 人です", "不明", "ZgotmplZ", "@@"} {
			if strings.Contains(body, bad) {
				t.Errorf("通常のアカウントの見積りに %q を出してはいけない", bad)
			}
		}
		// 見積りは 2 カラムの結果ページではない（data-page を付けない）。
		if !strings.Contains(body, `<html lang="ja">`) {
			t.Error(`見積り画面の <html> に data-page を付けてはいけない（結果ページの 2 カラムの CSS が当たる）`)
		}
		assertNoCharge(t, app, "フィクスチャモード")
	})

	t.Run("1 ページの人数を見積りと取得のリンクに使う", func(t *testing.T) {
		app := testApp(t, config.Config{FixtureDir: fixtureDir})
		body := serveGet(t, newRouter(app), estimateTarget("sample_star", "500")).Body.String()
		for _, want := range []string{
			`（1 ページ 500 人で約 25 ページ）`,
			`<span class="strong">500 人・$5.00</span>`,
			`<a class="btn" href="/followers?id=20000001&amp;max=500&amp;mode=execute&amp;username=sample_star">最初の 500 人を取得する（最大 $5.00）</a>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("指定した 1 ページの人数（500）を使うべき（既定の 100 人にしてはいけない）: %q が無い", want)
			}
		}
	})

	t.Run("鍵アカウントは警告を先頭に出し、取得のリンクは残す", func(t *testing.T) {
		app := testApp(t, config.Config{FixtureDir: fixtureDir})
		rec := serveGet(t, newRouter(app), estimateTarget("fx_protected", "100"))
		if rec.Code != http.StatusOK {
			t.Fatalf("鍵アカウントの見積りは 200 を返すべき: status=%d", rec.Code)
		}
		body := rec.Body.String()
		warn := sliceDiv(t, body, `<div class="notice notice-warn">`)
		if !strings.Contains(warn, "<ul><li>鍵アカウントです。App-only の Bearer Token ではフォロワーを取得できない可能性が高いです") {
			t.Errorf("鍵アカウントの警告を先頭に出すべき:\n%s", warn)
		}
		if !strings.Contains(warn, "課金されない見込みです") {
			t.Error("鍵アカウントの警告は、取得できなければ課金されない見込みであることも伝えるべき")
		}
		if !strings.Contains(body, `<span class="flag">鍵アカウント</span>`) {
			t.Error("カードに鍵アカウントの印が無い")
		}
		// 取得できるかは試すまで分からないので、リンクは消さない（ID は鍵アカウントのもの）。
		if !strings.Contains(body, `href="/followers?id=20000002&amp;max=100&amp;mode=execute&amp;username=fx_protected"`) {
			t.Error("鍵アカウントでも取得のリンクは出すべき（取得できなくても課金されない見込み）")
		}
		if strings.Contains(body, "ZgotmplZ") {
			t.Error("テンプレートが値を安全でないと判断した（ZgotmplZ）")
		}
		assertNoCharge(t, app, "フィクスチャモード")
	})

	t.Run("フォロワー 0 人は取得のリンクを出さない", func(t *testing.T) {
		app := testApp(t, config.Config{FixtureDir: fixtureDir})
		rec := serveGet(t, newRouter(app), estimateTarget("fx_empty", "100"))
		if rec.Code != http.StatusOK {
			t.Fatalf("0 人の見積りは 200 を返すべき: status=%d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{
			`フォロワー数: <span class="strong">0 人</span>`,
			"フォロワーは 0 人です（取得するものはありません）。",
			// 対照: 見積りのやり直しと修正の導線は残す。
			`href="/followers?max=100&amp;mode=estimate&amp;username=fx_empty">再見積り（+$0.010）</a>`,
			`href="/followers?max=100&amp;username=fx_empty">条件を修正する</a>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("0 人の見積りに %q が含まれていない", want)
			}
		}
		// 取得しても返るユーザーがいない。リンクも無効なボタンも出さない。
		for _, bad := range []string{"mode=execute", "人を取得する", "約 0 ページ", "ZgotmplZ"} {
			if strings.Contains(body, bad) {
				t.Errorf("フォロワー 0 人の見積りに %q を出してはいけない", bad)
			}
		}
		assertNoCharge(t, app, "フィクスチャモード")
	})

	t.Run("存在しないユーザーは 404 の専用画面", func(t *testing.T) {
		app := testApp(t, config.Config{FixtureDir: fixtureDir})
		// 全角の ＠ を付けて入力しても、本文には @ を 1 つだけ付けた名前を出す。
		rec := serveGet(t, newRouter(app), estimateTarget("＠fx_notfound", "100"))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("存在しないユーザーは 404 を返すべき: status=%d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{
			"<h1>ユーザーが見つかりませんでした</h1>",
			"@fx_notfound に当たるユーザーを X API が返しませんでした。",
			"凍結・削除された可能性があります",
			"課金されない見込みです",
			"<h2>対処手順</h2>",
			// X API としては成功（HTTP 200 で errors だけ）の応答であることと、errors の説明。
			"HTTP ステータス: 200",
			"Could not find user with username: [fx_notfound].",
			// 入力を保ったままフォームに戻る。
			`href="/followers?max=100&amp;username=%EF%BC%A0fx_notfound">フォームに戻る</a>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("存在しないユーザーの画面に %q が含まれていない", want)
			}
		}
		for _, bad := range []string{"mode=execute", "@＠", "@@", "生成されたクエリ", "ZgotmplZ"} {
			if strings.Contains(body, bad) {
				t.Errorf("存在しないユーザーの画面に %q を出してはいけない", bad)
			}
		}
		assertNoCharge(t, app, "フィクスチャモード")
	})
}

// TestFollowersEstimateUnknownCount は、フォロワー数が分からない（public_metrics が欠けた）ときに
// 全件の金額を「0」ではなく「不明」と出し、取得のリンクは残すことを確認する。
func TestFollowersEstimateUnknownCount(t *testing.T) {
	var got []*url.URL
	srv := lookupServer(t, http.StatusOK, nil, `{"data":{"id":"20000009","name":"指標なし","username":"no_metrics"}}`, &got)
	app := testApp(t, config.Config{BearerToken: "t", APIBaseURL: srv.URL})
	rec := serveGet(t, newRouter(app), estimateTarget("no_metrics", "100"))
	if rec.Code != http.StatusOK {
		t.Fatalf("見積りは 200 を返すべき: status=%d\n%s", rec.Code, truncate(rec.Body.String(), 400))
	}
	body := rec.Body.String()
	meta := between(t, body, `<ul class="meta-list">`, "</ul>")
	for _, want := range []string{
		`フォロワー数: <span class="strong">不明</span>`,
		`全件の予想コスト: <span class="strong">不明</span>`,
		// 最初のページは 1 ページの人数（上限側）で見積る。
		`<span class="strong">100 人・$1.00</span>`, "フォロワー数が不明なため 1 ページの上限で見積っています",
	} {
		if !strings.Contains(meta, want) {
			t.Errorf("フォロワー数が不明な見積りに %q が無い:\n%s", want, meta)
		}
	}
	// 不明を 0 として出すと「$0.00（約 0 ページ）」と読めてしまう。
	for _, bad := range []string{`<span class="strong">$0.00</span>`, "約 0 ページ", `<span class="strong">0 人</span>`} {
		if strings.Contains(meta, bad) {
			t.Errorf("フォロワー数が不明なのに %q と出してはいけない:\n%s", bad, meta)
		}
	}
	// 不明は 0 人ではない。取得のリンクは出す（0 人の扱いと取り違えない）。
	if !strings.Contains(body, "最初の 100 人を取得する（最大 $1.00）") || strings.Contains(body, "フォロワーは 0 人です") {
		t.Error("フォロワー数が不明なときは 0 人と扱わず、取得のリンクを出すべき")
	}
	// カードには指標のブロックを出さない（0 と不明を区別する）。
	if strings.Contains(body, `<div class="metrics">`) {
		t.Error("指標が欠けたユーザーのカードに指標（0）を出してはいけない")
	}
}

// TestFollowersLookupValidation は、入力の誤りを API を呼ばずに 400 のフォームで知らせることを確認する。
func TestFollowersLookupValidation(t *testing.T) {
	cases := []struct {
		name, username, max, rule string
	}{
		{"ユーザー名が空", "", "100", "[U01]"},
		{"@ だけ", "@", "100", "[U01]"},
		{"使えない文字", "a-b", "100", "[U02]"},
		{"@ が 2 つ", "@@nasa", "100", "[U02]"},
		{"人数が 0", "nasa", "0", "[U04]"},
		{"人数が読めない", "nasa", "abc", "[U04]"},
		{"人数が API の上限を超える", "nasa", "1001", "[U04]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			users := 0
			srv := usageOnlyServer(t, &users)
			app := testApp(t, config.Config{BearerToken: "t", APIBaseURL: srv.URL})
			rec := serveGet(t, newRouter(app), estimateTarget(tc.username, tc.max))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("入力の誤りは 400 を返すべき: status=%d", rec.Code)
			}
			body := rec.Body.String()
			for _, want := range []string{tc.rule, "取得できません（API は呼び出していません）", `<form action="/followers" method="get">`} {
				if !strings.Contains(body, want) {
					t.Errorf("%q が含まれていない:\n%s", want, truncate(body, 400))
				}
			}
			if users != 0 {
				t.Errorf("入力の誤りでユーザー検索（課金経路）を呼んではいけない: calls=%d", users)
			}
			assertNoCharge(t, app, "入力の誤り")
		})
	}

	t.Run("対照: 正しい入力ならユーザー検索に 1 回届く", func(t *testing.T) {
		users := 0
		srv := usageOnlyServer(t, &users) // /users/ には 404 を返す
		app := testApp(t, config.Config{BearerToken: "t", APIBaseURL: srv.URL})
		rec := serveGet(t, newRouter(app), estimateTarget("nasa", "100"))
		if users != 1 {
			t.Errorf("正しい入力ならユーザー検索はちょうど 1 回呼ばれるべき: calls=%d", users)
		}
		if rec.Code == http.StatusBadRequest {
			t.Error("正しい入力を 400 で弾いてはいけない")
		}
	})
}

// TestFollowersLookupBilling は、ユーザー検索の金額をユーザーが返ったときだけ計上することを確認する。
// 存在しないユーザー（errors だけの 200）と 429 は課金されないので計上しない。
func TestFollowersLookupBilling(t *testing.T) {
	future := strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10)
	cases := []struct {
		name       string
		status     int
		header     http.Header
		body       string
		wantStatus int
		wantUSD    float64
		wantCalls  int
	}{
		{"ユーザーが返れば 1 人分を計上する", http.StatusOK, nil,
			`{"data":{"id":"20000001","name":"n","username":"sample","public_metrics":{"followers_count":1,"following_count":0,"tweet_count":0,"listed_count":0}}}`,
			http.StatusOK, cost.UserLookupCost(), 1},
		{"errors だけ（存在しないユーザー）は計上しない", http.StatusOK, nil,
			`{"errors":[{"value":"sample","detail":"Could not find user with username: [sample].","title":"Not Found Error","resource_type":"user","parameter":"username","type":"https://api.twitter.com/2/problems/resource-not-found"}]}`,
			http.StatusNotFound, 0, 0},
		{"429 は計上しない", http.StatusTooManyRequests,
			http.Header{"X-Rate-Limit-Limit": {"300"}, "X-Rate-Limit-Remaining": {"0"}, "X-Rate-Limit-Reset": {future}},
			`{"title":"Too Many Requests","status":429,"errors":[{"code":88,"message":"Rate limit exceeded"}]}`,
			http.StatusTooManyRequests, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []*url.URL
			srv := lookupServer(t, tc.status, tc.header, tc.body, &got)
			app := testApp(t, config.Config{BearerToken: "t", APIBaseURL: srv.URL})
			rec := serveGet(t, newRouter(app), estimateTarget("sample", "100"))
			if rec.Code != tc.wantStatus {
				t.Errorf("status: got %d, want %d", rec.Code, tc.wantStatus)
			}
			if len(got) == 0 {
				t.Fatal("ユーザー検索が呼ばれていない（計上の検査が成り立たない）")
			}
			if usd := app.session.Total(); usd != tc.wantUSD {
				t.Errorf("セッション累計: got $%v, want $%v", usd, tc.wantUSD)
			}
			if n := app.session.CallCount(); n != tc.wantCalls {
				t.Errorf("課金の回数: got %d, want %d", n, tc.wantCalls)
			}
		})
	}
}

// TestFollowersLookupRequest はユーザー検索のリクエストの形と、画面・リンクに出すユーザー名を確認する。
func TestFollowersLookupRequest(t *testing.T) {
	const body = `{"data":{"id":"123","name":"API の表示名","username":"Sample_API","public_metrics":{"followers_count":5,"following_count":1,"tweet_count":2,"listed_count":0}}}`

	for _, input := range []string{"@Sample", "＠Sample", " Sample "} {
		t.Run("入力 "+strconv.Quote(input)+" は /users/by/username/Sample に届く", func(t *testing.T) {
			var got []*url.URL
			srv := lookupServer(t, http.StatusOK, nil, body, &got)
			app := testApp(t, config.Config{BearerToken: "t", APIBaseURL: srv.URL})
			rec := serveGet(t, newRouter(app), estimateTarget(input, "100"))
			if rec.Code != http.StatusOK {
				t.Fatalf("見積りは 200 を返すべき: status=%d", rec.Code)
			}
			if len(got) != 1 {
				t.Fatalf("ユーザー検索はちょうど 1 回呼ばれるべき: got %d", len(got))
			}
			// 先頭の @ / ＠ と前後の空白は落とし、大文字小文字は変えない。
			if got[0].Path != "/users/by/username/Sample" {
				t.Errorf("path: got %q, want %q", got[0].Path, "/users/by/username/Sample")
			}
			q := got[0].Query()
			if want := strings.Join(xapi.UserProfileFieldsRequested, ","); q.Get("user.fields") != want {
				t.Errorf("user.fields: got %q, want %q", q.Get("user.fields"), want)
			}
			// 【expansions を送ってはいけない】ピン留め投稿などが includes に入ると post read として課金され得る。
			if q.Has("expansions") {
				t.Errorf("expansions を送ってはいけない: %q", q.Get("expansions"))
			}
		})
	}

	t.Run("画面とリンクには API が返したユーザー名を使う", func(t *testing.T) {
		var got []*url.URL
		srv := lookupServer(t, http.StatusOK, nil, body, &got)
		app := testApp(t, config.Config{BearerToken: "t", APIBaseURL: srv.URL})
		page := serveGet(t, newRouter(app), estimateTarget("@Sample", "100")).Body.String()
		for _, want := range []string{
			`<a class="author-handle" href="https://x.com/Sample_API" rel="noreferrer">@Sample_API</a>`,
			`href="/followers?id=123&amp;max=100&amp;mode=execute&amp;username=Sample_API"`,
			`href="/followers?max=100&amp;mode=estimate&amp;username=Sample_API"`,
		} {
			if !strings.Contains(page, want) {
				t.Errorf("API が返したユーザー名を使うべき: %q が無い", want)
			}
		}
		// 入力値（"@Sample"）をそのまま出すと「@@Sample」になり、取得の画面にも @ 付きの値が渡る。
		if strings.Contains(page, "@@") || strings.Contains(page, "mode=execute&amp;username=%40") {
			t.Error("入力した @ 付きのユーザー名を画面や取得のリンクに使ってはいけない")
		}
	})

	t.Run("API がユーザー名を返さなければ @ を落とした入力をリンクに使う", func(t *testing.T) {
		var got []*url.URL
		srv := lookupServer(t, http.StatusOK, nil, `{"data":{"id":"123","name":"名前だけ"}}`, &got)
		app := testApp(t, config.Config{BearerToken: "t", APIBaseURL: srv.URL})
		page := serveGet(t, newRouter(app), estimateTarget("@Sample", "100")).Body.String()
		if !strings.Contains(page, `href="/followers?id=123&amp;max=100&amp;mode=execute&amp;username=Sample"`) {
			t.Errorf("ユーザー名が無ければ、実際に送った値（@ を落とした入力）を使うべき:\n%s", truncate(page, 400))
		}
		// カードには「@」だけを出さない。
		if strings.Contains(page, `class="author-handle"`) {
			t.Error("ユーザー名が無いのに @username の欄を出してはいけない")
		}
	})
}

// TestFollowersWithoutToken は、トークン未設定のまま見積ろうとしたときに API を呼ばずに案内を出すことを確認する。
func TestFollowersWithoutToken(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	app := testApp(t, config.Config{APIBaseURL: srv.URL}) // BearerToken 空
	router := newRouter(app)

	rec := serveGet(t, router, estimateTarget("@nasa", "100"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("トークン未設定なら 403 で止めるべき: status=%d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<h1>Bearer Token が設定されていません</h1>",
		"フォロワーを取得するには X API の Bearer Token が必要です。API はまだ呼び出していません（課金なし）。",
		"設定画面（" + settingsPath + "）",
		`href="/followers?max=100&amp;username=%40nasa">フォームに戻る</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("トークン未設定の案内に %q が含まれていない", want)
		}
	}

	t.Run("入力の誤りはトークンの有無より先に知らせる", func(t *testing.T) {
		rec := serveGet(t, router, estimateTarget("a-b", "100"))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "[U02]") {
			t.Errorf("入力の誤りは 400 で知らせるべき（トークンを設定しても直らない）: status=%d", rec.Code)
		}
	})

	if calls != 0 {
		t.Errorf("トークン未設定で API を呼んではいけない（usage 含む）: calls=%d", calls)
	}
	assertNoCharge(t, app, "トークン未設定")
}

// TestFollowersLookupErrorPages は、ユーザー検索の API エラーがフォロワー取得向けの画面になることを確認する。
// 検索向けの文言（生成クエリ・検索エンドポイント）が紛れ込まず、課金の有無を明記し、戻り先がフォロワー取得の
// フォームであること。
func TestFollowersLookupErrorPages(t *testing.T) {
	const secret = "SECRET-BEARER-TOKEN-DO-NOT-LEAK-123456"
	reset := time.Now().Add(10 * time.Minute).Truncate(time.Second)
	cases := []struct {
		name       string
		status     int
		header     http.Header
		body       string
		wantStatus int
		want       []string
		bad        []string // 本文（notice-error の中）に出してはいけないもの
	}{
		{"400", http.StatusBadRequest, nil,
			`{"errors":[{"parameters":{"username":["x"]},"message":"The username query parameter value [x] is not valid"}],"title":"Invalid Request","detail":"One or more parameters to your request was invalid.","type":"https://api.twitter.com/2/problems/invalid-request"}`,
			http.StatusBadRequest,
			[]string{"<h1>X API が要求を受け付けませんでした</h1>", "（HTTP 400・課金なし）", "1 ページの人数"},
			[]string{"検索条件", "API は呼び出していません"}},
		{"401", http.StatusUnauthorized, nil,
			`{"title":"Unauthorized","status":401,"detail":"Unauthorized"}`,
			http.StatusForbidden,
			[]string{"認証に失敗しました", "設定画面（" + settingsPath + "）で Bearer Token を確認してください。"}, nil},
		{"403 未エンロール", http.StatusForbidden, nil,
			`{"title":"Client Forbidden","reason":"client-not-enrolled","detail":"not attached to a Project"}`,
			http.StatusForbidden,
			[]string{"client-not-enrolled", "アクセスレベルがユーザー検索とフォロワー取得を含むか確認してください。"}, nil},
		{"403", http.StatusForbidden, nil,
			`{"title":"Forbidden","status":403,"detail":"Forbidden"}`,
			http.StatusForbidden,
			[]string{"アクセスが拒否されました", "X API がこの取得を許可しませんでした（HTTP 403）", "課金されない見込みです", "鍵アカウントのフォロワー"}, nil},
		{"404", http.StatusNotFound, nil,
			`{"title":"Not Found Error","status":404,"detail":"Not Found"}`,
			http.StatusNotFound,
			[]string{"<h1>ユーザーが見つかりませんでした</h1>", "（HTTP 404）", "課金されない見込みです", "ユーザー名の綴りを確認してください"},
			[]string{"対象が見つかりませんでした"}},
		{"429 リセット時刻あり", http.StatusTooManyRequests,
			http.Header{"X-Rate-Limit-Limit": {"300"}, "X-Rate-Limit-Remaining": {"0"}, "X-Rate-Limit-Reset": {strconv.FormatInt(reset.Unix(), 10)}},
			`{"title":"Too Many Requests","status":429,"errors":[{"code":88,"message":"Rate limit exceeded"}]}`,
			http.StatusTooManyRequests,
			// ヘッダのリセット時刻と上限を優先して出す（docs の回数に頼らない）。
			[]string{"（HTTP 429・課金なし）", reset.In(jst(t)).Format("15:04:05") + " 以降に再試行してください。", "この枠の上限は 300 リクエストです。"},
			[]string{"リセット時刻が返らなかった"}},
		{"429 リセット時刻なし", http.StatusTooManyRequests, nil,
			`{"title":"Too Many Requests","status":429,"errors":[{"code":88,"message":"Rate limit exceeded"}]}`,
			http.StatusTooManyRequests,
			// 枠は 15 分なので、ヘッダが無ければ 15 分待つよう案内する。
			[]string{"（HTTP 429・課金なし）", "リセット時刻が返らなかったため、15 分ほど待ってから再試行してください"},
			[]string{"以降に再試行してください"}},
		{"500", http.StatusInternalServerError, nil,
			`{"title":"Internal Server Error"}`,
			http.StatusBadGateway,
			[]string{"一時的なエラー", "数分待って再試行してください"}, nil},
		{"不正な JSON", http.StatusOK, nil,
			`{"data": { broken`,
			http.StatusBadGateway,
			[]string{"解析に失敗"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []*url.URL
			srv := lookupServer(t, tc.status, tc.header, tc.body, &got)
			app := testApp(t, config.Config{BearerToken: secret, APIBaseURL: srv.URL})
			rec := serveGet(t, newRouter(app), estimateTarget("sample", "100"))

			if rec.Code != tc.wantStatus {
				t.Errorf("status: got %d, want %d", rec.Code, tc.wantStatus)
			}
			body := rec.Body.String()
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Errorf("エラー画面に %q が含まれていない:\n%s", want, truncate(body, 600))
				}
			}
			msg := sliceDiv(t, body, `<div class="notice notice-error">`)
			for _, bad := range tc.bad {
				if strings.Contains(msg, bad) {
					t.Errorf("エラーの本文に %q を出してはいけない:\n%s", bad, msg)
				}
			}
			// 手順は必ず出し、検索向けの手順や生成クエリを紛れ込ませない。
			if !strings.Contains(body, "<h2>対処手順</h2>") {
				t.Error("対処手順が無い（apiErrorView.Steps を渡すべき）")
			}
			for _, bad := range []string{"生成されたクエリ", "検索エンドポイント", "全期間", ".env の BEARER_TOKEN"} {
				if strings.Contains(body, bad) {
					t.Errorf("フォロワー取得のエラー画面に検索向けの %q を出してはいけない", bad)
				}
			}
			if !strings.Contains(body, `href="/followers?max=100&amp;username=sample">フォームに戻る</a>`) {
				t.Error("「フォームに戻る」はフォロワー取得のフォーム（入力を保ったまま）へ戻すべき")
			}
			// トークン・Authorization・リクエスト URL は絶対に画面に出さない（P-TOKEN-LEAK）。
			if strings.Contains(body, secret) {
				t.Error("エラー画面にトークンが含まれている")
			}
			if strings.Contains(strings.ToLower(body), "authorization") {
				t.Error("エラー画面に Authorization が含まれている")
			}
			if strings.Contains(body, srv.URL) {
				t.Error("エラー画面にリクエスト URL が含まれている")
			}
			assertNoCharge(t, app, "API エラー")
		})
	}
}

// TestFollowersErrorText は followersErrorText と followersErrorSteps を直接確かめる。
func TestFollowersErrorText(t *testing.T) {
	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	// xapi が送る前に弾いたエラー（パスを壊す値）。Status は 0 のまま。
	rejected := &xapi.APIError{Kind: xapi.KindInvalidQuery, Detail: "ユーザー名が不正です（API は呼び出していません）。"}

	t.Run("送信前に弾いたエラーは API を呼んでいないと出す", func(t *testing.T) {
		heading, msg := app.followersErrorText(rejected)
		if !strings.Contains(msg, "API は呼び出していません") {
			t.Errorf("送信前に弾いたことを伝えるべき: %q", msg)
		}
		if strings.Contains(msg, "HTTP 400") || heading == xapi.KindInvalidQuery.Title() {
			t.Errorf("API を呼んだように読める文言や検索向けの見出しを使ってはいけない: %q / %q", heading, msg)
		}
		// 対照: 既定の UserMessage は「HTTP 400」と書く（そのまま使ってはいけない理由）。
		if !strings.Contains(rejected.UserMessage(app.loc), "HTTP 400") {
			t.Error("前提が変わった: UserMessage が HTTP 400 と書かなくなったなら、この分岐を見直すべき")
		}
	})

	t.Run("画面でも API を呼んでいないと出し、HTTP ステータスを出さない", func(t *testing.T) {
		rec := httptest.NewRecorder()
		app.renderFollowersAPIError(rec, newTestRequest(http.MethodGet, followersPath, nil), rejected,
			query.FollowersForm{Username: "@nasa", MaxResults: 100})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
		}
		body := rec.Body.String()
		for _, want := range []string{"API は呼び出していません", "<h2>対処手順</h2>", `href="/followers?max=100&amp;username=%40nasa">フォームに戻る</a>`} {
			if !strings.Contains(body, want) {
				t.Errorf("%q が含まれていない", want)
			}
		}
		for _, bad := range []string{"HTTP ステータス:", "HTTP 400・課金なし", "検索条件", "生成されたクエリ"} {
			if strings.Contains(body, bad) {
				t.Errorf("送信前に弾いたエラーの画面に %q を出してはいけない", bad)
			}
		}
	})

	t.Run("対照: HTTP 400 は課金なしと出す", func(t *testing.T) {
		_, msg := app.followersErrorText(&xapi.APIError{Kind: xapi.KindInvalidQuery, Status: http.StatusBadRequest})
		if !strings.Contains(msg, "（HTTP 400・課金なし）") || strings.Contains(msg, "API は呼び出していません") {
			t.Errorf("HTTP 400 は API が受け付けなかったこと（課金なし）を伝えるべき: %q", msg)
		}
	})

	t.Run("手順に検索の前提を混ぜない", func(t *testing.T) {
		kinds := []xapi.ErrorKind{
			xapi.KindInvalidQuery, xapi.KindUnauthorized, xapi.KindForbiddenNotEnrolled, xapi.KindForbidden,
			xapi.KindNotFound, xapi.KindRateLimited, xapi.KindServer, xapi.KindNetwork, xapi.KindDecode,
		}
		for _, k := range kinds {
			steps := followersErrorSteps(k)
			if len(steps) == 0 {
				t.Errorf("%s: 対処手順が無い", k.Title())
			}
			for _, s := range steps {
				for _, bad := range []string{"検索条件", "検索エンドポイント", "生成されたクエリ", "全期間", ".env"} {
					if strings.Contains(s, bad) {
						t.Errorf("%s: フォロワー取得の手順に %q を混ぜてはいけない: %q", k.Title(), bad, s)
					}
				}
			}
		}
	})
}

// TestProblemStatus は errors だけの応答から決める画面のステータスを確認する。
func TestProblemStatus(t *testing.T) {
	notFound := xapi.ProblemDetail{Type: "https://api.twitter.com/2/problems/resource-not-found"}
	notAuthorized := xapi.ProblemDetail{Type: "https://api.x.com/2/problems/not-authorized-for-resource"}
	other := xapi.ProblemDetail{Type: "https://api.twitter.com/2/problems/invalid-request"}
	cases := []struct {
		name  string
		probs []xapi.ProblemDetail
		want  int
	}{
		{"見つからない", []xapi.ProblemDetail{notFound}, http.StatusNotFound},
		{"読む権限が無い", []xapi.ProblemDetail{notAuthorized}, http.StatusForbidden},
		{"それ以外", []xapi.ProblemDetail{other}, http.StatusBadGateway},
		{"他のエラーに混ざった見つからない", []xapi.ProblemDetail{other, notFound}, http.StatusNotFound},
		{"権限が無いを優先する", []xapi.ProblemDetail{notFound, notAuthorized}, http.StatusForbidden},
		{"errors も無い想定外の応答", nil, http.StatusBadGateway},
	}
	for _, tc := range cases {
		if got := problemStatus(tc.probs); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestFollowersEstimateGateClosed は、ゲートが閉じたときに取得のリンクを無効なボタンにすることを確認する。
// ユーザーの読み取りはいまは月次 cap で止めない（常に許可）ので、テンプレートを直接描画して確かめる。
func TestFollowersEstimateGateClosed(t *testing.T) {
	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	const execute = "/followers?id=1&max=100&mode=execute&username=sample"
	p := FollowersEstimatePage{
		Page:          app.page("フォロワー取得の見積り", cost.CapStatus{}, xapi.EndpointRecent),
		User:          view.BuildUserView(xapi.User{ID: "1", Name: "n", Username: "sample"}, nil),
		Est:           cost.EstimateFollowers(12345, true, 100),
		LookupCost:    cost.UserLookupCost(),
		Allowed:       false,
		GateReason:    "テスト用の停止理由です。",
		ExecuteURL:    execute,
		ReestimateURL: "/followers?max=100&mode=estimate&username=sample",
		FormURL:       "/followers?max=100&username=sample",
	}
	out := mustRender(t, app, "followers_estimate.html", p)
	for _, want := range []string{
		`<span class="btn" aria-disabled="true">最初の 100 人を取得する（実行不可）</span>`,
		"実行できません", "テスト用の停止理由です。",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ゲートが閉じた見積りに %q が無い", want)
		}
	}
	if strings.Contains(out, `href="/followers?id=1&amp;max=100&amp;mode=execute`) {
		t.Error("ゲートが閉じているときに取得のリンクを出してはいけない")
	}

	// 対照: 許可されていればリンクになり、無効なボタンと理由は出ない。
	p.Allowed, p.GateReason = true, ""
	out = mustRender(t, app, "followers_estimate.html", p)
	if !strings.Contains(out, `<a class="btn" href="/followers?id=1&amp;max=100&amp;mode=execute&amp;username=sample">最初の 100 人を取得する（最大 $1.00）</a>`) {
		t.Error("許可されているときは取得のリンクを出すべき")
	}
	if strings.Contains(out, `aria-disabled="true"`) || strings.Contains(out, "実行できません") {
		t.Error("許可されているときに無効なボタンや停止の理由を出してはいけない")
	}
}

// TestUserCardMarkup はユーザーカード（_user.html）の表示要件・エスケープ・欠けた値の扱いを確認する。
func TestUserCardMarkup(t *testing.T) {
	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	render := func(t *testing.T, u xapi.User) string {
		t.Helper()
		out := mustRender(t, app, "followers_estimate.html", FollowersEstimatePage{
			Page:       app.page("フォロワー取得の見積り", cost.CapStatus{}, xapi.EndpointRecent),
			User:       view.BuildUserView(u, jst(t)),
			Est:        cost.EstimateFollowers(1, true, 100),
			LookupCost: cost.UserLookupCost(),
			Allowed:    true,
		})
		if n := strings.Count(out, `<article class="user-card">`); n != 1 {
			t.Fatalf("ユーザーカードはちょうど 1 枚であるべき: got %d", n)
		}
		return between(t, out, `<article class="user-card">`, "</article>")
	}

	t.Run("表示要件・エスケープ・各項目", func(t *testing.T) {
		card := render(t, xapi.User{
			ID:              "42",
			Name:            `<script>alert(1)</script>`,
			Username:        "card_user",
			ProfileImageURL: "https://pbs.twimg.com/profile_images/1/card_normal.jpg",
			Verified:        true,
			VerifiedType:    "blue",
			Description:     "1 行目\n<img src=x onerror=alert(1)>",
			Location:        "東京",
			URL:             "https://example.com/?a=1&b=2",
			Protected:       true,
			CreatedAt:       time.Date(2015, 6, 6, 15, 0, 0, 0, time.UTC),
			PublicMetrics:   &xapi.UserMetrics{FollowersCount: 12345, FollowingCount: 210, TweetCount: 9876, ListedCount: 55},
		})
		profile := "https://x.com/card_user"
		for _, want := range []string{
			// 表示要件: プロフィール画像・表示名・@username はいずれもプロフィールへリンクする。
			`<a href="` + profile + `" rel="noreferrer" aria-label=`,
			`<img class="avatar" src="https://pbs.twimg.com/profile_images/1/card_200x200.jpg"`,
			`width="48" height="48" loading="lazy" decoding="async"`,
			`<a class="author-name" href="` + profile + `" rel="noreferrer">`,
			`<a class="author-handle" href="` + profile + `" rel="noreferrer">@card_user</a>`,
			`<a class="x-logo" href="` + profile + `" title="X で表示" rel="noreferrer">`,
			`<span class="verified" title="認証済み（blue）">✔</span>`,
			`<span class="flag">鍵アカウント</span>`,
			// 自己紹介は改行を残したまま、エスケープして出す。
			"<div class=\"user-bio\">1 行目\n&lt;img src=x onerror=alert(1)&gt;</div>",
			"<span>東京</span>",
			`<a href="https://example.com/?a=1&amp;b=2" rel="noreferrer nofollow">`,
			// 登録日は表示タイムゾーン（JST）の日付、datetime は UTC。
			`<time datetime="2015-06-06T15:00:00Z">2015-06-07</time> に登録`,
			"<span>フォロワー 12,345</span>", "<span>フォロー 210</span>", "<span>投稿 9,876</span>", "<span>リスト 55</span>",
			"&lt;script&gt;alert(1)&lt;/script&gt;",
		} {
			if !strings.Contains(card, want) {
				t.Errorf("カードに %q が無い:\n%s", want, card)
			}
		}
		// アバターは表示名・@username の左に置き、X ロゴは右上（最後）に置く。
		iAvatar := strings.Index(card, `class="avatar"`)
		iName := strings.Index(card, `class="author-name"`)
		iHandle := strings.Index(card, `class="author-handle"`)
		iLogo := strings.Index(card, `class="x-logo"`)
		if !(iAvatar < iName && iName < iHandle && iHandle < iLogo) {
			t.Errorf("並びはアバター → 表示名 → @username → X ロゴであるべき: %d %d %d %d", iAvatar, iName, iHandle, iLogo)
		}
		for _, bad := range []string{"<script>alert(1)", "<img src=x", "ZgotmplZ", "<br>"} {
			if strings.Contains(card, bad) {
				t.Errorf("カードに %q を出してはいけない（エスケープ漏れ・未加工でない）", bad)
			}
		}
	})

	t.Run("欠けた値はリンクも空の欄も出さない", func(t *testing.T) {
		card := render(t, xapi.User{ID: "43", Name: "名前だけ", URL: "javascript:alert(1)"})
		if !strings.Contains(card, `<div class="avatar avatar-missing" aria-hidden="true">?</div>`) {
			t.Error("画像が無いときはプレースホルダを出すべき")
		}
		if !strings.Contains(card, `<span class="author-name">名前だけ</span>`) {
			t.Error("プロフィールのリンク先が無いときも表示名は出すべき")
		}
		for _, bad := range []string{
			"href=", // ユーザー名が無ければプロフィールへのリンクは作れない（空の href は今のページを指す）
			`class="author-handle"`, `class="x-logo"`, ">@<",
			"javascript:", `class="user-meta"`, `class="metrics"`, `class="user-bio"`,
		} {
			if strings.Contains(card, bad) {
				t.Errorf("欠けた値のカードに %q を出してはいけない:\n%s", bad, card)
			}
		}
	})
}
