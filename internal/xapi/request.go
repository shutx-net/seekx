package xapi

// request.go は「どのエンドポイントを叩くか」と「何を要求するか」を定義する。
//
// Endpoint と SearchParams は X API のリクエストそのものを表すため、
// クエリ組み立て（internal/query）ではなくここに置く。こうすることで
// internal/xapi を依存グラフの葉に保てる（internal/query が xapi を参照する向き）。
// ユーザー検索・フォロワー取得の UserLookupParams / FollowersParams も同じ理由でここに置く。

import "time"

// Endpoint は検索エンドポイントの種別。
type Endpoint string

const (
	EndpointRecent Endpoint = "recent"
	EndpointAll    Endpoint = "all"
)

// SearchParams は検索リクエストのパラメータ。internal/query が組み立て、Client が送信する。
type SearchParams struct {
	Query           string
	MaxResults      int
	StartTime       time.Time
	EndTime         time.Time
	SinceID         string
	UntilID         string
	PaginationToken string
	PostFields      []string
	Expansions      []string
	UserFields      []string
	MediaFields     []string
	Endpoint        Endpoint
}

// リクエストするフィールド一覧。post.fields と expansions を混同しない（P-EXPANSIONS-VS-FIELDS）。
// フィールドセレクタは post.fields に統一する。旧名は使わない（D-10 / P-FIELDS-MIX）。
var (
	PostFieldsRequested = []string{
		"created_at", "text", "lang", "public_metrics", "possibly_sensitive",
		"conversation_id", "attachments", "note_post", "reply_settings",
	}
	ExpansionsDefault   = []string{"author_id", "attachments.media_keys"}
	ExpansionsWithRefs  = []string{"author_id", "attachments.media_keys", "referenced_posts"}
	UserFieldsRequested = []string{
		"name", "username", "profile_image_url", "verified", "verified_type",
	}
	MediaFieldsRequested = []string{
		"type", "url", "preview_image_url", "width", "height", "alt_text",
		"duration_ms", "variants",
	}
)

// フォロワー取得（GET /2/users/:id/followers）の境界値。
// API の制約なのでここに置き、入力検証（internal/query）はこの値から範囲と文言を組む
// （同じ数値を 2 か所に持たない）。
const (
	// max_results の範囲（docs）。既定の人数はここでは決めない（画面の方針なので呼び出し側が持つ）。
	FollowersMaxResultsMin = 1
	FollowersMaxResultsMax = 1000

	// PaginationTokenMinLen は pagination_token の最短長（OpenAPI 定義の minLength。未実測）。
	// トークンは URL に載ってユーザーが書き換えられるので、明らかに短い値は送る前に弾けるようにする。
	PaginationTokenMinLen = 16
)

// UserProfileFieldsRequested はユーザー検索とフォロワー取得で要求する user.fields。
//
// 課金は返ったユーザーの人数単位でフィールド数に依らないので、ユーザーカードと CSV に出す項目を
// まとめて要求する。検索の UserFieldsRequested（投稿者の表示に要る 5 項目）とは別に持ち、
// 検索の要求は変えない。
var UserProfileFieldsRequested = []string{
	"created_at", "description", "location", "name", "profile_image_url",
	"protected", "public_metrics", "url", "username", "verified", "verified_type",
}

// UserLookupParams はユーザー検索（GET /2/users/by/username/:username）のパラメータ。
// internal/query が組み立て、Client が送信する。
//
// 【Expansions を持たせない】ピン留め投稿（pinned_post_id。旧 pinned_tweet_id）などを展開すると
// 投稿が includes に入り、post read として課金され得る。フィールドが無ければ付けようがないので、
// D-22（フィクスチャモードでは HTTP クライアントを作らない）と同じく課金経路を型で塞ぐ。
type UserLookupParams struct {
	Username   string // 先頭の @ を除いた値。形式の検証は internal/query の責務
	UserFields []string
}

// FollowersParams はフォロワー取得（GET /2/users/:id/followers）のパラメータ。
// internal/query が組み立て、Client が送信する。
//
// 【Expansions を持たせない】理由は UserLookupParams と同じ。
type FollowersParams struct {
	UserID          string // ユーザー検索の data.id（数字のみ）
	MaxResults      int
	PaginationToken string // 空なら最初のページ
	UserFields      []string
}
