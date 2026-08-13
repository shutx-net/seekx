package query

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/shutx-net/seekx/internal/xapi"
)

// TestBuildQuery は query_builder.worked_examples の 9 例をそのままテストケースにする。
func TestBuildQuery(t *testing.T) {
	cases := []struct {
		name    string
		form    SearchForm
		want    string
		wantErr bool
		errRule string
	}{
		{
			name:    "worked_example 1: 画像付きチェックのみ → V01",
			form:    SearchForm{HasImages: true},
			want:    "has:images -is:retweet",
			wantErr: true,
			errRule: "V01",
		},
		{
			name: "worked_example 2: キーワード=猫、画像付き=ON、言語=ja",
			form: SearchForm{Keywords: "猫", HasImages: true, Lang: "ja"},
			want: "猫 has:images lang:ja -is:retweet",
		},
		{
			name: "worked_example 3: from=nasa、画像付き=ON",
			form: SearchForm{FromUser: "nasa", HasImages: true},
			want: "from:nasa has:images -is:retweet",
		},
		{
			name: "worked_example 4: OR 群=猫 犬、画像付き=ON",
			form: SearchForm{OrWords: "猫 犬", HasImages: true},
			want: "(猫 OR 犬) has:images -is:retweet",
		},
		{
			name: "worked_example 5: キーワード=選挙、返信のみ、除外語=速報",
			form: SearchForm{Keywords: "選挙", ExcludeWords: "速報", ReplyMode: "only"},
			want: "選挙 -速報 is:reply -is:retweet",
		},
		{
			name: "worked_example 6: retweets_of=nasa、リポストを含める=OFF",
			form: SearchForm{RetweetsOf: "nasa"},
			want: "retweets_of:nasa",
		},
		{
			name: "worked_example 7: conversation_id + 返信のみ",
			form: SearchForm{ConversationID: "1234567890", ReplyMode: "only"},
			want: "conversation_id:1234567890 is:reply -is:retweet",
		},
		{
			name: "worked_example 8: キーワード=AI、min_likes=1000",
			form: SearchForm{Keywords: "AI", MinLikes: 1000},
			want: "AI min_likes:1000 -is:retweet",
		},
		{
			name: "worked_example 9: min_likes=1000 のみ（standalone 可）",
			form: SearchForm{MinLikes: 1000},
			want: "min_likes:1000 -is:retweet",
		},
		{
			name: "OR 群が 1 語なら括弧を作らない",
			form: SearchForm{OrWords: "猫"},
			want: "猫 -is:retweet",
		},
		{
			name: "除外語に空白を含む場合は引用符で囲む",
			form: SearchForm{Keywords: "選挙", ExcludeWords: `"速報 ニュース" 号外`},
			want: `選挙 -"速報 ニュース" -号外 -is:retweet`,
		},
		{
			name: "リポストを含める=ON なら -is:retweet を付けない",
			form: SearchForm{Keywords: "猫", IncludeReposts: true},
			want: "猫",
		},
		{
			name: "retweets_of とリポストを含める=ON の組み合わせ",
			form: SearchForm{RetweetsOf: "nasa", IncludeReposts: true},
			want: "retweets_of:nasa",
		},
		{
			name: "完全一致フレーズは引用符で囲む",
			form: SearchForm{ExactPhrase: "機械学習 入門"},
			want: `"機械学習 入門" -is:retweet`,
		},
		{
			name: "既に引用符付きのフレーズは二重に囲まない",
			form: SearchForm{ExactPhrase: `"機械学習 入門"`},
			want: `"機械学習 入門" -is:retweet`,
		},
		{
			name: "ハッシュタグの # は自動付与",
			form: SearchForm{Hashtag: "東京"},
			want: "#東京 -is:retweet",
		},
		{
			name: "ハッシュタグに # が付いていればそのまま",
			form: SearchForm{Hashtag: "#東京"},
			want: "#東京 -is:retweet",
		},
		{
			name: "@ 付きのアカウント名は正規化される",
			form: SearchForm{FromUser: "@nasa", ToUser: " @spacex ", MentionUser: "@x"},
			want: "from:nasa to:spacex @x -is:retweet",
		},
		{
			name: "返信を除く",
			form: SearchForm{Keywords: "選挙", ReplyMode: "exclude"},
			want: "選挙 -is:reply -is:retweet",
		},
		{
			name: "メディア系すべて",
			form: SearchForm{Keywords: "猫", HasImages: true, HasVideo: true, HasMedia: true, HasLinks: true},
			want: "猫 has:images has:video_link has:media has:links -is:retweet",
		},
		{
			name: "反応数の下限 3 種（0 は付与しない）",
			form: SearchForm{Keywords: "猫", MinLikes: 10, MinReplies: 0, MinReposts: 5},
			want: "猫 min_likes:10 min_reposts:5 -is:retweet",
		},
		{
			name: "キーワードに生の演算子を書ける（D-31）",
			form: SearchForm{Keywords: "(猫 OR 犬) -うさぎ"},
			want: "(猫 OR 犬) -うさぎ -is:retweet",
		},
		{
			name: "list / in_reply_to_tweet_id",
			form: SearchForm{ListID: "123456", InReplyToPostID: "1234567890"},
			want: "list:123456 in_reply_to_tweet_id:1234567890 -is:retweet",
		},
		{
			name:    "何も入力しない → V13/V01",
			form:    SearchForm{IncludeReposts: true},
			want:    "",
			wantErr: true,
			errRule: "V13",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := BuildQuery(tc.form)
			if got != tc.want {
				t.Errorf("クエリが違う:\n got  %q\n want %q", got, tc.want)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatal("エラーになるべき")
				}
				verrs, ok := err.(ValidationErrors)
				if !ok {
					t.Fatalf("ValidationErrors であるべき: %T", err)
				}
				found := false
				for _, e := range verrs {
					if e.RuleID == tc.errRule {
						found = true
					}
				}
				if !found {
					t.Errorf("ルール %s のエラーが必要: %v", tc.errRule, verrs.Messages())
				}
				return
			}
			if err != nil {
				t.Fatalf("予期しないエラー: %v", err)
			}
			// 連続スペース・前後スペースを作らない。
			if strings.Contains(got, "  ") || got != strings.TrimSpace(got) {
				t.Errorf("空白の扱いが不正: %q", got)
			}
		})
	}
}

func TestHasStandaloneTerm(t *testing.T) {
	cases := []struct {
		q    string
		want bool
	}{
		{"has:images", false},
		{"lang:ja", false},
		{"has:images lang:ja", false},
		{"has:images lang:ja -is:retweet", false},
		{"is:reply -is:retweet", false},
		{"猫", true},
		{"猫 has:images lang:ja -is:retweet", true},
		{"from:nasa has:images", true},
		{"-from:x", false},
		{"-from:x has:images", false},
		{"(猫 OR 犬) has:images", true},
		{"(has:images OR lang:ja)", false},
		{`"機械学習 入門" has:images`, true},
		{"#東京 lang:ja", true},
		{"$TSLA", true},
		{"@x has:links", true},
		{"min_likes:1000 -is:retweet", true},
		{"conversation_id:123 is:reply", true},
		{"", false},
		{"   ", false},
		{"-猫", false},
		{"OR", false},
		{"url:example.com has:links", true},
	}
	for _, tc := range cases {
		if got := HasStandaloneTerm(tc.q); got != tc.want {
			t.Errorf("HasStandaloneTerm(%q) = %v, want %v", tc.q, got, tc.want)
		}
	}
}

// japaneseQuery は「2 文字の日本語語をスペースで連結した」クエリを作る。
// 語数 k のとき rune 数 = 3k-1、バイト数 = 7k-1 になる（R-05 のプローブと同じ形）。
func japaneseQuery(words int) string {
	parts := make([]string, 0, words)
	for i := 0; i < words; i++ {
		parts = append(parts, "検索")
	}
	return strings.Join(parts, " ")
}

func TestQueryLength(t *testing.T) {
	t.Run("日本語ではバイト数が rune 数の約 3 倍になる", func(t *testing.T) {
		q := japaneseQuery(95)
		runes, bytes := QueryLength(q)
		if runes != 284 {
			t.Fatalf("rune 数: got %d, want 284", runes)
		}
		if bytes != 664 {
			t.Fatalf("バイト数: got %d, want 664", bytes)
		}
		if bytes <= runes*2 {
			t.Errorf("日本語ではバイト数が rune 数を大きく超えるはず: runes=%d bytes=%d", runes, bytes)
		}
	})

	t.Run("512 バイト超だが 512 rune 以下のクエリは検証を通過する（D-30 の要点）", func(t *testing.T) {
		q := japaneseQuery(95) // 284 rune / 664 byte
		runes, bytes := QueryLength(q)
		if bytes <= QueryLimitRecent {
			t.Fatalf("前提が崩れている: bytes=%d は 512 を超えているべき", bytes)
		}
		if runes > QueryLimitRecent {
			t.Fatalf("前提が崩れている: runes=%d は 512 以下であるべき", runes)
		}
		errs := validateQueryString(q, xapi.EndpointRecent)
		for _, e := range errs {
			if e.RuleID == "V02" {
				t.Fatalf("バイト基準で誤って拒否している（日本語クエリが約 170 文字で使えなくなる）: %s", e.Message)
			}
		}
	})

	t.Run("512 rune 超は V02 エラー", func(t *testing.T) {
		q := japaneseQuery(180) // 539 rune / 1259 byte
		runes, bytes := QueryLength(q)
		if runes != 539 || bytes != 1259 {
			t.Fatalf("前提: runes=%d bytes=%d（want 539 / 1259）", runes, bytes)
		}
		errs := validateQueryString(q, xapi.EndpointRecent)
		found := false
		for _, e := range errs {
			if e.RuleID == "V02" {
				found = true
				if !strings.Contains(e.Message, "539") || !strings.Contains(e.Message, "512") {
					t.Errorf("メッセージに現在値と上限値を含めるべき: %s", e.Message)
				}
			}
		}
		if !found {
			t.Error("V02 エラーになるべき")
		}
	})

	t.Run("recent の境界 511 / 512 / 513 rune", func(t *testing.T) {
		for _, n := range []int{511, 512, 513} {
			q := strings.Repeat("あ", n)
			if utf8.RuneCountInString(q) != n {
				t.Fatalf("前提が崩れている")
			}
			errs := validateQueryString(q, xapi.EndpointRecent)
			hasV02 := false
			for _, e := range errs {
				if e.RuleID == "V02" {
					hasV02 = true
				}
			}
			if n <= 512 && hasV02 {
				t.Errorf("%d rune は通過すべき", n)
			}
			if n > 512 && !hasV02 {
				t.Errorf("%d rune は V02 エラーになるべき", n)
			}
		}
	})

	t.Run("full-archive の上限は 1024 rune", func(t *testing.T) {
		// 513〜1024 rune は recent では NG、all では OK。
		q := strings.Repeat("あ", 700)
		if errs := validateQueryString(q, xapi.EndpointRecent); len(errs) == 0 {
			t.Error("recent では 700 rune は V02 エラーになるべき")
		}
		for _, e := range validateQueryString(q, xapi.EndpointAll) {
			if e.RuleID == "V02" {
				t.Errorf("all では 700 rune は通過すべき: %s", e.Message)
			}
		}
		long := strings.Repeat("あ", 1025)
		found := false
		for _, e := range validateQueryString(long, xapi.EndpointAll) {
			if e.RuleID == "V02" {
				found = true
				if !strings.Contains(e.Message, "1024") {
					t.Errorf("上限 1024 を示すべき: %s", e.Message)
				}
			}
		}
		if !found {
			t.Error("all で 1025 rune は V02 エラーになるべき")
		}
	})

	t.Run("ASCII のみならバイト数と rune 数は一致する", func(t *testing.T) {
		q := strings.Repeat("a", 100)
		runes, bytes := QueryLength(q)
		if runes != bytes {
			t.Errorf("ASCII では一致すべき: runes=%d bytes=%d", runes, bytes)
		}
	})

	t.Run("QueryLimitFor", func(t *testing.T) {
		if QueryLimitFor(xapi.EndpointRecent) != 512 {
			t.Error("recent は 512")
		}
		if QueryLimitFor(xapi.EndpointAll) != 1024 {
			t.Error("all は 1024")
		}
	})
}

func TestValidate(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, loc)

	hasRule := func(errs []ValidationError, rule string) bool {
		for _, e := range errs {
			if e.RuleID == rule {
				return true
			}
		}
		return false
	}

	cases := []struct {
		name     string
		form     SearchForm
		wantRule string // "" なら成功
	}{
		{
			name: "成功: キーワードのみ",
			form: SearchForm{Keywords: "猫", MaxResults: 10, Endpoint: "recent"},
		},
		{
			name:     "V01: 画像付きのみ",
			form:     SearchForm{HasImages: true, Endpoint: "recent"},
			wantRule: "V01",
		},
		{
			name:     "V01: lang のみ",
			form:     SearchForm{Lang: "ja", Endpoint: "recent"},
			wantRule: "V01",
		},
		{
			name:     "V01: has:images lang:ja のみ",
			form:     SearchForm{HasImages: true, Lang: "ja", Endpoint: "recent"},
			wantRule: "V01",
		},
		{
			name:     "V01: 返信のみ",
			form:     SearchForm{ReplyMode: "only", Endpoint: "recent"},
			wantRule: "V01",
		},
		{
			name:     "V02: クエリが長すぎる",
			form:     SearchForm{Keywords: japaneseQuery(180), Endpoint: "recent"},
			wantRule: "V02",
		},
		{
			name:     "V04: max_results が範囲外（小）",
			form:     SearchForm{Keywords: "猫", MaxResults: 5, Endpoint: "recent"},
			wantRule: "V04",
		},
		{
			name:     "V04: max_results が範囲外（大）",
			form:     SearchForm{Keywords: "猫", MaxResults: 101, Endpoint: "recent"},
			wantRule: "V04",
		},
		{
			name: "V04: max_results 100 は OK",
			form: SearchForm{Keywords: "猫", MaxResults: 100, Endpoint: "recent"},
		},
		{
			name:     "V05: recent で 8 日前",
			form:     SearchForm{Keywords: "猫", StartLocal: "2026-08-05T00:00", Endpoint: "recent"},
			wantRule: "V05",
		},
		{
			name: "V05: recent で 3 日前は OK",
			form: SearchForm{Keywords: "猫", StartLocal: "2026-08-10T00:00", Endpoint: "recent"},
		},
		{
			name: "V05: all なら 8 日前でも OK",
			form: SearchForm{Keywords: "猫", StartLocal: "2026-08-05T00:00", Endpoint: "all"},
		},
		{
			name:     "V05: all でも 2006-03-21 より前は NG",
			form:     SearchForm{Keywords: "猫", StartLocal: "2005-01-01T00:00", Endpoint: "all"},
			wantRule: "V05",
		},
		{
			name: "V06: start > end",
			form: SearchForm{
				Keywords: "猫", StartLocal: "2026-08-12T10:00", EndLocal: "2026-08-12T09:00",
				Endpoint: "recent",
			},
			wantRule: "V06",
		},
		{
			name: "V06: start == end",
			form: SearchForm{
				Keywords: "猫", StartLocal: "2026-08-12T10:00", EndLocal: "2026-08-12T10:00",
				Endpoint: "recent",
			},
			wantRule: "V06",
		},
		{
			name: "V06: 正しい順序なら OK",
			form: SearchForm{
				Keywords: "猫", StartLocal: "2026-08-12T09:00", EndLocal: "2026-08-12T10:00",
				Endpoint: "recent",
			},
		},
		{
			name:     "V08: username に記号",
			form:     SearchForm{FromUser: "na-sa", Endpoint: "recent"},
			wantRule: "V08",
		},
		{
			name:     "V08: username が 16 文字",
			form:     SearchForm{FromUser: strings.Repeat("a", 16), Endpoint: "recent"},
			wantRule: "V08",
		},
		{
			name: "V08: username が 15 文字は OK",
			form: SearchForm{FromUser: strings.Repeat("a", 15), Endpoint: "recent"},
		},
		{
			name:     "V09: ID に文字",
			form:     SearchForm{ConversationID: "abc", Endpoint: "recent"},
			wantRule: "V09",
		},
		{
			name:     "V09: list ID に記号",
			form:     SearchForm{ListID: "123-456", Endpoint: "recent"},
			wantRule: "V09",
		},
		{
			name:     "V10: 反応数が負",
			form:     SearchForm{Keywords: "猫", MinLikes: -1, Endpoint: "recent"},
			wantRule: "V10",
		},
		{
			name:     "V11: 括弧の対応が取れていない",
			form:     SearchForm{Keywords: "(猫 OR 犬", Endpoint: "recent"},
			wantRule: "V11",
		},
		{
			name:     "V11: 閉じ括弧が多い",
			form:     SearchForm{Keywords: "猫)", Endpoint: "recent"},
			wantRule: "V11",
		},
		{
			name: "V11: 引用符内の括弧は数えない",
			form: SearchForm{Keywords: `"猫(かわいい"`, Endpoint: "recent"},
		},
		{
			name:     "V13: 何も入力なし",
			form:     SearchForm{IncludeReposts: true, Endpoint: "recent"},
			wantRule: "V13",
		},
		{
			name:     "V14: endpoint が不正",
			form:     SearchForm{Keywords: "猫", Endpoint: "weekly"},
			wantRule: "V14",
		},
		{
			name: "V14: endpoint 未指定は recent として扱う",
			form: SearchForm{Keywords: "猫"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := Validate(tc.form, now)
			if tc.wantRule == "" {
				if len(errs) > 0 {
					t.Fatalf("エラーになってはいけない: %v", ValidationErrors(errs).Messages())
				}
				return
			}
			if !hasRule(errs, tc.wantRule) {
				t.Errorf("ルール %s のエラーが必要。実際: %v", tc.wantRule, ValidationErrors(errs).Messages())
			}
		})
	}

	t.Run("エラーは全件まとめて返す（最初の 1 件で止めない）", func(t *testing.T) {
		errs := Validate(SearchForm{
			FromUser: "bad-name", ConversationID: "abc", MinLikes: -1, Endpoint: "weekly",
		}, now)
		if len(errs) < 4 {
			t.Errorf("複数のエラーを返すべき: %v", ValidationErrors(errs).Messages())
		}
	})

	t.Run("SEEKX_MAX_RESULTS_CAP で上限を絞れる", func(t *testing.T) {
		f := SearchForm{Keywords: "猫", MaxResults: 100, Endpoint: "recent"}
		if errs := ValidateWithCap(f, now, 100); len(errs) > 0 {
			t.Fatalf("cap 100 なら OK: %v", ValidationErrors(errs).Messages())
		}
		errs := ValidateWithCap(f, now, 50)
		found := false
		for _, e := range errs {
			if e.RuleID == "V04" {
				found = true
			}
		}
		if !found {
			t.Errorf("cap 50 なら V04: %v", ValidationErrors(errs).Messages())
		}
	})
}

func TestTimeRange(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, loc)

	t.Run("JST 入力を UTC に変換する（9 時間ずれない）", func(t *testing.T) {
		f := SearchForm{
			Keywords: "猫", Endpoint: "recent",
			StartLocal: "2026-08-13T00:00", EndLocal: "2026-08-13T08:00",
		}
		start, end, _, errs := resolveTimeRange(f, now)
		if len(errs) > 0 {
			t.Fatalf("エラー: %v", ValidationErrors(errs).Messages())
		}
		wantStart := time.Date(2026, 8, 12, 15, 0, 0, 0, time.UTC)
		wantEnd := time.Date(2026, 8, 12, 23, 0, 0, 0, time.UTC)
		if !start.Equal(wantStart) {
			t.Errorf("start: got %s, want %s", start.Format(time.RFC3339), wantStart.Format(time.RFC3339))
		}
		if !end.Equal(wantEnd) {
			t.Errorf("end: got %s, want %s", end.Format(time.RFC3339), wantEnd.Format(time.RFC3339))
		}
	})

	t.Run("秒付きの datetime-local も受理する", func(t *testing.T) {
		f := SearchForm{Keywords: "猫", Endpoint: "recent", StartLocal: "2026-08-13T00:00:30"}
		start, _, _, errs := resolveTimeRange(f, now)
		if len(errs) > 0 {
			t.Fatalf("エラー: %v", ValidationErrors(errs).Messages())
		}
		if start.Second() != 30 {
			t.Errorf("秒が反映されていない: %s", start.Format(time.RFC3339))
		}
	})

	t.Run("end_time を now-30s にクランプし警告を返す（V07）", func(t *testing.T) {
		f := SearchForm{Keywords: "猫", Endpoint: "recent", EndLocal: "2026-08-13T23:59"}
		_, end, warns, errs := resolveTimeRange(f, now)
		if len(errs) > 0 {
			t.Fatalf("エラーにしてはいけない: %v", ValidationErrors(errs).Messages())
		}
		wantEnd := now.UTC().Add(-30 * time.Second)
		if !end.Equal(wantEnd) {
			t.Errorf("クランプ後: got %s, want %s", end.Format(time.RFC3339), wantEnd.Format(time.RFC3339))
		}
		found := false
		for _, w := range warns {
			if w.RuleID == "V07" {
				found = true
			}
		}
		if !found {
			t.Errorf("V07 警告が必要: %v", warns)
		}
	})

	t.Run("未来でない end_time はクランプしない", func(t *testing.T) {
		f := SearchForm{Keywords: "猫", Endpoint: "recent", EndLocal: "2026-08-12T10:00"}
		_, end, warns, _ := resolveTimeRange(f, now)
		if !end.Equal(time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)) {
			t.Errorf("end: got %s", end.Format(time.RFC3339))
		}
		for _, w := range warns {
			if w.RuleID == "V07" {
				t.Error("クランプ警告は不要")
			}
		}
	})

	t.Run("日時未指定ならゼロ値", func(t *testing.T) {
		start, end, _, errs := resolveTimeRange(SearchForm{Keywords: "猫"}, now)
		if !start.IsZero() || !end.IsZero() {
			t.Errorf("ゼロ値であるべき: %v %v", start, end)
		}
		if len(errs) > 0 {
			t.Errorf("エラー不要: %v", ValidationErrors(errs).Messages())
		}
	})

	t.Run("形式不正はエラー", func(t *testing.T) {
		_, _, _, errs := resolveTimeRange(SearchForm{Keywords: "猫", StartLocal: "yesterday"}, now)
		if len(errs) == 0 {
			t.Error("エラーになるべき")
		}
	})
}

func TestUsernameNormalize(t *testing.T) {
	cases := map[string]string{
		"@nasa":    "nasa",
		"nasa":     "nasa",
		"  @nasa ": "nasa",
		"":         "",
		"@":        "",
	}
	for in, want := range cases {
		if got := normalizeUsername(in); got != want {
			t.Errorf("normalizeUsername(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildParams(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, loc)

	t.Run("既定の expansions は author_id と attachments.media_keys のみ（D-20）", func(t *testing.T) {
		p, _, err := BuildParams(SearchForm{Keywords: "猫", MaxResults: 10, Endpoint: "recent"}, now)
		if err != nil {
			t.Fatalf("エラー: %v", err)
		}
		want := []string{"author_id", "attachments.media_keys"}
		if strings.Join(p.Expansions, ",") != strings.Join(want, ",") {
			t.Errorf("Expansions: got %v, want %v", p.Expansions, want)
		}
		if p.MaxResults != 10 {
			t.Errorf("MaxResults: got %d", p.MaxResults)
		}
		if p.Endpoint != xapi.EndpointRecent {
			t.Errorf("Endpoint: got %q", p.Endpoint)
		}
		// post.fields に author_id を入れてはいけない（P-EXPANSIONS-VS-FIELDS）。
		for _, f := range p.PostFields {
			switch f {
			case "author_id", "in_reply_to_user_id", "referenced_posts", "edit_history_post_ids", "username":
				t.Errorf("%q は expansions の値であり post.fields に入れてはいけない", f)
			}
		}
	})

	t.Run("リポストを含める=ON なら referenced_posts を追加する", func(t *testing.T) {
		p, _, err := BuildParams(SearchForm{Keywords: "猫", MaxResults: 10, IncludeReposts: true}, now)
		if err != nil {
			t.Fatalf("エラー: %v", err)
		}
		found := false
		for _, e := range p.Expansions {
			if e == "referenced_posts" {
				found = true
			}
		}
		if !found {
			t.Errorf("referenced_posts が必要: %v", p.Expansions)
		}
	})

	t.Run("max_results 未指定なら 10", func(t *testing.T) {
		p, _, err := BuildParams(SearchForm{Keywords: "猫"}, now)
		if err != nil {
			t.Fatalf("エラー: %v", err)
		}
		if p.MaxResults != 10 {
			t.Errorf("既定は 10: got %d", p.MaxResults)
		}
	})

	t.Run("検証エラー時はエラーを返す", func(t *testing.T) {
		_, _, err := BuildParams(SearchForm{HasImages: true}, now)
		if err == nil {
			t.Fatal("エラーになるべき")
		}
		if _, ok := err.(ValidationErrors); !ok {
			t.Errorf("ValidationErrors であるべき: %T", err)
		}
	})
}

func TestCountsGranularity(t *testing.T) {
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		start, end time.Time
		want       string
	}{
		{"1 日 → hour", now.Add(-24 * time.Hour), now, "hour"},
		{"2 日 → hour", now.Add(-48 * time.Hour), now, "hour"},
		{"3 日 → day", now.Add(-72 * time.Hour), now, "day"},
		{"未指定 → day（7 日窓）", time.Time{}, time.Time{}, "day"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CountsGranularity(tc.start, tc.end, now); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestValidateEndpointLimits は endpoint ごとの上限（クエリ長・max_results・期間）を検証する。
func TestValidateEndpointLimits(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, loc)

	hasRule := func(errs []ValidationError, rule string) bool {
		for _, e := range errs {
			if e.RuleID == rule {
				return true
			}
		}
		return false
	}

	t.Run("クエリ長の上限は recent 512 / all 1024 rune", func(t *testing.T) {
		q700 := strings.Repeat("あ", 700)
		if !hasRule(Validate(SearchForm{Keywords: q700, Endpoint: "recent"}, now), "V02") {
			t.Error("recent では 700 rune は V02")
		}
		if hasRule(Validate(SearchForm{Keywords: q700, Endpoint: "all"}, now), "V02") {
			t.Error("all では 700 rune は通る")
		}
		if !hasRule(Validate(SearchForm{Keywords: strings.Repeat("あ", 1025), Endpoint: "all"}, now), "V02") {
			t.Error("all では 1025 rune は V02")
		}
	})

	t.Run("max_results の上限は recent 100 / all 500（UI 上限で更に絞る）", func(t *testing.T) {
		if hasRule(ValidateWithCap(SearchForm{Keywords: "猫", MaxResults: 100, Endpoint: "recent"}, now, 500), "V04") {
			t.Error("recent 100 は OK")
		}
		if !hasRule(ValidateWithCap(SearchForm{Keywords: "猫", MaxResults: 101, Endpoint: "recent"}, now, 500), "V04") {
			t.Error("recent 101 は V04")
		}
		if hasRule(ValidateWithCap(SearchForm{Keywords: "猫", MaxResults: 500, Endpoint: "all"}, now, 500), "V04") {
			t.Error("all 500 は API 仕様上 OK")
		}
		if !hasRule(ValidateWithCap(SearchForm{Keywords: "猫", MaxResults: 500, Endpoint: "all"}, now, 100), "V04") {
			t.Error("UI 上限 100 なら all 500 は V04")
		}
	})

	t.Run("期間の下限は recent 7 日 / all 2006-03-21", func(t *testing.T) {
		if !hasRule(Validate(SearchForm{Keywords: "猫", StartLocal: "2026-08-05T00:00", Endpoint: "recent"}, now), "V05") {
			t.Error("recent の 8 日前は V05")
		}
		if hasRule(Validate(SearchForm{Keywords: "猫", StartLocal: "2015-06-01T00:00", Endpoint: "all"}, now), "V05") {
			t.Error("all の 2015 年は OK")
		}
		if !hasRule(Validate(SearchForm{Keywords: "猫", StartLocal: "2006-03-20T00:00", Endpoint: "all"}, now), "V05") {
			t.Error("all でも 2006-03-21 より前は V05")
		}
	})

	t.Run("検索パスと counts パスの対応", func(t *testing.T) {
		if xapi.SearchPath(xapi.EndpointRecent) != "/tweets/search/recent" || xapi.SearchPath(xapi.EndpointAll) != "/tweets/search/all" {
			t.Error("SearchPath")
		}
		if xapi.CountsPath(xapi.EndpointRecent) != "/tweets/counts/recent" || xapi.CountsPath(xapi.EndpointAll) != "/tweets/counts/all" {
			t.Error("CountsPath")
		}
	})
}
