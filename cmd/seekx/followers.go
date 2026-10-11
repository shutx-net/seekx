package main

// followers.go はフォロワー取得の画面（GET /followers）のハンドラと、フォームと URL の相互変換を担う。
//
// main.go と同じ制約（file_layout）を守る:
//   - X API へのリクエストを直接組み立てない（internal/xapi の責務）
//   - 入力を検証しない・パラメータを組み立てない（internal/query の責務）
//   - API レスポンスの struct を定義しない（internal/xapi の責務）
//   - 単価や見積り計算式を持たない（金額は internal/cost の関数から受け取る）
//   - テンプレートに *http.Request や Config をそのまま渡さない（ViewModel だけを渡す）

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/shutx-net/seekx/internal/cost"
	"github.com/shutx-net/seekx/internal/query"
	"github.com/shutx-net/seekx/internal/view"
	"github.com/shutx-net/seekx/internal/xapi"
)

// followersPath はフォロワー取得の画面のパス。テンプレート（Page.FollowersURL）・リンク・テストで
// 共有する単一情報源（settingsPath と同じ扱い）。
const followersPath = "/followers"

// followersPageSizes は 1 ページの人数の選択肢。最大は API の上限（xapi.FollowersMaxResultsMax）を
// そのまま使い、同じ数値を 2 か所に持たない。
//
// 【SEEKX_MAX_RESULTS_CAP（投稿検索の件数上限・既定 100）に連動させない】連動させると、
// 環境変数を変えない限り 100 人までしか選べず、合意済みの要件（1 ページ最大 1,000 人）と食い違う。
// 投稿検索の maxOptionsFor と違って cap を受け取らないのはこのため。
var followersPageSizes = []int{10, 100, 500, xapi.FollowersMaxResultsMax}

// fixtureFollowerExamples はフィクスチャモードのフォームで例示するユーザー名。
// それぞれ testdata/users_by_username_<名前>.json に対応し（TestFollowersFixtureExamplesExist が固定）、
// 鍵アカウント・フォロワー 0 人・存在しないユーザーの画面を課金なしで確かめられる。
// それ以外の名前はすべて users_by_username.json（通常のアカウント）に解決される（xapi の fixtureCandidates）。
var fixtureFollowerExamples = []string{"fx_protected", "fx_empty", "fx_notfound"}

// FollowersFormPage はフォロワー取得のフォーム画面。
type FollowersFormPage struct {
	Page
	Form            query.FollowersForm
	Errors          []string
	PageSizeOptions []MaxOption
	// LookupCost は見積り（ユーザー検索 1 回）の金額。見積りボタンのラベルに出す。
	LookupCost float64
	// FixtureExamples はフィクスチャモードのときだけ入る。実 API に向いているときに、
	// 存在しないかもしれない架空の名前を勧めないため。
	FixtureExamples []string
}

// FollowersEstimatePage はフォロワー取得の見積り画面（ユーザー検索の結果）。
type FollowersEstimatePage struct {
	Page
	Form query.FollowersForm
	// User は API が返した対象ユーザー。カードに出す。
	User view.ViewUser
	Est  cost.FollowersEstimate
	// LookupCost はこの見積り（ユーザー検索 1 回）の金額。すでに発生している額と再見積りのラベルに出す。
	LookupCost float64
	Warnings   []string
	Allowed    bool
	GateReason string
	// ExecuteURL は最初のページの取得。ReestimateURL は同じアカウントの見積りのやり直し。
	// どちらも API が返したユーザー名（と ID）から作る（handleFollowersEstimate を参照）。
	ExecuteURL    string
	ReestimateURL string
	// FormURL は「条件を修正する」。入力したとおりの値でフォームに戻す。
	FormURL string
}

// NoFollowers はフォロワーが 0 人と分かっているかを返す。0 人なら取得しても返るユーザーがいないので、
// テンプレートは取得のリンクを出さない。
//
// 【不明（TotalKnown が偽）を 0 人と扱わない】フォロワー数が不明のときも Est.Total は 0 なので、
// Total だけを見ると、指標が欠けたアカウントの取得のリンクまで消えてしまう。
func (p FollowersEstimatePage) NoFollowers() bool { return p.Est.TotalKnown && p.Est.Total == 0 }

// FollowersPage はフォロワー一覧（取得した 1 ページ分）の画面。
//
// 対象のユーザー名と ID は URL から来る（フォロワー取得の API は対象のユーザーを返さない）。取得は ID で行い、
// ユーザー名は見出し・リンク・CSV のファイル名にだけ使う。URL を書き換えると両者が食い違い得るので、
// 画面の参照情報には ID も並べる。
type FollowersPage struct {
	Page
	Result view.FollowersView
	// TargetUsername は先頭の @ を落とした対象のユーザー名。空なら見出しを ID で出す。
	TargetUsername string
	TargetID       string
	// PageSize は 1 ページの人数（max_results）。ページングのラベルと参照情報に出す。
	PageSize int
	// UnitCost はフォロワー 1 人あたりの金額で、今回のコストの内訳に出す。
	UnitCost float64
	NextURL  string
	PrevURL  string
	// NextCost は前後のページを取得したときの最大額（PageSize 人分）。課金は返った人数ぶんなので、
	// 実費はこれ以下になる。
	NextCost float64
	// LookupCost は再見積り（ユーザー検索 1 回）の金額。UnitCost と今は同額だが、料金表では別の行
	// （xapi.CostPerUserRead と CostPerFollowsRead）なので流用しない。
	LookupCost    float64
	PagingBlocked bool
	PagingReason  string
	// FormURL は「別のアカウントを調べる」。EstimateURL は「このアカウントを再見積り」で、
	// ユーザー名が無いとき（ID だけで開いたページ）は空にしてリンクを出さない（見積りはユーザー名で引くため）。
	FormURL        string
	EstimateURL    string
	RateLimitReset string
	// CSV は表示中のフォロワーの CSV。テンプレートがデータブロックに埋め、ブラウザが保存する
	// （追加の API 呼び出しもサーバ側の保存も無い）。
	CSV view.CSVExport
}

// PageID は結果ページと同じ 2 カラムの CSS を当てる（ResultsPage.PageID と同じ値）。
func (FollowersPage) PageID() string { return "results" }

// ---------------------------------------------------------------------------
// フォームと URL の相互変換（全状態を URL に載せる・D-23）
// ---------------------------------------------------------------------------

// parseFollowersForm は URL のクエリを FollowersForm にする（パラメータ名は followersURL が書き出すものと
// 1 対 1 に対応する）。
//
// 前後の空白は落とす。ID とトークンは query 側で補正しない（検証した値をそのまま API に送る）ので、
// コピー＆ペーストで紛れた空白をここで落とさないと U03 / U05 で弾かれる。
// ユーザー名の先頭の @ は落とさない（フォームには入力どおりに描き戻し、@ の扱いは
// query.NormalizeFollowersUsername に任せる）。
//
// 1 ページの人数は、空なら既定値、数として読めなければ -1 にする。【読めない値を既定値にしない】
// 既定値にすると指定と違う人数で取得して課金することになるので、-1 にして検証（U04）で止める
// （query.FollowersForm.MaxResults のコメント）。
func parseFollowersForm(q url.Values) query.FollowersForm {
	size := query.FollowersPageSizeDefault
	if v := strings.TrimSpace(q.Get("max")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			n = -1
		}
		size = n
	}
	return query.FollowersForm{
		Username:        strings.TrimSpace(q.Get("username")),
		UserID:          strings.TrimSpace(q.Get("id")),
		MaxResults:      size,
		PaginationToken: strings.TrimSpace(q.Get("token")),
	}
}

// followersValues は FollowersForm を URL のクエリに戻す（parseFollowersForm の逆）。
// ページングトークンと mode は含めない（followersURL が引数で受ける。searchURL と同じ形）。
//
// 1 ページの人数は 0 や負の値でも書き出す。省くと parseFollowersForm が既定値を入れるため、
// 検証で弾くべき値（読めなかった値の -1 など）がリンクを経て既定の人数に化け、指定と違う人数で
// 課金され得る。
func followersValues(f query.FollowersForm) url.Values {
	v := url.Values{}
	if f.Username != "" {
		v.Set("username", f.Username)
	}
	if f.UserID != "" {
		v.Set("id", f.UserID)
	}
	v.Set("max", strconv.Itoa(f.MaxResults))
	return v
}

// followersURL はフォロワー取得の画面への URL を作る。mode は "estimate"（見積り）か
// "execute"（取得）、token はページングトークン（空なら最初のページ）。
func followersURL(f query.FollowersForm, mode, token string) string {
	v := followersValues(f)
	if token != "" {
		v.Set("token", token)
	}
	if mode != "" {
		v.Set("mode", mode)
	}
	return followersPath + "?" + v.Encode()
}

// followersFormURL は入力（ユーザー名・1 ページの人数）を保ったままフォームに戻る URL を作る。
//
// ID は落とす（mode とトークンは followersValues が最初から含めない）。フォームには ID の入力欄が無く、
// 見積りは ID をユーザー名から引き直す（取得のリンクは API が返した ID から作り直す）ので、
// フォームの URL に載せても使われない。
func followersFormURL(f query.FollowersForm) string {
	v := followersValues(f)
	v.Del("id")
	return followersPath + "?" + v.Encode()
}

// followersPageSizeOptions は 1 ページの人数の選択肢を作る。各選択肢に最大額を書く
// （D-17 / P-MAXRESULTS-COST）。課金は返った人数ぶんなので、上限の人数で計算した額は最大値になる。
func followersPageSizeOptions() []MaxOption {
	opts := make([]MaxOption, 0, len(followersPageSizes))
	for _, n := range followersPageSizes {
		label := fmt.Sprintf("%s 人（最大 $%.2f）", humanInt(n), cost.FollowersCostFor(n))
		if n == query.FollowersPageSizeDefault {
			label += "・既定"
		}
		opts = append(opts, MaxOption{Value: n, Label: label})
	}
	return opts
}

// ---------------------------------------------------------------------------
// ハンドラ
// ---------------------------------------------------------------------------

// handleFollowers はフォロワー取得の画面（GET /followers）。mode=estimate で見積り（ユーザー検索）、
// mode=execute でフォロワー一覧の 1 ページの取得を行い、それ以外はフォームを出す（API は呼ばない＝課金なし）。
//
// どちらのモードも「入力の検証 → トークンの確認 → パラメータの組み立て → 実行」の順に進む。
func (a *App) handleFollowers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := parseFollowersForm(q)

	switch mode := q.Get("mode"); mode {
	case "estimate":
		// 検証はトークンの確認より前に行う（検索の handleSearch と同じ順）。入力の誤りはトークンを
		// 設定しても直らないので先に知らせる。どちらで止めても API は呼ばない（課金なし）。
		if errs := query.ValidateFollowersLookup(f); len(errs) > 0 {
			a.log.Info("validation failed（API は呼び出していません）", "errors", len(errs), "mode", mode, "path", followersPath)
			a.renderFollowersForm(w, r, f, query.ValidationErrors(errs).Messages(), http.StatusBadRequest)
			return
		}
		if !a.tokenReady() {
			// renderTokenMissing はログを出さない（何を止めたかを知っているのはハンドラ）。
			a.log.Info("トークン未設定のため実行しませんでした（API は呼び出していません）", "mode", mode, "path", followersPath)
			a.renderTokenMissing(w, r, "フォロワーを取得する", followersFormURL(f))
			return
		}
		params, err := query.BuildUserLookupParams(f)
		if err != nil {
			// 上の検証と同じ規則なので通常は来ない。来ても API は呼ばずにフォームへ戻す（課金なし）。
			msgs := []string{err.Error()}
			if verrs, ok := err.(query.ValidationErrors); ok {
				msgs = verrs.Messages()
			}
			a.renderFollowersForm(w, r, f, msgs, http.StatusBadRequest)
			return
		}
		a.handleFollowersEstimate(w, r, f, params)
	case "execute":
		// 順序と止め方は見積りと同じ（入力の誤り → 400 のフォーム、トークン未設定 → 案内。どちらも課金なし）。
		// 取得のリンクは見積り画面と一覧が作るが、URL は書き換えられるので同じように確かめる。
		if errs := query.ValidateFollowersFetch(f); len(errs) > 0 {
			a.log.Info("validation failed（API は呼び出していません）", "errors", len(errs), "mode", mode, "path", followersPath)
			a.renderFollowersForm(w, r, f, query.ValidationErrors(errs).Messages(), http.StatusBadRequest)
			return
		}
		// ここから先（見出し・リンク・CSV のファイル名・戻り先）は @ を落としたユーザー名を使う。
		// そのまま使うと「@@name」と出るうえ、@ 付きの値が前後のページのリンクに引き継がれる。
		// 形式は上の検証（U02）で確かめてある。@ だけなら空になり、ユーザー名が無いものとして扱う。
		f.Username = query.NormalizeFollowersUsername(f.Username)
		if !a.tokenReady() {
			a.log.Info("トークン未設定のため実行しませんでした（API は呼び出していません）", "mode", mode, "path", followersPath)
			a.renderTokenMissing(w, r, "フォロワーを取得する", followersFormURL(f))
			return
		}
		params, err := query.BuildFollowersParams(f)
		if err != nil {
			// 上の検証と同じ規則なので通常は来ない。来ても API は呼ばずにフォームへ戻す（課金なし）。
			msgs := []string{err.Error()}
			if verrs, ok := err.(query.ValidationErrors); ok {
				msgs = verrs.Messages()
			}
			a.renderFollowersForm(w, r, f, msgs, http.StatusBadRequest)
			return
		}
		a.handleFollowersExecute(w, r, f, params)
	default:
		a.renderFollowersForm(w, r, f, nil, http.StatusOK)
	}
}

// handleFollowersEstimate は対象アカウントをユーザー検索で引き、フォロワー数から取得の金額を見積る。
// f は入力（検証済み）、params は f から組み立てたユーザー検索のパラメータ。
func (a *App) handleFollowersEstimate(w http.ResponseWriter, r *http.Request, f query.FollowersForm, params xapi.UserLookupParams) {
	ctx := r.Context()
	cap := a.capStatus(ctx)

	resp, _, err := a.lookupUser(ctx, params)
	if err != nil {
		a.renderFollowersAPIError(w, r, err, f)
		return
	}
	if resp.Data == nil {
		// 存在しない・凍結されたユーザーは HTTP 200 で data が無く errors だけが返る（エラーにはならない）。
		// ユーザーが返っていないので課金されない見込みで、セッション累計にも計上しない。
		// 名前は API が返していないので、実際に送った値（先頭の @ を落とした入力）を出す。
		a.log.Info("ユーザー検索でユーザーが返りませんでした（課金なしの見込み）", "problems", len(resp.Errors))
		a.renderUserProblems(w, r, "ユーザーが見つかりませんでした",
			"@"+params.Username+" に当たるユーザーを X API が返しませんでした。ユーザー名の誤りのほか、"+
				"アカウントが凍結・削除された可能性があります。ユーザーが返っていないため、課金されない見込みです。",
			followersErrorSteps(xapi.KindNotFound), resp.Errors, followersFormURL(f))
		return
	}
	// User: Read はユーザーが返ったときだけ 1 人分かかる（存在しないユーザーは上で返している）。
	// フィクスチャモードは API を呼ばないので計上しない。
	if !a.client.FixtureMode() {
		a.session.Add(cost.UserLookupCost())
	}

	user := view.BuildUserView(*resp.Data, a.loc)

	// リンクには API が返したユーザー名を使う。入力値には先頭の @ / ＠ が付き得るので、そのまま渡すと
	// 取得の画面で「@@name」と出る。API が返さなかったとき（想定外）だけ、実際に送った値で代える。
	// ID は取得（GET /2/users/:id/followers）に要るので、API が返した値をそのまま載せる。
	username := user.Username
	if username == "" {
		username = params.Username
	}
	execute := query.FollowersForm{Username: username, UserID: user.ID, MaxResults: f.MaxResults}
	reestimate := query.FollowersForm{Username: username, MaxResults: f.MaxResults}

	// フォロワー取得は投稿を返さないので月次 cap（post read の上限）では止めない（いまは常に許可）。
	allowed, reason := cost.GateAllowsFor(cost.ReadUsers, cap, a.cfg)

	var warns []string
	if user.Protected {
		// 先頭に置く。取得のリンクは消さない（取得できるかどうかは試すまで分からず、取得できなくても
		// ユーザーが返らなければ課金されない見込みのため）。
		warns = append(warns, "鍵アカウントです。App-only の Bearer Token ではフォロワーを取得できない可能性が高いです"+
			"（取得できなかった場合はユーザーが返らないため、課金されない見込みです）。")
	}
	for _, pe := range resp.Errors {
		warns = append(warns, "X API が一部のエラーを返しました（HTTP 200）: "+pe.Describe())
	}

	p := FollowersEstimatePage{
		Page:          a.page("フォロワー取得の見積り", cap, xapi.EndpointRecent),
		Form:          f,
		User:          user,
		Est:           cost.EstimateFollowers(user.Metrics.Followers, user.Metrics.HasMetrics, f.MaxResults),
		LookupCost:    cost.UserLookupCost(),
		Warnings:      warns,
		Allowed:       allowed,
		GateReason:    reason,
		ExecuteURL:    followersURL(execute, "execute", ""),
		ReestimateURL: followersURL(reestimate, "estimate", ""),
		FormURL:       followersFormURL(f),
	}
	a.render(w, http.StatusOK, "followers_estimate.html", p)
}

// lookupUser は User: Read の課金が発生する唯一の呼び出し箇所（executeSearch と同じ扱い）。
// この関数の外から UserByUsername を呼んではならない。
func (a *App) lookupUser(ctx context.Context, p xapi.UserLookupParams) (*xapi.UserLookupResponse, *xapi.RateLimit, error) {
	return a.client.UserByUsername(ctx, p)
}

// handleFollowersExecute はフォロワー一覧を 1 ページ取得し、ユーザーカードを結果ページと同じ 2 カラムに並べる。
// f は入力（検証済み・ユーザー名は @ を落とした値）、params は f から組み立てたフォロワー取得のパラメータ。
func (a *App) handleFollowersExecute(w http.ResponseWriter, r *http.Request, f query.FollowersForm, params xapi.FollowersParams) {
	ctx := r.Context()
	cap := a.capStatus(ctx)

	// フォロワー取得は投稿を返さないので月次 cap（post read の上限）では止めない（いまは常に許可）。
	// 方針が変わってゲートが閉じたら、検索の handleExecute と同じく API を呼ぶ前に 403 で止める。
	allowed, reason := cost.GateAllowsFor(cost.ReadUsers, cap, a.cfg)
	if !allowed {
		a.log.Warn("実行をゲートで停止しました", "reason", reason, "path", followersPath)
		a.renderErrorTo(w, r, http.StatusForbidden, "実行できません", reason,
			[]string{"API は呼び出していません（課金なし）。停止の理由を確かめてから、見積りからやり直してください。"},
			followersFormURL(f))
		return
	}

	resp, rl, err := a.fetchFollowers(ctx, params)
	if err != nil {
		a.renderFollowersAPIError(w, r, err, f)
		return
	}
	if len(resp.Data) == 0 && len(resp.Errors) > 0 {
		// 読めないアカウント（鍵アカウントなど）は HTTP 200 で data が無く errors だけが返る（エラーにはならない）。
		// ユーザーが返っていないので課金されない見込みで、セッション累計にも計上しない。
		// errors も無い 0 人（フォロワーがいない）は下の通常の画面で「0 人でした」と出す。
		a.log.Info("フォロワー取得でユーザーが返りませんでした（課金なしの見込み）", "problems", len(resp.Errors))
		message, steps := followersProblemText(resp.Errors)
		a.renderUserProblems(w, r, "フォロワーを取得できませんでした", message, steps, resp.Errors, followersFormURL(f))
		return
	}

	// Following/Followers: Read は返ったユーザーの人数ぶんかかる（max_results の人数ではない）。
	// 0 人でも回数には数える（検索の handleExecute と同じ。API を 1 回呼んだことに変わりはない）。
	// フィクスチャモードは API を呼ばないので計上しない。
	spent := cost.FollowersCostFor(len(resp.Data))
	if !a.client.FixtureMode() {
		a.session.Add(spent)
	}

	result := view.BuildFollowersView(resp, view.ViewOptions{
		Now:         a.now(),
		Loc:         a.loc,
		RateLimit:   rl,
		SpentUSD:    spent,
		FixtureMode: a.cfg.FixtureMode(),
		Log:         a.log,
	})

	p := FollowersPage{
		// ヘッダのセッション累計に今回の分が入るよう、計上の後で作る。
		Page:           a.page("フォロワー一覧", cap, xapi.EndpointRecent),
		Result:         result,
		TargetUsername: f.Username,
		TargetID:       f.UserID,
		PageSize:       params.MaxResults,
		UnitCost:       cost.FollowersCostFor(1),
		NextCost:       cost.FollowersCostFor(params.MaxResults),
		LookupCost:     cost.UserLookupCost(),
		// 前後のページの取得も同じゲートを通る。ここまで来たなら開いているが、閉じたときの表示
		// （無効なボタンと理由）はテンプレートが持つので、判定の結果をそのまま渡す（検索と同じ）。
		PagingBlocked: !allowed,
		PagingReason:  reason,
		// 別のアカウントを調べるので、ユーザー名は空にして 1 ページの人数だけを保つ。
		// 同じアカウントのやり直しは EstimateURL（再見積り）が受け持つ。
		FormURL:        followersFormURL(query.FollowersForm{MaxResults: f.MaxResults}),
		RateLimitReset: rl.ResetDisplay(a.loc),
		CSV: view.CSVExport{
			// ファイル名の日時は表示タイムゾーンで書く（画面の時刻表示と揃える）。
			Filename: view.FollowersCSVFilename(f.Username, f.UserID, a.now().In(a.loc)),
			Text:     view.FollowersCSVFile(result.Users),
		},
	}
	if f.Username != "" {
		// 見積り画面の ReestimateURL と同じ形（ID は載せない。見積りはユーザー名から引き直す）。
		p.EstimateURL = followersURL(query.FollowersForm{Username: f.Username, MaxResults: f.MaxResults}, "estimate", "")
	}
	if result.NextToken != "" {
		p.NextURL = followersURL(f, "execute", result.NextToken)
	}
	if result.PrevToken != "" {
		p.PrevURL = followersURL(f, "execute", result.PrevToken)
	}

	a.render(w, http.StatusOK, "followers.html", p)
}

// fetchFollowers は Following/Followers: Read の課金が発生する唯一の呼び出し箇所（executeSearch と同じ扱い）。
// この関数の外から Followers を呼んではならない。
func (a *App) fetchFollowers(ctx context.Context, p xapi.FollowersParams) (*xapi.FollowersResponse, *xapi.RateLimit, error) {
	return a.client.Followers(ctx, p)
}

// renderFollowersForm はフォロワー取得のフォームを描画する。errs は入力の誤り（API は呼んでいない）。
//
// 1 ページの人数が 0 以下（読めなかった値など）なら既定の人数を選んだ状態で出す。どの選択肢にも
// selected が付かないと、ブラウザが先頭の選択肢を選ぶため。値そのものの誤りは検証（U04）が知らせる。
//
// Page の Endpoint を EndpointRecent に固定してよい理由は renderErrorTo と同じ
// （Endpoint が効くのは CountsCost だけで、followers_form.html と layout.html はそれを使わない）。
func (a *App) renderFollowersForm(w http.ResponseWriter, r *http.Request, f query.FollowersForm, errs []string, status int) {
	if f.MaxResults <= 0 {
		f.MaxResults = query.FollowersPageSizeDefault
	}
	p := FollowersFormPage{
		Page:            a.page("フォロワー取得", a.capStatus(r.Context()), xapi.EndpointRecent),
		Form:            f,
		Errors:          errs,
		PageSizeOptions: followersPageSizeOptions(),
		LookupCost:      cost.UserLookupCost(),
	}
	if a.cfg.FixtureMode() {
		p.FixtureExamples = fixtureFollowerExamples
	}
	a.render(w, status, "followers_form.html", p)
}

// ---------------------------------------------------------------------------
// エラー画面
// ---------------------------------------------------------------------------

// renderFollowersAPIError はフォロワー取得の API エラー画面（検索の renderAPIError に当たる）。
// 戻り先・手順・文言をフォロワー取得のものにして渡す。生成クエリは無いので Query は空のまま。
//
// 【Steps と Text を省かない】省くと手順が出ず、文言が検索向けの既定
// （「検索条件が…」「生成されたクエリを確認してください」）になる。
func (a *App) renderFollowersAPIError(w http.ResponseWriter, r *http.Request, err error, f query.FollowersForm) {
	a.renderAPIErrorTo(w, r, err, apiErrorView{
		BackURL: followersFormURL(f),
		Steps:   followersErrorSteps,
		Text:    a.followersErrorText,
	})
}

// followersErrorText はフォロワー取得の API エラーの見出しと本文（apiErrorView.Text）。
// 400 / 401 / 403 / 404 / 429 をフォロワー取得向けにし、それ以外は ErrorKind の既定のままにする。
// 429 のリセット時刻を表示タイムゾーンで出すため、App のメソッドにしてある。
func (a *App) followersErrorText(ae *xapi.APIError) (heading, message string) {
	switch ae.Kind {
	case xapi.KindUnauthorized:
		// 既定の本文は「.env の BEARER_TOKEN を確認」で、同じ画面の手順（設定画面で確認する）と食い違う。
		// 設定画面の値は .env より優先されるので、設定画面だけを案内すれば足りる。
		return ae.Kind.Title(), "認証に失敗しました（HTTP 401）。Bearer Token が正しいか、設定画面（" + settingsPath + "）で確認してください。"
	case xapi.KindInvalidQuery:
		if ae.Status == 0 {
			// xapi が送る前に弾いた（パスを壊す値）。UserMessage は「HTTP 400・課金なし」と書くが、
			// 実際には HTTP を送っていないので、そのままだと API を呼んだように読める。
			return "入力を受け付けられませんでした",
				"ユーザー名か ID に使えない文字が含まれるため、送信前に止めました（API は呼び出していません）。"
		}
		return "X API が要求を受け付けませんでした",
			"ユーザー名・1 ページの人数・ページングトークンのいずれかが X API に受け付けられませんでした（HTTP 400・課金なし）。"
	case xapi.KindForbidden:
		return ae.Kind.Title(),
			"X API がこの取得を許可しませんでした（HTTP 403）。App のアクセスレベルを確認してください。" +
				"ユーザーが返っていないため、課金されない見込みです。"
	case xapi.KindNotFound:
		return "ユーザーが見つかりませんでした",
			"X API が対象のユーザーを見つけられませんでした（HTTP 404）。ユーザー名の誤りのほか、" +
				"アカウントが凍結・削除された可能性があります。ユーザーが返っていないため、課金されない見込みです。"
	case xapi.KindRateLimited:
		// リセット時刻はレスポンスヘッダ（x-rate-limit-reset）の値を優先する。【docs の回数だけで案内しない】
		// docs は 15 分 300 リクエストとするが、古い移行ガイドにはフォロワー取得を 15 分 15 リクエストと
		// 書いたものがある。どちらでも枠は 15 分なので、ヘッダが無いときは 15 分待てば外れない。
		if reset := ae.RateLimit.ResetDisplay(a.loc); reset != "" {
			msg := fmt.Sprintf("X API の回数の上限に達しました（HTTP 429・課金なし）。%s 以降に再試行してください。", reset)
			if ae.RateLimit.Limit > 0 {
				msg += fmt.Sprintf("X API が返したこの枠の上限は %s リクエストです。", humanInt(ae.RateLimit.Limit))
			}
			return ae.Kind.Title(), msg
		}
		return ae.Kind.Title(),
			"X API の回数の上限に達しました（HTTP 429・課金なし）。リセット時刻が返らなかったため、" +
				"15 分ほど待ってから再試行してください（docs の上限は 15 分あたり 300 リクエストです）。"
	default:
		return ae.Kind.Title(), ae.UserMessage(a.loc)
	}
}

// followersErrorSteps は ErrorKind ごとの対処手順（apiErrorView.Steps）。検索の errorSteps を使わないのは、
// 検索エンドポイントや生成クエリを前提にした手順になっているため。
func followersErrorSteps(k xapi.ErrorKind) []string {
	switch k {
	case xapi.KindInvalidQuery:
		return []string{
			"フォームに戻り、ユーザー名（半角英数字と _ のみ。先頭の @ は省略できます）と 1 ページの人数を確認してください。",
			"HTTP 400 は課金されません（送信前に止めた場合は API を呼び出していません）。",
		}
	case xapi.KindUnauthorized:
		return []string{
			"設定画面（" + settingsPath + "）で Bearer Token を確認してください。",
			"X 開発者ポータルでトークンを再生成した場合は、新しいトークンを設定画面に入力してください。",
		}
	case xapi.KindForbiddenNotEnrolled:
		return []string{
			"X 開発者ポータルで App が Project に紐付いているか確認してください。",
			"pay-per-use のクレジット残高を確認してください。",
			"アクセスレベルがユーザー検索とフォロワー取得を含むか確認してください。",
		}
	case xapi.KindForbidden:
		return []string{
			"X 開発者ポータルで、App のアクセスレベルがユーザー検索とフォロワー取得を含むか確認してください。",
			"鍵アカウントのフォロワーは、App-only の Bearer Token では取得できない見込みです。",
		}
	case xapi.KindNotFound:
		return []string{
			"ユーザー名の綴りを確認してください（先頭の @ は省略できます）。",
			"X でそのアカウントが表示されるか（凍結・削除されていないか）を確認してください。",
		}
	case xapi.KindRateLimited:
		return []string{
			"表示されたリセット時刻以降に再試行してください（429 は課金されません）。",
			"リセット時刻が出ていない場合は、15 分ほど待ってから再試行してください。",
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
	default:
		// KindUsageCap はフォロワー取得では起きない（月次 cap は post read の上限で、ユーザーの読み取りは
		// GateAllowsFor(ReadUsers) が止めない）。
		return nil
	}
}

// renderUserProblems は、HTTP 200 で data が無く errors だけが返ったとき（存在しないユーザー・
// 読めないアカウントなど）のエラー画面。見出し・本文・手順は呼び出し側が渡す。
//
// X API としては成功の応答なので、技術的な詳細の HTTP ステータスは 200 と出し、errors の 1 行説明を並べる。
// この画面自体のステータスは problemStatus が errors の種類から決める。
func (a *App) renderUserProblems(w http.ResponseWriter, r *http.Request, heading, message string, steps []string, probs []xapi.ProblemDetail, backURL string) {
	msgs := make([]string, 0, len(probs))
	for _, pe := range probs {
		msgs = append(msgs, pe.Describe())
	}
	p := ErrorPage{
		Page:     a.page("エラー", a.capStatus(r.Context()), xapi.EndpointRecent),
		Heading:  heading,
		Message:  message,
		Steps:    steps,
		Status:   http.StatusOK,
		Messages: msgs,
		FormURL:  backURL,
	}
	a.render(w, problemStatus(probs), "error.html", p)
}

// followersProblemText は、フォロワー取得で data が無く errors だけが返ったときの本文と対処手順を返す。
// 分類には画面のステータスを決める problemStatus をそのまま使い、ステータスと本文が食い違わないようにする。
func followersProblemText(probs []xapi.ProblemDetail) (message string, steps []string) {
	const noCharge = "ユーザーが返っていないため、課金されない見込みです。"
	switch problemStatus(probs) {
	case http.StatusForbidden:
		// 鍵アカウントのフォロワーを App-only の Bearer Token で読んだときに返る想定（未実測）。
		return "X API がこのアカウントのフォロワーを読む権限が無いと返しました。鍵アカウントのフォロワーは、" +
				"App-only の Bearer Token では取得できない見込みです。" + noCharge,
			followersErrorSteps(xapi.KindForbidden)
	case http.StatusNotFound:
		return "X API が対象のアカウントを見つけられませんでした。アカウントが凍結・削除されたか、" +
				"URL のユーザー ID が誤っている可能性があります。" + noCharge,
			followersErrorSteps(xapi.KindNotFound)
	default:
		return "X API がフォロワーを返さず、エラーだけを返しました（HTTP 200）。" + noCharge,
			[]string{
				"技術的な詳細で、X API が返したエラーの内容を確認してください。",
				"時間をおいて、見積りからやり直してください。",
			}
	}
}

// problemStatus は data が無く errors だけが返ったときの、この画面のステータスを決める。
// 読む権限が無い（鍵アカウントなど）→ 403、見つからない（存在しない・凍結）→ 404、それ以外 → 502。
//
// 両方が混ざっていたら 403 を優先する。権限が無いと言われたアカウントは存在しているので、
// 「見つからない」より原因として具体的なため。errors が空（data も errors も無い想定外の応答）は 502。
func problemStatus(probs []xapi.ProblemDetail) int {
	for _, pe := range probs {
		if pe.IsNotAuthorized() {
			return http.StatusForbidden
		}
	}
	for _, pe := range probs {
		if pe.IsNotFound() {
			return http.StatusNotFound
		}
	}
	return http.StatusBadGateway
}
