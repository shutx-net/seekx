package query

// followers.go はフォロワー取得（ユーザー検索で見積り → フォロワー一覧を 1 ページずつ取得）の
// 入力検証と、xapi のリクエストパラメータの組み立てを行う。
//
// query.go と同じ制約を守る（HTTP パッケージと internal/config を import しない・API を呼ばない）。
// xapi からは UserLookupParams / FollowersParams という型と、境界値・要求する user.fields だけを
// 借りる。人数の範囲とトークンの最短長は API の事実なので xapi が持ち、ここではその値から
// 範囲と文言を組む（同じ数値を 2 か所に持たない）。
//
// ルール ID は投稿検索の V01〜V14 と混ざらないよう U（ユーザー）で始める。画面には
// ValidationErrors.Messages() を通して "[U02] …" の形で出る。
//
//   - U01: ユーザー名が空（見積り）
//   - U02: ユーザー名の形式（見積り・取得。取得ではユーザー名があるときだけ）
//   - U03: ユーザー ID の形式（取得）
//   - U04: 1 ページの人数の範囲（見積り・取得）
//   - U05: ページングトークンの形式（取得。空は最初のページ）

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/shutx-net/seekx/internal/xapi"
)

// FollowersPageSizeDefault は 1 ページの人数の既定値。
//
// フォームで未指定のときに cmd 側のパーサが入れる値で、検証と組み立てでは使わない
// （FollowersForm.MaxResults のコメントを参照）。投稿検索の取得件数の上限
// （defaultMaxResultsCap / SEEKX_MAX_RESULTS_CAP）とは連動させない。1 ページ最大 1,000 人を
// 選べることが要件で、連動させると環境変数を変えない限り 100 人までに絞られてしまうため。
const FollowersPageSizeDefault = 100

// ユーザー ID とページングトークンの形式の境界。
const (
	// userIDMaxDigits はユーザー ID の最大桁数。X の ID は 64 ビット符号付き整数に収まり、
	// その最大値（9223372036854775807）が 19 桁なので、20 桁以上は ID になり得ない。
	// 投稿検索の idRe（20 桁まで）より狭いが、正当な ID は 19 桁に収まるので弾くことはない。
	userIDMaxDigits = 19

	// paginationTokenMaxLen はページングトークンの最長。API の制約ではなく、URL に載せる値として
	// 常識的な長さで打ち切るためのもの（X のトークンは 16 文字前後）。
	paginationTokenMaxLen = 256
)

var (
	// userIDRe はユーザー ID の形式（ASCII の数字のみ）。
	userIDRe = regexp.MustCompile(fmt.Sprintf(`^[0-9]{1,%d}$`, userIDMaxDigits))

	// paginationTokenRe はページングトークンの形式。
	//
	// 文字種を英数字と _ - に絞るのは、パスやフィクスチャのファイル名に混ぜても安全にするため
	// （/ . % 空白などを通さない）。X の形式（大文字英数字 16 文字前後）よりは広く取り、
	// 将来の形式の変更で正当なトークンを弾かないようにする。下限は API の定義
	// （xapi.PaginationTokenMinLen）から組む。
	paginationTokenRe = regexp.MustCompile(fmt.Sprintf(`^[A-Za-z0-9_-]{%d,%d}$`,
		xapi.PaginationTokenMinLen, paginationTokenMaxLen))
)

// FollowersForm はフォロワー取得の画面（フォーム・見積り・一覧）が URL のクエリで受け取る入力。
// URL はユーザーが書き換えられるので、どの値も検証してから使う。
type FollowersForm struct {
	// Username は対象アカウントのユーザー名。先頭の @ / ＠ は付いたままでよい
	// （検証と組み立ての前に NormalizeFollowersUsername で落とす）。
	Username string

	// UserID はユーザー検索が返した data.id。見積り画面のリンクで渡る。
	UserID string

	// MaxResults は 1 ページの人数。xapi.FollowersMaxResultsMin〜Max（1〜1000）だけが有効。
	// 未指定のときの既定値（FollowersPageSizeDefault）は cmd 側のパーサが入れ、数として読めない値は
	// -1 で受ける。【検証で既定値に置き換えない】0 や読めない値を黙って既定値にすると、
	// 指定とは違う人数を取得して課金することになる。
	MaxResults int

	// PaginationToken は API が返した next_token / previous_token。空なら最初のページ。
	PaginationToken string
}

// NormalizeFollowersUsername は前後の空白を除き、先頭の @ か ＠ を 1 つだけ落とす。
//
// 全角の ＠ も落とすのは、日本語入力のまま打つと全角になるため（前後の全角空白も TrimSpace で除く）。
// 1 つだけにするのは、"@@nasa" のような入力を黙って直さず、U02 で入力の誤りとして見せるため。
// 大文字小文字は変えない（X のユーザー名は大文字小文字を区別しないので、そのまま送ってよい）。
// 投稿検索の normalizeUsername は検索の挙動を変えないよう、手を入れずに別に残す。
func NormalizeFollowersUsername(s string) string {
	s = strings.TrimSpace(s)
	for _, at := range [...]string{"@", "＠"} {
		if rest, ok := strings.CutPrefix(s, at); ok {
			return rest
		}
	}
	return s
}

// ValidateFollowersLookup は見積り（ユーザー検索）の入力を検証し、エラーを全件まとめて返す。
//
// 人数はユーザー検索には送らないが、見積り（1 ページの金額）と取得のリンクに使うので、
// 課金されるユーザー検索の前に確かめる。ID とトークンは見ない（見積りには使わず、
// 取得のリンクは API が返した ID から作り直す）。
func ValidateFollowersLookup(f FollowersForm) []ValidationError {
	var errs []ValidationError
	if u := NormalizeFollowersUsername(f.Username); u == "" {
		errs = append(errs, ValidationError{"U01", "username", "ユーザー名を入力してください。"})
	} else {
		errs = append(errs, followersUsernameErrors(u)...)
	}
	errs = append(errs, followersPageSizeErrors(f.MaxResults)...)
	return errs
}

// ValidateFollowersFetch はフォロワー一覧の取得の入力を検証し、エラーを全件まとめて返す。
//
// API に送るのは ID・人数・トークンで、ユーザー名は見出しと再見積りのリンクにだけ使う。
// そのためユーザー名は無くてもよく（@ だけのときも無いものとして扱う）、あるときだけ形式を
// 確かめる（書き換えられた値を見出しやリンクに出さない）。
// ID とトークンはリンクで渡る機械的な値なので、空白の除去などの補正はしない。確かめた値を
// BuildFollowersParams がそのまま送るので、検証した値と送る値がずれない。
func ValidateFollowersFetch(f FollowersForm) []ValidationError {
	var errs []ValidationError
	if u := NormalizeFollowersUsername(f.Username); u != "" {
		errs = append(errs, followersUsernameErrors(u)...)
	}
	if !userIDRe.MatchString(f.UserID) {
		errs = append(errs, ValidationError{"U03", "id",
			fmt.Sprintf("ユーザー ID が不正です（数字のみ・%d 桁以内）。見積り画面のリンクから開き直してください。", userIDMaxDigits)})
	}
	errs = append(errs, followersPageSizeErrors(f.MaxResults)...)
	if f.PaginationToken != "" && !paginationTokenRe.MatchString(f.PaginationToken) {
		errs = append(errs, ValidationError{"U05", "token", "ページングトークンが不正です。最初のページから取得し直してください。"})
	}
	return errs
}

// followersUsernameErrors は正規化済みで空でないユーザー名 u の形式を確かめる（U02）。
// 文字種と長さは投稿検索のアカウント名（V08）と同じ usernameRe で判定する。
// 文言には @ を落とした後の値を出す（何が不正かを、判定した値そのもので見せる）。
func followersUsernameErrors(u string) []ValidationError {
	if usernameRe.MatchString(u) {
		return nil
	}
	return []ValidationError{{"U02", "username",
		fmt.Sprintf("ユーザー名「%s」が不正です。半角英数字とアンダースコアのみ、15 文字以内です（先頭の @ は省略できます）。", u)}}
}

// followersPageSizeErrors は 1 ページの人数を確かめる（U04）。範囲と文言は xapi の境界値から組む。
// 投稿検索の V04 と違い 0 を「未指定」とみなさない（未指定の既定値はパーサが入れるので、
// ここに 0 が来たら入力の誤り）。
func followersPageSizeErrors(n int) []ValidationError {
	if n >= xapi.FollowersMaxResultsMin && n <= xapi.FollowersMaxResultsMax {
		return nil
	}
	return []ValidationError{{"U04", "max",
		fmt.Sprintf("1 ページの人数は %d〜%d の整数で指定してください。", xapi.FollowersMaxResultsMin, xapi.FollowersMaxResultsMax)}}
}

// BuildUserLookupParams はフォームからユーザー検索のパラメータを組み立てる。
// 検証（ValidateFollowersLookup）に失敗したら ValidationErrors を返す。
// ユーザー名は先頭の @ を落とした値を送る（xapi は @ の付いた値をパスに埋めずに弾く）。
//
// 【失敗時に途中まで埋めた値を返さない】ゼロ値なら、呼び出し側がエラーを見落としても空の
// ユーザー名はパスにできず、xapi が HTTP を呼ばずに弾く（課金されない）。
func BuildUserLookupParams(f FollowersForm) (xapi.UserLookupParams, error) {
	if errs := ValidateFollowersLookup(f); len(errs) > 0 {
		return xapi.UserLookupParams{}, ValidationErrors(errs)
	}
	return xapi.UserLookupParams{
		Username:   NormalizeFollowersUsername(f.Username),
		UserFields: xapi.UserProfileFieldsRequested,
	}, nil
}

// BuildFollowersParams はフォームからフォロワー取得のパラメータを組み立てる。
// 検証（ValidateFollowersFetch）に失敗したら ValidationErrors を返す。
// 人数とトークンは検証した値をそのまま渡す（既定値の補完や補正はしない）。
//
// 【失敗時に途中まで埋めた値を返さない】理由は BuildUserLookupParams と同じ（空の ID はパスにできない）。
func BuildFollowersParams(f FollowersForm) (xapi.FollowersParams, error) {
	if errs := ValidateFollowersFetch(f); len(errs) > 0 {
		return xapi.FollowersParams{}, ValidationErrors(errs)
	}
	return xapi.FollowersParams{
		UserID:          f.UserID,
		MaxResults:      f.MaxResults,
		PaginationToken: f.PaginationToken,
		UserFields:      xapi.UserProfileFieldsRequested,
	}, nil
}
