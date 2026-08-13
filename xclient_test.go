package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestClient は httptest の偽サーバに向いた Client を作る。
// cfg.APIBaseURL の既定値（api.x.com）は一切参照しない。
func newTestClient(t *testing.T, h http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient(Config{
		BearerToken: "test-token-not-a-real-secret",
		APIBaseURL:  srv.URL,
	}, discardLogger())
	// テストで実時間を待たない。
	c.sleep = func(time.Duration) {}
	c.pacer.sleep = func(time.Duration) {}
	return c, srv
}

func testParams() SearchParams {
	return SearchParams{
		Query:       "猫 has:images lang:ja -is:retweet",
		MaxResults:  10,
		PostFields:  PostFieldsRequested,
		Expansions:  ExpansionsDefault,
		UserFields:  UserFieldsRequested,
		MediaFields: MediaFieldsRequested,
		Endpoint:    EndpointRecent,
	}
}

func TestClientSuccess(t *testing.T) {
	body, err := os.ReadFile("testdata/search_recent_photos.json")
	if err != nil {
		t.Fatal(err)
	}

	var gotAuth, gotAccept, gotUA, gotPath, gotRawQuery string
	var gotValues url.Values

	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotUA = r.Header.Get("User-Agent")
		gotPath = r.URL.Path
		gotRawQuery = r.URL.RawQuery
		gotValues = r.URL.Query()
		w.Header().Set("x-rate-limit-limit", "450")
		w.Header().Set("x-rate-limit-remaining", "449")
		w.Header().Set("x-rate-limit-reset", "1786000000")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))

	resp, rl, err := c.SearchRecent(context.Background(), testParams())
	if err != nil {
		t.Fatalf("予期しないエラー: %v", err)
	}
	if resp.Meta.ResultCount != 4 {
		t.Errorf("result_count: got %d", resp.Meta.ResultCount)
	}

	if gotAuth != "Bearer test-token-not-a-real-secret" {
		t.Errorf("Authorization ヘッダが設定されていない: %q", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept: %q", gotAccept)
	}
	if !strings.HasPrefix(gotUA, "seekx/") {
		t.Errorf("User-Agent: %q", gotUA)
	}
	if gotPath != "/tweets/search/recent" {
		t.Errorf("path: %q", gotPath)
	}

	// パラメータ名は post.fields。tweet.fields を使ってはいけない（D-10）。
	if gotValues.Get("post.fields") == "" {
		t.Error("post.fields が送られていない")
	}
	if _, ok := gotValues["tweet.fields"]; ok {
		t.Error("旧名のフィールドセレクタを送ってはいけない")
	}
	if gotValues.Get("query") != "猫 has:images lang:ja -is:retweet" {
		t.Errorf("query: %q", gotValues.Get("query"))
	}
	if gotValues.Get("max_results") != "10" {
		t.Errorf("max_results: %q", gotValues.Get("max_results"))
	}
	if gotValues.Get("expansions") != "author_id,attachments.media_keys" {
		t.Errorf("expansions: %q", gotValues.Get("expansions"))
	}
	if gotValues.Get("user.fields") != "name,username,profile_image_url,verified,verified_type" {
		t.Errorf("user.fields: %q", gotValues.Get("user.fields"))
	}
	if !strings.Contains(gotValues.Get("media.fields"), "variants") {
		t.Errorf("media.fields: %q", gotValues.Get("media.fields"))
	}
	// カンマ区切りにスペースを入れない。
	if strings.Contains(gotValues.Get("post.fields"), " ") {
		t.Errorf("post.fields にスペースが含まれる: %q", gotValues.Get("post.fields"))
	}

	if rl == nil {
		t.Fatal("RateLimit が nil")
	}
	if rl.Limit != 450 || rl.Remaining != 449 {
		t.Errorf("RateLimit: %+v", rl)
	}
	if rl.Reset.Unix() != 1786000000 {
		t.Errorf("Reset: %v", rl.Reset)
	}
	_ = gotRawQuery
}

func TestClientQueryEncoding(t *testing.T) {
	var raw string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"meta":{"result_count":0}}`))
	}))

	if _, _, err := c.SearchRecent(context.Background(), testParams()); err != nil {
		t.Fatalf("エラー: %v", err)
	}

	// 空白は %20 でなければならない（P-URLENC-PLUS）。
	if strings.Contains(raw, "+") {
		t.Errorf("生成 URL に + が含まれる（%%20 でなければならない）: %s", raw)
	}
	if !strings.Contains(raw, "%20") {
		t.Errorf("空白が %%20 になっていない: %s", raw)
	}
	if !strings.Contains(raw, "post.fields=") {
		t.Errorf("post.fields がない: %s", raw)
	}
	if !strings.Contains(raw, "%2C") {
		t.Errorf("カンマがエンコードされていない: %s", raw)
	}
}

func TestClientTimeParams(t *testing.T) {
	var vals url.Values
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vals = r.URL.Query()
		_, _ = w.Write([]byte(`{"meta":{"result_count":0}}`))
	}))

	p := testParams()
	p.StartTime = time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	p.EndTime = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	p.PaginationToken = "tok123"
	if _, _, err := c.SearchRecent(context.Background(), p); err != nil {
		t.Fatalf("エラー: %v", err)
	}
	if vals.Get("start_time") != "2026-08-06T00:00:00Z" {
		t.Errorf("start_time: %q", vals.Get("start_time"))
	}
	if vals.Get("end_time") != "2026-08-13T00:00:00Z" {
		t.Errorf("end_time: %q", vals.Get("end_time"))
	}
	if vals.Get("pagination_token") != "tok123" {
		t.Errorf("pagination_token: %q", vals.Get("pagination_token"))
	}
	if _, ok := vals["next_token"]; ok {
		t.Error("next_token は使わない（pagination_token のみ）")
	}
}

func TestClientError400(t *testing.T) {
	body, err := os.ReadFile("testdata/error_400_invalid_query.json")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(body)
	}))

	_, _, err = c.SearchRecent(context.Background(), testParams())
	if err == nil {
		t.Fatal("エラーになるべき")
	}
	ae, ok := err.(*APIError)
	if !ok {
		t.Fatalf("APIError であるべき: %T", err)
	}
	if ae.Kind != KindInvalidQuery {
		t.Errorf("Kind: got %v, want KindInvalidQuery", ae.Kind)
	}
	if ae.Status != 400 {
		t.Errorf("Status: %d", ae.Status)
	}
	if calls != 1 {
		t.Errorf("400 はリトライしない: calls=%d", calls)
	}
	if len(ae.Messages) == 0 {
		t.Error("errors[].message を拾うべき")
	}
	if !strings.Contains(ae.UserMessage(time.UTC), "課金なし") {
		t.Errorf("UserMessage: %q", ae.UserMessage(time.UTC))
	}
}

func TestClientError400QueryTooLong(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"parameters":{"query":["..."]},"message":"The ` + "`query`" + ` query parameter length must be <= 512"}],"title":"Invalid Request","detail":"One or more parameters to your request was invalid.","type":"https://api.twitter.com/2/problems/invalid-request"}`))
	}))

	_, _, err := c.SearchRecent(context.Background(), testParams())
	ae, ok := err.(*APIError)
	if !ok {
		t.Fatalf("APIError であるべき: %T", err)
	}
	// API が返した上限値をそのまま使う（自前の定数より API を信じる・DR-06）。
	if ae.QueryLimit != 512 {
		t.Errorf("QueryLimit: got %d, want 512", ae.QueryLimit)
	}
	if !strings.Contains(ae.UserMessage(time.UTC), "512") {
		t.Errorf("UserMessage に上限値を出すべき: %q", ae.UserMessage(time.UTC))
	}

	// full-archive の 1024 も同じパターンでパースできる。
	if got := parseQueryLengthLimit("The `query` query parameter length must be <= 1024"); got != 1024 {
		t.Errorf("1024 のパース: got %d", got)
	}
	if got := parseQueryLengthLimit("something else"); got != 0 {
		t.Errorf("該当なしは 0: got %d", got)
	}
}

func TestClientError401(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"title":"Unauthorized","type":"about:blank","status":401,"detail":"Unauthorized"}`))
	}))
	_, _, err := c.SearchRecent(context.Background(), testParams())
	ae, ok := err.(*APIError)
	if !ok {
		t.Fatalf("APIError であるべき: %T", err)
	}
	if ae.Kind != KindUnauthorized {
		t.Errorf("Kind: %v", ae.Kind)
	}
	if !strings.Contains(ae.UserMessage(time.UTC), "BEARER_TOKEN") {
		t.Errorf("UserMessage: %q", ae.UserMessage(time.UTC))
	}
}

func TestClientError403(t *testing.T) {
	body, err := os.ReadFile("testdata/error_403_not_enrolled.json")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write(body)
	}))
	_, _, err = c.SearchRecent(context.Background(), testParams())
	ae, ok := err.(*APIError)
	if !ok {
		t.Fatalf("APIError であるべき: %T", err)
	}
	if ae.Kind != KindForbiddenNotEnrolled {
		t.Errorf("Kind: got %v, want KindForbiddenNotEnrolled", ae.Kind)
	}
	if ae.Reason != "client-not-enrolled" {
		t.Errorf("Reason: %q", ae.Reason)
	}

	// reason が無い 403 は汎用 KindForbidden。
	c2, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"title":"Forbidden","detail":"nope"}`))
	}))
	_, _, err = c2.SearchRecent(context.Background(), testParams())
	ae2 := err.(*APIError)
	if ae2.Kind != KindForbidden {
		t.Errorf("Kind: got %v, want KindForbidden", ae2.Kind)
	}
}

func TestClientError429(t *testing.T) {
	body, err := os.ReadFile("testdata/error_429.json")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-rate-limit-limit", "450")
		w.Header().Set("x-rate-limit-remaining", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write(body)
	}))
	_, _, err = c.SearchRecent(context.Background(), testParams())
	ae, ok := err.(*APIError)
	if !ok {
		t.Fatalf("APIError であるべき: %T", err)
	}
	if ae.Kind != KindRateLimited {
		t.Errorf("Kind: %v", ae.Kind)
	}
	if ae.Code != 88 {
		t.Errorf("errors[].code 88 を拾うべき: got %d", ae.Code)
	}
}

func TestClientRetry429(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

	t.Run("reset が 3 秒後なら 1 回リトライして成功する", func(t *testing.T) {
		calls := 0
		var slept []time.Duration
		c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls == 1 {
				w.Header().Set("x-rate-limit-reset", itoa64(now.Add(3*time.Second).Unix()))
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"title":"Too Many Requests","errors":[{"code":88}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"meta":{"result_count":0}}`))
		}))
		c.now = func() time.Time { return now }
		c.sleep = func(d time.Duration) { slept = append(slept, d) }

		if _, _, err := c.SearchRecent(context.Background(), testParams()); err != nil {
			t.Fatalf("リトライして成功すべき: %v", err)
		}
		if calls != 2 {
			t.Errorf("2 回呼ばれるべき: calls=%d", calls)
		}
		if len(slept) != 1 {
			t.Fatalf("1 回だけ待つべき: %v", slept)
		}
		if slept[0] < 3*time.Second || slept[0] > 5*time.Second {
			t.Errorf("待ち時間が不正: %v", slept[0])
		}
	})

	t.Run("reset が 900 秒後ならリトライせず KindRateLimited", func(t *testing.T) {
		calls := 0
		c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("x-rate-limit-reset", itoa64(now.Add(900*time.Second).Unix()))
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"title":"Too Many Requests","errors":[{"code":88}]}`))
		}))
		c.now = func() time.Time { return now }
		c.sleep = func(d time.Duration) { t.Fatalf("待ってはいけない: %v", d) }

		_, _, err := c.SearchRecent(context.Background(), testParams())
		ae, ok := err.(*APIError)
		if !ok {
			t.Fatalf("APIError であるべき: %T", err)
		}
		if ae.Kind != KindRateLimited {
			t.Errorf("Kind: %v", ae.Kind)
		}
		if calls != 1 {
			t.Errorf("1 回だけ呼ばれるべき: calls=%d", calls)
		}
		msg := ae.UserMessage(time.UTC)
		if !strings.Contains(msg, "再試行") {
			t.Errorf("リセット時刻を案内すべき: %q", msg)
		}
	})
}

func itoa64(n int64) string {
	return strconv.FormatInt(n, 10)
}

func TestClientRetry5xx(t *testing.T) {
	calls := 0
	var slept []time.Duration
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"title":"Internal Server Error"}`))
	}))
	c.sleep = func(d time.Duration) { slept = append(slept, d) }

	_, _, err := c.SearchRecent(context.Background(), testParams())
	ae, ok := err.(*APIError)
	if !ok {
		t.Fatalf("APIError であるべき: %T", err)
	}
	if ae.Kind != KindServer {
		t.Errorf("Kind: %v", ae.Kind)
	}
	if calls != 3 {
		t.Errorf("3 回試すべき: calls=%d", calls)
	}
	if len(slept) != 2 || slept[0] != 1*time.Second || slept[1] != 3*time.Second {
		t.Errorf("バックオフは 1s → 3s: %v", slept)
	}
}

func TestClientBadJSON(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data": [ this is not json`))
	}))
	_, _, err := c.SearchRecent(context.Background(), testParams())
	ae, ok := err.(*APIError)
	if !ok {
		t.Fatalf("APIError であるべき: %T", err)
	}
	if ae.Kind != KindDecode {
		t.Errorf("Kind: got %v, want KindDecode", ae.Kind)
	}
	if ae.Raw == "" {
		t.Error("生ボディの先頭を残すべき")
	}
}

func TestClientEmptyQueryRejected(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("空クエリで API を呼んではいけない")
	}))
	p := testParams()
	p.Query = "   "
	if _, _, err := c.SearchRecent(context.Background(), p); err == nil {
		t.Fatal("エラーになるべき")
	}
}

func TestParseRateLimit(t *testing.T) {
	t.Run("ヘッダ欠落で nil", func(t *testing.T) {
		if got := parseRateLimit(http.Header{}); got != nil {
			t.Errorf("nil であるべき: %+v", got)
		}
		if got := parseRateLimit(nil); got != nil {
			t.Errorf("nil であるべき: %+v", got)
		}
	})
	t.Run("一部だけあっても読める", func(t *testing.T) {
		h := http.Header{}
		h.Set("x-rate-limit-remaining", "12")
		rl := parseRateLimit(h)
		if rl == nil || rl.Remaining != 12 {
			t.Fatalf("got %+v", rl)
		}
		if !rl.Reset.IsZero() {
			t.Errorf("Reset はゼロ値であるべき: %v", rl.Reset)
		}
		if rl.ResetDisplay(time.UTC) != "" {
			t.Errorf("ResetDisplay は空であるべき: %q", rl.ResetDisplay(time.UTC))
		}
	})
}

func TestPacer(t *testing.T) {
	t.Run("連続 3 回で min の 2 倍以上待つ", func(t *testing.T) {
		base := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
		cur := base
		var slept []time.Duration

		p := &pacer{min: 1100 * time.Millisecond}
		p.now = func() time.Time { return cur }
		p.sleep = func(d time.Duration) {
			slept = append(slept, d)
			cur = cur.Add(d) // スリープで時間が進む
		}

		for i := 0; i < 3; i++ {
			if err := p.Wait(context.Background()); err != nil {
				t.Fatalf("Wait: %v", err)
			}
		}
		total := time.Duration(0)
		for _, d := range slept {
			total += d
		}
		if len(slept) != 2 {
			t.Errorf("2 回待つべき（1 回目は待たない）: %v", slept)
		}
		if total < 2200*time.Millisecond {
			t.Errorf("合計待機は 2200ms 以上であるべき: %v", total)
		}
	})

	t.Run("十分に時間が経っていれば待たない", func(t *testing.T) {
		base := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
		cur := base
		p := &pacer{min: 1100 * time.Millisecond}
		p.now = func() time.Time { return cur }
		p.sleep = func(d time.Duration) { t.Fatalf("待つべきではない: %v", d) }

		_ = p.Wait(context.Background())
		cur = cur.Add(2 * time.Second)
		_ = p.Wait(context.Background())
	})

	t.Run("search/all と counts/all だけがペーシング対象", func(t *testing.T) {
		if !needsPacing(pathSearchAll) || !needsPacing(pathCountsAll) {
			t.Error("full-archive はペーシング対象")
		}
		if needsPacing(pathSearchRecent) || needsPacing(pathCountsRecent) || needsPacing(pathUsage) {
			t.Error("recent / usage はペーシング対象ではない")
		}
	})

	t.Run("エラー応答の後もペーサーが働く（400 でも枠を消費する）", func(t *testing.T) {
		calls := 0
		c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"title":"Invalid Request"}`))
		}))
		base := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
		cur := base
		var slept []time.Duration
		c.pacer.now = func() time.Time { return cur }
		c.pacer.sleep = func(d time.Duration) { slept = append(slept, d); cur = cur.Add(d) }

		p := testParams()
		p.Endpoint = EndpointAll
		_, _, _ = c.SearchAll(context.Background(), p) // 1 本目: 400
		_, _, _ = c.SearchAll(context.Background(), p) // 2 本目: ペーシングされるべき
		if len(slept) != 1 {
			t.Errorf("400 の後もペーシングすべき: %v (calls=%d)", slept, calls)
		}
	})
}

func TestClientFixtureMode(t *testing.T) {
	c := NewClient(Config{FixtureDir: "testdata"}, discardLogger())
	if !c.FixtureMode() {
		t.Fatal("フィクスチャモードであるべき")
	}
	if c.httpClient != nil {
		t.Error("フィクスチャモードでは HTTP クライアントを作らない")
	}

	resp, rl, err := c.SearchRecent(context.Background(), testParams())
	if err != nil {
		t.Fatalf("フィクスチャの読み込みに失敗: %v", err)
	}
	if resp.Meta.ResultCount == 0 {
		t.Error("フィクスチャから投稿が読めるべき")
	}
	if rl != nil {
		t.Error("フィクスチャモードでは RateLimit は nil")
	}

	cnt, _, err := c.CountsRecent(context.Background(), "猫", "day", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("counts フィクスチャ: %v", err)
	}
	if cnt.Meta.TotalPostCount != 10000 {
		t.Errorf("total_tweet_count: got %d", cnt.Meta.TotalPostCount)
	}

	u, err := c.Usage(context.Background())
	if err != nil {
		t.Fatalf("usage フィクスチャ: %v", err)
	}
	if u.Data.ProjectCap.Int() != 2000000 {
		t.Errorf("project_cap: got %d", u.Data.ProjectCap.Int())
	}

	// counts_all.json は無いので counts_recent.json にフォールバックする。
	cntAll, _, err := c.CountsAll(context.Background(), "猫", "day", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("counts_all フォールバック: %v", err)
	}
	if cntAll.Meta.TotalPostCount != 10000 {
		t.Errorf("フォールバック後の total: got %d", cntAll.Meta.TotalPostCount)
	}

	t.Run("SEEKX_FIXTURE_FILE で個別上書きできる", func(t *testing.T) {
		c2 := NewClient(Config{FixtureDir: "testdata", FixtureFile: "search_recent_empty.json"}, discardLogger())
		r, _, err := c2.SearchRecent(context.Background(), testParams())
		if err != nil {
			t.Fatalf("エラー: %v", err)
		}
		if r.Meta.ResultCount != 0 {
			t.Errorf("空フィクスチャを読むべき: got %d", r.Meta.ResultCount)
		}
	})
}

func TestClientUsageCache(t *testing.T) {
	calls := 0
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"data":{"project_id":"1","project_cap":"2000000","project_usage":42,"cap_reset_day":12}}`))
	}))
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if _, err := c.Usage(context.Background()); err != nil {
			t.Fatalf("エラー: %v", err)
		}
	}
	if calls != 1 {
		t.Errorf("60 秒キャッシュで 1 回だけ呼ぶべき: calls=%d", calls)
	}

	now = now.Add(61 * time.Second)
	if _, err := c.Usage(context.Background()); err != nil {
		t.Fatalf("エラー: %v", err)
	}
	if calls != 2 {
		t.Errorf("キャッシュ期限切れで再取得すべき: calls=%d", calls)
	}
}

func TestClientPathSelection(t *testing.T) {
	var paths []string
	var mu sync.Mutex
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"meta":{"result_count":0}}`))
	}))

	p := testParams()
	if _, _, err := c.SearchRecent(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	p.Endpoint = EndpointAll
	if _, _, err := c.SearchAll(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	want := []string{"/tweets/search/recent", "/tweets/search/all"}
	for i, w := range want {
		if paths[i] != w {
			t.Errorf("paths[%d] = %q, want %q", i, paths[i], w)
		}
	}
	if SearchPath(EndpointRecent) != pathSearchRecent || SearchPath(EndpointAll) != pathSearchAll {
		t.Error("SearchPath の対応が不正")
	}
	if CountsPath(EndpointRecent) != pathCountsRecent || CountsPath(EndpointAll) != pathCountsAll {
		t.Error("CountsPath の対応が不正")
	}
}

// TestClientNoTokenInLogs はログにトークンや Authorization が出ないことを確認する（P-TOKEN-LEAK）。
func TestClientNoTokenInLogs(t *testing.T) {
	var buf strings.Builder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-rate-limit-remaining", "10")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"title":"Invalid Request","detail":"bad"}`))
	}))
	defer srv.Close()

	const secret = "SUPER-SECRET-TOKEN-VALUE-0123456789"
	c := NewClient(Config{BearerToken: secret, APIBaseURL: srv.URL},
		slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	c.sleep = func(time.Duration) {}

	_, _, err := c.SearchRecent(context.Background(), testParams())
	if err == nil {
		t.Fatal("エラーになるべき")
	}
	logged := buf.String()
	if strings.Contains(logged, secret) {
		t.Error("ログにトークンが出ている")
	}
	if strings.Contains(strings.ToLower(logged), "authorization") {
		t.Error("ログに Authorization が出ている")
	}
	if strings.Contains(err.Error(), secret) {
		t.Error("エラーメッセージにトークンが出ている")
	}
	// エラー詳細にリクエスト URL 全文を含めない。
	if strings.Contains(err.Error(), srv.URL) {
		t.Error("エラーメッセージにリクエスト URL が出ている")
	}
}

// TestClientNoRealAPI はテストが api.x.com を参照しないことを保証する。
func TestClientNoRealAPI(t *testing.T) {
	c, srv := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"meta":{"result_count":0}}`))
	}))
	if strings.Contains(c.baseURL, "api.x.com") {
		t.Fatalf("テストは実 API を参照してはいけない: %s", c.baseURL)
	}
	if !strings.HasPrefix(c.baseURL, srv.URL) {
		t.Errorf("baseURL が httptest を指していない: %s", c.baseURL)
	}
}
