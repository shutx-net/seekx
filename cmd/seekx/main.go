package main

// main.go はエントリポイント。Config 読み込み、テンプレート・静的アセットの embed、
// ルーティング、HTTP ハンドラ、サーバ起動を担う。
//
// 制約（file_layout）:
//   - X API へのリクエストを直接組み立てない（internal/xapi の責務）
//   - 検索クエリを組み立てない・検証しない（internal/query の責務）
//   - API レスポンスの struct を定義しない（internal/xapi の責務）
//   - 単価や見積り計算式を持たない（internal/cost の責務）
//   - ポート番号のリテラルを持たない（config.DefaultPort のみ）
//   - テンプレートに *http.Request や Config をそのまま渡さない（ViewModel だけを渡す）

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shutx-net/seekx/internal/config"
	"github.com/shutx-net/seekx/internal/cost"
	"github.com/shutx-net/seekx/internal/query"
	"github.com/shutx-net/seekx/internal/view"
	"github.com/shutx-net/seekx/internal/xapi"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

var templateFuncs = template.FuncMap{
	"usd":  func(f float64) string { return fmt.Sprintf("$%.3f", f) },
	"usd2": func(f float64) string { return fmt.Sprintf("$%.2f", f) },
	"pct":  func(f float64) string { return fmt.Sprintf("%.1f%%", f) },
	"num":  humanInt,
}

// humanInt は 3 桁区切りの整数を返す。
func humanInt(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

var tmpl = template.Must(template.New("").Funcs(templateFuncs).ParseFS(templatesFS, "templates/*.html"))

// App は HTTP ハンドラが共有する依存。
type App struct {
	cfg     config.Config
	client  *xapi.Client
	tmpl    *template.Template
	log     *slog.Logger
	session *cost.SessionSpend
	tokens  *tokenStore
	loc     *time.Location
	now     func() time.Time
}

func main() {
	cfg, err := config.LoadConfig()
	// トークン未設定は起動を止めない。設定画面から入力できるため（D-09 の例外）。
	// それ以外の設定エラー（数値の書式など）は従来どおり致命的に扱う。
	noToken := errors.Is(err, config.ErrNoBearerToken)
	if err != nil && !noToken {
		fmt.Fprintln(os.Stderr, "起動できません: "+err.Error())
		os.Exit(1)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: config.SlogLevel(cfg.LogLevel)}))
	log.Info("seekx を起動します",
		"bearer_token", config.MaskToken(cfg.BearerToken),
		"fixture_mode", cfg.FixtureMode(),
		"fixture_dir", cfg.FixtureDir,
		"api_base", cfg.APIBaseURL,
		"tz", cfg.DisplayTZ,
		"session_budget_usd", cfg.SessionBudgetUSD,
		"max_results_cap", cfg.MaxResultsCap,
		"allowed_hosts", strings.Join(cfg.AllowedHosts, ","),
	)
	if cfg.FixtureMode() {
		log.Info("フィクスチャモードです。X API は呼び出されません（課金 0）", "dir", cfg.FixtureDir)
	}
	if noToken {
		log.Warn("BEARER_TOKEN が未設定のまま起動します。検索は設定画面でトークンを入力してから実行できます",
			"settings_url", "http://localhost:"+cfg.Port+settingsPath)
	}

	app := &App{
		cfg:     cfg,
		client:  xapi.NewClient(clientOptions(cfg), log),
		tmpl:    tmpl,
		log:     log,
		session: &cost.SessionSpend{},
		tokens:  newTokenStore(cfg.BearerToken),
		loc:     cfg.Location(),
		now:     time.Now,
	}

	// 0.0.0.0 にバインドする。127.0.0.1 はコンテナ外（ホスト）から到達できない（D-08 / P-BIND-0000）。
	addr := "0.0.0.0:" + cfg.Port
	log.Info("listening", "addr", addr,
		"note", "devcontainer.json の forwardPorts と一致しているか確認してください",
		"url", "http://localhost:"+cfg.Port+"/")

	srv := &http.Server{
		Addr:              addr,
		Handler:           newRouter(app),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("サーバの起動に失敗しました", "err", err)
		os.Exit(1)
	}
}

// clientOptions は Config から xapi.Options を作る（xapi は internal/config を import しない）。
func clientOptions(cfg config.Config) xapi.Options {
	return xapi.Options{
		BearerToken: cfg.BearerToken,
		APIBaseURL:  cfg.APIBaseURL,
		FixtureDir:  cfg.FixtureDir,
		FixtureFile: cfg.FixtureFile,
		RawDumpDir:  cfg.RawDumpDir,
	}
}

// 設定画面のパス。テンプレート・ログ・リダイレクト先で共有する単一情報源。
const (
	settingsPath      = "/settings"
	settingsTokenPath = "/settings/token"
	settingsClearPath = "/settings/token/clear"
)

func newRouter(app *App) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", app.handleIndex)
	mux.HandleFunc("GET /search", app.handleSearch)
	mux.HandleFunc("GET /healthz", app.handleHealthz)
	// トークンの設定は POST のみ。GET にすると値が URL・履歴・ログに残る（D-23 の例外）。
	mux.HandleFunc("GET "+settingsPath, app.handleSettings)
	mux.HandleFunc("POST "+settingsTokenPath, app.handleSetToken)
	mux.HandleFunc("POST "+settingsClearPath, app.handleClearToken)
	mux.Handle("GET /static/", app.staticHandler())
	// セキュリティヘッダを外側に置く。guard が拒否した 403 応答にもヘッダを付けるため。
	return withSecurityHeaders(withRequestGuard(mux, app.cfg.AllowedHosts, app.log))
}

// contentSecurityPolicy は script について厳格な CSP。
//
// 一次防御は html/template の文脈依存自動エスケープで、これは効いている
// （template.HTML / template.JS / template.URL による迂回はコードベースに無い）。
// CSP は多層防御で、守る対象は「将来の変更が API 由来の値を template.HTML に通して
// エスケープを失う」経路。そのとき実行そのものを止める。
//
// style-src に 'unsafe-inline' が必要な理由: estimate.html のバー幅と _media.html の
// aspect-ratio が項目ごとに変わる数値で、CSS クラスに追い出せない。nonce は <style>
// 要素にしか効かず style 属性には適用されないため、ここは厳格にできない。
// スタイル注入はスクリプト注入より影響が小さいので、この妥協は受け入れる。
//
// ホストは testdata/ の実レスポンスから確認した（画像 pbs.twimg.com / 動画 video.twimg.com）。
// connect-src 'none' は JS が fetch / XHR / WebSocket を使わないことと一致する。
const contentSecurityPolicy = "default-src 'none'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' https://pbs.twimg.com; " +
	"media-src 'self' https://video.twimg.com; " +
	"form-action 'self'; " +
	"base-uri 'none'; " +
	"frame-ancestors 'none'; " +
	"connect-src 'none'"

// withSecurityHeaders は全レスポンス（/static/ を含む）にセキュリティヘッダを付ける。
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		// nosniff を付けると、.js が正しい MIME タイプで配信されていない場合に
		// ブラウザがスクリプトの実行を拒否する（P-NOSNIFF-JS）。
		h.Set("X-Content-Type-Options", "nosniff")
		// 既定の strict-origin-when-cross-origin でもクエリ文字列は送られないが、
		// 既定に依存しないよう明示する。
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (a *App) staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embed 済みなので起こらない
	}
	fileServer := http.StripPrefix("/static/", http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 開発中の CSS 変更が即反映されるように no-cache。
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})
}

func (a *App) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

// ---------------------------------------------------------------------------
// ページのビューモデル（テンプレートに渡す唯一の型。Config や *http.Request は渡さない）
// ---------------------------------------------------------------------------

type Page struct {
	Title        string
	SessionUSD   float64
	SessionCalls int
	Cap          cost.CapStatus
	FixtureMode  bool
	TZ           string
	CountsCost   float64

	// TokenConfigured は Bearer Token が設定済みかどうか。値そのものは渡さない。
	TokenConfigured bool
	SettingsURL     string
}

// SettingsPage は設定画面。トークンの値は渡さない（マスク表示のみ）。
type SettingsPage struct {
	Page
	TokenMasked  string
	TokenSource  string
	HasEnvToken  bool
	HasUIToken   bool
	Saved        bool
	Cleared      bool
	Errors       []string
	TokenPath    string
	ClearPath    string
	SessionReset bool
}

type MaxOption struct {
	Value int
	Label string
}

type LangOption struct {
	Value string
	Label string
}

type FormPage struct {
	Page
	Form           query.SearchForm
	Errors         []string
	Warnings       []string
	MaxOptions     []MaxOption
	LangOptions    []LangOption
	MaxCost        float64
	StandaloneOps  []query.OperatorSpec
	ConjunctionOps []query.OperatorSpec
}

type BucketView struct {
	Label string
	Count int
	Pct   float64
}

type EstimatePage struct {
	Page
	Form          query.SearchForm
	Query         string
	QueryRunes    int
	QueryBytes    int
	QueryLimit    int
	EndpointName  string
	StartDisplay  string
	EndDisplay    string
	RangeUTC      string
	Granularity   string
	Total         int
	MaxResults    int
	Est           cost.Estimate
	Buckets       []BucketView
	Warnings      []string
	Allowed       bool
	GateReason    string
	ExecuteURL    string
	ReestimateURL string
	FormURL       string
}

type ResultsPage struct {
	Page
	Result         view.ViewResult
	NextURL        string
	PrevURL        string
	NextCost       float64
	PagingBlocked  bool
	PagingReason   string
	FormURL        string
	EstimateURL    string
	EmptyHints     []string
	RateLimitReset string
}

type ErrorPage struct {
	Page
	Heading          string
	Message          string
	Steps            []string
	Query            string
	Status           int
	APITitle         string
	Detail           string
	Reason           string
	Messages         []string
	ValidationErrors []string
	FormURL          string
}

func (a *App) page(title string, cap cost.CapStatus, ep xapi.Endpoint) Page {
	return Page{
		Title:           title,
		SessionUSD:      a.session.Total(),
		SessionCalls:    a.session.CallCount(),
		Cap:             cap,
		FixtureMode:     a.cfg.FixtureMode(),
		TZ:              a.cfg.DisplayTZ,
		CountsCost:      cost.CountsCostFor(ep),
		TokenConfigured: a.client.HasToken(),
		SettingsURL:     settingsPath,
	}
}

// tokenReady は検索を実行できる状態かを返す。
// フィクスチャモードでは API を叩かないためトークンは不要（D-22）。
func (a *App) tokenReady() bool {
	return a.cfg.FixtureMode() || a.client.HasToken()
}

var langOptions = []LangOption{
	{"", "指定なし（推奨）"},
	{"ja", "日本語（ja）"},
	{"en", "英語（en）"},
	{"ko", "韓国語（ko）"},
	{"zh", "中国語（zh）"},
	{"es", "スペイン語（es）"},
	{"fr", "フランス語（fr）"},
	{"de", "ドイツ語（de）"},
}

// maxOptionsFor は取得件数の選択肢を作る。各選択肢に予想コストを書く（D-17 / P-MAXRESULTS-COST）。
func maxOptionsFor(cap int) []MaxOption {
	candidates := []int{10, 25, 50, 100}
	if cap > 100 {
		candidates = append(candidates, cap)
	}
	opts := make([]MaxOption, 0, len(candidates))
	for _, n := range candidates {
		if n > cap {
			continue
		}
		label := fmt.Sprintf("%d 件（最大 $%.2f）", n, cost.MaxCostFor(n))
		if n == 10 {
			label += "・既定"
		}
		opts = append(opts, MaxOption{Value: n, Label: label})
	}
	return opts
}

// ---------------------------------------------------------------------------
// フォームと URL の相互変換（全状態を URL に載せる・D-23）
// ---------------------------------------------------------------------------

func parseSearchForm(q url.Values) query.SearchForm {
	atoiOr := func(key string, def int) int {
		v := strings.TrimSpace(q.Get(key))
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return def
		}
		return n
	}
	checked := func(key string) bool {
		v := q.Get(key)
		return v == "1" || v == "on" || v == "true"
	}
	ep := strings.TrimSpace(q.Get("endpoint"))
	if ep == "" {
		ep = string(xapi.EndpointRecent)
	}
	return query.SearchForm{
		Keywords:        strings.TrimSpace(q.Get("kw")),
		ExcludeWords:    strings.TrimSpace(q.Get("exclude")),
		OrWords:         strings.TrimSpace(q.Get("or")),
		ExactPhrase:     strings.TrimSpace(q.Get("phrase")),
		Hashtag:         strings.TrimSpace(q.Get("hashtag")),
		FromUser:        strings.TrimSpace(q.Get("from")),
		ToUser:          strings.TrimSpace(q.Get("to")),
		MentionUser:     strings.TrimSpace(q.Get("mention")),
		RetweetsOf:      strings.TrimSpace(q.Get("retweets_of")),
		ListID:          strings.TrimSpace(q.Get("list")),
		ReplyMode:       strings.TrimSpace(q.Get("reply")),
		ConversationID:  strings.TrimSpace(q.Get("conv")),
		InReplyToPostID: strings.TrimSpace(q.Get("in_reply_to")),
		HasImages:       checked("images"),
		HasVideo:        checked("video"),
		HasMedia:        checked("media"),
		HasLinks:        checked("links"),
		Lang:            strings.TrimSpace(q.Get("lang")),
		MinLikes:        atoiOr("min_likes", 0),
		MinReplies:      atoiOr("min_replies", 0),
		MinReposts:      atoiOr("min_reposts", 0),
		IncludeReposts:  checked("include_reposts"),
		StartLocal:      strings.TrimSpace(q.Get("start")),
		EndLocal:        strings.TrimSpace(q.Get("end")),
		Endpoint:        ep,
		MaxResults:      atoiOr("max", 10),
		PaginationToken: strings.TrimSpace(q.Get("token")),
	}
}

// formValues は SearchForm を URL クエリに戻す（パラメータ名は 1 対 1 対応）。
func formValues(f query.SearchForm) url.Values {
	v := url.Values{}
	set := func(key, val string) {
		if val != "" {
			v.Set(key, val)
		}
	}
	setBool := func(key string, b bool) {
		if b {
			v.Set(key, "1")
		}
	}
	setInt := func(key string, n int) {
		if n > 0 {
			v.Set(key, strconv.Itoa(n))
		}
	}
	set("kw", f.Keywords)
	set("exclude", f.ExcludeWords)
	set("or", f.OrWords)
	set("phrase", f.ExactPhrase)
	set("hashtag", f.Hashtag)
	set("from", f.FromUser)
	set("to", f.ToUser)
	set("mention", f.MentionUser)
	set("retweets_of", f.RetweetsOf)
	set("list", f.ListID)
	set("reply", f.ReplyMode)
	set("conv", f.ConversationID)
	set("in_reply_to", f.InReplyToPostID)
	setBool("images", f.HasImages)
	setBool("video", f.HasVideo)
	setBool("media", f.HasMedia)
	setBool("links", f.HasLinks)
	set("lang", f.Lang)
	setInt("min_likes", f.MinLikes)
	setInt("min_replies", f.MinReplies)
	setInt("min_reposts", f.MinReposts)
	setBool("include_reposts", f.IncludeReposts)
	set("start", f.StartLocal)
	set("end", f.EndLocal)
	set("endpoint", f.Endpoint)
	setInt("max", f.MaxResults)
	return v
}

func searchURL(f query.SearchForm, mode, token string) string {
	v := formValues(f)
	if token != "" {
		v.Set("token", token)
	}
	if mode != "" {
		v.Set("mode", mode)
	}
	return "/search?" + v.Encode()
}

func formURL(f query.SearchForm) string {
	v := formValues(f)
	if len(v) == 0 {
		return "/"
	}
	return "/?" + v.Encode()
}

// ---------------------------------------------------------------------------
// ハンドラ
// ---------------------------------------------------------------------------

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		a.renderError(w, r, http.StatusNotFound, "ページが見つかりません",
			"URL を確認してください。", nil, query.SearchForm{})
		return
	}
	f := parseSearchForm(r.URL.Query())
	a.renderForm(w, r, f, nil, nil, http.StatusOK)
}

func (a *App) renderForm(w http.ResponseWriter, r *http.Request, f query.SearchForm, errs []string, warns []string, status int) {
	cap := a.capStatus(r.Context())
	if f.MaxResults <= 0 {
		f.MaxResults = 10
	}
	p := FormPage{
		Page:           a.page("検索", cap, query.EndpointOf(f.Endpoint)),
		Form:           f,
		Errors:         errs,
		Warnings:       warns,
		MaxOptions:     maxOptionsFor(a.cfg.MaxResultsCap),
		LangOptions:    langOptions,
		MaxCost:        cost.MaxCostFor(f.MaxResults),
		StandaloneOps:  query.StandaloneOperators,
		ConjunctionOps: query.ConjunctionRequiredOperators,
	}
	a.render(w, status, "form.html", p)
}

// ---------------------------------------------------------------------------
// 設定画面（Bearer Token）
//
// 方針:
//   - 値は絶対に画面へ描き戻さない。マスク表示だけ（P-TOKEN-LEAK）。
//     ページを読めた相手にトークンを渡さないことが、認証を持たないこのサーバでの前提。
//   - 受け取りは POST のみ。GET だと URL・ブラウザ履歴・ログに値が残る。
//   - CSRF は guard.go の cross-site 検証で塞ぐ。他サイトからの POST には
//     ブラウザが必ず Origin を付けるため、トークン方式（＝Cookie でのセッション導入）を
//     持ち込まずに済む。
// ---------------------------------------------------------------------------

// maxSettingsBody は設定フォームの本文サイズの上限。トークンは 1KB 以内に収まる。
const maxSettingsBody = 64 << 10

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	a.renderSettings(w, r, nil, http.StatusOK, q.Get("saved") == "1", q.Get("cleared") == "1")
}

func (a *App) renderSettings(w http.ResponseWriter, r *http.Request, errs []string, status int, saved, cleared bool) {
	tok, src := a.tokens.Effective()
	p := SettingsPage{
		Page:         a.page("設定", a.capStatus(r.Context()), xapi.EndpointRecent),
		TokenMasked:  config.MaskToken(tok),
		TokenSource:  src.String(),
		HasEnvToken:  a.tokens.HasEnv(),
		HasUIToken:   a.tokens.HasUI(),
		Saved:        saved,
		Cleared:      cleared,
		Errors:       errs,
		TokenPath:    settingsTokenPath,
		ClearPath:    settingsClearPath,
		SessionReset: saved || cleared,
	}
	// マスク済みとはいえ設定状態が残るページなので、キャッシュさせない。
	w.Header().Set("Cache-Control", "no-store")
	a.render(w, status, "settings.html", p)
}

func (a *App) handleSetToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSettingsBody)
	if err := r.ParseForm(); err != nil {
		// err には本文が含まれ得るのでそのままは出さない。
		a.log.Warn("設定フォームの解析に失敗しました")
		a.renderSettings(w, r, []string{"フォームを読み取れませんでした。入力内容を確認してください。"},
			http.StatusBadRequest, false, false)
		return
	}

	changed, err := a.tokens.Set(r.PostFormValue("bearer"))
	if err != nil {
		// err は validateToken が組み立てたもので、入力値は含まない。
		a.renderSettings(w, r, []string{err.Error()}, http.StatusBadRequest, false, false)
		return
	}
	a.applyToken(changed)
	// PRG。リロードで再送信されないようにし、POST 本文を履歴に残さない。
	http.Redirect(w, r, settingsPath+"?saved=1", http.StatusSeeOther)
}

func (a *App) handleClearToken(w http.ResponseWriter, r *http.Request) {
	a.applyToken(a.tokens.Clear())
	http.Redirect(w, r, settingsPath+"?cleared=1", http.StatusSeeOther)
}

// applyToken は tokenStore の現在値を Client に反映する。
//
// トークンが実際に変わったときはセッション累計も 0 に戻す。別アカウントの支出を
// 1 つの累計に混ぜると予算ガードの判定が狂うため（usage キャッシュの破棄は
// Client.SetToken の側で行われる）。同じ値を入れ直しただけなら累計は維持する。
//
// トークンの差し替えはこの 1 箇所に集約する。反映と破棄がばらけると必ず片方を忘れる。
func (a *App) applyToken(changed bool) {
	tok, src := a.tokens.Effective()
	a.client.SetToken(tok)
	if changed {
		a.session.Reset()
	}
	a.log.Info("Bearer Token を更新しました",
		"source", src.String(),
		"bearer_token", config.MaskToken(tok),
		"changed", changed,
		"session_reset", changed)
}

func (a *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := parseSearchForm(q)
	mode := q.Get("mode")

	// バリデーションは両モードの前に実行する。エラー時は API を呼ばずフォームに戻す。
	if errs := query.ValidateWithCap(f, a.now().In(a.loc), a.cfg.MaxResultsCap); len(errs) > 0 {
		a.log.Info("validation failed（API は呼び出していません）", "errors", len(errs), "mode", mode)
		a.renderForm(w, r, f, query.ValidationErrors(errs).Messages(), nil, http.StatusBadRequest)
		return
	}

	params, warns, err := query.BuildParamsWithCap(f, a.now().In(a.loc), a.cfg.MaxResultsCap)
	if err != nil {
		var msgs []string
		if verrs, ok := err.(query.ValidationErrors); ok {
			msgs = verrs.Messages()
		} else {
			msgs = []string{err.Error()}
		}
		a.renderForm(w, r, f, msgs, nil, http.StatusBadRequest)
		return
	}

	// API を呼ぶ手前でトークンの有無を確かめる。無いまま呼んでも 401 が返るだけだが、
	// 原因と次の一手が分からない画面になる（課金は発生しない）。
	if (mode == "estimate" || mode == "execute") && !a.tokenReady() {
		a.log.Info("トークン未設定のため実行しませんでした（API は呼び出していません）", "mode", mode)
		a.renderError(w, r, http.StatusForbidden, "Bearer Token が設定されていません",
			"検索するには X API の Bearer Token が必要です。API はまだ呼び出していません（課金なし）。",
			[]string{
				"設定画面（" + settingsPath + "）でトークンを入力してください。プロセス内にのみ保持され、ディスクには保存されません。",
				"起動のたびの入力を避けたい場合に限り .env に BEARER_TOKEN=... を書けますが、その場合トークンはディスク上に残ります。",
				"課金ゼロで UI を確認するだけなら SEEKX_FIXTURE_DIR=testdata を指定して再起動してください。",
			}, f)
		return
	}

	switch mode {
	case "estimate":
		a.handleEstimate(w, r, f, params, warns)
	case "execute":
		a.handleExecute(w, r, f, params, warns)
	default:
		a.renderForm(w, r, f, nil,
			[]string{"「件数を見積る」または「見積りなしで検索する」を選んでください。"}, http.StatusOK)
	}
}

func (a *App) handleEstimate(w http.ResponseWriter, r *http.Request, f query.SearchForm, params xapi.SearchParams, warns []query.Warning) {
	ctx := r.Context()
	cap := a.capStatus(ctx)
	gran := query.CountsGranularity(params.StartTime, params.EndTime, a.now().UTC())

	counts, _, err := a.client.Counts(ctx, params.Endpoint, params.Query, gran, params.StartTime, params.EndTime)
	if err != nil {
		a.renderAPIError(w, r, err, f, params.Query)
		return
	}
	// counts はリクエスト単位で課金されるが、リソースを返さない 400 / 429 は無課金なので
	// 成功時のみ計上する（README の「HTTP 400 / 429 | 課金なし」と揃える）。
	if !a.client.FixtureMode() {
		a.session.Add(cost.CountsCostFor(params.Endpoint))
	}

	total := counts.Meta.TotalPostCount
	est := cost.EstimateSearch(total, params.MaxResults)
	est.Granularity = gran
	est.Buckets = counts.Data
	est.CountsCostUSD = cost.CountsCostFor(params.Endpoint)

	allowed, reason := cost.GateAllows(cap, a.session, est, a.cfg)

	p := EstimatePage{
		Page:          a.page("見積り", cap, params.Endpoint),
		Form:          f,
		Query:         params.Query,
		EndpointName:  query.EndpointName(params.Endpoint),
		QueryLimit:    query.QueryLimitFor(params.Endpoint),
		Granularity:   gran,
		Total:         total,
		MaxResults:    params.MaxResults,
		Est:           est,
		Buckets:       bucketViews(counts.Data, a.loc, gran),
		Warnings:      warningMessages(warns),
		Allowed:       allowed,
		GateReason:    reason,
		ExecuteURL:    searchURL(f, "execute", ""),
		ReestimateURL: searchURL(f, "estimate", ""),
		FormURL:       formURL(f),
	}
	p.QueryRunes, p.QueryBytes = query.QueryLength(params.Query)
	p.StartDisplay, p.EndDisplay = a.displayRange(params.StartTime, params.EndTime, params.Endpoint)
	p.RangeUTC = utcRange(params.StartTime, params.EndTime)

	a.render(w, http.StatusOK, "estimate.html", p)
}

func (a *App) handleExecute(w http.ResponseWriter, r *http.Request, f query.SearchForm, params xapi.SearchParams, warns []query.Warning) {
	ctx := r.Context()
	cap := a.capStatus(ctx)

	// 総件数は不明なので max_results を上限として見積り、ゲートを通す。
	est := cost.EstimateSearch(params.MaxResults, params.MaxResults)
	if allowed, reason := cost.GateAllows(cap, a.session, est, a.cfg); !allowed {
		a.log.Warn("実行をゲートで停止しました", "reason", reason)
		a.renderError(w, r, http.StatusForbidden, xapi.KindUsageCap.Title(), reason,
			[]string{
				"月次 cap の場合: リセット日を待つか SEEKX_ALLOW_OVER_CAP=1 を設定して再起動してください。",
				"セッション予算の場合: SEEKX_SESSION_BUDGET_USD を上げて再起動してください。",
				"取得件数を減らすと 1 回あたりのコストが下がります。",
			}, f)
		return
	}

	resp, rl, err := a.executeSearch(ctx, params)
	if err != nil {
		a.renderAPIError(w, r, err, f, params.Query)
		return
	}

	spent := float64(resp.Meta.ResultCount) * xapi.CostPerPostRead
	if !a.client.FixtureMode() {
		a.session.Add(spent) // 実際に返った件数で計上する（max_results ではない）
	}

	result := view.BuildView(resp, view.ViewOptions{
		Now:         a.now(),
		Loc:         a.loc,
		RateLimit:   rl,
		SpentUSD:    spent,
		SessionUSD:  a.session.Total(),
		FixtureMode: a.cfg.FixtureMode(),
		Log:         a.log,
	})
	result.QueryString = params.Query
	result.QueryRunes, result.QueryBytes = query.QueryLength(params.Query)
	result.QueryLimit = query.QueryLimitFor(params.Endpoint)
	result.EndpointName = query.EndpointName(params.Endpoint)
	result.MaxResults = params.MaxResults
	result.StartDisplay, result.EndDisplay = a.displayRange(params.StartTime, params.EndTime, params.Endpoint)
	result.Warnings = append(warningMessages(warns), result.Warnings...)

	pagingEst := cost.EstimateSearch(params.MaxResults, params.MaxResults)
	pagingAllowed, pagingReason := cost.GateAllows(cap, a.session, pagingEst, a.cfg)

	p := ResultsPage{
		Page:           a.page("検索結果", cap, params.Endpoint),
		Result:         result,
		NextCost:       cost.MaxCostFor(params.MaxResults),
		PagingBlocked:  !pagingAllowed,
		PagingReason:   pagingReason,
		FormURL:        formURL(f),
		EstimateURL:    searchURL(f, "estimate", ""),
		EmptyHints:     emptyHints(f),
		RateLimitReset: rl.ResetDisplay(a.loc),
	}
	if result.NextToken != "" {
		p.NextURL = searchURL(f, "execute", result.NextToken)
	}
	if result.PrevToken != "" {
		p.PrevURL = searchURL(f, "execute", result.PrevToken)
	}

	a.render(w, http.StatusOK, "results.html", p)
}

// executeSearch は post read の課金が発生する唯一の呼び出し箇所（cost_control.confirmation_gate_location）。
// この関数の外から SearchRecent / SearchAll を呼んではならない。
func (a *App) executeSearch(ctx context.Context, p xapi.SearchParams) (*xapi.SearchResponse, *xapi.RateLimit, error) {
	if p.Endpoint == xapi.EndpointAll {
		return a.client.SearchAll(ctx, p)
	}
	return a.client.SearchRecent(ctx, p)
}

// capStatus は usage を（キャッシュ経由で）取得して CapStatus を返す。失敗しても検索は続行できる。
func (a *App) capStatus(ctx context.Context) cost.CapStatus {
	// トークンが無ければ usage も 401 になる。毎ページ叩きに行かない。
	if !a.tokenReady() {
		return cost.CapStatus{Cap: cost.MonthlyPostCapDefault}
	}
	u, err := a.client.Usage(ctx)
	if err != nil {
		a.log.Warn("usage の取得に失敗しました（cap 表示を省略します）", "err", err.Error())
		return cost.CapStatus{Cap: cost.MonthlyPostCapDefault}
	}
	cs := cost.EvaluateCap(u)
	a.log.Info("usage",
		"project_usage", cs.Usage, "project_cap", cs.Cap,
		"ratio", cs.Ratio, "cap_reset_day", cs.ResetDay)
	return cs
}

func (a *App) displayRange(start, end time.Time, ep xapi.Endpoint) (string, string) {
	const layout = "2006-01-02 15:04"
	s, e := "指定なし", "指定なし"
	if !start.IsZero() {
		s = start.In(a.loc).Format(layout)
	} else if ep == xapi.EndpointRecent {
		s = "7 日前（既定）"
	} else {
		s = "2006-03-21 以降（既定）"
	}
	if !end.IsZero() {
		e = end.In(a.loc).Format(layout)
	} else {
		e = "現在"
	}
	return s, e
}

func utcRange(start, end time.Time) string {
	if start.IsZero() && end.IsZero() {
		return ""
	}
	s, e := "-", "-"
	if !start.IsZero() {
		s = start.UTC().Format(time.RFC3339)
	}
	if !end.IsZero() {
		e = end.UTC().Format(time.RFC3339)
	}
	return s + " 〜 " + e
}

func warningMessages(warns []query.Warning) []string {
	if len(warns) == 0 {
		return nil
	}
	out := make([]string, 0, len(warns))
	for _, w := range warns {
		out = append(out, w.Message)
	}
	return out
}

func bucketViews(buckets []xapi.CountBucket, loc *time.Location, gran string) []BucketView {
	if len(buckets) == 0 {
		return nil
	}
	max := 0
	for _, b := range buckets {
		if b.PostCount > max {
			max = b.PostCount
		}
	}
	layout := "01-02"
	if gran == "hour" {
		layout = "01-02 15:04"
	} else if gran == "minute" {
		layout = "01-02 15:04"
	}
	out := make([]BucketView, 0, len(buckets))
	for _, b := range buckets {
		bv := BucketView{Count: b.PostCount}
		if !b.Start.IsZero() {
			bv.Label = b.Start.In(loc).Format(layout)
		}
		if max > 0 {
			bv.Pct = float64(b.PostCount) / float64(max) * 100
		}
		out = append(out, bv)
	}
	return out
}

func emptyHints(f query.SearchForm) []string {
	var hints []string
	if f.Lang != "" {
		hints = append(hints, "言語フィルタ（lang:"+f.Lang+"）を外す")
	}
	if f.HasImages || f.HasVideo || f.HasMedia || f.HasLinks {
		hints = append(hints, "メディア条件（has:images など）を外す")
	}
	if !f.IncludeReposts {
		hints = append(hints, "「リポストを含める」を ON にする（既定では -is:retweet を付与しています）")
	}
	if f.StartLocal != "" || f.EndLocal != "" {
		hints = append(hints, "期間を広げる、または期間指定を外す")
	}
	if f.Endpoint != string(xapi.EndpointAll) {
		hints = append(hints, "検索範囲を「全期間」に切り替える（直近 7 日に該当がない可能性）")
	}
	if f.MinLikes > 0 || f.MinReplies > 0 || f.MinReposts > 0 {
		hints = append(hints, "反応数の下限を下げる")
	}
	hints = append(hints, "キーワードを短くする、または別の語を試す")
	return hints
}

// ---------------------------------------------------------------------------
// レンダリング
// ---------------------------------------------------------------------------

func (a *App) render(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := a.tmpl.ExecuteTemplate(w, name, data); err != nil {
		// ヘッダは送信済みなのでエラーページには切り替えられない。ログにだけ残す。
		a.log.Error("テンプレートの描画に失敗しました", "template", name, "err", err)
	}
}

func (a *App) renderError(w http.ResponseWriter, r *http.Request, status int, heading, message string, steps []string, f query.SearchForm) {
	p := ErrorPage{
		Page:     a.page("エラー", a.capStatus(r.Context()), query.EndpointOf(f.Endpoint)),
		Heading:  heading,
		Message:  message,
		Steps:    steps,
		FormURL:  formURL(f),
		Status:   0,
		Messages: nil,
	}
	a.render(w, status, "error.html", p)
}

// renderAPIError は APIError を ErrorKind ごとの日本語画面に変換する。
// トークン・Authorization ヘッダ・リクエスト URL 全文は絶対に出さない。
func (a *App) renderAPIError(w http.ResponseWriter, r *http.Request, err error, f query.SearchForm, generatedQuery string) {
	ae, ok := err.(*xapi.APIError)
	if !ok {
		a.renderError(w, r, http.StatusInternalServerError, "予期しないエラー", err.Error(), nil, f)
		return
	}

	status := http.StatusBadGateway
	switch ae.Kind {
	case xapi.KindInvalidQuery:
		status = http.StatusBadRequest
	case xapi.KindUnauthorized, xapi.KindForbidden, xapi.KindForbiddenNotEnrolled:
		status = http.StatusForbidden
	case xapi.KindNotFound:
		status = http.StatusNotFound
	case xapi.KindRateLimited:
		status = http.StatusTooManyRequests
	}

	p := ErrorPage{
		Page:     a.page("エラー", a.capStatus(r.Context()), query.EndpointOf(f.Endpoint)),
		Heading:  ae.Kind.Title(),
		Message:  ae.UserMessage(a.loc),
		Steps:    errorSteps(ae.Kind),
		Status:   ae.Status,
		APITitle: ae.Title,
		Detail:   ae.Detail,
		Reason:   ae.Reason,
		Messages: ae.Messages,
		FormURL:  formURL(f),
	}
	if ae.Kind == xapi.KindInvalidQuery {
		p.Query = generatedQuery // 400 のときは生成クエリ全文を必ず出す
	}
	a.render(w, status, "error.html", p)
}

func errorSteps(k xapi.ErrorKind) []string {
	switch k {
	case xapi.KindInvalidQuery:
		return []string{
			"生成されたクエリを確認してください（HTTP 400 は課金されません）。",
			"「画像付き」「言語」「返信のみ」などの条件だけでは検索できません。キーワードかアカウントを併せて指定してください。",
			"クエリが長すぎる場合は条件を減らしてください（文字数で判定されます）。",
		}
	case xapi.KindUnauthorized:
		return []string{
			"ワークスペース直下の .env の BEARER_TOKEN を確認してください。",
			"X 開発者ポータルでトークンを再生成した場合は .env を更新して再起動してください。",
		}
	case xapi.KindForbiddenNotEnrolled:
		return []string{
			"X 開発者ポータルで App が Project に紐付いているか確認してください。",
			"pay-per-use のクレジット残高を確認してください。",
			"アクセスレベルが検索エンドポイントを含むか確認してください。",
		}
	case xapi.KindRateLimited:
		return []string{
			"表示されたリセット時刻以降に再試行してください（429 は課金されません）。",
			"「全期間」検索は 1 リクエスト/秒の制限があります。連続実行を避けてください。",
		}
	case xapi.KindServer:
		return []string{"数分待って再試行してください（自動で 3 回まで再試行しています）。"}
	case xapi.KindNetwork:
		return []string{
			"コンテナからインターネットに出られるか確認してください。",
			"プロキシ環境の場合は HTTPS_PROXY の設定を確認してください。",
		}
	case xapi.KindDecode:
		return []string{
			"技術的な詳細のレスポンス断片を確認してください。",
			"API のレスポンス形式が変わった可能性があります（フィールド名の変更）。",
		}
	case xapi.KindUsageCap:
		return []string{"月次 cap またはセッション予算の設定を見直してください。"}
	default:
		return nil
	}
}
