package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shutx-net/seekx/internal/config"
	"github.com/shutx-net/seekx/internal/cost"
	"github.com/shutx-net/seekx/internal/query"
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
