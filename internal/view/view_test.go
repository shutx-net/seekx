package view

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/shutx-net/seekx/internal/xapi"
)

func loadFixture(t *testing.T, name string, out any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../../testdata", name))
	if err != nil {
		t.Fatalf("フィクスチャ %s の読み込みに失敗: %v", name, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("フィクスチャ %s の Unmarshal に失敗: %v", name, err)
	}
}

func jst(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatalf("Asia/Tokyo が解決できません: %v", err)
	}
	return loc
}

func TestJoinIncludes(t *testing.T) {
	t.Run("includes が nil でも空マップを返す", func(t *testing.T) {
		users, media, posts := joinIncludes(&xapi.SearchResponse{})
		if users == nil || media == nil || posts == nil {
			t.Fatal("nil マップを返してはいけない")
		}
		if len(users) != 0 || len(media) != 0 || len(posts) != 0 {
			t.Errorf("空でなければならない: %d %d %d", len(users), len(media), len(posts))
		}
	})

	t.Run("resp 自体が nil でも落ちない", func(t *testing.T) {
		users, media, posts := joinIncludes(nil)
		if users == nil || media == nil || posts == nil {
			t.Fatal("nil マップを返してはいけない")
		}
	})

	t.Run("users / media / posts をキーで引ける", func(t *testing.T) {
		var resp xapi.SearchResponse
		loadFixture(t, "search_recent_edge.json", &resp)
		users, media, posts := joinIncludes(&resp)
		if got, ok := users["10000001"]; !ok || got.Username != "sample_taro" {
			t.Errorf("users の引き当てに失敗: %+v ok=%v", got, ok)
		}
		if got, ok := media["3_1000000000000000201"]; !ok || got.Type != "photo" {
			t.Errorf("media の引き当てに失敗: %+v ok=%v", got, ok)
		}
		if got, ok := posts["1900000000000000001"]; !ok || got.ID != "1900000000000000001" {
			t.Errorf("includes.posts の引き当てに失敗: %+v ok=%v", got, ok)
		}
	})
}

func TestBuildView(t *testing.T) {
	loc := jst(t)
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, loc)

	t.Run("画像フィクスチャ: 順序保持・グリッド・投稿者欠落", func(t *testing.T) {
		var resp xapi.SearchResponse
		loadFixture(t, "search_recent_photos.json", &resp)
		v := BuildView(&resp, ViewOptions{Now: now, Loc: loc})

		if v.ResultCount != 4 || len(v.Posts) != 4 {
			t.Fatalf("件数が違う: ResultCount=%d len(Posts)=%d", v.ResultCount, len(v.Posts))
		}
		wantOrder := []string{
			"1900000000000000001", "1900000000000000002",
			"1900000000000000003", "1900000000000000004",
		}
		for i, id := range wantOrder {
			if v.Posts[i].ID != id {
				t.Errorf("Posts[%d].ID = %s, want %s（data の順序を保持すること）", i, v.Posts[i].ID, id)
			}
		}
		wantGrid := []string{"grid-1", "grid-2", "grid-3", "grid-4"}
		for i, g := range wantGrid {
			if v.Posts[i].GridClass != g {
				t.Errorf("Posts[%d].GridClass = %q, want %q", i, v.Posts[i].GridClass, g)
			}
		}

		// author_id が includes.users に無い投稿でも捨てられず、プレースホルダ作者になる。
		last := v.Posts[3]
		if !last.Author.Missing {
			t.Error("Author.Missing が true になるべき")
		}
		if last.Author.Name != UnknownAuthorName {
			t.Errorf("プレースホルダ名: got %q", last.Author.Name)
		}
		if last.PermalinkURL != "https://x.com/i/status/1900000000000000004" {
			t.Errorf("投稿者不明時の恒久リンク: got %q", last.PermalinkURL)
		}
		if len(last.Media) != 4 {
			t.Errorf("メディア 4 枚: got %d", len(last.Media))
		}
		foundWarn := false
		for _, w := range v.Warnings {
			if strings.Contains(w, "投稿者情報") {
				foundWarn = true
			}
		}
		if !foundWarn {
			t.Errorf("投稿者欠落の警告が必要: %v", v.Warnings)
		}

		// photo は url を使い、preview_image_url（null）を使わない。
		first := v.Posts[0]
		if len(first.Media) != 1 {
			t.Fatalf("メディア 1 枚: got %d", len(first.Media))
		}
		m := first.Media[0]
		if m.Kind != "photo" {
			t.Errorf("Kind: got %q", m.Kind)
		}
		if !strings.Contains(m.ThumbURL, "AAAAAAAAAAAAAAA.jpg") || !strings.Contains(m.ThumbURL, "name=small") {
			t.Errorf("ThumbURL: got %q", m.ThumbURL)
		}
		if !strings.Contains(m.FullURL, "name=orig") {
			t.Errorf("FullURL: got %q", m.FullURL)
		}
		if m.PosterURL != "" {
			t.Errorf("photo に PosterURL は不要: got %q", m.PosterURL)
		}
		if m.AspectRatio != "1200 / 675" {
			t.Errorf("AspectRatio: got %q", m.AspectRatio)
		}
		if m.Alt != "青空と雲の写真" {
			t.Errorf("Alt: got %q", m.Alt)
		}

		// メトリクス
		if !first.Metrics.HasMetrics || first.Metrics.Likes != 145 || first.Metrics.Reposts != 12 {
			t.Errorf("メトリクス: %+v", first.Metrics)
		}
		// JST 表示（09:04 UTC → 18:04 JST）
		if first.CreatedAtDisplay != "2026-08-12 18:04" {
			t.Errorf("CreatedAtDisplay（JST）: got %q, want 2026-08-12 18:04", first.CreatedAtDisplay)
		}
		if first.CreatedAtISO == "" {
			t.Error("CreatedAtISO が空")
		}
		if first.Author.ProfileImageURL != "https://pbs.twimg.com/profile_images/1000000000000000001/abcdefg_400x400.jpg" {
			t.Errorf("プロフィール画像の解像度置換: got %q", first.Author.ProfileImageURL)
		}
		if first.PermalinkURL != "https://x.com/sample_taro/status/1900000000000000001" {
			t.Errorf("恒久リンク: got %q", first.PermalinkURL)
		}
		if v.NextToken == "" || v.NewestID == "" || v.OldestID == "" {
			t.Errorf("meta の引き継ぎ: next=%q newest=%q oldest=%q", v.NextToken, v.NewestID, v.OldestID)
		}
	})

	t.Run("動画フィクスチャ: variant 選択と HLS のみの警告", func(t *testing.T) {
		var resp xapi.SearchResponse
		loadFixture(t, "search_recent_video.json", &resp)
		v := BuildView(&resp, ViewOptions{Now: now, Loc: loc})

		if len(v.Posts) != 3 {
			t.Fatalf("3 件: got %d", len(v.Posts))
		}
		vid := v.Posts[0].Media[0]
		if vid.Kind != "video" {
			t.Errorf("Kind: got %q", vid.Kind)
		}
		if !strings.Contains(vid.VideoURL, "high.mp4") {
			t.Errorf("最大 bit_rate の mp4 を選ぶべき: got %q", vid.VideoURL)
		}
		if vid.PosterURL == "" {
			t.Error("video には poster が必要")
		}
		if vid.Duration != "0:47" {
			t.Errorf("Duration: got %q, want 0:47", vid.Duration)
		}

		gif := v.Posts[1].Media[0]
		if gif.Kind != "gif" {
			t.Errorf("animated_gif は gif: got %q", gif.Kind)
		}
		if !strings.HasSuffix(gif.VideoURL, ".mp4") {
			t.Errorf("gif の mp4: got %q", gif.VideoURL)
		}

		hls := v.Posts[2].Media[0]
		if hls.VideoURL != "" {
			t.Errorf("m3u8 のみなら VideoURL は空: got %q", hls.VideoURL)
		}
		if hls.PlaybackNote == "" {
			t.Error("PlaybackNote が必要")
		}
		found := false
		for _, w := range v.Warnings {
			if strings.Contains(w, "HLS") {
				found = true
			}
		}
		if !found {
			t.Errorf("HLS のみの警告が必要: %v", v.Warnings)
		}
	})

	t.Run("エッジフィクスチャ: note_post 優先・metrics nil・フラグ・オーバーフロー", func(t *testing.T) {
		var resp xapi.SearchResponse
		loadFixture(t, "search_recent_edge.json", &resp)
		v := BuildView(&resp, ViewOptions{Now: now, Loc: loc})

		if len(v.Posts) != 7 {
			t.Fatalf("7 件: got %d", len(v.Posts))
		}

		if !v.Posts[0].Sensitive {
			t.Error("possibly_sensitive が反映されていない")
		}

		// note_post が本文として優先される（D-25）。
		note := v.Posts[1]
		if strings.Contains(note.Text, "…") {
			t.Errorf("切り詰められた text ではなく note_post.text を使うべき: %q", note.Text)
		}
		if !strings.Contains(note.Text, "note_post が優先されていることの証拠") {
			t.Errorf("note_post.text の内容が使われていない: %q", note.Text)
		}

		// public_metrics 欠落
		if v.Posts[2].Metrics.HasMetrics {
			t.Error("public_metrics 欠落時は HasMetrics=false")
		}
		if len(v.Posts[2].Media) != 0 {
			t.Error("attachments 欠落時はメディアなし")
		}
		if v.Posts[2].GridClass != "" {
			t.Errorf("メディアなしなら GridClass は空: got %q", v.Posts[2].GridClass)
		}

		// referenced_posts のフラグ
		if !v.Posts[3].IsRepost {
			t.Error("retweeted → IsRepost")
		}
		if !v.Posts[4].IsReply || !v.Posts[4].IsQuote {
			t.Errorf("replied_to → IsReply / quoted → IsQuote: %+v", v.Posts[4])
		}

		// 本文は未加工のまま（エスケープはテンプレートの責務）
		if !strings.Contains(v.Posts[5].Text, "<script>") {
			t.Error("本文を加工してはいけない（エスケープはテンプレート側）")
		}

		// 5 枚以上のオーバーフロー（media 1 件は欠落しているので 6 枚解決される）
		over := v.Posts[6]
		if len(over.Media) != 6 {
			t.Fatalf("解決できたメディアは 6 枚: got %d", len(over.Media))
		}
		if len(over.GridMedia) != 4 {
			t.Errorf("グリッド描画は先頭 4 枚: got %d", len(over.GridMedia))
		}
		if over.OverflowCount != 2 {
			t.Errorf("OverflowCount: got %d, want 2", over.OverflowCount)
		}
		if over.GridClass != "grid-4" {
			t.Errorf("GridClass: got %q", over.GridClass)
		}
		foundMissingMedia := false
		for _, w := range v.Warnings {
			if strings.Contains(w, "メディアが取得できません") {
				foundMissingMedia = true
			}
		}
		if !foundMissingMedia {
			t.Errorf("メディア欠落の警告が必要: %v", v.Warnings)
		}
	})

	t.Run("0 件フィクスチャ", func(t *testing.T) {
		var resp xapi.SearchResponse
		loadFixture(t, "search_recent_empty.json", &resp)
		v := BuildView(&resp, ViewOptions{Now: now, Loc: loc})
		if v.ResultCount != 0 || len(v.Posts) != 0 {
			t.Errorf("0 件であるべき: %d %d", v.ResultCount, len(v.Posts))
		}
		if v.NextToken != "" {
			t.Errorf("next_token は無い: %q", v.NextToken)
		}
	})

	t.Run("部分成功エラーは PartialErrors に入る", func(t *testing.T) {
		var resp xapi.SearchResponse
		loadFixture(t, "search_recent_partial_errors.json", &resp)
		v := BuildView(&resp, ViewOptions{Now: now, Loc: loc})
		if len(v.Posts) != 1 {
			t.Fatalf("投稿は表示する: got %d", len(v.Posts))
		}
		if len(v.PartialErrors) != 1 || !strings.Contains(v.PartialErrors[0], "Could not find user") {
			t.Errorf("PartialErrors: %v", v.PartialErrors)
		}
	})

	t.Run("resp が nil でも落ちない", func(t *testing.T) {
		v := BuildView(nil, ViewOptions{Now: now, Loc: loc})
		if len(v.Posts) != 0 {
			t.Error("空であるべき")
		}
	})
}

func TestSelectVideoVariant(t *testing.T) {
	cases := []struct {
		name    string
		in      []xapi.Variant
		wantURL string
		wantOK  bool
	}{
		{
			name: "最大 bit_rate の mp4 を選ぶ",
			in: []xapi.Variant{
				{BitRate: 832000, ContentType: "video/mp4", URL: "mid.mp4"},
				{BitRate: 2176000, ContentType: "video/mp4", URL: "high.mp4"},
				{BitRate: 256000, ContentType: "video/mp4", URL: "low.mp4"},
			},
			wantURL: "high.mp4", wantOK: true,
		},
		{
			name: "bit_rate 同値なら先勝ち",
			in: []xapi.Variant{
				{BitRate: 1000, ContentType: "video/mp4", URL: "first.mp4"},
				{BitRate: 1000, ContentType: "video/mp4", URL: "second.mp4"},
			},
			wantURL: "first.mp4", wantOK: true,
		},
		{
			name: "m3u8 は除外する",
			in: []xapi.Variant{
				{ContentType: "application/x-mpegURL", URL: "playlist.m3u8"},
				{BitRate: 100, ContentType: "video/mp4", URL: "only.mp4"},
			},
			wantURL: "only.mp4", wantOK: true,
		},
		{
			name: "mp4 が無ければ false",
			in: []xapi.Variant{
				{ContentType: "application/x-mpegURL", URL: "playlist.m3u8"},
			},
			wantURL: "", wantOK: false,
		},
		{
			name:    "variants が空",
			in:      nil,
			wantURL: "", wantOK: false,
		},
		{
			name: "animated_gif（bit_rate 無し）",
			in: []xapi.Variant{
				{ContentType: "video/mp4", URL: "gif.mp4"},
			},
			wantURL: "gif.mp4", wantOK: true,
		},
		{
			name: "content_type 欠落時は拡張子で救う",
			in: []xapi.Variant{
				{URL: "https://video.twimg.com/x/fallback.mp4"},
			},
			wantURL: "https://video.twimg.com/x/fallback.mp4", wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := selectVideoVariant(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok: got %v, want %v", ok, tc.wantOK)
			}
			if got.URL != tc.wantURL {
				t.Errorf("URL: got %q, want %q", got.URL, tc.wantURL)
			}
		})
	}
}

func TestSizedMediaURL(t *testing.T) {
	cases := []struct {
		name, raw, size, want string
	}{
		{
			name: "素の URL に format と name を付ける",
			raw:  "https://pbs.twimg.com/media/AAA.jpg", size: "small",
			want: "https://pbs.twimg.com/media/AAA.jpg?format=jpg&name=small",
		},
		{
			name: "既存クエリを保持しつつ上書きする",
			raw:  "https://pbs.twimg.com/media/AAA.jpg?format=png&name=large&x=1", size: "orig",
			want: "https://pbs.twimg.com/media/AAA.jpg?format=jpg&name=orig&x=1",
		},
		{
			name: "name が空なら素の URL を返す（フォールバック経路）",
			raw:  "https://pbs.twimg.com/media/AAA.jpg", size: "",
			want: "https://pbs.twimg.com/media/AAA.jpg",
		},
		{
			name: "URL が空なら空",
			raw:  "", size: "small",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sizedMediaURL(tc.raw, tc.size); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProfileImageURL(t *testing.T) {
	cases := []struct {
		name, raw, size, want string
	}{
		{
			name: "_normal を置換する",
			raw:  "https://pbs.twimg.com/profile_images/1/a_normal.jpg", size: "400x400",
			want: "https://pbs.twimg.com/profile_images/1/a_400x400.jpg",
		},
		{
			name: "png でも置換する",
			raw:  "https://pbs.twimg.com/profile_images/1/a_normal.png", size: "400x400",
			want: "https://pbs.twimg.com/profile_images/1/a_400x400.png",
		},
		{
			name: "パターン不一致ならそのまま",
			raw:  "https://pbs.twimg.com/profile_images/1/a.jpg", size: "400x400",
			want: "https://pbs.twimg.com/profile_images/1/a.jpg",
		},
		{
			name: "空文字はそのまま",
			raw:  "", size: "400x400",
			want: "",
		},
		{
			name: "size が空ならそのまま",
			raw:  "https://pbs.twimg.com/profile_images/1/a_normal.jpg", size: "",
			want: "https://pbs.twimg.com/profile_images/1/a_normal.jpg",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := profileImageURL(tc.raw, tc.size); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGridClass(t *testing.T) {
	want := map[int]string{0: "", 1: "grid-1", 2: "grid-2", 3: "grid-3", 4: "grid-4", 5: "grid-4", 6: "grid-4"}
	for n, w := range want {
		if got := gridClass(n); got != w {
			t.Errorf("gridClass(%d) = %q, want %q", n, got, w)
		}
	}
}

func TestPermalink(t *testing.T) {
	if got := permalink("sample_taro", "123"); got != "https://x.com/sample_taro/status/123" {
		t.Errorf("got %q", got)
	}
	if got := permalink("", "123"); got != "https://x.com/i/status/123" {
		t.Errorf("投稿者不明時: got %q", got)
	}
	if got := permalink(unknownAuthorUsername, "123"); got != "https://x.com/i/status/123" {
		t.Errorf("プレースホルダ username: got %q", got)
	}
	if got := permalink("x", ""); got != "" {
		t.Errorf("ID が無ければ空: got %q", got)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[int]string{0: "0:00", 1000: "0:01", 47320: "0:47", 61000: "1:01", 3600000: "60:00"}
	for ms, want := range cases {
		if got := formatDuration(ms); got != want {
			t.Errorf("formatDuration(%d) = %q, want %q", ms, got, want)
		}
	}
}

// TestAspectRatioCSS は html/template の CSS フィルタを素通りする形式であることを固定する。
// "1200 / 675" のようなスラッシュ入りの値は ZgotmplZ に置換されて捨てられる（実測確認済み）。
func TestAspectRatioCSS(t *testing.T) {
	var resp xapi.SearchResponse
	loadFixture(t, "search_recent_photos.json", &resp)
	loc := jst(t)
	v := BuildView(&resp, ViewOptions{Now: time.Date(2026, 8, 13, 9, 0, 0, 0, loc), Loc: loc})

	m := v.Posts[0].Media[0]
	if m.AspectRatio != "1200 / 675" {
		t.Errorf("参考表示用の AspectRatio: got %q", m.AspectRatio)
	}
	if m.AspectRatioCSS != "1.7778" {
		t.Errorf("CSS 用の値は小数: got %q, want 1.7778", m.AspectRatioCSS)
	}
	for _, r := range m.AspectRatioCSS {
		if !(r >= '0' && r <= '9') && r != '.' {
			t.Errorf("CSS 用の値に使えない文字が含まれる: %q", m.AspectRatioCSS)
		}
	}

	// width / height が無いメディアでは空にする。
	var edge xapi.SearchResponse
	loadFixture(t, "search_recent_edge.json", &edge)
	_ = edge
	if got := (ViewMedia{}).AspectRatioCSS; got != "" {
		t.Errorf("ゼロ値では空: got %q", got)
	}
}

// TestBuildViewRepostCount は BuildView が repost_count / レガシー retweet_count の
// どちらからでもリポスト数を取れることをフィクスチャで確認する（R-03）。
// JSON タグそのものの検証は internal/xapi の TestPostMetricsRepostCount が担当する。
func TestBuildViewRepostCount(t *testing.T) {
	loc := jst(t)
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, loc)

	var photos xapi.SearchResponse
	loadFixture(t, "search_recent_photos.json", &photos)
	v := BuildView(&photos, ViewOptions{Now: now, Loc: loc})
	if v.Posts[0].Metrics.Reposts != 12 {
		t.Errorf("repost_count を読めていない: got %d, want 12", v.Posts[0].Metrics.Reposts)
	}

	// エッジフィクスチャの先頭投稿はレガシー名 retweet_count を残している。
	var edge xapi.SearchResponse
	loadFixture(t, "search_recent_edge.json", &edge)
	ve := BuildView(&edge, ViewOptions{Now: now, Loc: loc})
	if ve.Posts[0].Metrics.Reposts != 3 {
		t.Errorf("レガシー名からフォールバックできていない: got %d, want 3", ve.Posts[0].Metrics.Reposts)
	}
}

// TestBuildViewFilterKeys は一覧の絞り込みキー（D-37 / D-48）が
// 実フィクスチャに対して期待どおりに算出されることを確認する。
// 期待値は現行フィクスチャの実測値。
func TestBuildViewFilterKeys(t *testing.T) {
	loc := jst(t)
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, loc)

	type want struct {
		runes int
		media int
		likes int
		flags string
	}

	cases := []struct {
		fixture string
		want    []want
	}{
		{
			fixture: "search_recent_edge.json",
			want: []want{
				// possibly_sensitive + 写真 1 枚 + いいね 22（レガシー retweet_count のケース）
				{runes: 23, media: 1, likes: 22, flags: "sensitive media photo"},
				// note_post 優先（text は 41 rune、note_post.text は 152 rune）
				{runes: 152, media: 0, likes: 64, flags: ""},
				// public_metrics 欠落 → -1
				{runes: 39, media: 0, likes: -1, flags: ""},
				// referenced_posts: retweeted
				{runes: 29, media: 0, likes: -1, flags: "repost"},
				// referenced_posts: replied_to + quoted
				{runes: 20, media: 0, likes: -1, flags: "reply quote"},
				// like_count: 0（不明の -1 と区別される）
				{runes: 68, media: 0, likes: 0, flags: ""},
				// media_keys 7 件 / includes に 6 件 → 6
				{runes: 52, media: 6, likes: -1, flags: "media photo"},
			},
		},
		{
			fixture: "search_recent_photos.json",
			want: []want{
				{runes: 19, media: 1, likes: 145, flags: "media photo"},
				{runes: 10, media: 2, likes: 4, flags: "media photo"},
				{runes: 27, media: 3, likes: 903, flags: "media photo"},
				{runes: 57, media: 4, likes: 30, flags: "media photo"},
			},
		},
		{
			fixture: "search_recent_video.json",
			want: []want{
				{runes: 43, media: 1, likes: 210, flags: "media video"},
				{runes: 39, media: 1, likes: 17, flags: "media gif"},
				// HLS のみでも media は解決されるので枚数・トークンは立つ
				{runes: 31, media: 1, likes: -1, flags: "media video"},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			var resp xapi.SearchResponse
			loadFixture(t, c.fixture, &resp)
			v := BuildView(&resp, ViewOptions{Now: now, Loc: loc})
			if len(v.Posts) != len(c.want) {
				t.Fatalf("件数が違う: got %d, want %d", len(v.Posts), len(c.want))
			}
			for i, w := range c.want {
				p := v.Posts[i]
				if p.FilterTextRunes != w.runes {
					t.Errorf("Posts[%d].FilterTextRunes = %d, want %d", i, p.FilterTextRunes, w.runes)
				}
				if p.FilterMediaCount != w.media {
					t.Errorf("Posts[%d].FilterMediaCount = %d, want %d", i, p.FilterMediaCount, w.media)
				}
				if p.FilterLikes != w.likes {
					t.Errorf("Posts[%d].FilterLikes = %d, want %d", i, p.FilterLikes, w.likes)
				}
				if p.FilterFlags != w.flags {
					t.Errorf("Posts[%d].FilterFlags = %q, want %q", i, p.FilterFlags, w.flags)
				}
			}
		})
	}

	t.Run("note_post 投稿は text の rune 数にならない", func(t *testing.T) {
		var resp xapi.SearchResponse
		loadFixture(t, "search_recent_edge.json", &resp)
		v := BuildView(&resp, ViewOptions{Now: now, Loc: loc})
		note := v.Posts[1]
		rawText := utf8.RuneCountInString(resp.Data[1].Text)
		if rawText != 41 {
			t.Fatalf("フィクスチャの text は 41 rune の想定: got %d", rawText)
		}
		if note.FilterTextRunes == rawText {
			t.Errorf("note_post.text ではなく text を数えている: %d", note.FilterTextRunes)
		}
		if note.FilterTextRunes != 152 {
			t.Errorf("FilterTextRunes = %d, want 152（note_post.text の rune 数）", note.FilterTextRunes)
		}
	})

	t.Run("メディア 6 枚の投稿は GridMedia 4 / OverflowCount 2 と同時に成立する", func(t *testing.T) {
		var resp xapi.SearchResponse
		loadFixture(t, "search_recent_edge.json", &resp)
		v := BuildView(&resp, ViewOptions{Now: now, Loc: loc})
		over := v.Posts[6]
		if over.FilterMediaCount != 6 {
			t.Errorf("FilterMediaCount = %d, want 6（GridMedia ではなく総数）", over.FilterMediaCount)
		}
		if len(over.GridMedia) != 4 || over.OverflowCount != 2 {
			t.Errorf("GridMedia=%d OverflowCount=%d, want 4 / 2", len(over.GridMedia), over.OverflowCount)
		}
	})

	t.Run("いいね不明（-1）と 0 件は区別される", func(t *testing.T) {
		var resp xapi.SearchResponse
		loadFixture(t, "search_recent_edge.json", &resp)
		v := BuildView(&resp, ViewOptions{Now: now, Loc: loc})
		if v.Posts[2].Metrics.HasMetrics || v.Posts[2].FilterLikes != -1 {
			t.Errorf("metrics 欠落は -1: HasMetrics=%v FilterLikes=%d", v.Posts[2].Metrics.HasMetrics, v.Posts[2].FilterLikes)
		}
		if !v.Posts[5].Metrics.HasMetrics || v.Posts[5].FilterLikes != 0 {
			t.Errorf("like_count 0 は 0: HasMetrics=%v FilterLikes=%d", v.Posts[5].Metrics.HasMetrics, v.Posts[5].FilterLikes)
		}
	})
}

// TestFilterTextRunesIsRuneBased は文字数がバイト数ではなく rune 数であることを固定する
// （D-30 / TestQueryLength と同じ単位・同じ意図）。
func TestFilterTextRunesIsRuneBased(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		wantRunes int
	}{
		{"日本語 5 文字", "こんにちは", 5},
		{"ASCII 10 文字", "abcdefghij", 10},
		{"絵文字 1 つ（サロゲートペア）", "🐈", 1},
		{"絵文字混在", "猫🐈です", 4},
		{"空文字", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := utf8.RuneCountInString(c.text)
			if got != c.wantRunes {
				t.Errorf("rune 数 = %d, want %d", got, c.wantRunes)
			}
			if c.text != "" && len(c.text) == got && c.name != "ASCII 10 文字" {
				t.Errorf("バイト数 %d と rune 数 %d が一致してしまっている（単位の違いが見えない）", len(c.text), got)
			}
		})
	}

	// BuildView 経由でも同じ単位であること（日本語 5 文字 = 15 バイト → 5）。
	loc := jst(t)
	resp := xapi.SearchResponse{Data: []xapi.Post{{ID: "1", Text: "こんにちは"}}}
	v := BuildView(&resp, ViewOptions{Now: time.Date(2026, 8, 13, 9, 0, 0, 0, loc), Loc: loc})
	if len(v.Posts) != 1 {
		t.Fatalf("1 件: got %d", len(v.Posts))
	}
	if len(v.Posts[0].Text) != 15 {
		t.Fatalf("フィクスチャ前提: 15 バイトのはず: got %d", len(v.Posts[0].Text))
	}
	if v.Posts[0].FilterTextRunes != 5 {
		t.Errorf("FilterTextRunes = %d, want 5（バイト数 15 ではない）", v.Posts[0].FilterTextRunes)
	}
}

// TestFilterFlags はトークンの順序と条件を固定する（D-48）。
func TestFilterFlags(t *testing.T) {
	photo := ViewMedia{Kind: "photo"}
	video := ViewMedia{Kind: "video"}
	gif := ViewMedia{Kind: "gif"}

	cases := []struct {
		name string
		in   ViewPost
		want string
	}{
		{"何も立たない", ViewPost{Text: "ふつうの投稿"}, ""},
		{"返信のみ", ViewPost{IsReply: true}, "reply"},
		{"リポストのみ", ViewPost{IsRepost: true}, "repost"},
		{"引用のみ", ViewPost{IsQuote: true}, "quote"},
		{"センシティブのみ", ViewPost{Sensitive: true}, "sensitive"},
		{"リンクのみ", ViewPost{Text: "詳細は https://t.co/abc"}, "link"},
		{"写真 1 枚", ViewPost{Media: []ViewMedia{photo}}, "media photo"},
		{"写真 2 枚（photo は 1 回だけ）", ViewPost{Media: []ViewMedia{photo, photo}}, "media photo"},
		{"動画", ViewPost{Media: []ViewMedia{video}}, "media video"},
		{"GIF", ViewPost{Media: []ViewMedia{gif}}, "media gif"},
		{"photo + video + gif の混在は宣言順ではなく固定順", ViewPost{Media: []ViewMedia{gif, video, photo}}, "media photo video gif"},
		{"未知の Kind は種別トークンを立てないが media は立つ", ViewPost{Media: []ViewMedia{{Kind: ""}}}, "media"},
		{
			name: "全部立つ（順序の固定）",
			in: ViewPost{
				IsReply: true, IsRepost: true, IsQuote: true, Sensitive: true,
				Media: []ViewMedia{photo, video, gif},
				Text:  "http://example.com",
			},
			want: "reply repost quote sensitive media photo video gif link",
		},
		{
			name: "返信 + 引用（フィクスチャのパターン）",
			in:   ViewPost{IsReply: true, IsQuote: true},
			want: "reply quote",
		},
		{
			name: "センシティブ + 写真（フィクスチャのパターン）",
			in:   ViewPost{Sensitive: true, Media: []ViewMedia{photo}},
			want: "sensitive media photo",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := filterFlags(c.in)
			if got != c.want {
				t.Errorf("filterFlags() = %q, want %q", got, c.want)
			}
			if strings.HasPrefix(got, " ") || strings.HasSuffix(got, " ") {
				t.Errorf("前後に余分な空白がある: %q", got)
			}
			if strings.Contains(got, "  ") {
				t.Errorf("空白が連続している: %q", got)
			}
		})
	}
}

// TestHasLinkInText はリンク判定（D-50 / R-22 のヒューリスティック）を固定する。
func TestHasLinkInText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"t.co の短縮 URL", "写真 https://t.co/abc", true},
		{"http スキーム", "http://example.com", true},
		{"本文中央に URL", "前 https://t.co/xyz 後", true},
		{"note_post 相当の生 URL", "詳しくは https://example.com/very/long/path をご覧ください", true},
		{"スキームだけの文字列", "https", false},
		{"スラッシュが足りない", "https:/example.com", false},
		{"URL なし", "ふつうの投稿です。", false},
		{"空文字", "", false},
		{"大文字は拾わない（ヒューリスティックの既知の限界）", "HTTPS://EXAMPLE.COM", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasLinkInText(c.in); got != c.want {
				t.Errorf("hasLinkInText(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
