package xapi

// types.go は X API v2 のレスポンス型を定義する。
//
// 制約（file_layout）:
//   - 他の internal パッケージを import しない（このパッケージは依存グラフの葉である）
//   - HTML やテンプレートに触らない
//   - JSON タグは検証済みの実レスポンスに厳密に一致させる

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RateLimit は x-rate-limit-* ヘッダの内容。ヘッダ欠落時は nil を使う。
type RateLimit struct {
	Limit     int
	Remaining int
	Reset     time.Time
}

// ResetDisplay はリセット時刻を指定タイムゾーンで整形する。
func (r *RateLimit) ResetDisplay(loc *time.Location) string {
	if r == nil || r.Reset.IsZero() {
		return ""
	}
	if loc == nil {
		loc = time.UTC
	}
	return r.Reset.In(loc).Format("15:04:05")
}

// SearchResponse は search/recent と search/all 共通のレスポンス。
type SearchResponse struct {
	Data     []Post          `json:"data"`
	Includes *Includes       `json:"includes,omitempty"`
	Meta     Meta            `json:"meta"`
	Errors   []ProblemDetail `json:"errors,omitempty"` // 200 での部分成功エラー
}

// Post は投稿 1 件。data[] と includes.posts[] の両方で使う。
// Source フィールドは定義しない（post.fields の enum にあるが返却されない・DR-09）。
type Post struct {
	ID                 string           `json:"id"`
	Text               string           `json:"text"`
	CreatedAt          time.Time        `json:"created_at"`
	AuthorID           string           `json:"author_id"` // expansions=author_id で付与される
	ConversationID     string           `json:"conversation_id"`
	InReplyToUserID    string           `json:"in_reply_to_user_id"` // v1 では要求しない（D-20）
	Lang               string           `json:"lang"`
	PossiblySensitive  bool             `json:"possibly_sensitive"`
	ReplySettings      string           `json:"reply_settings"`
	EditHistoryPostIDs []string         `json:"edit_history_post_ids"` // 旧 edit_history_tweet_ids
	Attachments        *Attachments     `json:"attachments,omitempty"`
	NotePost           *NotePost        `json:"note_post,omitempty"` // 旧 note_tweet
	PublicMetrics      *PostMetrics     `json:"public_metrics,omitempty"`
	ReferencedPosts    []ReferencedPost `json:"referenced_posts,omitempty"` // 旧 referenced_tweets
}

// Attachments の media_keys の順序が表示順になる。順序を保持すること。
type Attachments struct {
	MediaKeys []string `json:"media_keys"`
	PollIDs   []string `json:"poll_ids"`
}

// NotePost は 280 文字超の長文投稿の全文（D-25 / P-NOTE-POST-TRUNCATION）。
type NotePost struct {
	Text string `json:"text"`
}

// PostMetrics は public_metrics。
//
// 実 API（2026-08-13 実測）が返すのは repost_count であり、retweet_count ではない。
// A-05 は「レガシー形のまま」と仮定していたが誤りで、post 系リネームは public_metrics にも
// 及んでいた（R-03 の解決）。retweet_count のまま実装すると値が常に 0 になり、
// エラーにならないため気付けない。
// 互換のため両方のフィールドを持ち、Reposts() で非ゼロの方を採用する
// （tweet.fields のレガシー互換モードでは retweet_count が返る可能性がある）。
type PostMetrics struct {
	RepostCount     int `json:"repost_count"`  // 実 API が返す現行の名前（検証済み）
	RetweetCount    int `json:"retweet_count"` // レガシー名。現行 API では返らない
	ReplyCount      int `json:"reply_count"`
	LikeCount       int `json:"like_count"`
	QuoteCount      int `json:"quote_count"`
	BookmarkCount   int `json:"bookmark_count"`
	ImpressionCount int `json:"impression_count"`
}

// Reposts はリポスト数を返す。repost_count を優先し、無ければ retweet_count を使う。
func (m PostMetrics) Reposts() int {
	if m.RepostCount != 0 {
		return m.RepostCount
	}
	return m.RetweetCount
}

// ReferencedPost は RT / 引用 / リプライの判定に使う。
type ReferencedPost struct {
	Type string `json:"type"` // retweeted | quoted | replied_to
	ID   string `json:"id"`
}

// Includes は expansions の展開結果。places / polls / topics は使わない。
type Includes struct {
	Users  []User            `json:"users"`
	Media  []Media           `json:"media"`
	Posts  []Post            `json:"posts"` // 旧 includes.tweets
	Places []json.RawMessage `json:"places"`
	Polls  []json.RawMessage `json:"polls"`
	Topics []json.RawMessage `json:"topics"`
}

// User は投稿者。表示要件（名前・@username・プロフィール画像）を満たすため必須。
type User struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Username        string `json:"username"`
	ProfileImageURL string `json:"profile_image_url"`
	Verified        bool   `json:"verified"`
	VerifiedType    string `json:"verified_type"`
}

// Media は media_key で join する。
// photo の preview_image_url は null（P-PHOTO-PREVIEW-NULL）。video/gif には url が無い。
type Media struct {
	MediaKey        string    `json:"media_key"`
	Type            string    `json:"type"` // photo | video | animated_gif
	URL             string    `json:"url"`
	PreviewImageURL string    `json:"preview_image_url"`
	Width           int       `json:"width"`
	Height          int       `json:"height"`
	AltText         string    `json:"alt_text"`
	DurationMS      int       `json:"duration_ms"`
	Variants        []Variant `json:"variants"`
}

// Variant は動画の画質バリエーション。
// JSON タグは bit_rate（v1.1 の bitrate ではない・P-VARIANT-BITRATE）。
type Variant struct {
	BitRate     int    `json:"bit_rate"`
	ContentType string `json:"content_type"`
	URL         string `json:"url"`
}

// Meta はページング。newest_id / oldest_id は docs に無いが実際に返る（DR-07）。
// この 2 つは UI では使っていない。API についての事実として型に残してある
// （このパッケージは X API の実際の挙動を記録する場所）。
type Meta struct {
	ResultCount   int    `json:"result_count"`
	NextToken     string `json:"next_token"`
	PreviousToken string `json:"previous_token"`
	NewestID      string `json:"newest_id"`
	OldestID      string `json:"oldest_id"`
}

// CountsResponse は件数見積り。
type CountsResponse struct {
	Data   []CountBucket   `json:"data"`
	Meta   CountsMeta      `json:"meta"`
	Errors []ProblemDetail `json:"errors,omitempty"`
}

// CountBucket の JSON タグは tweet_count。docs の post_count は誤り（DR-01 / P-COUNTS-NAMES）。
// Go のフィールド名を PostCount にすることでドキュメントとの差異を意図的に可視化する。
type CountBucket struct {
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	PostCount int       `json:"tweet_count"`
}

// CountsMeta の JSON タグは total_tweet_count。docs の total_post_count は誤り（DR-01）。
type CountsMeta struct {
	TotalPostCount int    `json:"total_tweet_count"`
	NextToken      string `json:"next_token"`
}

// UsageResponse は月次 cap の消費状況。project_cap は文字列で返る（R-04）。
type UsageResponse struct {
	Data struct {
		ProjectID    string  `json:"project_id"`
		ProjectCap   flexInt `json:"project_cap"`
		ProjectUsage flexInt `json:"project_usage"`
		CapResetDay  int     `json:"cap_reset_day"`
	} `json:"data"`
}

// APIProblem は 4xx / 5xx の problem-details 形式エラー本文。
type APIProblem struct {
	Title  string          `json:"title"`
	Detail string          `json:"detail"`
	Type   string          `json:"type"`
	Status int             `json:"status"`
	Reason string          `json:"reason"` // 403 の client-not-enrolled 等
	Errors []ProblemDetail `json:"errors"`
}

// ProblemDetail は 200 の部分成功エラーと 4xx のパラメータエラーの両方の形を受ける。
type ProblemDetail struct {
	Message      string              `json:"message"`
	Code         int                 `json:"code"` // 429 は 88
	Parameters   map[string][]string `json:"parameters"`
	Title        string              `json:"title"`
	Detail       string              `json:"detail"`
	Type         string              `json:"type"`
	ResourceType string              `json:"resource_type"`
	Parameter    string              `json:"parameter"`
	Value        json.RawMessage     `json:"value"`
}

// Describe は UI 表示・ログ用の 1 行説明を返す（トークンや URL は含まない）。
func (p ProblemDetail) Describe() string {
	switch {
	case p.Detail != "":
		return p.Detail
	case p.Message != "":
		return p.Message
	case p.Title != "":
		return p.Title
	default:
		return "詳細不明のエラー"
	}
}

// flexInt は JSON の文字列でも数値でも int として受け取る（R-04 への保険）。
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("flexInt: %q を整数として解釈できません", s)
	}
	*f = flexInt(n)
	return nil
}

func (f flexInt) Int() int { return int(f) }
