package xapi

// client.go は X API v2 のクライアント。
//
// 制約（file_layout）:
//   - 他の internal パッケージを import しない（このパッケージは依存グラフの葉である）
//   - クエリ構文のバリデーションを行わない（internal/query が済ませている前提。空クエリだけは防御的に弾く）
//   - HTML やテンプレートに触らない
//   - Authorization ヘッダやトークンをログ・エラーに出さない
//   - includes の結合を行わない（internal/view の責務）
//   - 課金の可否判断を行わない（internal/cost の責務）
//   - goroutine で並行リクエストを投げない（直列固定）

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrorKind はエラーの分類。
type ErrorKind int

const (
	KindInvalidQuery ErrorKind = iota
	KindUnauthorized
	KindForbiddenNotEnrolled
	KindForbidden
	KindNotFound
	KindRateLimited
	KindServer
	KindNetwork
	KindDecode
	KindUsageCap
)

// Title は ErrorKind の日本語見出し。
func (k ErrorKind) Title() string {
	switch k {
	case KindInvalidQuery:
		return "検索条件が受け付けられませんでした"
	case KindUnauthorized:
		return "認証に失敗しました"
	case KindForbiddenNotEnrolled:
		return "この App は Project に紐付いていません"
	case KindForbidden:
		return "アクセスが拒否されました"
	case KindNotFound:
		return "対象が見つかりませんでした"
	case KindRateLimited:
		return "レートリミットに達しました"
	case KindServer:
		return "X API 側で一時的なエラーが発生しました"
	case KindNetwork:
		return "X API に接続できませんでした"
	case KindDecode:
		return "レスポンスの解析に失敗しました"
	case KindUsageCap:
		return "月次上限に近づいています"
	default:
		return "エラーが発生しました"
	}
}

// APIError は分類済みの API エラー。トークンやリクエスト URL は含めない。
type APIError struct {
	Kind       ErrorKind
	Status     int
	Title      string
	Detail     string
	Reason     string
	Code       int
	RetryAfter time.Duration
	Raw        string
	Messages   []string
	QueryLimit int // 400 の本文が上限値を述べていた場合の数値（DR-06）
	RateLimit  *RateLimit
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Detail != "" {
		return fmt.Sprintf("%s (HTTP %d): %s", e.Kind.Title(), e.Status, e.Detail)
	}
	return fmt.Sprintf("%s (HTTP %d)", e.Kind.Title(), e.Status)
}

// UserMessage は UI に出す日本語メッセージ。
func (e *APIError) UserMessage(loc *time.Location) string {
	switch e.Kind {
	case KindInvalidQuery:
		if e.QueryLimit > 0 {
			return fmt.Sprintf("検索クエリが長すぎます（X API の上限は %d 文字）。条件を減らしてください（HTTP 400・課金なし）。", e.QueryLimit)
		}
		return "検索条件が X API に受け付けられませんでした（HTTP 400・課金なし）。生成されたクエリを確認してください。"
	case KindUnauthorized:
		return "認証に失敗しました（HTTP 401）。.env の BEARER_TOKEN が正しいか確認してください。"
	case KindForbiddenNotEnrolled:
		return "この App は Project に紐付いていない、またはクレジットがありません（HTTP 403 client-not-enrolled）。X 開発者ポータルで App を Project に紐付け、pay-per-use のクレジットを確認してください。"
	case KindForbidden:
		return "この操作は許可されていません（HTTP 403）。アクセスレベルを確認してください。"
	case KindNotFound:
		return "対象が見つかりませんでした（HTTP 404）。"
	case KindRateLimited:
		if e.RateLimit != nil && !e.RateLimit.Reset.IsZero() {
			return fmt.Sprintf("レートリミットに達しました（HTTP 429・課金なし）。%s 以降に再試行してください。", e.RateLimit.ResetDisplay(loc))
		}
		return "レートリミットに達しました（HTTP 429・課金なし）。しばらく待って再試行してください。"
	case KindServer:
		return fmt.Sprintf("X API 側で一時的なエラーが発生しました（HTTP %d）。しばらく待って再試行してください。", e.Status)
	case KindNetwork:
		return "X API に接続できませんでした。コンテナのネットワークを確認してください。"
	case KindDecode:
		return "レスポンスの解析に失敗しました。API のレスポンス形式が変わった可能性があります（詳細を確認してください）。"
	case KindUsageCap:
		return "月次上限に近づいているため実行を停止しました。"
	default:
		return "不明なエラーが発生しました。"
	}
}

// ---------------------------------------------------------------------------
// pacer（full-archive の 1 req/sec ハード制約を守る・R-17 / P-ALL-1RPS）
// ---------------------------------------------------------------------------

type pacer struct {
	mu    sync.Mutex
	last  time.Time
	min   time.Duration
	now   func() time.Time
	sleep func(time.Duration)
}

func newPacer(min time.Duration) *pacer {
	return &pacer{min: min, now: time.Now, sleep: time.Sleep}
}

// Wait は前回リクエストから min 未満なら差分だけ待つ。
// 失敗した（400 / 429）リクエストの後も枠が消費されるため、成否に関わらず間隔を空ける。
func (p *pacer) Wait(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	nowFn, sleepFn := p.now, p.sleep
	if nowFn == nil {
		nowFn = time.Now
	}
	if sleepFn == nil {
		sleepFn = time.Sleep
	}

	now := nowFn()
	if !p.last.IsZero() {
		if wait := p.min - now.Sub(p.last); wait > 0 {
			if ctx != nil {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
			}
			sleepFn(wait)
			now = nowFn()
		}
	}
	p.last = now
	return nil
}

const (
	fullArchiveMinInterval = 1100 * time.Millisecond // 1000ms + マージン 100ms
	httpTimeout            = 20 * time.Second
	handlerMaxWait         = 5 * time.Second // 429 でハンドラ内に待たせる上限（D-32）
	usageCacheTTL          = 60 * time.Second
	userAgent              = "seekx/0.1 (local; +https://github.com/shutx-net/seekx)"
)

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

// Options は Client の生成に必要な設定。internal/config を import しないために
// Config そのものではなく必要な項目だけを受け取る（xapi を依存グラフの葉に保つ）。
type Options struct {
	BearerToken string
	APIBaseURL  string
	FixtureDir  string // 非空ならフィクスチャモード（API を叩かない）
	FixtureFile string
	RawDumpDir  string

	// Sleep はリトライ・ペーシングの待機に使う。nil なら time.Sleep。
	// テストで実時間を待たないための注入口（本番では常に nil）。
	Sleep func(time.Duration)
}

type Client struct {
	httpClient  *http.Client
	token       string
	baseURL     string
	pacer       *pacer
	fixtureDir  string
	fixtureFile string
	rawDumpDir  string
	log         *slog.Logger

	sleep func(time.Duration)
	now   func() time.Time

	usageMu   sync.Mutex
	usageAt   time.Time
	usageResp *UsageResponse
}

func NewClient(opts Options, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	c := &Client{
		token:       opts.BearerToken,
		baseURL:     strings.TrimSuffix(opts.APIBaseURL, "/"),
		pacer:       newPacer(fullArchiveMinInterval),
		fixtureDir:  opts.FixtureDir,
		fixtureFile: opts.FixtureFile,
		rawDumpDir:  opts.RawDumpDir,
		log:         log,
		sleep:       time.Sleep,
		now:         time.Now,
	}
	if opts.Sleep != nil {
		c.sleep = opts.Sleep
		c.pacer.sleep = opts.Sleep
	}
	// フィクスチャモードでは HTTP クライアントを作らない（課金経路が存在しないことを型で担保する・D-22）。
	if opts.FixtureDir == "" {
		c.httpClient = &http.Client{Timeout: httpTimeout}
	}
	return c
}

func (c *Client) FixtureMode() bool { return c.fixtureDir != "" }

// ---------------------------------------------------------------------------
// エンドポイント
// ---------------------------------------------------------------------------

const (
	pathSearchRecent = "/tweets/search/recent"
	pathSearchAll    = "/tweets/search/all"
	pathCountsRecent = "/tweets/counts/recent"
	pathCountsAll    = "/tweets/counts/all"
	pathUsage        = "/usage/tweets"
)

// SearchPath は endpoint に対応する検索パスを返す。
func SearchPath(e Endpoint) string {
	if e == EndpointAll {
		return pathSearchAll
	}
	return pathSearchRecent
}

// CountsPath は endpoint に対応する counts パスを返す。
func CountsPath(e Endpoint) string {
	if e == EndpointAll {
		return pathCountsAll
	}
	return pathCountsRecent
}

func (c *Client) SearchRecent(ctx context.Context, p SearchParams) (*SearchResponse, *RateLimit, error) {
	return c.search(ctx, pathSearchRecent, p)
}

func (c *Client) SearchAll(ctx context.Context, p SearchParams) (*SearchResponse, *RateLimit, error) {
	return c.search(ctx, pathSearchAll, p)
}

func (c *Client) search(ctx context.Context, path string, p SearchParams) (*SearchResponse, *RateLimit, error) {
	if strings.TrimSpace(p.Query) == "" {
		return nil, nil, &APIError{Kind: KindInvalidQuery, Detail: "検索クエリが空です。"}
	}
	q := searchValues(p)
	var out SearchResponse
	rl, err := c.do(ctx, path, q, &out)
	if err != nil {
		return nil, rl, err
	}
	return &out, rl, nil
}

func searchValues(p SearchParams) url.Values {
	v := url.Values{}
	v.Set("query", p.Query)
	if p.MaxResults > 0 {
		v.Set("max_results", strconv.Itoa(p.MaxResults))
	}
	if len(p.PostFields) > 0 {
		v.Set("post.fields", strings.Join(p.PostFields, ","))
	}
	if len(p.Expansions) > 0 {
		v.Set("expansions", strings.Join(p.Expansions, ","))
	}
	if len(p.UserFields) > 0 {
		v.Set("user.fields", strings.Join(p.UserFields, ","))
	}
	if len(p.MediaFields) > 0 {
		v.Set("media.fields", strings.Join(p.MediaFields, ","))
	}
	if !p.StartTime.IsZero() {
		v.Set("start_time", p.StartTime.UTC().Format(time.RFC3339))
	}
	if !p.EndTime.IsZero() {
		v.Set("end_time", p.EndTime.UTC().Format(time.RFC3339))
	}
	if p.SinceID != "" {
		v.Set("since_id", p.SinceID)
	}
	if p.UntilID != "" {
		v.Set("until_id", p.UntilID)
	}
	if p.PaginationToken != "" {
		v.Set("pagination_token", p.PaginationToken)
	}
	return v
}

func (c *Client) CountsRecent(ctx context.Context, query, granularity string, start, end time.Time) (*CountsResponse, *RateLimit, error) {
	return c.counts(ctx, pathCountsRecent, query, granularity, start, end)
}

func (c *Client) CountsAll(ctx context.Context, query, granularity string, start, end time.Time) (*CountsResponse, *RateLimit, error) {
	return c.counts(ctx, pathCountsAll, query, granularity, start, end)
}

// Counts は endpoint に応じて counts を呼び分ける。
func (c *Client) Counts(ctx context.Context, e Endpoint, query, granularity string, start, end time.Time) (*CountsResponse, *RateLimit, error) {
	if e == EndpointAll {
		return c.CountsAll(ctx, query, granularity, start, end)
	}
	return c.CountsRecent(ctx, query, granularity, start, end)
}

func (c *Client) counts(ctx context.Context, path, query, granularity string, start, end time.Time) (*CountsResponse, *RateLimit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil, &APIError{Kind: KindInvalidQuery, Detail: "検索クエリが空です。"}
	}
	v := url.Values{}
	v.Set("query", query)
	if granularity != "" {
		v.Set("granularity", granularity)
	}
	if !start.IsZero() {
		v.Set("start_time", start.UTC().Format(time.RFC3339))
	}
	if !end.IsZero() {
		v.Set("end_time", end.UTC().Format(time.RFC3339))
	}
	var out CountsResponse
	rl, err := c.do(ctx, path, v, &out)
	if err != nil {
		return nil, rl, err
	}
	return &out, rl, nil
}

// Usage は月次 cap の消費状況を返す。60 秒メモリキャッシュする（D-29 / R-06）。
func (c *Client) Usage(ctx context.Context) (*UsageResponse, error) {
	c.usageMu.Lock()
	defer c.usageMu.Unlock()

	nowFn := c.now
	if nowFn == nil {
		nowFn = time.Now
	}
	if c.usageResp != nil && nowFn().Sub(c.usageAt) < usageCacheTTL {
		return c.usageResp, nil
	}
	var out UsageResponse
	if _, err := c.do(ctx, pathUsage, url.Values{}, &out); err != nil {
		return nil, err
	}
	c.usageResp = &out
	c.usageAt = nowFn()
	return &out, nil
}

// ---------------------------------------------------------------------------
// do（HTTP 実行・リトライ・エラー分類）
// ---------------------------------------------------------------------------

func (c *Client) do(ctx context.Context, path string, q url.Values, out any) (*RateLimit, error) {
	if c.fixtureDir != "" {
		return c.readFixture(path, out)
	}
	if c.httpClient == nil {
		return nil, &APIError{Kind: KindNetwork, Detail: "HTTP クライアントが初期化されていません。"}
	}

	// url.Values.Encode() は空白を + にする。%20 に統一する（P-URLENC-PLUS）。
	enc := strings.ReplaceAll(q.Encode(), "+", "%20")
	full := c.baseURL + path + "?" + enc

	sleepFn := c.sleep
	if sleepFn == nil {
		sleepFn = time.Sleep
	}

	var lastErr error
	var lastRL *RateLimit

	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// full-archive は 1 req/sec のハードスロットル。成否に関わらず間隔を空ける（R-17）。
		if needsPacing(path) {
			if err := c.pacer.Wait(ctx); err != nil {
				return lastRL, &APIError{Kind: KindNetwork, Detail: "リクエストが中断されました。"}
			}
		}

		status, body, header, err := c.roundTrip(ctx, full)
		if err != nil {
			lastErr = &APIError{Kind: KindNetwork, Detail: networkDetail(err)}
			c.log.Warn("api call failed", "endpoint", path, "attempt", attempt, "err", networkDetail(err))
			if attempt < 2 { // ネットワークエラーは 1 回だけ再試行
				sleepFn(1 * time.Second)
				continue
			}
			return lastRL, lastErr
		}

		rl := parseRateLimit(header)
		lastRL = rl
		c.dumpRaw(path, status, body)

		if status >= 200 && status < 300 {
			if err := json.Unmarshal(body, out); err != nil {
				raw := truncate(string(body), 500)
				c.log.Error("api response decode failed", "endpoint", path, "status", status, "raw", raw)
				return rl, &APIError{Kind: KindDecode, Status: status, Detail: err.Error(), Raw: raw}
			}
			c.logCall(path, q, status, out, rl)
			return rl, nil
		}

		apiErr := classifyError(status, body)
		apiErr.RateLimit = rl
		c.log.Warn("api call error",
			"endpoint", path, "status", status, "kind", apiErr.Kind.Title(),
			"detail", truncate(apiErr.Detail, 200), "attempt", attempt,
			"rl_remaining", remainingOf(rl))

		switch apiErr.Kind {
		case KindRateLimited:
			// 429 は無課金。リセットが近ければ待って 1 回だけ再試行する（D-32）。
			wait := retryAfterFor(apiErr, rl, c.nowTime(), attempt)
			if wait > 0 && wait <= handlerMaxWait && attempt < maxAttempts {
				sleepFn(wait)
				lastErr = apiErr
				continue
			}
			return rl, apiErr
		case KindServer:
			if attempt < maxAttempts {
				delay := 1 * time.Second
				if attempt == 2 {
					delay = 3 * time.Second
				}
				sleepFn(delay)
				lastErr = apiErr
				continue
			}
			return rl, apiErr
		default:
			// 400 / 401 / 403 / 404 / 422 はリトライしない。
			return rl, apiErr
		}
	}
	if lastErr == nil {
		lastErr = &APIError{Kind: KindServer, Detail: "リクエストが完了しませんでした。"}
	}
	return lastRL, lastErr
}

func (c *Client) nowTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *Client) roundTrip(ctx context.Context, full string) (int, []byte, http.Header, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, full, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	// Authorization ヘッダはここでのみ設定する。ログ・エラーには一切出さない。
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, resp.Header, err
	}
	return resp.StatusCode, body, resp.Header, nil
}

// dumpRaw は生レスポンス本文を SEEKX_RAW_DUMP_DIR に保存する。
// 実 API を 1 回叩いたら以降はこの保存物（フィクスチャ）で開発・デバッグするための仕組み（D-22 / D-34）。
// 保存するのはレスポンス本文のみ。リクエストヘッダ（Authorization）は絶対に含めない。
func (c *Client) dumpRaw(path string, status int, body []byte) {
	if c.rawDumpDir == "" || len(body) == 0 {
		return
	}
	if err := os.MkdirAll(c.rawDumpDir, 0o755); err != nil {
		c.log.Warn("生レスポンスの保存先を作成できません", "dir", c.rawDumpDir, "err", err)
		return
	}
	name := fmt.Sprintf("%s-%d-%s.json",
		strings.TrimPrefix(strings.ReplaceAll(path, "/", "_"), "_"),
		status,
		time.Now().UTC().Format("20060102T150405Z"))
	file := filepath.Join(c.rawDumpDir, name)
	if err := os.WriteFile(file, body, 0o600); err != nil {
		c.log.Warn("生レスポンスを保存できません", "file", file, "err", err)
		return
	}
	c.log.Info("生レスポンスを保存しました", "file", file, "bytes", len(body))
}

// needsPacing は full-archive 系のパスかどうかを判定する。
func needsPacing(path string) bool {
	return path == pathSearchAll || path == pathCountsAll
}

// networkDetail はネットワークエラーの説明を返す。URL は含めない（トークンは載らないが方針として出さない）。
func networkDetail(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "タイムアウトしました。"
	}
	if errors.Is(err, context.Canceled) {
		return "リクエストが中断されました。"
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err.Error()
	}
	return err.Error()
}

// retryAfterFor は 429 のときに待つべき時間を返す。
func retryAfterFor(e *APIError, rl *RateLimit, now time.Time, attempt int) time.Duration {
	if e.RetryAfter > 0 {
		return e.RetryAfter
	}
	if rl != nil && !rl.Reset.IsZero() {
		return rl.Reset.Sub(now) + time.Second
	}
	// ヘッダが無い場合は 1s → 2s → 4s の指数バックオフ。
	return time.Duration(1<<(attempt-1)) * time.Second
}

func remainingOf(rl *RateLimit) int {
	if rl == nil {
		return -1
	}
	return rl.Remaining
}

func (c *Client) logCall(path string, q url.Values, status int, out any, rl *RateLimit) {
	resultCount := -1
	est := 0.0
	switch v := out.(type) {
	case *SearchResponse:
		resultCount = v.Meta.ResultCount
		est = float64(v.Meta.ResultCount) * CostPerPostRead
	case *CountsResponse:
		resultCount = len(v.Data)
		if path == pathCountsAll {
			est = CostPerCountsAll
		} else {
			est = CostPerCountsRecent
		}
	}
	c.log.Info("api call",
		"endpoint", path,
		"query", q.Get("query"),
		"max_results", q.Get("max_results"),
		"status", status,
		"result_count", resultCount,
		"rl_remaining", remainingOf(rl),
		"est_usd", est,
	)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// parseRateLimit は x-rate-limit-* ヘッダを読む。欠落時は nil を返す。
func parseRateLimit(h http.Header) *RateLimit {
	if h == nil {
		return nil
	}
	limitStr := h.Get("x-rate-limit-limit")
	remStr := h.Get("x-rate-limit-remaining")
	resetStr := h.Get("x-rate-limit-reset")
	if limitStr == "" && remStr == "" && resetStr == "" {
		return nil
	}
	rl := &RateLimit{}
	if n, err := strconv.Atoi(limitStr); err == nil {
		rl.Limit = n
	}
	if n, err := strconv.Atoi(remStr); err == nil {
		rl.Remaining = n
	}
	if n, err := strconv.ParseInt(resetStr, 10, 64); err == nil {
		rl.Reset = time.Unix(n, 0)
	}
	return rl
}

// classifyError は HTTP ステータスと本文からエラーを分類する。
func classifyError(status int, body []byte) *APIError {
	e := &APIError{Status: status, Raw: truncate(string(body), 500)}

	var prob APIProblem
	if err := json.Unmarshal(body, &prob); err == nil {
		e.Title = prob.Title
		e.Detail = prob.Detail
		e.Reason = prob.Reason
		for _, pe := range prob.Errors {
			if m := pe.Describe(); m != "" {
				e.Messages = append(e.Messages, m)
			}
			if pe.Code != 0 {
				e.Code = pe.Code
			}
		}
	}
	if e.Detail == "" && len(e.Messages) > 0 {
		e.Detail = e.Messages[0]
	}

	switch {
	case status == http.StatusBadRequest:
		e.Kind = KindInvalidQuery
		// API 自身が上限値を述べる。自前の定数より API の数値を信じる（DR-06 / DR-13）。
		e.QueryLimit = parseQueryLengthLimit(string(body))
	case status == http.StatusUnauthorized:
		e.Kind = KindUnauthorized
	case status == http.StatusForbidden:
		if prob.Reason == "client-not-enrolled" {
			e.Kind = KindForbiddenNotEnrolled
		} else {
			e.Kind = KindForbidden
		}
	case status == http.StatusNotFound:
		e.Kind = KindNotFound
	case status == http.StatusTooManyRequests:
		e.Kind = KindRateLimited
	case status >= 500:
		e.Kind = KindServer
	case status == http.StatusUnprocessableEntity:
		e.Kind = KindInvalidQuery
	default:
		e.Kind = KindServer
	}
	if e.Detail == "" {
		e.Detail = e.Title
	}
	return e
}

// parseQueryLengthLimit は「query parameter length must be <= N」から N を取り出す。
// 見つからなければ 0。
func parseQueryLengthLimit(body string) int {
	const marker = "length must be <="
	i := strings.Index(body, marker)
	if i < 0 {
		return 0
	}
	rest := strings.TrimSpace(body[i+len(marker):])
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0
	}
	return n
}

// ---------------------------------------------------------------------------
// フィクスチャモード（D-22）
// ---------------------------------------------------------------------------

func (c *Client) readFixture(path string, out any) (*RateLimit, error) {
	name := fixtureNameFor(path)
	if c.fixtureFile != "" && (path == pathSearchRecent || path == pathSearchAll) {
		name = c.fixtureFile
	}
	file := filepath.Join(c.fixtureDir, name)
	b, err := os.ReadFile(file)
	if err != nil && os.IsNotExist(err) && path == pathCountsAll {
		file = filepath.Join(c.fixtureDir, "counts_recent.json")
		b, err = os.ReadFile(file)
	}
	if err != nil {
		return nil, &APIError{
			Kind:   KindDecode,
			Detail: fmt.Sprintf("フィクスチャ %s を読み込めません: %v", file, err),
		}
	}
	if err := json.Unmarshal(b, out); err != nil {
		return nil, &APIError{
			Kind:   KindDecode,
			Detail: fmt.Sprintf("フィクスチャ %s の解析に失敗しました: %v", file, err),
			Raw:    truncate(string(b), 500),
		}
	}
	c.log.Info("fixture read", "endpoint", path, "file", file)
	return nil, nil
}

func fixtureNameFor(path string) string {
	switch path {
	case pathSearchRecent:
		return "search_recent.json"
	case pathSearchAll:
		return "search_all.json"
	case pathCountsRecent:
		return "counts_recent.json"
	case pathCountsAll:
		return "counts_all.json"
	case pathUsage:
		return "usage.json"
	default:
		return strings.TrimPrefix(strings.ReplaceAll(path, "/", "_"), "_") + ".json"
	}
}
