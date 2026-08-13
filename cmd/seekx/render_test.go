package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/shutx-net/seekx/internal/config"
	"github.com/shutx-net/seekx/internal/cost"
	"github.com/shutx-net/seekx/internal/query"
	"github.com/shutx-net/seekx/internal/view"
	"github.com/shutx-net/seekx/internal/xapi"
)

// fixtureDir はリポジトリ直下の testdata を指す（go test の cwd はパッケージディレクトリ）。
const fixtureDir = "../../testdata"

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func loadFixture(t *testing.T, name string, out any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("フィクスチャ %s の読み込みに失敗: %v", name, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("フィクスチャ %s の Unmarshal に失敗: %v", name, err)
	}
}

func jst(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatalf("Asia/Tokyo が解決できません: %v", err)
	}
	return loc
}

// truncate はテストの失敗メッセージを短く保つためのローカルヘルパ。
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func testApp(t *testing.T, cfg config.Config) *App {
	t.Helper()
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = "http://127.0.0.1:1/unused-in-tests"
	}
	if cfg.DisplayTZ == "" {
		cfg.DisplayTZ = "Asia/Tokyo"
	}
	if cfg.MaxResultsCap == 0 {
		cfg.MaxResultsCap = 100
	}
	if cfg.SessionBudgetUSD == 0 {
		cfg.SessionBudgetUSD = 5.0
	}
	log := discardLogger()
	opts := clientOptions(cfg)
	opts.Sleep = func(time.Duration) {} // テストで実時間を待たない（リトライ・ペーシング）
	return &App{
		cfg:     cfg,
		client:  xapi.NewClient(opts, log),
		tmpl:    tmpl,
		log:     log,
		session: &cost.SessionSpend{},
		loc:     cfg.Location(),
		now:     func() time.Time { return time.Date(2026, 8, 13, 9, 0, 0, 0, cfg.Location()) },
	}
}

func sampleViewResult(t *testing.T, fixture string) view.ViewResult {
	t.Helper()
	var resp xapi.SearchResponse
	loadFixture(t, fixture, &resp)
	loc := jst(t)
	v := view.BuildView(&resp, view.ViewOptions{
		Now:        time.Date(2026, 8, 13, 9, 0, 0, 0, loc),
		Loc:        loc,
		RateLimit:  &xapi.RateLimit{Limit: 450, Remaining: 449, Reset: time.Unix(1786000000, 0)},
		SpentUSD:   0.05,
		SessionUSD: 0.055,
	})
	v.QueryString = "猫 has:images lang:ja -is:retweet"
	v.QueryRunes, v.QueryBytes = query.QueryLength(v.QueryString)
	v.QueryLimit = query.QueryLimitRecent
	v.EndpointName = "直近7日"
	v.MaxResults = 10
	v.StartDisplay = "2026-08-06 00:00"
	v.EndDisplay = "2026-08-13 09:00"
	return v
}

// TestRenderTemplates は全テンプレートを代表的なデータで描画し、エラーが出ないことを確認する。
func TestRenderTemplates(t *testing.T) {
	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	cap := cost.CapStatus{Usage: 42, Cap: 2000000, Known: true, ResetDay: 12}

	t.Run("form.html", func(t *testing.T) {
		p := FormPage{
			Page:           app.page("検索", cap, xapi.EndpointRecent),
			Form:           query.SearchForm{MaxResults: 10, Endpoint: "recent"},
			MaxOptions:     maxOptionsFor(100),
			LangOptions:    langOptions,
			MaxCost:        0.05,
			StandaloneOps:  query.StandaloneOperators,
			ConjunctionOps: query.ConjunctionRequiredOperators,
		}
		out := mustRender(t, app, "form.html", p)
		for _, want := range []string{
			`name="kw"`, `name="phrase"`, `name="or"`, `name="exclude"`, `name="hashtag"`,
			`name="from"`, `name="to"`, `name="mention"`, `name="retweets_of"`, `name="list"`,
			`name="reply"`, `name="conv"`, `name="in_reply_to"`,
			`name="images"`, `name="video"`, `name="media"`, `name="links"`, `name="lang"`,
			`name="min_likes"`, `name="min_replies"`, `name="min_reposts"`, `name="include_reposts"`,
			`name="start"`, `name="end"`, `name="endpoint"`, `name="max"`,
			`value="estimate"`, `value="execute"`,
			"has:images", "フィクスチャモード",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("form.html に %q が含まれていない", want)
			}
		}
		// 全コントロールに <label for> が付いている（サンプル確認）。
		for _, id := range []string{"kw", "phrase", "or", "exclude", "hashtag", "from", "lang", "max", "start", "end"} {
			if !strings.Contains(out, `for="`+id+`"`) {
				t.Errorf("label for=%q がない", id)
			}
		}
	})

	t.Run("form.html（エラー・警告表示）", func(t *testing.T) {
		p := FormPage{
			Page:        app.page("検索", cap, xapi.EndpointRecent),
			Form:        query.SearchForm{HasImages: true, MaxResults: 10, Endpoint: "recent"},
			Errors:      []string{"[V01] 画像付きだけでは検索できません。"},
			Warnings:    []string{"[V07] 終了日時を調整しました。"},
			MaxOptions:  maxOptionsFor(100),
			LangOptions: langOptions,
		}
		out := mustRender(t, app, "form.html", p)
		if !strings.Contains(out, "V01") || !strings.Contains(out, "V07") {
			t.Error("エラー・警告が表示されていない")
		}
	})

	t.Run("estimate.html", func(t *testing.T) {
		var counts xapi.CountsResponse
		loadFixture(t, "counts_recent.json", &counts)
		est := cost.EstimateSearch(counts.Meta.TotalPostCount, 10)
		est.Granularity = "day"
		p := EstimatePage{
			Page:          app.page("見積り", cap, xapi.EndpointRecent),
			Form:          query.SearchForm{Keywords: "猫", MaxResults: 10, Endpoint: "recent"},
			Query:         "猫 has:images lang:ja -is:retweet",
			QueryLimit:    query.QueryLimitRecent,
			EndpointName:  "直近7日",
			StartDisplay:  "2026-08-06 00:00",
			EndDisplay:    "2026-08-13 09:00",
			RangeUTC:      "2026-08-05T15:00:00Z 〜 2026-08-13T00:00:00Z",
			Granularity:   "day",
			Total:         counts.Meta.TotalPostCount,
			MaxResults:    10,
			Est:           est,
			Buckets:       bucketViews(counts.Data, jst(t), "day"),
			Warnings:      []string{"終了日時を調整しました。"},
			Allowed:       true,
			ExecuteURL:    "/search?kw=%E7%8C%AB&mode=execute",
			ReestimateURL: "/search?kw=%E7%8C%AB&mode=estimate",
			FormURL:       "/?kw=%E7%8C%AB",
		}
		p.QueryRunes, p.QueryBytes = query.QueryLength(p.Query)
		out := mustRender(t, app, "estimate.html", p)
		for _, want := range []string{"10,000", "この条件で検索する", "文字", "バイト", "bucket-bar"} {
			if !strings.Contains(out, want) {
				t.Errorf("estimate.html に %q が含まれていない", want)
			}
		}
		if strings.Contains(out, "ZgotmplZ") {
			t.Error("テンプレートが値を安全でないと判断した（ZgotmplZ）")
		}
	})

	t.Run("estimate.html（ゲートで実行不可）", func(t *testing.T) {
		p := EstimatePage{
			Page:       app.page("見積り", cost.CapStatus{Usage: 1960000, Cap: 2000000, Ratio: 0.98, Level: cost.CapBlock, Known: true, ResetDay: 12}, xapi.EndpointRecent),
			Query:      "猫",
			QueryLimit: query.QueryLimitRecent,
			Est:        cost.EstimateSearch(100, 10),
			Allowed:    false,
			GateReason: "月次上限の 98.0% を消費しているため実行を停止しました。",
			FormURL:    "/",
		}
		out := mustRender(t, app, "estimate.html", p)
		if !strings.Contains(out, `aria-disabled="true"`) {
			t.Error("実行ボタンが無効化されていない")
		}
		if !strings.Contains(out, "実行を停止") {
			t.Error("理由が表示されていない")
		}
		if !strings.Contains(out, "banner-block") {
			t.Error("cap 超過バナーが出ていない")
		}
	})

	t.Run("results.html（画像・動画・エッジを含む）", func(t *testing.T) {
		for _, fixture := range []string{
			"search_recent.json", "search_recent_photos.json",
			"search_recent_video.json", "search_recent_edge.json",
			"search_recent_partial_errors.json",
		} {
			v := sampleViewResult(t, fixture)
			p := ResultsPage{
				Page:           app.page("検索結果", cap, xapi.EndpointRecent),
				Result:         v,
				NextURL:        "/search?kw=x&mode=execute&token=abc",
				NextCost:       0.05,
				FormURL:        "/?kw=x",
				EstimateURL:    "/search?kw=x&mode=estimate",
				EmptyHints:     emptyHints(query.SearchForm{}),
				RateLimitReset: "12:34:56",
			}
			out := mustRender(t, app, "results.html", p)
			if strings.Contains(out, "ZgotmplZ") {
				t.Errorf("%s: ZgotmplZ が出力された", fixture)
			}
			if !strings.Contains(out, "次の") {
				t.Errorf("%s: ページングリンクがない", fixture)
			}
		}
	})

	t.Run("results.html（0 件）", func(t *testing.T) {
		v := sampleViewResult(t, "search_recent_empty.json")
		p := ResultsPage{
			Page:       app.page("検索結果", cap, xapi.EndpointRecent),
			Result:     v,
			FormURL:    "/",
			EmptyHints: emptyHints(query.SearchForm{Lang: "ja", HasImages: true}),
		}
		out := mustRender(t, app, "results.html", p)
		if !strings.Contains(out, "0 件でした") {
			t.Error("0 件のメッセージがない")
		}
		if !strings.Contains(out, "lang:ja") {
			t.Error("緩和ヒントがない")
		}
	})

	t.Run("results.html（ページングがゲートで無効）", func(t *testing.T) {
		v := sampleViewResult(t, "search_recent_photos.json")
		p := ResultsPage{
			Page:          app.page("検索結果", cap, xapi.EndpointRecent),
			Result:        v,
			NextURL:       "/search?mode=execute&token=abc",
			PagingBlocked: true,
			PagingReason:  "セッション予算を超えました。",
			FormURL:       "/",
		}
		out := mustRender(t, app, "results.html", p)
		if !strings.Contains(out, `aria-disabled="true"`) {
			t.Error("ページングリンクが無効化されていない")
		}
		if strings.Contains(out, `href="/search?mode=execute&amp;token=abc"`) {
			t.Error("無効化時にリンクを出してはいけない")
		}
	})

	t.Run("error.html（全 ErrorKind）", func(t *testing.T) {
		kinds := []xapi.ErrorKind{
			xapi.KindInvalidQuery, xapi.KindUnauthorized, xapi.KindForbiddenNotEnrolled, xapi.KindForbidden,
			xapi.KindNotFound, xapi.KindRateLimited, xapi.KindServer, xapi.KindNetwork, xapi.KindDecode, xapi.KindUsageCap,
		}
		for _, k := range kinds {
			ae := &xapi.APIError{Kind: k, Status: 400, Title: "Invalid Request", Detail: "詳細", Reason: "client-not-enrolled",
				Messages: []string{"errors[].message の内容"}}
			p := ErrorPage{
				Page:     app.page("エラー", cap, xapi.EndpointRecent),
				Heading:  k.Title(),
				Message:  ae.UserMessage(jst(t)),
				Steps:    errorSteps(k),
				Status:   ae.Status,
				APITitle: ae.Title,
				Detail:   ae.Detail,
				Reason:   ae.Reason,
				Messages: ae.Messages,
				Query:    "猫 has:images",
				FormURL:  "/",
			}
			out := mustRender(t, app, "error.html", p)
			if !strings.Contains(out, k.Title()) {
				t.Errorf("%v: 見出しがない", k)
			}
			if !strings.Contains(out, "フォームに戻る") {
				t.Errorf("%v: 戻るリンクがない", k)
			}
		}
	})
}

func mustRender(t *testing.T, app *App, name string, data any) string {
	t.Helper()
	var sb strings.Builder
	if err := app.tmpl.ExecuteTemplate(&sb, name, data); err != nil {
		t.Fatalf("%s の描画に失敗: %v", name, err)
	}
	out := sb.String()
	if out == "" {
		t.Fatalf("%s の出力が空", name)
	}
	if !strings.Contains(out, "<!doctype html>") {
		t.Errorf("%s: レイアウトが適用されていない", name)
	}
	return out
}

// TestNoRawHTML は本文の HTML がエスケープされることを確認する（D-24 / P-TEMPLATE-TEXT）。
func TestNoRawHTML(t *testing.T) {
	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	v := sampleViewResult(t, "search_recent_edge.json")

	found := false
	for _, p := range v.Posts {
		if strings.Contains(p.Text, "<script>") {
			found = true
		}
	}
	if !found {
		t.Fatal("フィクスチャに <script> を含む投稿が必要")
	}

	out := mustRender(t, app, "results.html", ResultsPage{
		Page:    app.page("検索結果", cost.CapStatus{}, xapi.EndpointRecent),
		Result:  v,
		FormURL: "/",
	})
	if strings.Contains(out, "<script>alert") {
		t.Error("本文の <script> がエスケープされていない（XSS）")
	}
	if !strings.Contains(out, "&lt;script&gt;alert") {
		t.Error("エスケープされた本文が見つからない")
	}
	// <br> 置換をしていないこと（改行は CSS の pre-wrap で表現する）。
	if strings.Contains(out, "改行<br>") {
		t.Error("本文を <br> に置換してはいけない")
	}
	// 改行の表現は style.css の white-space: pre-wrap が担うため、テンプレート側は
	// 素の本文をそのまま出す。CSS 側の存在は TestStyleSheetInvariants で確認する。
}

// TestDisplayRequirementsPresence は表示要件の必須要素が出力に含まれることを確認する。
func TestDisplayRequirementsPresence(t *testing.T) {
	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	v := sampleViewResult(t, "search_recent_photos.json")
	out := mustRender(t, app, "results.html", ResultsPage{
		Page:    app.page("検索結果", cost.CapStatus{}, xapi.EndpointRecent),
		Result:  v,
		FormURL: "/",
	})

	checks := map[string]string{
		"C-01 表示名":       `class="author-name"`,
		"C-02 @username": `class="author-handle"`,
		"C-03 プロフィール画像":  `class="avatar"`,
		"C-04 本文":        `class="post-text"`,
		"C-05 X ロゴ":      `/static/x-logo.svg`,
		"C-07 恒久リンク":     `https://x.com/sample_taro/status/1900000000000000001`,
	}
	for label, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("%s: %q が出力に含まれていない", label, want)
		}
	}
	// 投稿者不明でもカードを描画し、プレースホルダを出す。
	if !strings.Contains(out, view.UnknownAuthorName) {
		t.Error("投稿者不明のプレースホルダが出ていない")
	}
	if !strings.Contains(out, "削除済み・非公開アカウントの可能性") {
		t.Error("投稿者不明の説明が出ていない")
	}
	// X ロゴは投稿カード内（作者情報の近傍）にある。
	firstPost := strings.Index(out, `class="post"`)
	logo := strings.Index(out, "/static/x-logo.svg")
	firstText := strings.Index(out, `class="post-text"`)
	if !(firstPost < logo && logo < firstText) {
		t.Error("X ロゴが投稿カードのヘッダ位置にない")
	}
}

// TestHandlersFixtureMode はフィクスチャモードで全画面を一巡できることを確認する（API 呼び出し 0 回）。
func TestHandlersFixtureMode(t *testing.T) {
	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	router := newRouter(app)

	get := func(t *testing.T, target string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	t.Run("GET / でフォームが出る", func(t *testing.T) {
		rec := get(t, "/")
		if rec.Code != http.StatusOK {
			t.Fatalf("status: %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `action="/search"`) {
			t.Error("フォームがない")
		}
	})

	t.Run("GET /healthz", func(t *testing.T) {
		rec := get(t, "/healthz")
		if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Errorf("status=%d body=%q", rec.Code, rec.Body.String())
		}
	})

	t.Run("GET /static/style.css", func(t *testing.T) {
		rec := get(t, "/static/style.css")
		if rec.Code != http.StatusOK {
			t.Fatalf("status: %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), ".media-grid") {
			t.Error("CSS が配信されていない")
		}
		if rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("Cache-Control: %q", rec.Header().Get("Cache-Control"))
		}
	})

	t.Run("conjunction-required のみは API を呼ばずエラー表示", func(t *testing.T) {
		rec := get(t, "/search?images=1&mode=execute")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status: %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "V01") {
			t.Error("V01 エラーが表示されていない")
		}
		if !strings.Contains(body, "API は呼び出していません") {
			t.Error("API を呼んでいないことを明示すべき")
		}
	})

	t.Run("mode=estimate で見積り画面", func(t *testing.T) {
		rec := get(t, "/search?kw=%E7%8C%AB&images=1&lang=ja&max=10&endpoint=recent&mode=estimate")
		if rec.Code != http.StatusOK {
			t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "見積り") {
			t.Error("見積り画面ではない")
		}
		if !strings.Contains(body, "10,000") {
			t.Error("counts の総件数が出ていない")
		}
		if !strings.Contains(body, "この条件で検索する") {
			t.Error("実行ボタンがない")
		}
		if !strings.Contains(body, "mode=execute") {
			t.Error("実行リンクがない")
		}
		if !strings.Contains(body, "猫 has:images lang:ja -is:retweet") {
			t.Error("生成クエリが表示されていない")
		}
	})

	t.Run("mode=execute で結果画面", func(t *testing.T) {
		rec := get(t, "/search?kw=%E7%8C%AB&max=10&endpoint=recent&mode=execute")
		if rec.Code != http.StatusOK {
			t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "検索結果") {
			t.Error("結果画面ではない")
		}
		if !strings.Contains(body, "sample_taro") {
			t.Error("投稿が描画されていない")
		}
		if !strings.Contains(body, "フィクスチャモード") {
			t.Error("フィクスチャモードバッジがない")
		}
	})

	t.Run("mode 未指定はフォームに戻す", func(t *testing.T) {
		rec := get(t, "/search?kw=%E7%8C%AB")
		if rec.Code != http.StatusOK {
			t.Fatalf("status: %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `action="/search"`) {
			t.Error("フォームに戻すべき")
		}
	})

	t.Run("フィクスチャモードではセッションコストが増えない", func(t *testing.T) {
		app2 := testApp(t, config.Config{FixtureDir: fixtureDir})
		r2 := newRouter(app2)
		for _, target := range []string{
			"/search?kw=%E7%8C%AB&mode=estimate",
			"/search?kw=%E7%8C%AB&mode=execute",
		} {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			r2.ServeHTTP(httptest.NewRecorder(), req)
		}
		if got := app2.session.Total(); got != 0 {
			t.Errorf("フィクスチャモードでは課金 0: got %v", got)
		}
	})

	t.Run("recent で 8 日前を指定すると API を呼ばず V05", func(t *testing.T) {
		rec := get(t, "/search?kw=%E7%8C%AB&start=2026-08-01T00:00&endpoint=recent&mode=execute")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status: %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "V05") {
			t.Error("V05 エラーが必要")
		}
	})
}

// TestErrorPages は偽サーバで各エラー応答を再現し、専用の日本語画面が出ることを確認する。
func TestErrorPages(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantText   string
	}{
		{"400 不正なクエリ", http.StatusBadRequest,
			`{"errors":[{"parameters":{"query":["has:images"]},"message":"no viable alternative"}],"title":"Invalid Request","detail":"One or more parameters to your request was invalid."}`,
			http.StatusBadRequest, "課金なし"},
		{"401 認証失敗", http.StatusUnauthorized,
			`{"title":"Unauthorized","status":401,"detail":"Unauthorized"}`,
			http.StatusForbidden, "BEARER_TOKEN"},
		{"403 未エンロール", http.StatusForbidden,
			`{"title":"Client Forbidden","reason":"client-not-enrolled","detail":"not attached to a Project"}`,
			http.StatusForbidden, "client-not-enrolled"},
		{"429 レートリミット", http.StatusTooManyRequests,
			`{"title":"Too Many Requests","errors":[{"code":88,"message":"Rate limit exceeded"}]}`,
			http.StatusTooManyRequests, "レートリミット"},
		{"500 サーバエラー", http.StatusInternalServerError,
			`{"title":"Internal Server Error"}`,
			http.StatusBadGateway, "一時的なエラー"},
		{"不正な JSON", http.StatusOK,
			`{"data": [ broken`,
			http.StatusBadGateway, "解析に失敗"},
	}

	const secret = "SECRET-BEARER-TOKEN-DO-NOT-LEAK-123456"

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("x-rate-limit-limit", "450")
				w.Header().Set("x-rate-limit-remaining", "0")
				w.Header().Set("x-rate-limit-reset", "1786000000")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			app := testApp(t, config.Config{BearerToken: secret, APIBaseURL: srv.URL})
			router := newRouter(app)

			req := httptest.NewRequest(http.MethodGet, "/search?kw=%E7%8C%AB&max=10&mode=execute", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status: got %d, want %d", rec.Code, tc.wantStatus)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.wantText) {
				t.Errorf("エラー画面に %q が含まれていない:\n%s", tc.wantText, truncate(body, 400))
			}
			// トークンや Authorization は絶対に画面に出さない（P-TOKEN-LEAK）。
			if strings.Contains(body, secret) {
				t.Error("エラー画面にトークンが含まれている")
			}
			if strings.Contains(strings.ToLower(body), "authorization") {
				t.Error("エラー画面に Authorization が含まれている")
			}
			if strings.Contains(body, srv.URL) {
				t.Error("エラー画面にリクエスト URL が含まれている")
			}
			if !strings.Contains(body, "フォームに戻る") {
				t.Error("フォームに戻るリンクがない")
			}
		})
	}
}

// TestFormRoundTrip はフォーム → URL → フォームの往復が壊れないことを確認する（D-23）。
func TestFormRoundTrip(t *testing.T) {
	in := query.SearchForm{
		Keywords: "猫 (犬 OR 鳥)", ExcludeWords: "速報", OrWords: "猫 犬", ExactPhrase: "機械学習 入門",
		Hashtag: "#東京", FromUser: "nasa", ToUser: "spacex", MentionUser: "x", RetweetsOf: "nasa",
		ListID: "123", ReplyMode: "only", ConversationID: "456", InReplyToPostID: "789",
		HasImages: true, HasVideo: true, HasMedia: true, HasLinks: true, Lang: "ja",
		MinLikes: 10, MinReplies: 20, MinReposts: 30, IncludeReposts: true,
		StartLocal: "2026-08-12T00:00", EndLocal: "2026-08-13T00:00",
		Endpoint: "all", MaxResults: 25,
	}
	u := searchURL(in, "estimate", "tok")
	parsed, err := parseQueryOf(u)
	if err != nil {
		t.Fatal(err)
	}
	out := parseSearchForm(parsed)
	out.PaginationToken = "" // token は別扱い
	in2 := in
	in2.PaginationToken = ""
	if out != in2 {
		t.Errorf("往復で値が変わった:\n got  %+v\n want %+v", out, in2)
	}
	if parsed.Get("mode") != "estimate" || parsed.Get("token") != "tok" {
		t.Errorf("mode / token が載っていない: %v", parsed)
	}
}

func parseQueryOf(target string) (url.Values, error) {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	return req.URL.Query(), nil
}

// TestExecuteSearchIsOnlyBillingPath は課金経路がゲートを通ることを確認する。
func TestExecuteSearchIsOnlyBillingPath(t *testing.T) {
	searchCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 課金対象の検索呼び出しだけを数える（/usage/tweets は cap 表示のために呼ばれる）。
		if strings.HasPrefix(r.URL.Path, "/tweets/search/") {
			searchCalls++
		}
		_, _ = w.Write([]byte(`{"data":[],"meta":{"result_count":0}}`))
	}))
	defer srv.Close()

	app := testApp(t, config.Config{BearerToken: "t", APIBaseURL: srv.URL, SessionBudgetUSD: 0.001})
	router := newRouter(app)

	// セッション予算 $0.001 では max_results=10（最大 $0.15）が通らない。
	req := httptest.NewRequest(http.MethodGet, "/search?kw=%E7%8C%AB&max=10&mode=execute", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("ゲートで止めるべき: status=%d", rec.Code)
	}
	if searchCalls != 0 {
		t.Errorf("ゲート不成立時に検索 API を呼んではいけない: searchCalls=%d", searchCalls)
	}
	if !strings.Contains(rec.Body.String(), "セッション") {
		t.Errorf("理由が表示されていない: %s", truncate(rec.Body.String(), 300))
	}
}

// TestEstimateChargesCountsOnlyOnSuccess は counts の課金計上が成功時のみ行われることを
// 確認する。リソースを返さない 400 / 429 は無課金（README のコスト表）。
// 失敗時も計上していた不具合の回帰テスト。「常に計上しない」という誤修正でも通らないよう、
// 成功時に計上されることも同時に固定する。
// TestFilterAccordion は絞り込みがアコーディオンとして描画され、既定で閉じていることを
// 確認する。件数表示とコスト注記は折りたたみの中、絞り込み中の件数バッジは summary 側。
// TestColumnIcons は列数の切り替えがアイコンで描画され、ネイティブのラジオが
// 残っていることを確認する。input を display: none で隠すとキーボードで操作できなくなる。
func TestColumnIcons(t *testing.T) {
	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	out := resultsHTML(t, app, "search_recent_edge.json")

	// ラジオは 3 つとも残っている（見た目だけ変える）。
	for _, v := range []string{"1", "2", "3"} {
		if !strings.Contains(out, `<input type="radio" name="cols" value="`+v+`"`) {
			t.Errorf("cols=%s のラジオが無い（ネイティブのラジオを残すこと）", v)
		}
	}
	// 棒の本数が列数を表す。1 列 = 1 本、3 列 = 3 本。
	for n, want := range map[int]string{
		1: `<span class="cols-icon" aria-hidden="true"><span></span></span>`,
		2: `<span class="cols-icon" aria-hidden="true"><span></span><span></span></span>`,
		3: `<span class="cols-icon" aria-hidden="true"><span></span><span></span><span></span></span>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%d 列のアイコンの棒の本数が合っていない", n)
		}
	}
	// アイコンだけでは何の選択肢か伝わらないので、読み上げ用のテキストを残す。
	for _, v := range []string{"1 列", "2 列", "3 列"} {
		if !strings.Contains(out, `<span class="sr-only">`+v+`</span>`) {
			t.Errorf("%q の読み上げ用テキストが無い", v)
		}
	}
	// 見出しは視覚的にも残す（数字を消したぶん、唯一の文字情報になる）。
	if !strings.Contains(out, `<legend>表示列数</legend>`) {
		t.Error("表示列数の見出しは視覚的に残す")
	}
	// 検索フォームと共用のクラスを列数側で使い回さない（変更が波及する）。
	i := strings.Index(out, `<fieldset aria-describedby="cols-note">`)
	end := strings.Index(out[i:], "</fieldset>")
	if block := out[i : i+end]; strings.Contains(block, `class="radio"`) || strings.Contains(block, `class="checks"`) {
		t.Error("列数の操作部で .checks / .radio を使わない（フォーム画面に波及する）")
	}
}

// TestStickyHeader は上部固定の CSS が壊れていないことを確認する。
// 固定するのはヘッダと一覧の操作部だけで、クエリ表示・メタ情報・ページングは流す。
func TestStickyHeader(t *testing.T) {
	b, err := staticFS.ReadFile("static/style.css")
	if err != nil {
		t.Fatalf("style.css を読めない: %v", err)
	}
	css := string(b)

	// 固定は 720px 以上のみ。狭い画面で固定すると投稿を読む領域が残らない。
	i := strings.Index(css, "@media (min-width: 720px)")
	if i < 0 {
		t.Fatal("固定は @media (min-width: 720px) の中に置く")
	}
	// メディアクエリのブロックだけを切り出す（波括弧を数える）。ファイル末尾までを見ると
	// 無関係な .dialog-close の position: sticky まで数えてしまう。
	open := strings.Index(css[i:], "{")
	if open < 0 {
		t.Fatal("@media のブロックが開いていない")
	}
	depth, end := 0, -1
	for j := i + open; j < len(css); j++ {
		switch css[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = j
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		t.Fatal("@media のブロックが閉じていない")
	}
	sticky := css[i:end]
	if n := strings.Count(sticky, "position: sticky"); n != 2 {
		t.Errorf("固定するのは .site-header と .list-controls の 2 つだけ: position: sticky = %d", n)
	}

	// 固定要素は必ず背景を塗る。透明だと下のカードが透ける。
	for _, block := range []string{".site-header {", ".list-controls {"} {
		j := strings.Index(sticky, block)
		if j < 0 {
			t.Errorf("%s が固定ブロックに無い", block)
			continue
		}
		end := strings.Index(sticky[j:], "}")
		if !strings.Contains(sticky[j:j+end], "background: var(--bg)") {
			t.Errorf("%s に background 指定が無い（下のカードが透ける）", block)
		}
	}

	// 流す側を誤って固定していないこと。
	for _, must := range []string{".query-box", ".meta-list", ".paging"} {
		j := strings.Index(sticky, must+" {")
		if j < 0 {
			continue
		}
		end := strings.Index(sticky[j:], "}")
		if strings.Contains(sticky[j:j+end], "position: sticky") {
			t.Errorf("%s は固定しない（ページングは金額付きリンクなので誤クリックを招く）", must)
		}
	}

	// 絞り込みを開いたときに画面を埋め尽くさないよう、高さを制限して内部スクロールにする。
	if !strings.Contains(sticky, "max-height: calc(100vh") || !strings.Contains(sticky, "overflow-y: auto") {
		t.Error("固定した操作部は max-height + overflow-y で画面内に収める")
	}
}

// TestColumnRadioStaysFocusable は列数のラジオを隠す手段が
// キーボード操作を壊していないことを確認する。
func TestColumnRadioStaysFocusable(t *testing.T) {
	b, err := staticFS.ReadFile("static/style.css")
	if err != nil {
		t.Fatalf("style.css を読めない: %v", err)
	}
	css := string(b)

	i := strings.Index(css, ".cols-option input {")
	if i < 0 {
		t.Fatal(".cols-option input の規則が無い")
	}
	end := strings.Index(css[i:], "}")
	block := css[i : i+end]

	// opacity: 0 で隠す。display: none / visibility: hidden はフォーカスを奪う。
	if !strings.Contains(block, "opacity: 0") {
		t.Error("ラジオは opacity: 0 で隠す")
	}
	for _, bad := range []string{"display: none", "visibility: hidden"} {
		if strings.Contains(block, bad) {
			t.Errorf("%q でラジオを隠してはいけない（キーボードで操作できなくなる）", bad)
		}
	}
}

func TestFilterAccordion(t *testing.T) {
	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	out := resultsHTML(t, app, "search_recent_edge.json")

	i := strings.Index(out, `<details class="filter-details"`)
	if i < 0 {
		t.Fatalf("絞り込みが <details> で描画されていない")
	}
	end := strings.Index(out[i:], "</details>")
	if end < 0 {
		t.Fatalf("</details> が無い")
	}
	body := out[i : i+end]

	// 既定は閉じている（open 属性を付けない）。
	if strings.Contains(body[:strings.Index(body, ">")+1], " open") {
		t.Error("既定は閉じた状態にする（open を付けない）")
	}
	// 件数表示・コスト注記・リセットは折りたたみの中。
	for _, want := range []string{
		`id="filter-count"`,
		`id="filter-cost-note"`,
		`id="filter-reset"`,
		`id="filter-summary-count"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("%q が <details> の中に無い", want)
		}
	}
	// 件数バッジは summary の中（閉じていても見えるのはここだけ）。
	sum := body[:strings.Index(body, "</summary>")]
	if !strings.Contains(sum, `id="filter-summary-count"`) {
		t.Error("件数バッジは summary の中に置く（閉じているとき唯一見える箇所）")
	}
	// バッジは空で出力し、絞り込みが効いたときだけ JS が文字を入れる（CSS の :empty で制御）。
	if !strings.Contains(sum, `id="filter-summary-count"></span>`) {
		t.Error("バッジは空要素として出力する（:empty で隠すため）")
	}
	// 折りたたみ対象は絞り込みだけ。列数は常に見える。
	if strings.Contains(body, `name="cols"`) {
		t.Error("列数の操作部を折りたたみの中に入れてはいけない")
	}
}

func TestEstimateChargesCountsOnlyOnSuccess(t *testing.T) {
	newApp := func(t *testing.T, status int, body string) (*App, http.Handler) {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasPrefix(r.URL.Path, "/tweets/counts/"):
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
			case strings.HasPrefix(r.URL.Path, "/usage/"):
				_, _ = w.Write([]byte(`{"data":{"project_cap":"2000000","project_usage":"0","cap_reset_day":12}}`))
			default:
				_, _ = w.Write([]byte(`{"data":[],"meta":{"result_count":0}}`))
			}
		}))
		t.Cleanup(srv.Close)
		app := testApp(t, config.Config{BearerToken: "t", APIBaseURL: srv.URL})
		return app, newRouter(app)
	}
	estimate := func(h http.Handler) {
		req := httptest.NewRequest(http.MethodGet, "/search?kw=%E7%8C%AB&max=10&mode=estimate", nil)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	t.Run("429 で失敗した counts は課金しない", func(t *testing.T) {
		app, h := newApp(t, http.StatusTooManyRequests, `{"title":"Too Many Requests","status":429}`)
		estimate(h)
		if got := app.session.Total(); got != 0 {
			t.Errorf("失敗した counts を課金してはいけない: got=%v want=0", got)
		}
	})

	t.Run("成功した counts は計上する", func(t *testing.T) {
		app, h := newApp(t, http.StatusOK, `{"data":[],"meta":{"total_tweet_count":0}}`)
		estimate(h)
		want := cost.CountsCostFor(xapi.EndpointRecent)
		if got := app.session.Total(); got != want {
			t.Errorf("成功した counts は計上する: got=%v want=%v", got, want)
		}
	})
}

func TestExecuteSearchDispatch(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`{"data":[],"meta":{"result_count":0}}`))
	}))
	defer srv.Close()

	app := testApp(t, config.Config{BearerToken: "t", APIBaseURL: srv.URL})

	p := xapi.SearchParams{Query: "猫", MaxResults: 10, Endpoint: xapi.EndpointRecent}
	if _, _, err := app.executeSearch(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	p.Endpoint = xapi.EndpointAll
	if _, _, err := app.executeSearch(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	want := []string{"/tweets/search/recent", "/tweets/search/all"}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("paths[%d] = %q, want %q", i, paths[i], want[i])
		}
	}
}

// TestDisplayRequirementsLinks は表示要件の「プロフィール画像・表示名・@username を
// いずれもプロフィールへリンクする」「タイムスタンプの隣に X で表示を置く」を検証する。
// 原文（developer.x.com/en/developer-terms/display-requirements・2026-08-13 に取得成功）に基づく。
func TestDisplayRequirementsLinks(t *testing.T) {
	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	v := sampleViewResult(t, "search_recent_photos.json")
	out := mustRender(t, app, "results.html", ResultsPage{
		Page:    app.page("検索結果", cost.CapStatus{}, xapi.EndpointRecent),
		Result:  v,
		FormURL: "/",
	})

	profile := "https://x.com/sample_taro"
	if !strings.Contains(out, `<a href="`+profile+`" rel="noreferrer" aria-label=`) {
		t.Error("プロフィール画像がプロフィールへリンクしていない")
	}
	if !strings.Contains(out, `<a class="author-name" href="`+profile+`"`) {
		t.Error("表示名がプロフィールへリンクしていない")
	}
	if !strings.Contains(out, `<a class="author-handle" href="`+profile+`"`) {
		t.Error("@username がプロフィールへリンクしていない")
	}
	if !strings.Contains(out, `class="post-view-on-x"`) {
		t.Error("「X で表示」リンクがない（アクションアイコンの代替）")
	}
	if !strings.Contains(out, "X で表示") {
		t.Error("「X で表示」の文言がない")
	}

	// アバターは表示名・@username より前（左）に来る。
	iAvatar := strings.Index(out, `class="avatar"`)
	iName := strings.Index(out, `class="author-name"`)
	iHandle := strings.Index(out, `class="author-handle"`)
	if !(iAvatar < iName && iName < iHandle) {
		t.Errorf("アバターは表示名・@username の左に置く: avatar=%d name=%d handle=%d", iAvatar, iName, iHandle)
	}

	// 投稿者不明のカードではプロフィールリンクを出さない（リンク先が存在しない）。
	if strings.Contains(out, `href="https://x.com/unknown"`) {
		t.Error("プレースホルダ作者に対してプロフィールリンクを出してはいけない")
	}
}

// TestHeaderShowsUsageCounts はヘッダに月次 cap の実数が出ることを確認する。
// パーセント表示だけだと消費が少ないとき 0.0% になり、実 API 実行前後の増加が読み取れない。
func TestHeaderShowsUsageCounts(t *testing.T) {
	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	p := FormPage{
		Page:        app.page("検索", cost.CapStatus{Usage: 62, Cap: 2000000, Ratio: 0.000031, Known: true, ResetDay: 12}, xapi.EndpointRecent),
		Form:        query.SearchForm{MaxResults: 10, Endpoint: "recent"},
		MaxOptions:  maxOptionsFor(100),
		LangOptions: langOptions,
	}
	out := mustRender(t, app, "form.html", p)
	if !strings.Contains(out, "62 / 2,000,000") {
		t.Error("ヘッダに project_usage / project_cap の実数が出ていない")
	}
	if !strings.Contains(out, "0.0%") {
		t.Error("パーセント表示も併記すべき")
	}
}

// resultsHTML は results.html を描画し、インライン <script> を除いた HTML を返す。
func resultsHTML(t *testing.T, app *App, fixture string) string {
	t.Helper()
	v := sampleViewResult(t, fixture)
	out := mustRender(t, app, "results.html", ResultsPage{
		Page:        app.page("検索結果", cost.CapStatus{}, xapi.EndpointRecent),
		Result:      v,
		FormURL:     "/",
		EstimateURL: "/search?mode=estimate",
		EmptyHints:  emptyHints(query.SearchForm{}),
	})
	return stripScripts(out)
}

// stripScripts はインライン <script> ブロックを取り除いた HTML を返す。
// layout.html のインライン JS には #post-list / data-f-* / 「件表示中」といった
// マークアップと同じ文字列が現れるため、マークアップの検証はこれを除いて行う
// （JS 側の不変条件は TestInlineScriptInvariants が別に見る）。
func stripScripts(html string) string {
	var b strings.Builder
	rest := html
	for {
		i := strings.Index(rest, "<script>")
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:i])
		j := strings.Index(rest[i:], "</script>")
		if j < 0 {
			return b.String() // 閉じタグが無いのは想定外
		}
		rest = rest[i+j+len("</script>"):]
	}
}

// TestPostListMarkup は一覧ラッパとコントロールの構造を固定する（D-40 / D-41 / D-47 / D-56）。
func TestPostListMarkup(t *testing.T) {
	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	v := sampleViewResult(t, "search_recent_edge.json")
	out := resultsHTML(t, app, "search_recent_edge.json")

	// ラッパは 1 ページに 1 つだけ（A-12）。
	if n := strings.Count(out, `id="post-list"`); n != 1 {
		t.Errorf(`id="post-list" の出現回数 = %d, want 1`, n)
	}
	if n := strings.Count(out, `<div class="post-list" id="post-list">`); n != 1 {
		t.Errorf("一覧ラッパの開始タグが 1 個であるべき: got %d", n)
	}
	if n := strings.Count(out, `id="list-controls"`); n != 1 {
		t.Errorf(`id="list-controls" の出現回数 = %d, want 1`, n)
	}

	// カードはラッパの内側にある。
	iControls := strings.Index(out, `id="list-controls"`)
	iList := strings.Index(out, `id="post-list"`)
	iFirstCard := strings.Index(out, `<article class="post"`)
	iEmpty := strings.Index(out, `id="filter-empty"`)
	iPaging := strings.Index(out, `class="paging"`)
	if !(iControls < iList && iList < iFirstCard && iFirstCard < iEmpty && iEmpty < iPaging) {
		t.Errorf("配置順が違う（コントロール → 一覧 → 0 件通知 → ページング）: controls=%d list=%d card=%d empty=%d paging=%d",
			iControls, iList, iFirstCard, iEmpty, iPaging)
	}

	// カード数が Posts と一致し、サーバは何も隠さない。
	if n := strings.Count(out, `<article class="post"`); n != len(v.Posts) {
		t.Errorf("カード数 = %d, want %d", n, len(v.Posts))
	}
	if strings.Contains(out, `<article class="post" hidden`) {
		t.Error("サーバがカードを hidden にしてはいけない")
	}

	// <form> を使わない（D-47 / P-FORM-ENTER）。list-controls の内側に <form> が無いこと。
	controls := out[iControls:iList]
	if strings.Contains(controls, "<form") {
		t.Error("list-controls の内側に <form> があってはいけない（Enter で再検索が走る）")
	}
	if strings.Contains(controls, `type="submit"`) || strings.Contains(controls, `type="reset"`) {
		t.Error("submit / reset ボタンを置いてはいけない")
	}
	if !strings.Contains(controls, `<button type="button" id="filter-reset"`) {
		t.Error("リセットは <button type=\"button\"> であるべき")
	}
	// コントロールは既定で hidden（D-52）。
	if !strings.Contains(out, `<div class="list-controls" id="list-controls" hidden>`) {
		t.Error("list-controls は hidden 属性付きで出力する")
	}
	if !strings.Contains(out, `id="filter-empty" hidden>`) {
		t.Error("filter-empty は hidden 属性付きで出力する")
	}

	// 件数の初期値はサーバが描画する（D-39）。
	want := fmt.Sprintf(`role="status">%d / %d 件表示中`, v.ResultCount, v.ResultCount)
	if !strings.Contains(out, want) {
		t.Errorf("件数の初期値 %q が出力に無い", want)
	}
	if n := strings.Count(out, "件表示中"); n != 1 {
		t.Errorf("「件表示中」の出現回数 = %d, want 1", n)
	}

	// メディアグリッドのクラス名を一覧に流用していない（P-CLASS-COLLIDE）。
	head := out[:iFirstCard]
	for _, bad := range []string{"grid-1", "grid-2", "grid-3", "grid-4"} {
		if strings.Contains(head, bad) {
			t.Errorf("一覧のマークアップに %q を使ってはいけない", bad)
		}
	}

	// アクセシビリティ（AC-01 / AC-02 / AC-03 / AC-04）。
	for _, want := range []string{
		`<legend>表示列数</legend>`,
		// 絞り込みは fieldset/legend ではなく details/summary。summary がラベル兼トグル。
		`<summary aria-describedby="filter-scope-note">このページの絞り込み（再検索なし）`,
		`aria-describedby="cols-note"`,
		`aria-describedby="filter-scope-note"`,
		`aria-describedby="f-likes-note"`,
		`role="status"`,
		`<label for="f-runes-min">`,
		`<label for="f-runes-max">`,
		`<label for="f-media-min">`,
		`<label for="f-likes-min">`,
		`name="cols" value="1"`,
		`name="cols" value="2"`,
		`name="cols" value="3"`,
		`id="f-no-reply"`, `id="f-no-repost"`, `id="f-no-quote"`,
		`id="f-no-sensitive"`, `id="f-no-link"`,
		"絞り込んでもコストは下がりません",
		"検索結果の全体には及びません",
		"画面幅が狭いときは自動的に列数を減らします",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("コントロールに %q が含まれていない", want)
		}
	}
}

// TestFilterDataAttributes はカードの data-f-* 属性を確認する（D-37 / D-48）。
func TestFilterDataAttributes(t *testing.T) {
	app := testApp(t, config.Config{FixtureDir: fixtureDir})

	t.Run("エッジフィクスチャ", func(t *testing.T) {
		v := sampleViewResult(t, "search_recent_edge.json")
		out := resultsHTML(t, app, "search_recent_edge.json")

		for _, attr := range []string{"data-f-runes", "data-f-media", "data-f-likes", "data-f-flags"} {
			if n := strings.Count(out, attr); n != len(v.Posts) {
				t.Errorf("%s の出現回数 = %d, want %d（カード 1 枚に 1 個）", attr, n, len(v.Posts))
			}
		}

		// センシティブ投稿のフラグ。
		if !strings.Contains(out, `data-f-flags="sensitive media photo"`) {
			t.Error(`data-f-flags="sensitive media photo" が無い`)
		}
		// メトリクス欠落は -1（D-49）。
		if !strings.Contains(out, `data-f-likes="-1"`) {
			t.Error(`data-f-likes="-1" が無い（metrics 欠落の表現）`)
		}
		// like_count 0 は 0。
		if !strings.Contains(out, `data-f-likes="0"`) {
			t.Error(`data-f-likes="0" が無い（いいね 0 件の表現）`)
		}
		// note_post の rune 数。
		if !strings.Contains(out, `data-f-runes="152"`) {
			t.Error(`data-f-runes="152" が無い（note_post.text の rune 数）`)
		}
		// メディア 6 枚。
		if !strings.Contains(out, `data-f-media="6"`) {
			t.Error(`data-f-media="6" が無い`)
		}
		// bool を直接出していない（P-TEMPLATE-BOOL）。
		// {{.IsReply}} は "true" / "false" と描画され、JS 側では "false" も真になる。
		// 対象は data 属性に限る。aria-hidden="true" のような正規の literal は問題ない
		// （ページ全体から `="true"` を探すと、それらに誤反応する）。
		if m := regexp.MustCompile(`data-[a-z-]+="(?:true|false)"`).FindString(out); m != "" {
			t.Errorf("bool をそのまま data 属性に出してはいけない: %s", m)
		}
		// 初期状態でどのカードも隠れていない。
		if strings.Contains(out, "post\" hidden") {
			t.Error("初期状態でカードに hidden を付けてはいけない")
		}
	})

	t.Run("動画フィクスチャ", func(t *testing.T) {
		out := resultsHTML(t, app, "search_recent_video.json")
		if !strings.Contains(out, `data-f-flags="media video"`) {
			t.Error(`data-f-flags="media video" が無い`)
		}
		if !strings.Contains(out, `data-f-flags="media gif"`) {
			t.Error(`data-f-flags="media gif" が無い`)
		}
	})
}

// TestZeroResultsHasNoControls は 0 件のときにコントロールを出さないことを確認する（D-56）。
func TestZeroResultsHasNoControls(t *testing.T) {
	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	v := sampleViewResult(t, "search_recent_empty.json")
	out := stripScripts(mustRender(t, app, "results.html", ResultsPage{
		Page:       app.page("検索結果", cost.CapStatus{}, xapi.EndpointRecent),
		Result:     v,
		FormURL:    "/",
		EmptyHints: emptyHints(query.SearchForm{Lang: "ja", HasImages: true}),
	}))

	for _, bad := range []string{"post-list", "list-controls", "filter-count", "filter-empty", "件表示中", "表示列数"} {
		if strings.Contains(out, bad) {
			t.Errorf("0 件のときに %q を出力してはいけない", bad)
		}
	}
	// 既存の 0 件通知とヒントは残る。
	if !strings.Contains(out, "0 件でした") {
		t.Error("0 件のメッセージが消えている")
	}
	if !strings.Contains(out, "lang:ja") {
		t.Error("EmptyHints が消えている")
	}
}

// TestStyleSheetInvariants は static/style.css の不変条件を固定する（css_spec）。
// staticFS から読むので、実際に配信される内容を検証している。
func TestStyleSheetInvariants(t *testing.T) {
	b, err := staticFS.ReadFile("static/style.css")
	if err != nil {
		t.Fatalf("style.css を読めない: %v", err)
	}
	css := string(b)

	for _, want := range []string{
		".post-list {",
		".post-list > .post {",
		`:root[data-cols="2"]`,
		`:root[data-cols="3"]`,
		"[hidden] { display: none !important; }",
		"align-items: start",
		".list-controls",
		".filter-count",
		".filter-details",
		// 件数バッジの表示制御は :empty に依存している。消えるとバッジが常時出る。
		".filter-badge:empty { display: none; }",
		// summary に display: flex を当てるとネイティブの開閉マーカーが消えるため、
		// ＋ / − を ::before で自前描画している。消えると開閉できることが分からなくなる。
		".filter-details > summary::before",
		".filter-details[open] > summary::before",
		// 上部固定。--header-h / min-height / top の 3 つが揃っていないと、
		// #list-controls がヘッダの下に潜るか、隙間が空いてカードが透ける。
		"--header-h:",
		"min-height: var(--header-h)",
		"top: var(--header-h)",
		"@media (min-width: 720px)",
		// 列数アイコン。読み上げ用テキストと選択・フォーカス表示が揃っていること。
		".sr-only",
		".cols-icon",
		".cols-option input:checked + .cols-icon",
		".cols-option input:focus-visible + .cols-icon",
		"@media (max-width: 719px)",
		"@media (max-width: 1039px)",
		// 本文の改行は <br> 置換ではなく CSS で表現する（D-24）。
		// テンプレート側が素の本文を出す前提なので、この宣言が消えると改行が失われる。
		"white-space: pre-wrap",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css に %q が無い", want)
		}
	}

	// P-GRID-1FR: 一覧のトラック定義は必ず minmax(0, 1fr)。
	for _, bad := range []string{"repeat(2, 1fr)", "repeat(3, 1fr)"} {
		if strings.Contains(css, bad) {
			t.Errorf("style.css に %q を書いてはいけない（D-38 / P-GRID-1FR）", bad)
		}
	}
	if n := strings.Count(css, "minmax(0, 1fr)"); n < 5 {
		t.Errorf("minmax(0, 1fr) の出現回数 = %d, want >= 5", n)
	}

	// AC-08: フォーカスリングを消さない。
	if strings.Contains(css, "outline: none") {
		t.Error("outline: none を書いてはいけない（AC-08）")
	}

	// NG-17 / NG-18: multi-column（段組み）と masonry は使わない。
	// 注意: `columns:` の単純な部分一致は grid-template-columns: に当たってしまうため
	// （style.css には以前から複数ある）、直前が "-" でない出現だけを段組みと見なす。
	for i := 0; i+len("columns:") <= len(css); i++ {
		if !strings.HasPrefix(css[i:], "columns:") {
			continue
		}
		if i > 0 && css[i-1] == '-' {
			continue // grid-template-columns: など
		}
		t.Errorf("style.css に multi-column の columns: を書いてはいけない（NG-17）: %q", css[max(0, i-40):i+20])
	}
	for _, bad := range []string{"column-count", "column-gap", "rows: masonry"} {
		if strings.Contains(css, bad) {
			t.Errorf("style.css に %q を書いてはいけない", bad)
		}
	}

	// D-42: 100vw による幅指定は横スクロールを生む。
	if strings.Contains(css, "100vw") {
		t.Error("100vw を使ってはいけない（スクロールバー幅で横スクロールが出る）")
	}

	// .avatar の意図的な円形指定が無傷であること（CS-05）。
	if n := strings.Count(css, "border-radius: 50%"); n != 1 {
		t.Errorf("border-radius: 50%% の出現回数 = %d, want 1（.avatar のみ）", n)
	}

	// P-HIDDEN-DISPLAY: .post に display を足していないこと。
	post := css[strings.Index(css, "\n.post {"):]
	post = post[:strings.Index(post, "}")]
	if strings.Contains(post, "display") {
		t.Error(".post に display を指定してはいけない（hidden 属性が効かなくなる）")
	}
}

// TestInlineScriptInvariants はインライン JS の不変条件と行数予算を固定する（D-35 / D-55）。
func TestInlineScriptInvariants(t *testing.T) {
	b, err := templatesFS.ReadFile("templates/layout.html")
	if err != nil {
		t.Fatalf("layout.html を読めない: %v", err)
	}
	html := string(b)

	// 外部 JS を持ち込まない（D-35）。
	if strings.Contains(html, "<script src") {
		t.Error("<script src> を書いてはいけない（D-35）")
	}
	// ネットワークアクセスをしない。
	for _, bad := range []string{"fetch(", "XMLHttpRequest", "WebSocket"} {
		if strings.Contains(html, bad) {
			t.Errorf("%q を使ってはいけない（絞り込みは取得済み DOM に対する操作）", bad)
		}
	}
	// 表示状態を URL に載せない（D-36）。
	for _, bad := range []string{"location", "history.push", "history.replace"} {
		if strings.Contains(html, bad) {
			t.Errorf("%q を使ってはいけない（D-36）", bad)
		}
	}
	// 既存の ES5 スタイルを維持する（js_spec.style_constraints）。
	for _, bad := range []string{"=>", "const ", "let "} {
		if strings.Contains(html, bad) {
			t.Errorf("%q を使ってはいけない（既存の ES5 スタイルに揃える）", bad)
		}
	}
	// localStorage は 2 箇所（head の getItem と body の setItem）だけ。
	if n := strings.Count(html, "localStorage"); n != 2 {
		t.Errorf("localStorage の出現回数 = %d, want 2", n)
	}
	// P-LS-THROW: どちらも try で囲われている。
	if n := strings.Count(html, "try {"); n != 2 {
		t.Errorf("try { の出現回数 = %d, want 2（localStorage アクセスがいずれも try 内）", n)
	}
	for _, part := range strings.Split(html, "localStorage")[:2] {
		if !strings.Contains(part[max(0, len(part)-200):], "try {") {
			t.Error("localStorage アクセスの直前に try { が無い（P-LS-THROW）")
		}
	}

	// 必須の要素（列数の保存キー・絞り込みの土台）。
	for _, want := range []string{
		`"seekx.cols"`,
		`setAttribute("data-cols"`,
		`removeAttribute("data-cols")`,
		`getElementById("post-list")`,
		`getElementById("list-controls")`,
		`removeAttribute("hidden")`,
		`setAttribute("hidden", "")`,
		`dialog[open]`,
		"setTimeout(apply, 150)",
		"textContent",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("インライン JS に %q が無い", want)
		}
	}
	// innerHTML を使わない（AC-04）。
	if strings.Contains(html, "innerHTML") {
		t.Error("innerHTML を使ってはいけない")
	}

	// D-55: <script> ブロック内の総行数が 90 以下。
	lines := strings.Split(html, "\n")
	inScript := false
	count := 0
	for _, ln := range lines {
		if strings.Contains(ln, "<script>") {
			inScript = true
		}
		if inScript {
			count++
		}
		if strings.Contains(ln, "</script>") {
			inScript = false
		}
	}
	if count > 90 {
		t.Errorf("<script> ブロックの行数 = %d, want <= 90（D-55）", count)
	}
	t.Logf("インライン JS の行数: %d / 90", count)
}
