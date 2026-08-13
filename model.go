package main

// model.go は X API v2 のレスポンス型と、テンプレートに渡す ViewModel の組み立てを担う。
//
// 制約（file_layout）:
//   - HTTP パッケージを import しない（純粋関数に保ち、テストをネットワーク非依存にする）
//   - テンプレートパッケージを import しない（HTML の組み立てはテンプレートの責務）
//   - API を呼ばない
//   - time.Now() を直接呼ばない（現在時刻は ViewOptions.Now で受け取る）

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// API レスポンス型（JSON タグは検証済みの実レスポンスに厳密に一致させる）
// ---------------------------------------------------------------------------

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

// Meta はページングと差分取得。newest_id / oldest_id は docs に無いが実際に返る（DR-07）。
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

// ---------------------------------------------------------------------------
// ViewModel（テンプレートに渡す唯一の型）
// ---------------------------------------------------------------------------

type ViewResult struct {
	Posts         []ViewPost
	ResultCount   int
	QueryString   string // 生成された query 全文（画面表示用）
	QueryRunes    int    // 上限判定に使う値（recent 512 / all 1024）
	QueryBytes    int    // 参考表示のみ。判定には使わない（D-30）
	QueryLimit    int    // 該当エンドポイントの rune 上限
	EndpointName  string // 「直近7日」/「全期間」
	MaxResults    int
	NextToken     string
	PrevToken     string
	NewestID      string
	OldestID      string
	SpentUSD      float64 // 今回のリクエストで発生した推定コスト
	SessionUSD    float64
	Cap           CapStatus
	RateLimit     *RateLimit
	Warnings      []string
	PartialErrors []string
	FixtureMode   bool
	Form          SearchForm // 再表示・ページングリンク再構築用
	StartDisplay  string
	EndDisplay    string
}

type ViewPost struct {
	ID               string
	PermalinkURL     string
	Text             string // NotePost.Text があればそちら（未加工）
	CreatedAtDisplay string // 例 2026-08-13 09:04（JST）
	CreatedAtISO     string // <time datetime> 用の RFC 3339
	Author           ViewAuthor
	Metrics          ViewMetrics
	Media            []ViewMedia
	GridMedia        []ViewMedia // 先頭 4 枚（グリッド描画用）
	GridClass        string      // grid-1 | grid-2 | grid-3 | grid-4
	OverflowCount    int         // 5 枚以上のときの +N
	IsReply          bool
	IsRepost         bool
	IsQuote          bool
	Sensitive        bool
	Lang             string

	// 一覧の絞り込み用に Go 側で算出するキー（D-37）。テンプレートは data-f-* にそのまま出す。
	FilterTextRunes  int    // 表示している本文の rune 数（note_post 優先後の Text を数える）
	FilterMediaCount int    // len(Media)。GridMedia（先頭 4 枚）ではなく総数
	FilterLikes      int    // いいね数。Metrics.HasMetrics が false のときは -1（不明）
	FilterFlags      string // 空白区切りトークン。順序固定: reply repost quote sensitive media photo video gif link
}

type ViewAuthor struct {
	Name            string
	Username        string // @ は付けずに保持し、テンプレート側で @ を出す
	ProfileURL      string
	ProfileImageURL string // 解像度置換済み
	Verified        bool
	VerifiedType    string
	Missing         bool // includes に存在しなかった場合 true
}

type ViewMetrics struct {
	Replies, Reposts, Likes, Quotes, Bookmarks, Impressions int
	HasMetrics                                              bool
}

type ViewMedia struct {
	Kind        string // photo | video | gif
	ThumbURL    string
	FullURL     string
	PosterURL   string
	VideoURL    string
	AspectRatio string // 参考表示用 "1200 / 675"
	// AspectRatioCSS は style="aspect-ratio:..." に埋める値。小数で持つ。
	// html/template の CSS フィルタは "1200 / 675" のようなスラッシュを含む値を
	// ZgotmplZ に置換して捨てるため（実測確認済み）、W/H を小数化して渡す。
	// template.CSS で escaping を迂回するより、フィルタを素通りする形にする方が安全。
	AspectRatioCSS string
	Alt            string
	Width          int
	Height         int
	Duration       string // 例 0:47
	DialogID       string
	PlaybackNote   string
}

// ViewOptions は BuildView の入力。time.Now() を関数内で呼ばないための注入口。
type ViewOptions struct {
	Now         time.Time
	Loc         *time.Location
	Form        SearchForm
	Cap         CapStatus
	RateLimit   *RateLimit
	SpentUSD    float64
	SessionUSD  float64
	FixtureMode bool
	Log         *slog.Logger // nil 可（その場合は破棄）
}

func (o ViewOptions) logger() *slog.Logger {
	if o.Log != nil {
		return o.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (o ViewOptions) location() *time.Location {
	if o.Loc != nil {
		return o.Loc
	}
	return time.UTC
}

// ---------------------------------------------------------------------------
// includes の結合
// ---------------------------------------------------------------------------

// joinIncludes は includes を O(len(includes)) でマップ化する。
// 線形探索のネストループは禁止（100 posts × 240 media で無駄が出る）。
func joinIncludes(resp *SearchResponse) (usersByID map[string]User, mediaByKey map[string]Media, postsByID map[string]Post) {
	if resp == nil || resp.Includes == nil {
		return map[string]User{}, map[string]Media{}, map[string]Post{}
	}
	inc := resp.Includes
	usersByID = make(map[string]User, len(inc.Users))
	for _, u := range inc.Users {
		usersByID[u.ID] = u
	}
	mediaByKey = make(map[string]Media, len(inc.Media))
	for _, m := range inc.Media {
		mediaByKey[m.MediaKey] = m
	}
	postsByID = make(map[string]Post, len(inc.Posts))
	for _, p := range inc.Posts {
		postsByID[p.ID] = p
	}
	return usersByID, mediaByKey, postsByID
}

const (
	unknownAuthorName     = "（不明なアカウント）"
	unknownAuthorUsername = "unknown"
)

// BuildView は SearchResponse を ViewResult に変換する。
func BuildView(resp *SearchResponse, opt ViewOptions) ViewResult {
	log := opt.logger()
	loc := opt.location()

	out := ViewResult{
		Form:        opt.Form,
		Cap:         opt.Cap,
		RateLimit:   opt.RateLimit,
		SpentUSD:    opt.SpentUSD,
		SessionUSD:  opt.SessionUSD,
		FixtureMode: opt.FixtureMode,
	}
	if resp == nil {
		return out
	}

	usersByID, mediaByKey, _ := joinIncludes(resp)

	out.ResultCount = resp.Meta.ResultCount
	out.NextToken = resp.Meta.NextToken
	out.PrevToken = resp.Meta.PreviousToken
	out.NewestID = resp.Meta.NewestID
	out.OldestID = resp.Meta.OldestID

	for _, pe := range resp.Errors {
		out.PartialErrors = append(out.PartialErrors, pe.Describe())
	}

	missingAuthors := 0
	missingMedia := 0
	hlsOnly := 0
	unknownKinds := 0

	// resp.Data の順序をそのまま維持する（API は概ね新しい順で返す。並べ替えはしない）。
	out.Posts = make([]ViewPost, 0, len(resp.Data))
	for _, p := range resp.Data {
		vp := ViewPost{
			ID:        p.ID,
			Text:      postText(p),
			Sensitive: p.PossiblySensitive,
			Lang:      p.Lang,
		}
		if !p.CreatedAt.IsZero() {
			vp.CreatedAtDisplay = p.CreatedAt.In(loc).Format("2006-01-02 15:04")
			vp.CreatedAtISO = p.CreatedAt.Format(time.RFC3339)
		}

		// 投稿者の解決。見つからない場合もカードを空欄にしたり投稿を捨てたりしない（P-INCLUDES-MISSING）。
		if u, ok := usersByID[p.AuthorID]; ok {
			vp.Author = ViewAuthor{
				Name:            u.Name,
				Username:        u.Username,
				ProfileURL:      profileURL(u.Username),
				ProfileImageURL: profileImageURL(u.ProfileImageURL, "400x400"),
				Verified:        u.Verified,
				VerifiedType:    u.VerifiedType,
			}
		} else {
			missingAuthors++
			log.Warn("includes.users に author_id が存在しません", "post_id", p.ID, "author_id", p.AuthorID)
			vp.Author = ViewAuthor{
				Name:     unknownAuthorName,
				Username: unknownAuthorUsername,
				Missing:  true,
			}
		}
		vp.PermalinkURL = permalink(vp.Author.Username, p.ID)
		if vp.Author.Missing {
			vp.PermalinkURL = permalink("", p.ID)
		}

		if p.PublicMetrics != nil {
			vp.Metrics = ViewMetrics{
				Replies:     p.PublicMetrics.ReplyCount,
				Reposts:     p.PublicMetrics.Reposts(),
				Likes:       p.PublicMetrics.LikeCount,
				Quotes:      p.PublicMetrics.QuoteCount,
				Bookmarks:   p.PublicMetrics.BookmarkCount,
				Impressions: p.PublicMetrics.ImpressionCount,
				HasMetrics:  true,
			}
		}

		for _, ref := range p.ReferencedPosts {
			switch ref.Type {
			case "retweeted":
				vp.IsRepost = true
			case "quoted":
				vp.IsQuote = true
			case "replied_to":
				vp.IsReply = true
			}
		}

		// メディアの解決。宣言順（media_keys の順）を保持する。
		if p.Attachments != nil {
			for i, key := range p.Attachments.MediaKeys {
				m, ok := mediaByKey[key]
				if !ok {
					missingMedia++
					continue // 地域制限・削除で欠けることがある。黙ってスキップする。
				}
				vm, note := buildViewMedia(m, p.ID, i)
				switch note {
				case mediaNoteHLSOnly:
					hlsOnly++
				case mediaNoteUnknownKind:
					unknownKinds++
					log.Warn("未知の media type", "post_id", p.ID, "media_key", m.MediaKey, "type", m.Type)
					continue
				}
				vp.Media = append(vp.Media, vm)
			}
		}
		// alt_text が無いメディアには説明的な代替文を生成する（accessibility_and_polish）。
		for i := range vp.Media {
			if vp.Media[i].Alt == "" {
				vp.Media[i].Alt = fmt.Sprintf("%s の投稿のメディア %d/%d", vp.Author.Name, i+1, len(vp.Media))
			}
		}

		vp.GridClass = gridClass(len(vp.Media))
		vp.GridMedia = vp.Media
		if len(vp.Media) > 4 {
			vp.GridMedia = vp.Media[:4]
			vp.OverflowCount = len(vp.Media) - 4
		}

		// 一覧の絞り込みキー（D-37）。Media が確定した後で算出する。
		vp.FilterTextRunes = utf8.RuneCountInString(vp.Text)
		vp.FilterMediaCount = len(vp.Media)
		vp.FilterLikes = -1 // 不明（public_metrics 欠落）と 0 件を区別する（D-49）
		if vp.Metrics.HasMetrics {
			vp.FilterLikes = vp.Metrics.Likes
		}
		vp.FilterFlags = filterFlags(vp)

		out.Posts = append(out.Posts, vp)
	}

	if missingAuthors > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d 件の投稿で投稿者情報が取得できませんでした（削除済み・凍結・非公開アカウントの可能性）。", missingAuthors))
	}
	if missingMedia > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d 件のメディアが取得できませんでした（地域制限・削除の可能性）。", missingMedia))
	}
	if hlsOnly > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d 件の動画はブラウザで直接再生できません（HLS のみ提供）。X で開いてください。", hlsOnly))
	}
	if unknownKinds > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d 件の未知の種類のメディアを表示できませんでした。", unknownKinds))
	}
	return out
}

// postText は本文を返す。長文投稿は note_post.text を優先する（D-25）。
func postText(p Post) string {
	if p.NotePost != nil && p.NotePost.Text != "" {
		return p.NotePost.Text
	}
	return p.Text
}

type mediaNote int

const (
	mediaNoteNone mediaNote = iota
	mediaNoteHLSOnly
	mediaNoteUnknownKind
)

func buildViewMedia(m Media, postID string, idx int) (ViewMedia, mediaNote) {
	vm := ViewMedia{
		Alt:      m.AltText,
		Width:    m.Width,
		Height:   m.Height,
		DialogID: fmt.Sprintf("dlg-%s-%d", postID, idx),
	}
	if m.Width > 0 && m.Height > 0 {
		vm.AspectRatio = fmt.Sprintf("%d / %d", m.Width, m.Height)
		vm.AspectRatioCSS = strconv.FormatFloat(float64(m.Width)/float64(m.Height), 'f', 4, 64)
	}

	switch m.Type {
	case "photo":
		vm.Kind = "photo"
		// photo は url を使う。preview_image_url は null（P-PHOTO-PREVIEW-NULL）。
		vm.ThumbURL = sizedMediaURL(m.URL, "small")
		vm.FullURL = sizedMediaURL(m.URL, "orig")
		return vm, mediaNoteNone
	case "video", "animated_gif":
		if m.Type == "video" {
			vm.Kind = "video"
		} else {
			vm.Kind = "gif"
		}
		vm.PosterURL = m.PreviewImageURL
		vm.ThumbURL = m.PreviewImageURL
		vm.FullURL = m.PreviewImageURL
		if m.DurationMS > 0 {
			vm.Duration = formatDuration(m.DurationMS)
		}
		if v, ok := selectVideoVariant(m.Variants); ok {
			vm.VideoURL = v.URL
			return vm, mediaNoteNone
		}
		vm.PlaybackNote = "この動画はブラウザで直接再生できません（HLS のみ提供）。"
		return vm, mediaNoteHLSOnly
	default:
		return vm, mediaNoteUnknownKind
	}
}

// formatDuration は duration_ms を m:ss に整形する。
func formatDuration(ms int) string {
	total := ms / 1000
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

// selectVideoVariant は再生に使う mp4 variant を選ぶ。
// m3u8（application/x-mpegURL）は常に除外する。
func selectVideoVariant(vs []Variant) (Variant, bool) {
	var best Variant
	found := false
	for _, v := range vs {
		if !strings.EqualFold(v.ContentType, "video/mp4") {
			continue
		}
		if !found || v.BitRate > best.BitRate {
			best, found = v, true // bit_rate 同値なら先に現れたものを維持（決定的）
		}
	}
	if found {
		return best, true
	}
	// content_type が欠落している場合の保険: 拡張子で判定する。
	for _, v := range vs {
		if strings.Contains(strings.ToLower(v.ContentType), "mpegurl") {
			continue
		}
		if u := strings.ToLower(v.URL); strings.HasSuffix(u, ".mp4") || strings.Contains(u, ".mp4?") {
			return v, true
		}
	}
	return Variant{}, false
}

// sizedMediaURL は画像のサイズ変種 URL を作る。
// ?format=jpg&name=small|orig は公式仕様ではなく慣例なので、この 1 関数に閉じ込める（R-11 / D-27）。
// name が空なら素の rawURL を返す（フォールバック経路）。
func sizedMediaURL(rawURL, name string) string {
	if rawURL == "" || name == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set("format", "jpg")
	q.Set("name", name)
	u.RawQuery = q.Encode()
	return u.String()
}

// profileImageURL は _normal を指定サイズに置換する（v1.1 由来の慣例・A-09）。
// パターンに一致しない場合は raw をそのまま返す。
func profileImageURL(raw, size string) string {
	if raw == "" || size == "" {
		return raw
	}
	i := strings.LastIndex(raw, "_normal")
	if i < 0 {
		return raw
	}
	rest := raw[i+len("_normal"):]
	// _normal の直後は拡張子（.jpg 等）でなければならない。
	if rest != "" && !strings.HasPrefix(rest, ".") {
		return raw
	}
	return raw[:i] + "_" + size + rest
}

// permalink は投稿の恒久リンクを返す（A-08）。
func permalink(username, id string) string {
	if id == "" {
		return ""
	}
	if username == "" || username == unknownAuthorUsername {
		return "https://x.com/i/status/" + id
	}
	return "https://x.com/" + username + "/status/" + id
}

// profileURL はプロフィールページの URL を返す。
func profileURL(username string) string {
	if username == "" || username == unknownAuthorUsername {
		return ""
	}
	return "https://x.com/" + username
}

// gridClass はメディア枚数に対応する CSS クラス名を返す。
func gridClass(n int) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return "grid-1"
	case n == 2:
		return "grid-2"
	case n == 3:
		return "grid-3"
	default:
		return "grid-4"
	}
}

// filterFlags は一覧の絞り込み用トークン列を返す（D-48）。
// トークンの出力順は reply repost quote sensitive media photo video gif link に固定する
// （テストで期待値を書けるようにするため）。何も立たない場合は空文字列。
// bool を個別の data 属性に出すと "true" / "false" という文字列になるため、
// Go 側で 1 本のトークン列に集約している（P-TEMPLATE-BOOL）。
func filterFlags(p ViewPost) string {
	var toks []string
	if p.IsReply {
		toks = append(toks, "reply")
	}
	if p.IsRepost {
		toks = append(toks, "repost")
	}
	if p.IsQuote {
		toks = append(toks, "quote")
	}
	if p.Sensitive {
		toks = append(toks, "sensitive")
	}
	if len(p.Media) > 0 {
		toks = append(toks, "media")
	}
	var photo, video, gif bool
	for _, m := range p.Media {
		switch m.Kind {
		case "photo":
			photo = true
		case "video":
			video = true
		case "gif":
			gif = true
		}
	}
	if photo {
		toks = append(toks, "photo")
	}
	if video {
		toks = append(toks, "video")
	}
	if gif {
		toks = append(toks, "gif")
	}
	if hasLinkInText(p.Text) {
		toks = append(toks, "link")
	}
	return strings.Join(toks, " ")
}

// hasLinkInText は本文に URL が含まれるかを返す（D-50）。
// X の本文中の URL は t.co に短縮されて https://t.co/... の形で入るため、
// スキーム文字列の検出で足りる。entities は取得しない（親計画 D-20）。
func hasLinkInText(s string) bool {
	return strings.Contains(s, "https://") || strings.Contains(s, "http://")
}
