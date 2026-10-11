package view

// users.go はユーザー検索・フォロワー取得のレスポンスから、ユーザーカードとフォロワー一覧の
// ViewModel を組み立てる。制約は view.go の冒頭と同じ（internal/query・internal/cost・HTTP・
// テンプレートのパッケージを import しない、API を呼ばない、time.Now() を呼ばない）。

import (
	"fmt"
	"strings"
	"time"

	"github.com/shutx-net/seekx/internal/xapi"
)

// ViewUser はユーザーカード 1 枚分の表示モデル。フォロワー一覧のカードと見積り画面の対象ユーザーの
// 両方で使い、CSV の 1 行もここから作る（FollowersCSVFile）。
//
// 表示名・自己紹介・場所は API の値を加工せずに持つ（エスケープはテンプレートの責務、
// 数式の無害化は CSV の責務）。
type ViewUser struct {
	ID               string
	Name             string // 表示名。API の値が空のときは Username（@ は付けない）
	Username         string // @ は付けずに保持し、テンプレート側で @ を出す
	ProfileURL       string // https://x.com/<username>。Username が空なら空
	ProfileImageURL  string // 解像度置換済み（userAvatarSize）
	Description      string // 自己紹介（改行を含み得る）
	Location         string
	URL              string // プロフィールの Web サイト。http:// か https:// で始まるものだけ（それ以外は空）
	Verified         bool
	VerifiedType     string
	Protected        bool   // 鍵アカウント
	CreatedAtDisplay string // アカウント作成日。例 2015-06-07（表示タイムゾーン）
	CreatedAtISO     string // <time datetime> と CSV 用の RFC 3339（UTC）
	Metrics          ViewUserMetrics
}

// ViewUserMetrics はユーザーの public_metrics。
// HasMetrics が false のときは「不明」で、値（すべて 0）を表示に使わない。public_metrics が
// 欠落したユーザーを「フォロワー 0 人」と表示しないため（投稿の D-49 と同じ考え方）。
type ViewUserMetrics struct {
	Followers, Following, Posts, Listed int
	HasMetrics                          bool
}

// FollowersView はフォロワー一覧 1 ページ分の ViewModel。
//
// 取得の条件（対象のユーザー名・ID・人数）と月次 cap はここでは受け取らない。ViewOptions のコメントと
// 同じ方針で、テンプレートはそれらをページ側（cmd/seekx）の型から読むため、view が知る理由が無い。
type FollowersView struct {
	Users         []ViewUser // data の順序のまま
	ResultCount   int        // len(Users)。meta.result_count ではない（BuildFollowersView を参照）
	NextToken     string
	PrevToken     string
	SpentUSD      float64 // 今回のリクエストで発生した推定コスト
	RateLimit     *xapi.RateLimit
	Warnings      []string
	PartialErrors []string
	FixtureMode   bool
}

// userAvatarSize はユーザーカードのプロフィール画像の解像度（profileImageURL に渡す値）。
//
// 投稿カードは 400x400 だが、フォロワー一覧は 1 ページに最大 1,000 枚並ぶので転送量を抑える。
// 表示は 48px なので、200x200 なら 2x 以上の高解像度の画面でも足りる。abs.twimg.com の既定アイコン
// （default_profile_normal.png）にも _200x200 の画像があることは実測済み。
const userAvatarSize = "200x200"

// BuildUserView は xapi.User をユーザーカードの表示モデルにする。
// loc は作成日の表示に使うタイムゾーンで、nil なら UTC にする（Time.In(nil) は panic するため）。
func BuildUserView(u xapi.User, loc *time.Location) ViewUser {
	if loc == nil {
		loc = time.UTC
	}
	vu := ViewUser{
		ID:              u.ID,
		Name:            u.Name,
		Username:        u.Username,
		ProfileURL:      profileURL(u.Username),
		ProfileImageURL: profileImageURL(u.ProfileImageURL, userAvatarSize),
		Description:     u.Description,
		Location:        u.Location,
		URL:             websiteURL(u.URL),
		Verified:        u.Verified,
		VerifiedType:    u.VerifiedType,
		Protected:       u.Protected,
	}
	// 表示名が空だと、カードの見出しも画像の alt（「<表示名> のプロフィール画像」）も空になる。
	if vu.Name == "" {
		vu.Name = u.Username
	}
	if !u.CreatedAt.IsZero() {
		vu.CreatedAtDisplay = u.CreatedAt.In(loc).Format("2006-01-02")
		// API と同じ UTC にそろえる。表示タイムゾーンの設定で CSV の中身が変わらないようにする。
		vu.CreatedAtISO = u.CreatedAt.UTC().Format(time.RFC3339)
	}
	if pm := u.PublicMetrics; pm != nil {
		vu.Metrics = ViewUserMetrics{
			Followers:  pm.FollowersCount,
			Following:  pm.FollowingCount,
			Posts:      pm.Posts(), // tweet_count / post_count のどちらで返っても拾う
			Listed:     pm.ListedCount,
			HasMetrics: true,
		}
	}
	return vu
}

// websiteURL はプロフィールの Web サイトのうち、http:// か https:// で始まるものだけを返す
// （それ以外は空）。
//
// カードの href と CSV の両方に出す値なので、javascript: などのスキームは手前で捨てる。
// html/template も危険なスキームを #ZgotmplZ に置き換えるが、それではカードに壊れたリンクが出るうえ、
// テンプレートを通らない CSV には効かない。判定は前方一致の許可リストにして単純に保つ
// （大文字の HTTPS:// なども捨てる側に倒れるだけで、危険な値を通すことはない）。
func websiteURL(raw string) string {
	if strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://") {
		return raw
	}
	return ""
}

// BuildFollowersView はフォロワー取得のレスポンスを 1 ページ分の ViewModel にする。
// resp が nil でも落ちない（opt から受け取る値だけが入る）。
func BuildFollowersView(resp *xapi.FollowersResponse, opt ViewOptions) FollowersView {
	out := FollowersView{
		SpentUSD:    opt.SpentUSD,
		RateLimit:   opt.RateLimit,
		FixtureMode: opt.FixtureMode,
	}
	if resp == nil {
		return out
	}
	log := opt.logger()
	loc := opt.location()

	// 件数はカードの枚数（＝ CSV の行数・課金される人数）にそろえる。meta と食い違っても
	// 画面に出すのは data の人数なので、ログに残すだけにする。
	out.ResultCount = len(resp.Data)
	if resp.Meta.ResultCount != len(resp.Data) {
		log.Warn("meta.result_count と data の人数が一致しません", "result_count", resp.Meta.ResultCount, "users", len(resp.Data))
	}
	out.NextToken = resp.Meta.NextToken
	out.PrevToken = resp.Meta.PreviousToken

	for _, pe := range resp.Errors {
		out.PartialErrors = append(out.PartialErrors, pe.Describe())
	}

	// resp.Data の順序をそのまま維持する（並べ替えない）。
	missingMetrics := 0
	out.Users = make([]ViewUser, 0, len(resp.Data))
	for _, u := range resp.Data {
		vu := BuildUserView(u, loc)
		if !vu.Metrics.HasMetrics {
			missingMetrics++
		}
		out.Users = append(out.Users, vu)
	}
	if missingMetrics > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d 人のフォロワー数などの指標を取得できませんでした。", missingMetrics))
	}
	return out
}
