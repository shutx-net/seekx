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
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/shutx-net/seekx/internal/cost"
	"github.com/shutx-net/seekx/internal/query"
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

// handleFollowers はフォロワー取得の画面（GET /followers）。
//
// 見積り（mode=estimate・ユーザー検索）と取得（mode=execute・フォロワー一覧）は次以降のフェーズで足す。
// それまでは mode に関わらずフォームを出す（API は呼ばない＝課金なし）。
func (a *App) handleFollowers(w http.ResponseWriter, r *http.Request) {
	f := parseFollowersForm(r.URL.Query())
	a.renderFollowersForm(w, r, f, nil, http.StatusOK)
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
