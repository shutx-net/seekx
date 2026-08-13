package query

// package query はフォーム入力から検索クエリ文字列とリクエストパラメータを生成する。
//
// 制約（file_layout）:
//   - HTTP パッケージを import しない
//   - API を呼ばない（xapi.Endpoint / xapi.SearchParams という型だけを借りる）
//   - internal/config を import しない（既定値は下の defaultMaxResultsCap）
//   - 現在時刻を直接取得しない（now を引数で受け取る）
//   - エラーメッセージは UI にそのまま出す日本語

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shutx-net/seekx/internal/xapi"
)

// defaultMaxResultsCap は Validate / BuildParams（cap 未指定版）が使う既定の UI 上限。
// config.LoadConfig が SEEKX_MAX_RESULTS_CAP に使う既定値と同じ値でなければならない。
// internal/config を import すると query が純粋でなくなるため、値を複製している。
const defaultMaxResultsCap = 100

// クエリ長の上限（rune 数。バイト数ではない・D-30 / R-05 / R-15）。
const (
	QueryLimitRecent = 512
	QueryLimitAll    = 1024
)

// full-archive の遡及下限。
var fullArchiveFloor = time.Date(2006, 3, 21, 0, 0, 0, 0, time.UTC)

// recent の遡及上限（7 日）。
const recentWindow = 7 * 24 * time.Hour

// end_time は now-30s にクランプする（A-10 / V07）。
const endTimeSafetyMargin = 30 * time.Second

// SearchForm は UI の入力そのまま。
type SearchForm struct {
	Keywords        string // 生のクエリ断片（演算子・括弧をそのまま通す・D-31）
	ExcludeWords    string // 空白区切り。各語に - を付ける
	OrWords         string // 空白区切り。(a OR b) に組む
	ExactPhrase     string // " " で囲む
	Hashtag         string // # は任意（無ければ付ける）
	FromUser        string
	ToUser          string
	MentionUser     string
	RetweetsOf      string
	ListID          string
	ReplyMode       string // "" | "only" | "exclude"
	ConversationID  string
	InReplyToPostID string
	HasImages       bool
	HasVideo        bool
	HasMedia        bool
	HasLinks        bool
	Lang            string // "" | ja | en | ...
	MinLikes        int
	MinReplies      int
	MinReposts      int
	IncludeReposts  bool // false なら -is:retweet を付与
	StartLocal      string
	EndLocal        string
	Endpoint        string // "recent" | "all"
	MaxResults      int
	PaginationToken string
}

// OperatorSpec は演算子インベントリの 1 項目。
type OperatorSpec struct {
	Operator string
	Example  string
	JP       string
}

// StandaloneOperators は単独で検索が成立する演算子。
var StandaloneOperators = []OperatorSpec{
	{"keyword", "猫", "単純キーワード。形態素単位でマッチ。"},
	{`"exact phrase"`, `"機械学習 入門"`, "完全一致フレーズ。"},
	{"#hashtag", "#東京", "ハッシュタグ。"},
	{"$cashtag", "$TSLA", "キャッシュタグ（銘柄）。"},
	{"@user", "@x", "本文中のメンション。"},
	{"from:", "from:x", "指定アカウントの投稿。"},
	{"to:", "to:x", "指定アカウントへの返信。"},
	{"retweets_of:", "retweets_of:x", "指定アカウントの投稿をリポストしたもの。"},
	{"url:", "url:example.com", "展開後 URL に含まれる文字列。"},
	{"conversation_id:", "conversation_id:1234567890", "スレッド全体。"},
	{"in_reply_to_tweet_id:", "in_reply_to_tweet_id:1234567890", "指定投稿への直接の返信。"},
	{"retweets_of_tweet_id:", "retweets_of_tweet_id:1234567890", "指定投稿のリポスト。"},
	{"quotes_of_tweet_id:", "quotes_of_tweet_id:1234567890", "指定投稿の引用。"},
	{"list:", "list:123456", "リストメンバーの投稿。"},
	{"min_likes:", "min_likes:100", "いいね数の下限。"},
	{"min_replies:", "min_replies:10", "返信数の下限。"},
	{"min_reposts:", "min_reposts:50", "リポスト数の下限。"},
}

// ConjunctionRequiredOperators は単独では HTTP 400 になる演算子（P-CONJ-400 / DR-08）。
var ConjunctionRequiredOperators = []OperatorSpec{
	{"is:retweet", "is:retweet", "リポストのみ。"},
	{"is:reply", "is:reply", "返信のみ。"},
	{"is:quote", "is:quote", "引用のみ。"},
	{"is:verified", "is:verified", "認証済みアカウントのみ。"},
	{"-is:nullcast", "-is:nullcast", "プロモ専用投稿を除外。"},
	{"has:hashtags", "has:hashtags", "ハッシュタグを含む。"},
	{"has:cashtags", "has:cashtags", "キャッシュタグを含む。"},
	{"has:links", "has:links", "リンクを含む。"},
	{"has:mentions", "has:mentions", "メンションを含む。"},
	{"has:media", "has:media", "画像・動画・GIF のいずれかを含む。"},
	{"has:images", "has:images", "画像を含む。"},
	{"has:video_link", "has:video_link", "動画を含む。"},
	{"has:geo", "has:geo", "位置情報付き。"},
	{"lang:", "lang:ja", "言語。"},
}

// ValidationError は 1 件のバリデーション違反。UI にそのまま表示する。
type ValidationError struct {
	RuleID  string
	Field   string
	Message string
}

// Warning はエラーではない注意（クランプなど）。
type Warning struct {
	RuleID  string
	Message string
}

// ValidationErrors は複数のバリデーション違反をまとめた error。
type ValidationErrors []ValidationError

func (v ValidationErrors) Error() string {
	msgs := make([]string, 0, len(v))
	for _, e := range v {
		msgs = append(msgs, e.Message)
	}
	return strings.Join(msgs, " / ")
}

// Messages は UI 表示用のメッセージ一覧を返す。
func (v ValidationErrors) Messages() []string {
	msgs := make([]string, 0, len(v))
	for _, e := range v {
		msgs = append(msgs, fmt.Sprintf("[%s] %s", e.RuleID, e.Message))
	}
	return msgs
}

// QueryLength は rune 数とバイト数を返す。上限判定に使うのは runes のみ（D-30）。
func QueryLength(q string) (runes int, bytes int) {
	return utf8.RuneCountInString(q), len(q)
}

// QueryLimitFor はエンドポイントごとの rune 上限を返す。
func QueryLimitFor(e xapi.Endpoint) int {
	if e == xapi.EndpointAll {
		return QueryLimitAll
	}
	return QueryLimitRecent
}

// EndpointName は画面表示用の名前を返す。
func EndpointName(e xapi.Endpoint) string {
	if e == xapi.EndpointAll {
		return "全期間"
	}
	return "直近7日"
}

// EndpointOf はフォームの文字列を Endpoint に変換する（不正値はそのまま保持して V14 で弾く）。
func EndpointOf(s string) xapi.Endpoint {
	if s == "" {
		return xapi.EndpointRecent
	}
	return xapi.Endpoint(s)
}

var (
	usernameRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)
	idRe       = regexp.MustCompile(`^[0-9]{1,20}$`)
)

// normalizeUsername は先頭の @ を除去して trim する。
func normalizeUsername(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "@")
}

// normalizeHashtag は先頭に # を付ける（既に付いていればそのまま）。
func normalizeHashtag(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "#") || strings.HasPrefix(s, "＃") {
		return s
	}
	return "#" + s
}

// splitWords は空白区切りの入力を語に分割する（" で囲まれた語は 1 つとして扱う）。
func splitWords(s string) []string {
	return tokenizeQuery(s)
}

// quoteIfNeeded は空白を含む語を " で囲む。
func quoteIfNeeded(s string) string {
	if strings.ContainsAny(s, " \t") && !strings.HasPrefix(s, `"`) {
		return `"` + s + `"`
	}
	return s
}

// BuildQuery はフォームから検索クエリ文字列を組み立てる（build_algorithm のとおり）。
func BuildQuery(f SearchForm) (string, []Warning, error) {
	q, warns := buildQueryString(f)
	var errs ValidationErrors
	errs = append(errs, validateQueryString(q, EndpointOf(f.Endpoint))...)
	if len(errs) > 0 {
		return q, warns, errs
	}
	return q, warns, nil
}

// buildQueryString は検証を行わずにクエリ文字列だけを組み立てる。
func buildQueryString(f SearchForm) (string, []Warning) {
	var terms []string
	var warns []Warning

	add := func(s string) {
		if s != "" {
			terms = append(terms, s)
		}
	}

	// 1. 完全一致フレーズ
	if p := strings.TrimSpace(f.ExactPhrase); p != "" {
		if strings.HasPrefix(p, `"`) && strings.HasSuffix(p, `"`) && len(p) >= 2 {
			add(p)
		} else {
			add(`"` + p + `"`)
		}
	}

	// 2. キーワード（生のクエリ断片としてそのまま通す・D-31）
	if kw := strings.TrimSpace(f.Keywords); kw != "" {
		add(kw)
		for _, t := range tokenizeQuery(kw) {
			if t == "or" {
				warns = append(warns, Warning{
					RuleID:  "V12",
					Message: "OR は大文字でないと論理和として扱われません（小文字の or は単語として検索されます）。",
				})
				break
			}
		}
	}

	// 3. 除外語
	for _, w := range splitWords(f.ExcludeWords) {
		if w == "" {
			continue
		}
		add("-" + quoteIfNeeded(w))
	}

	// 4. OR 群（2 語以上のときのみ括弧で囲む。AND が OR より強く結合するため括弧は必須）
	orWords := splitWords(f.OrWords)
	switch len(orWords) {
	case 0:
	case 1:
		add(quoteIfNeeded(orWords[0]))
	default:
		quoted := make([]string, 0, len(orWords))
		for _, w := range orWords {
			quoted = append(quoted, quoteIfNeeded(w))
		}
		add("(" + strings.Join(quoted, " OR ") + ")")
	}

	// 5. ハッシュタグ・アカウント系
	add(normalizeHashtag(f.Hashtag))
	if u := normalizeUsername(f.FromUser); u != "" {
		add("from:" + u)
	}
	if u := normalizeUsername(f.ToUser); u != "" {
		add("to:" + u)
	}
	if u := normalizeUsername(f.MentionUser); u != "" {
		add("@" + u)
	}
	retweetsOf := normalizeUsername(f.RetweetsOf)
	if retweetsOf != "" {
		add("retweets_of:" + retweetsOf)
	}
	if id := strings.TrimSpace(f.ListID); id != "" {
		add("list:" + id)
	}
	if id := strings.TrimSpace(f.ConversationID); id != "" {
		add("conversation_id:" + id)
	}
	if id := strings.TrimSpace(f.InReplyToPostID); id != "" {
		add("in_reply_to_tweet_id:" + id)
	}

	// 6. 反応数の下限（0 のときは付与しない）
	if f.MinLikes > 0 {
		add("min_likes:" + itoa(f.MinLikes))
	}
	if f.MinReplies > 0 {
		add("min_replies:" + itoa(f.MinReplies))
	}
	if f.MinReposts > 0 {
		add("min_reposts:" + itoa(f.MinReposts))
	}

	// 7. 返信・メディア・言語（すべて conjunction-required）
	switch f.ReplyMode {
	case "only":
		add("is:reply")
	case "exclude":
		add("-is:reply")
	}
	if f.HasImages {
		add("has:images")
	}
	if f.HasVideo {
		add("has:video_link")
	}
	if f.HasMedia {
		add("has:media")
	}
	if f.HasLinks {
		add("has:links")
	}
	if lang := strings.TrimSpace(f.Lang); lang != "" {
		add("lang:" + lang)
	}

	// 8. -is:retweet（既定で付与。retweets_of: があるときは付与しない・D-11）
	if !f.IncludeReposts && retweetsOf == "" {
		add("-is:retweet")
	}

	return strings.Join(terms, " "), warns
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// tokenizeQuery はクエリ文字列を項に分割する。
// " で囲まれた範囲、( ) のグループ、[ ] の範囲は 1 つの項として扱う。
func tokenizeQuery(q string) []string {
	var terms []string
	var cur strings.Builder
	inQuote := false
	depthParen := 0
	depthBracket := 0

	flush := func() {
		if cur.Len() > 0 {
			terms = append(terms, cur.String())
			cur.Reset()
		}
	}

	for _, r := range q {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case inQuote:
			cur.WriteRune(r)
		case r == '(':
			depthParen++
			cur.WriteRune(r)
		case r == ')':
			if depthParen > 0 {
				depthParen--
			}
			cur.WriteRune(r)
		case r == '[':
			depthBracket++
			cur.WriteRune(r)
		case r == ']':
			if depthBracket > 0 {
				depthBracket--
			}
			cur.WriteRune(r)
		case (r == ' ' || r == '\t' || r == '\n' || r == '\r') && depthParen == 0 && depthBracket == 0:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return terms
}

// isConjunctionRequiredTerm は項が conjunction-required 演算子かを判定する。
func isConjunctionRequiredTerm(t string) bool {
	l := strings.ToLower(t)
	return strings.HasPrefix(l, "is:") || strings.HasPrefix(l, "has:") || strings.HasPrefix(l, "lang:")
}

// HasStandaloneTerm はクエリに「否定でない standalone 項」が 1 つ以上あるかを判定する（V01）。
// conjunction-required 同士の組み合わせ（has:images lang:ja）は false になる（DR-08）。
func HasStandaloneTerm(q string) bool {
	for _, t := range tokenizeQuery(q) {
		if termIsStandalone(t) {
			return true
		}
	}
	return false
}

func termIsStandalone(t string) bool {
	if t == "" {
		return false
	}
	// 否定形は standalone とみなさない（安全側）。
	if strings.HasPrefix(t, "-") {
		return false
	}
	if t == "OR" || t == "or" || t == "AND" || t == "and" {
		return false
	}
	if isConjunctionRequiredTerm(t) {
		return false
	}
	// グループは中身で判定する。
	if strings.HasPrefix(t, "(") {
		inner := strings.TrimSuffix(strings.TrimPrefix(t, "("), ")")
		if inner == t {
			return false
		}
		for _, it := range tokenizeQuery(inner) {
			if termIsStandalone(it) {
				return true
			}
		}
		return false
	}
	// 引用符のみ・記号のみは項として数えない。
	if strings.Trim(t, `"'`) == "" {
		return false
	}
	return true
}

// validateQueryString は生成済みクエリ文字列そのものに対する検証（V01 / V02 / V13）。
func validateQueryString(q string, ep xapi.Endpoint) []ValidationError {
	var errs []ValidationError
	if strings.TrimSpace(q) == "" {
		errs = append(errs, ValidationError{"V13", "kw", "検索条件が入力されていません。"})
		return errs
	}
	if !HasStandaloneTerm(q) {
		errs = append(errs, ValidationError{"V01", "kw",
			"「画像付き」「返信のみ」「言語」などの条件だけでは検索できません（X API の仕様）。キーワード、アカウント（from: など）、ハッシュタグのいずれかを 1 つ以上指定してください。"})
	}
	runes, _ := QueryLength(q)
	limit := QueryLimitFor(ep)
	if runes > limit {
		errs = append(errs, ValidationError{"V02", "kw",
			fmt.Sprintf("検索クエリが長すぎます（現在 %d 文字 / 上限 %d 文字）。条件を減らしてください。", runes, limit)})
	}
	return errs
}

// Validate は全ルール（V01〜V14）を検証し、エラーを全件まとめて返す。
// now のタイムゾーンを入力（datetime-local）の解釈に使う（D-18）。
func Validate(f SearchForm, now time.Time) []ValidationError {
	return ValidateWithCap(f, now, defaultMaxResultsCap)
}

// ValidateWithCap は max_results の UI 上限（SEEKX_MAX_RESULTS_CAP）を指定できる Validate。
func ValidateWithCap(f SearchForm, now time.Time, maxResultsCap int) []ValidationError {
	var errs []ValidationError
	ep := EndpointOf(f.Endpoint)

	// V14: endpoint
	if ep != xapi.EndpointRecent && ep != xapi.EndpointAll {
		errs = append(errs, ValidationError{"V14", "endpoint", "検索範囲の指定が不正です。"})
	}

	// V08: username
	for _, fieldName := range []struct {
		field string
		value string
		label string
	}{
		{"from", f.FromUser, "このアカウントの投稿"},
		{"to", f.ToUser, "このアカウントへの返信"},
		{"mention", f.MentionUser, "このアカウントへのメンション"},
		{"retweets_of", f.RetweetsOf, "このアカウントのリポスト"},
	} {
		u := normalizeUsername(fieldName.value)
		if u == "" {
			continue
		}
		if !usernameRe.MatchString(u) {
			errs = append(errs, ValidationError{"V08", fieldName.field,
				fmt.Sprintf("アカウント名「%s」が不正です。半角英数字とアンダースコアのみ、15 文字以内です。", u)})
		}
	}

	// V09: ID 系
	for _, idf := range []struct {
		field string
		value string
	}{
		{"list", f.ListID},
		{"conv", f.ConversationID},
		{"in_reply_to", f.InReplyToPostID},
	} {
		v := strings.TrimSpace(idf.value)
		if v == "" {
			continue
		}
		if !idRe.MatchString(v) {
			errs = append(errs, ValidationError{"V09", idf.field, "ID は数字のみで入力してください。"})
		}
	}

	// V10: 反応数の下限
	for _, mf := range []struct {
		field string
		value int
	}{
		{"min_likes", f.MinLikes},
		{"min_replies", f.MinReplies},
		{"min_reposts", f.MinReposts},
	} {
		if mf.value < 0 {
			errs = append(errs, ValidationError{"V10", mf.field, "反応数の下限は 0 以上で指定してください。"})
		}
	}

	// V11: 括弧の対応
	if !parensBalanced(f.Keywords) {
		errs = append(errs, ValidationError{"V11", "kw", "キーワード欄の括弧の対応が取れていません。"})
	}

	// V04: max_results
	epMax := 100
	if ep == xapi.EndpointAll {
		epMax = 500
	}
	if maxResultsCap > 0 && maxResultsCap < epMax {
		epMax = maxResultsCap
	}
	if f.MaxResults != 0 && (f.MaxResults < 10 || f.MaxResults > epMax) {
		errs = append(errs, ValidationError{"V04", "max",
			fmt.Sprintf("取得件数は 10〜%d の範囲で指定してください。", epMax)})
	}

	// V03 / V05 / V06 / V07: 期間
	_, _, _, timeErrs := resolveTimeRange(f, now)
	errs = append(errs, timeErrs...)

	// V01 / V02 / V13: 生成クエリ
	q, _ := buildQueryString(f)
	errs = append(errs, validateQueryString(q, ep)...)

	return errs
}

func parensBalanced(s string) bool {
	depth := 0
	inQuote := false
	for _, r := range s {
		switch r {
		case '"':
			inQuote = !inQuote
		case '(':
			if !inQuote {
				depth++
			}
		case ')':
			if !inQuote {
				depth--
				if depth < 0 {
					return false
				}
			}
		}
	}
	return depth == 0
}

// datetime-local の受理形式（ブラウザによって秒が付くことがある）。
var localTimeLayouts = []string{
	"2006-01-02T15:04",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

func parseLocalTime(s string, loc *time.Location) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range localTimeLayouts {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// resolveTimeRange は JST 入力を UTC に変換し、クランプと期間の検証を行う（D-18 / P-TIMEZONE）。
func resolveTimeRange(f SearchForm, now time.Time) (start, end time.Time, warns []Warning, errs []ValidationError) {
	loc := now.Location()
	if loc == nil {
		loc = time.UTC
	}
	ep := EndpointOf(f.Endpoint)

	if s := strings.TrimSpace(f.StartLocal); s != "" {
		t, ok := parseLocalTime(s, loc)
		if !ok {
			errs = append(errs, ValidationError{"V06", "start", "開始日時の形式が不正です。"})
		} else {
			start = t.UTC()
		}
	}
	if s := strings.TrimSpace(f.EndLocal); s != "" {
		t, ok := parseLocalTime(s, loc)
		if !ok {
			errs = append(errs, ValidationError{"V06", "end", "終了日時の形式が不正です。"})
		} else {
			end = t.UTC()
		}
	}

	// V07: end_time を now-30s にクランプする（エラーではなく警告）。
	limit := now.UTC().Add(-endTimeSafetyMargin)
	if !end.IsZero() && end.After(limit) {
		end = limit
		warns = append(warns, Warning{"V07", "終了日時を現在時刻の 30 秒前に調整しました（X API の制約）。"})
	}

	// V06: start < end
	if !start.IsZero() && !end.IsZero() && !start.Before(end) {
		errs = append(errs, ValidationError{"V06", "start", "開始日時は終了日時より前にしてください。"})
	}

	// V05: recent は 7 日以内
	if ep == xapi.EndpointRecent && !start.IsZero() {
		floor := now.UTC().Add(-recentWindow).Add(1 * time.Minute)
		if start.Before(floor) {
			errs = append(errs, ValidationError{"V05", "start",
				"「直近7日」検索では 7 日より前の日時を指定できません。「全期間」検索に切り替えてください。"})
		}
	}

	// full-archive の遡及下限（2006-03-21）
	if ep == xapi.EndpointAll && !start.IsZero() && start.Before(fullArchiveFloor) {
		errs = append(errs, ValidationError{"V05", "start",
			"「全期間」検索でも 2006 年 3 月 21 日より前は指定できません。"})
	}

	// V03: 排他（UI からは since_id / until_id を出さないため防御的チェック）
	if !start.IsZero() && strings.TrimSpace(f.PaginationToken) != "" && false {
		_ = start // 到達しない。V03 は BuildParams 側で SearchParams に対して検証する。
	}

	return start, end, warns, errs
}

// BuildParams はフォームから SearchParams を組み立てる。検証に失敗した場合は API を呼ばずにエラーを返す。
func BuildParams(f SearchForm, now time.Time) (xapi.SearchParams, []Warning, error) {
	return BuildParamsWithCap(f, now, defaultMaxResultsCap)
}

// BuildParamsWithCap は max_results の UI 上限を指定できる BuildParams。
func BuildParamsWithCap(f SearchForm, now time.Time, maxResultsCap int) (xapi.SearchParams, []Warning, error) {
	q, warns := buildQueryString(f)
	start, end, timeWarns, _ := resolveTimeRange(f, now)
	warns = append(warns, timeWarns...)

	if errs := ValidateWithCap(f, now, maxResultsCap); len(errs) > 0 {
		return xapi.SearchParams{Query: q, Endpoint: EndpointOf(f.Endpoint)}, warns, ValidationErrors(errs)
	}

	maxResults := f.MaxResults
	if maxResults == 0 {
		maxResults = 10
	}

	exps := xapi.ExpansionsDefault
	if f.IncludeReposts {
		exps = xapi.ExpansionsWithRefs // RT/引用の元投稿を表示するため（D-20）
	}

	p := xapi.SearchParams{
		Query:           q,
		MaxResults:      maxResults,
		StartTime:       start,
		EndTime:         end,
		PaginationToken: strings.TrimSpace(f.PaginationToken),
		PostFields:      xapi.PostFieldsRequested,
		Expansions:      exps,
		UserFields:      xapi.UserFieldsRequested,
		MediaFields:     xapi.MediaFieldsRequested,
		Endpoint:        EndpointOf(f.Endpoint),
	}

	// V03: 排他パラメータの防御的チェック。
	if err := checkMutuallyExclusive(p); err != nil {
		return p, warns, err
	}
	return p, warns, nil
}

func checkMutuallyExclusive(p xapi.SearchParams) error {
	var errs ValidationErrors
	if !p.StartTime.IsZero() && p.SinceID != "" {
		errs = append(errs, ValidationError{"V03", "start", "開始日時と since_id は同時に指定できません。"})
	}
	if !p.EndTime.IsZero() && p.UntilID != "" {
		errs = append(errs, ValidationError{"V03", "end", "終了日時と until_id は同時に指定できません。"})
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// CountsGranularity は期間からバケット粒度を決める（2 日以内は hour、それ以上は day）。
func CountsGranularity(start, end time.Time, now time.Time) string {
	s, e := start, end
	if e.IsZero() {
		e = now.UTC()
	}
	if s.IsZero() {
		s = e.Add(-recentWindow)
	}
	if e.Sub(s) <= 48*time.Hour {
		return "hour"
	}
	return "day"
}
