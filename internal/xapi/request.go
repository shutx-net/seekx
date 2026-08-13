package xapi

// request.go は「どのエンドポイントを叩くか」と「何を要求するか」を定義する。
//
// Endpoint と SearchParams は X API のリクエストそのものを表すため、
// クエリ組み立て（internal/query）ではなくここに置く。こうすることで
// internal/xapi を依存グラフの葉に保てる（internal/query が xapi を参照する向き）。

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
