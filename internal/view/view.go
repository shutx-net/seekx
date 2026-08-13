package view

// package view は X API のレスポンスからテンプレートに渡す ViewModel を組み立てる。
//
// 制約（file_layout）:
//   - internal/query と internal/cost を import しない（純粋な表示モデルに保つ）
//   - HTTP パッケージを import しない（純粋関数に保ち、テストをネットワーク非依存にする）
//   - テンプレートパッケージを import しない（HTML の組み立てはテンプレートの責務）
//   - API を呼ばない
//   - time.Now() を直接呼ばない（現在時刻は ViewOptions.Now で受け取る）

import (
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shutx-net/seekx/internal/xapi"
)

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
	RateLimit     *xapi.RateLimit
	Warnings      []string
	PartialErrors []string
	FixtureMode   bool
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
//
// 検索条件（query.SearchForm）と月次 cap（cost.CapStatus）はここでは受け取らない。
// テンプレートは ResultsPage 側（cmd/seekx）からそれらを読むため、view が
// internal/query / internal/cost に依存する理由が無い。
type ViewOptions struct {
	Now         time.Time
	Loc         *time.Location
	RateLimit   *xapi.RateLimit
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
func joinIncludes(resp *xapi.SearchResponse) (usersByID map[string]xapi.User, mediaByKey map[string]xapi.Media, postsByID map[string]xapi.Post) {
	if resp == nil || resp.Includes == nil {
		return map[string]xapi.User{}, map[string]xapi.Media{}, map[string]xapi.Post{}
	}
	inc := resp.Includes
	usersByID = make(map[string]xapi.User, len(inc.Users))
	for _, u := range inc.Users {
		usersByID[u.ID] = u
	}
	mediaByKey = make(map[string]xapi.Media, len(inc.Media))
	for _, m := range inc.Media {
		mediaByKey[m.MediaKey] = m
	}
	postsByID = make(map[string]xapi.Post, len(inc.Posts))
	for _, p := range inc.Posts {
		postsByID[p.ID] = p
	}
	return usersByID, mediaByKey, postsByID
}

// UnknownAuthorName は includes.users に author_id が無かった場合の表示名。
// cmd/seekx のテンプレートテストが期待値として参照するため公開している。
const UnknownAuthorName = "（不明なアカウント）"

const unknownAuthorUsername = "unknown"

// BuildView は SearchResponse を ViewResult に変換する。
func BuildView(resp *xapi.SearchResponse, opt ViewOptions) ViewResult {
	log := opt.logger()
	loc := opt.location()

	out := ViewResult{
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
				Name:     UnknownAuthorName,
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
func postText(p xapi.Post) string {
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

func buildViewMedia(m xapi.Media, postID string, idx int) (ViewMedia, mediaNote) {
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
func selectVideoVariant(vs []xapi.Variant) (xapi.Variant, bool) {
	var best xapi.Variant
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
	return xapi.Variant{}, false
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
