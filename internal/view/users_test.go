package view

// users_test.go はユーザーカードとフォロワー一覧の ViewModel を固定する。
// フィクスチャ（testdata/users_followers*.json）の中身は internal/xapi の users_fixture_test.go が
// 固定しているので、ここではその値を前提に、表示モデルへの対応づけを確かめる。

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shutx-net/seekx/internal/xapi"
)

// フィクスチャのページングトークン（testdata/users_followers_<トークン>.json のファイル名でもある）。
const (
	followersPage2Token = "FIXTUREPAGE2ZZZZ"
	followersPage3Token = "FIXTUREPAGE3ZZZZ"
)

// viewUserIDs は users の ID を順にカンマでつなぐ（並び順と過不足をまとめて比べるため）。
func viewUserIDs(users []ViewUser) string {
	ids := make([]string, len(users))
	for i, u := range users {
		ids[i] = u.ID
	}
	return strings.Join(ids, ",")
}

// viewUsersByID は users を ID で引けるようにする。
func viewUsersByID(users []ViewUser) map[string]ViewUser {
	m := make(map[string]ViewUser, len(users))
	for _, u := range users {
		m[u.ID] = u
	}
	return m
}

func TestBuildUserView(t *testing.T) {
	loc := jst(t)
	// UTC と JST で日付が変わる時刻（UTC の 15:30 は JST の翌日 00:30）。フィクスチャの created_at は
	// UTC の 0 時で JST でも同じ日付になり、表示タイムゾーンの取り違えを検出できないので、ここで作る。
	created := time.Date(2015, 6, 7, 15, 30, 0, 0, time.UTC)
	base := xapi.User{
		ID:              "30000001",
		Name:            "サンプル ファン",
		Username:        "sample_fan01",
		ProfileImageURL: "https://pbs.twimg.com/profile_images/3000000000000000001/fan01_normal.jpg",
		Verified:        true,
		VerifiedType:    "blue",
		Description:     "猫と写真が好きです。\n東京在住。",
		Location:        "東京",
		URL:             "https://t.co/fixture02",
		Protected:       true,
		CreatedAt:       created,
		PublicMetrics:   &xapi.UserMetrics{FollowersCount: 120, FollowingCount: 80, TweetCount: 4567, ListedCount: 3},
	}

	t.Run("各フィールドを対応づける", func(t *testing.T) {
		got := BuildUserView(base, loc)
		want := ViewUser{
			ID:               "30000001",
			Name:             "サンプル ファン",
			Username:         "sample_fan01",
			ProfileURL:       "https://x.com/sample_fan01",
			ProfileImageURL:  "https://pbs.twimg.com/profile_images/3000000000000000001/fan01_200x200.jpg",
			Description:      "猫と写真が好きです。\n東京在住。",
			Location:         "東京",
			URL:              "https://t.co/fixture02",
			Verified:         true,
			VerifiedType:     "blue",
			Protected:        true,
			CreatedAtDisplay: "2015-06-08",
			CreatedAtISO:     "2015-06-07T15:30:00Z",
			Metrics:          ViewUserMetrics{Followers: 120, Following: 80, Posts: 4567, Listed: 3, HasMetrics: true},
		}
		if got != want {
			t.Errorf("BuildUserView の結果が違う:\n got  %+v\n want %+v", got, want)
		}
	})

	t.Run("プロフィール画像は _normal を 200x200 に置き換える（投稿カードの 400x400 にしない）", func(t *testing.T) {
		cases := []struct{ name, raw, want string }{
			{
				name: "pbs.twimg.com の画像",
				raw:  "https://pbs.twimg.com/profile_images/3000000000000000001/fan01_normal.jpg",
				want: "https://pbs.twimg.com/profile_images/3000000000000000001/fan01_200x200.jpg",
			},
			{
				name: "abs.twimg.com の既定アイコンも置き換える",
				raw:  "https://abs.twimg.com/sticky/default_profile_images/default_profile_normal.png",
				want: "https://abs.twimg.com/sticky/default_profile_images/default_profile_200x200.png",
			},
			{
				name: "画像が無ければ空のまま（テンプレートが代わりの表示を出す）",
				raw:  "",
				want: "",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				u := base
				u.ProfileImageURL = tc.raw
				if got := BuildUserView(u, loc).ProfileImageURL; got != tc.want {
					t.Errorf("got %q, want %q", got, tc.want)
				}
			})
		}
	})

	t.Run("表示名が空ならユーザー名を表示名にする（@ は付けない）", func(t *testing.T) {
		u := base
		u.Name = ""
		got := BuildUserView(u, loc)
		if got.Name != "sample_fan01" {
			t.Errorf("Name: got %q, want %q", got.Name, "sample_fan01")
		}
		if got.Username != "sample_fan01" {
			t.Errorf("Username を変えてはいけない: got %q", got.Username)
		}
	})

	t.Run("Web サイトは http:// か https:// で始まるものだけ残す", func(t *testing.T) {
		cases := []struct{ name, raw, want string }{
			{"https の t.co（対照）", "https://t.co/fixture02", "https://t.co/fixture02"},
			{"http（対照）", "http://example.com/a?b=c", "http://example.com/a?b=c"},
			{"javascript: は捨てる", "javascript:alert(1)", ""},
			{"大文字混じりの javascript: も捨てる", "JavaScript:alert(1)", ""},
			{"data: は捨てる", "data:text/html,<script>alert(1)</script>", ""},
			{"スキームの無い // は捨てる", "//example.com", ""},
			{"スキームの無いホスト名は捨てる", "example.com", ""},
			{"ftp: は捨てる", "ftp://example.com", ""},
			{"空は空のまま", "", ""},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				u := base
				u.URL = tc.raw
				if got := BuildUserView(u, loc).URL; got != tc.want {
					t.Errorf("got %q, want %q", got, tc.want)
				}
			})
		}
	})

	t.Run("作成日は loc の日付で表示し、ISO は UTC の RFC 3339 にする", func(t *testing.T) {
		got := BuildUserView(base, loc)
		if got.CreatedAtDisplay != "2015-06-08" {
			t.Errorf("CreatedAtDisplay は JST の日付であるべき: got %q, want %q", got.CreatedAtDisplay, "2015-06-08")
		}
		if got.CreatedAtISO != "2015-06-07T15:30:00Z" {
			t.Errorf("CreatedAtISO: got %q, want %q", got.CreatedAtISO, "2015-06-07T15:30:00Z")
		}

		// 対照: UTC で表示すれば前日になる（loc を無視して固定のタイムゾーンを使っていないこと）。
		if d := BuildUserView(base, time.UTC).CreatedAtDisplay; d != "2015-06-07" {
			t.Errorf("UTC での CreatedAtDisplay: got %q, want %q", d, "2015-06-07")
		}

		// 入力が JST の time.Time でも ISO は UTC にそろえる（+09:00 にしない）。
		u := base
		u.CreatedAt = created.In(loc)
		if iso := BuildUserView(u, loc).CreatedAtISO; iso != "2015-06-07T15:30:00Z" {
			t.Errorf("CreatedAtISO は UTC であるべき: got %q, want %q", iso, "2015-06-07T15:30:00Z")
		}
	})

	t.Run("loc が nil でも落ちず UTC の日付で表示する", func(t *testing.T) {
		got := BuildUserView(base, nil)
		if got.CreatedAtDisplay != "2015-06-07" {
			t.Errorf("CreatedAtDisplay: got %q, want %q", got.CreatedAtDisplay, "2015-06-07")
		}
	})

	t.Run("作成日時がゼロなら表示も ISO も空にする（0001-01-01 を出さない）", func(t *testing.T) {
		u := base
		u.CreatedAt = time.Time{}
		got := BuildUserView(u, loc)
		if got.CreatedAtDisplay != "" || got.CreatedAtISO != "" {
			t.Errorf("空であるべき: display=%q iso=%q", got.CreatedAtDisplay, got.CreatedAtISO)
		}
	})

	t.Run("public_metrics が欠落していれば HasMetrics は false（0 人と区別する）", func(t *testing.T) {
		u := base
		u.PublicMetrics = nil
		if got := BuildUserView(u, loc).Metrics; got != (ViewUserMetrics{}) {
			t.Errorf("欠落時はゼロ値（HasMetrics false）であるべき: got %+v", got)
		}

		// 対照: 指標がすべて 0 でも、返っていれば HasMetrics は true。
		u.PublicMetrics = &xapi.UserMetrics{}
		if got := BuildUserView(u, loc).Metrics; got != (ViewUserMetrics{HasMetrics: true}) {
			t.Errorf("すべて 0 の指標は HasMetrics true であるべき: got %+v", got)
		}
	})

	t.Run("投稿数は post_count を優先し、無ければ tweet_count を使う", func(t *testing.T) {
		cases := []struct {
			name string
			in   xapi.UserMetrics
			want int
		}{
			{"post_count だけ", xapi.UserMetrics{PostCount: 12000}, 12000},
			{"tweet_count だけ", xapi.UserMetrics{TweetCount: 4567}, 4567},
			{"両方あれば post_count", xapi.UserMetrics{TweetCount: 1, PostCount: 2}, 2},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				u := base
				m := tc.in
				u.PublicMetrics = &m
				if got := BuildUserView(u, loc).Metrics.Posts; got != tc.want {
					t.Errorf("Posts: got %d, want %d", got, tc.want)
				}
			})
		}
	})

	t.Run("表示名・自己紹介・場所は加工しない（エスケープはテンプレート、無害化は CSV の責務）", func(t *testing.T) {
		u := base
		u.Name = "<img src=x onerror=alert(1)>"
		u.Description = "</script><script>alert(1)</script>\n2 行目"
		u.Location = `=HYPERLINK("https://example.com")`
		got := BuildUserView(u, loc)
		if got.Name != u.Name || got.Description != u.Description || got.Location != u.Location {
			t.Errorf("値を加工してはいけない: got Name=%q Description=%q Location=%q", got.Name, got.Description, got.Location)
		}
	})
}

func TestBuildFollowersView(t *testing.T) {
	loc := jst(t)

	build := func(t *testing.T, fixture string) FollowersView {
		t.Helper()
		var resp xapi.FollowersResponse
		loadFixture(t, fixture, &resp)
		return BuildFollowersView(&resp, ViewOptions{Loc: loc})
	}

	t.Run("1 ページ目: 6 人を data の順に並べ、次のトークンと指標の欠落の警告を出す", func(t *testing.T) {
		v := build(t, "users_followers.json")
		if got, want := viewUserIDs(v.Users), "30000001,30000002,30000003,30000004,30000005,30000006"; got != want {
			t.Fatalf("ID の並び: got %s, want %s（data の順序を保つべき）", got, want)
		}
		if v.ResultCount != 6 {
			t.Errorf("ResultCount: got %d, want 6", v.ResultCount)
		}
		if v.NextToken != followersPage2Token {
			t.Errorf("NextToken: got %q, want %q", v.NextToken, followersPage2Token)
		}
		if v.PrevToken != "" {
			t.Errorf("1 ページ目に PrevToken があってはいけない: got %q", v.PrevToken)
		}
		// 指標の欠落は 30000006 の 1 人だけ。
		if want := []string{"1 人のフォロワー数などの指標を取得できませんでした。"}; !slices.Equal(v.Warnings, want) {
			t.Errorf("Warnings: got %q, want %q", v.Warnings, want)
		}
		if len(v.PartialErrors) != 0 {
			t.Errorf("1 ページ目に部分エラーがあってはいけない: %q", v.PartialErrors)
		}

		byID := viewUsersByID(v.Users)
		// 投稿数の名前（tweet_count / post_count）で 01 と 02 が互いの対照になる。
		if got, want := byID["30000001"].Metrics, (ViewUserMetrics{Followers: 120, Following: 80, Posts: 4567, Listed: 3, HasMetrics: true}); got != want {
			t.Errorf("30000001 の指標（tweet_count 由来の投稿数）: got %+v, want %+v", got, want)
		}
		if got, want := byID["30000002"].Metrics, (ViewUserMetrics{Followers: 5400, Following: 310, Posts: 12000, Listed: 21, HasMetrics: true}); got != want {
			t.Errorf("30000002 の指標（post_count 由来の投稿数）: got %+v, want %+v", got, want)
		}
		// 「0」と「不明」: 05 は指標が返っていて多くが 0、06 は public_metrics が欠落している。
		if got, want := byID["30000005"].Metrics, (ViewUserMetrics{Following: 5, HasMetrics: true}); got != want {
			t.Errorf("30000005 の指標（0 人は不明ではない）: got %+v, want %+v", got, want)
		}
		if got := byID["30000006"].Metrics; got.HasMetrics {
			t.Errorf("30000006 は public_metrics が欠落しているので HasMetrics は false であるべき: got %+v", got)
		}

		if got, want := byID["30000005"].ProfileImageURL, "https://abs.twimg.com/sticky/default_profile_images/default_profile_200x200.png"; got != want {
			t.Errorf("30000005 の既定アイコン: got %q, want %q", got, want)
		}
		u1 := byID["30000001"]
		if u1.ProfileURL != "https://x.com/sample_fan01" {
			t.Errorf("30000001 の ProfileURL: got %q", u1.ProfileURL)
		}
		// created_at は UTC の 0 時なので、JST でも同じ日付になる。
		if u1.CreatedAtDisplay != "2015-06-07" || u1.CreatedAtISO != "2015-06-07T00:00:00Z" {
			t.Errorf("30000001 の作成日: display=%q iso=%q", u1.CreatedAtDisplay, u1.CreatedAtISO)
		}
		if !byID["30000003"].Protected || u1.Protected {
			t.Errorf("鍵アカウントは 30000003 だけであるべき: 30000003=%v 30000001=%v", byID["30000003"].Protected, u1.Protected)
		}
		if got, want := byID["30000006"].VerifiedType, "government"; !byID["30000006"].Verified || got != want {
			t.Errorf("30000006 の認証: verified=%v type=%q", byID["30000006"].Verified, got)
		}
		if got, want := byID["30000004"].Name, `=HYPERLINK("https://example.com")`; got != want {
			t.Errorf("表示名を加工してはいけない（無害化は CSV の責務）: got %q, want %q", got, want)
		}
	})

	t.Run("2 ページ目: 全員が指標を持てば警告を出さない", func(t *testing.T) {
		v := build(t, "users_followers_"+followersPage2Token+".json")
		if got, want := viewUserIDs(v.Users), "30000007,30000008,30000009,30000010,30000011"; got != want {
			t.Fatalf("ID の並び: got %s, want %s", got, want)
		}
		if v.NextToken != followersPage3Token || v.PrevToken != "" {
			t.Errorf("トークン: next=%q prev=%q, want next=%q prev=%q", v.NextToken, v.PrevToken, followersPage3Token, "")
		}
		if len(v.Warnings) != 0 || len(v.PartialErrors) != 0 {
			t.Errorf("警告も部分エラーも出してはいけない: warnings=%q partial=%q", v.Warnings, v.PartialErrors)
		}
	})

	t.Run("3 ページ目: 前のトークンと部分エラーを出し、次のトークンは無い", func(t *testing.T) {
		v := build(t, "users_followers_"+followersPage3Token+".json")
		if got, want := viewUserIDs(v.Users), "30000012,30000013"; got != want {
			t.Fatalf("ID の並び: got %s, want %s", got, want)
		}
		if v.ResultCount != 2 {
			t.Errorf("ResultCount: got %d, want 2", v.ResultCount)
		}
		if v.PrevToken != followersPage2Token {
			t.Errorf("PrevToken: got %q, want %q", v.PrevToken, followersPage2Token)
		}
		if v.NextToken != "" {
			t.Errorf("最終ページに NextToken があってはいけない: got %q", v.NextToken)
		}
		if want := []string{"Could not find user with id: [30000999]."}; !slices.Equal(v.PartialErrors, want) {
			t.Errorf("PartialErrors: got %q, want %q", v.PartialErrors, want)
		}
		if len(v.Warnings) != 0 {
			t.Errorf("全員が指標を持つので警告を出してはいけない: %q", v.Warnings)
		}
	})

	t.Run("鍵アカウント: data が無く errors だけなら 0 人で、エラーを部分エラーに出す", func(t *testing.T) {
		v := build(t, "users_followers_20000002.json")
		if len(v.Users) != 0 || v.ResultCount != 0 {
			t.Errorf("0 人であるべき: users=%d ResultCount=%d", len(v.Users), v.ResultCount)
		}
		if want := []string{"Sorry, you are not authorized to see the user with id: [20000002]."}; !slices.Equal(v.PartialErrors, want) {
			t.Errorf("PartialErrors: got %q, want %q", v.PartialErrors, want)
		}
		if len(v.Warnings) != 0 {
			t.Errorf("警告を出してはいけない: %q", v.Warnings)
		}
	})

	t.Run("フォロワー 0 人: 警告も部分エラーも出さない", func(t *testing.T) {
		v := build(t, "users_followers_20000003.json")
		if len(v.Users) != 0 || v.ResultCount != 0 || v.NextToken != "" {
			t.Errorf("0 人で次のページも無いべき: users=%d ResultCount=%d next=%q", len(v.Users), v.ResultCount, v.NextToken)
		}
		if len(v.Warnings) != 0 || len(v.PartialErrors) != 0 {
			t.Errorf("警告も部分エラーも出してはいけない: warnings=%q partial=%q", v.Warnings, v.PartialErrors)
		}
	})

	t.Run("data の順序を並べ替えない", func(t *testing.T) {
		// フィクスチャは ID の昇順に並んでいて、並べ替えても順序が変わらないので、ここで崩した順を作る。
		resp := xapi.FollowersResponse{
			Data: []xapi.User{
				{ID: "30000003", Username: "c"},
				{ID: "30000001", Username: "b"},
				{ID: "30000002", Username: "a"},
			},
			Meta: xapi.Meta{ResultCount: 3},
		}
		v := BuildFollowersView(&resp, ViewOptions{Loc: loc})
		if got, want := viewUserIDs(v.Users), "30000003,30000001,30000002"; got != want {
			t.Errorf("ID の並び: got %s, want %s", got, want)
		}
	})

	t.Run("指標の欠落は人数を数えて 1 件の警告にまとめる", func(t *testing.T) {
		resp := xapi.FollowersResponse{
			Data: []xapi.User{
				{ID: "1", Username: "a"},
				{ID: "2", Username: "b", PublicMetrics: &xapi.UserMetrics{}},
				{ID: "3", Username: "c"},
			},
			Meta: xapi.Meta{ResultCount: 3},
		}
		v := BuildFollowersView(&resp, ViewOptions{Loc: loc})
		if want := []string{"2 人のフォロワー数などの指標を取得できませんでした。"}; !slices.Equal(v.Warnings, want) {
			t.Errorf("Warnings: got %q, want %q", v.Warnings, want)
		}
	})

	t.Run("件数は data の人数にし、meta と食い違えばログだけに残す", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, nil))
		resp := xapi.FollowersResponse{
			Data: []xapi.User{
				{ID: "1", Username: "a", PublicMetrics: &xapi.UserMetrics{}},
				{ID: "2", Username: "b", PublicMetrics: &xapi.UserMetrics{}},
			},
			Meta: xapi.Meta{ResultCount: 5},
		}
		v := BuildFollowersView(&resp, ViewOptions{Loc: loc, Log: logger})
		if v.ResultCount != 2 {
			t.Errorf("ResultCount はカードの枚数（data の人数）であるべき: got %d, want 2", v.ResultCount)
		}
		logs := buf.String()
		for _, want := range []string{"level=WARN", "result_count=5", "users=2"} {
			if !strings.Contains(logs, want) {
				t.Errorf("食い違いを警告ログに残すべき（%s が無い）: %q", want, logs)
			}
		}
		if len(v.Warnings) != 0 {
			t.Errorf("画面の警告に出してはいけない: %q", v.Warnings)
		}

		// 対照: 一致していればログを出さない。
		buf.Reset()
		resp.Meta.ResultCount = 2
		BuildFollowersView(&resp, ViewOptions{Loc: loc, Log: logger})
		if buf.Len() != 0 {
			t.Errorf("一致しているときはログを出してはいけない: %q", buf.String())
		}
	})

	t.Run("コスト・レートリミット・フィクスチャモードは opt から受け取る", func(t *testing.T) {
		var resp xapi.FollowersResponse
		loadFixture(t, "users_followers.json", &resp)
		rl := &xapi.RateLimit{Limit: 15, Remaining: 14, Reset: time.Date(2026, 8, 13, 0, 15, 0, 0, time.UTC)}
		v := BuildFollowersView(&resp, ViewOptions{Loc: loc, RateLimit: rl, SpentUSD: 0.06, FixtureMode: true})
		if v.RateLimit != rl {
			t.Errorf("RateLimit は opt の値をそのまま渡すべき: got %+v", v.RateLimit)
		}
		if v.SpentUSD != 0.06 {
			t.Errorf("SpentUSD: got %v, want 0.06", v.SpentUSD)
		}
		if !v.FixtureMode {
			t.Error("FixtureMode が true になるべき")
		}

		// 対照: opt がゼロ値なら、どれもゼロ値のまま（固定値を入れていないこと）。
		z := BuildFollowersView(&resp, ViewOptions{Loc: loc})
		if z.RateLimit != nil || z.SpentUSD != 0 || z.FixtureMode {
			t.Errorf("ゼロ値であるべき: RateLimit=%+v SpentUSD=%v FixtureMode=%v", z.RateLimit, z.SpentUSD, z.FixtureMode)
		}
	})

	t.Run("resp が nil でも落ちず、opt から受け取る値だけが入る", func(t *testing.T) {
		rl := &xapi.RateLimit{Limit: 15}
		v := BuildFollowersView(nil, ViewOptions{Loc: loc, RateLimit: rl, SpentUSD: 0.01, FixtureMode: true})
		if len(v.Users) != 0 || v.ResultCount != 0 || v.NextToken != "" || v.PrevToken != "" {
			t.Errorf("空であるべき: %+v", v)
		}
		if len(v.Warnings) != 0 || len(v.PartialErrors) != 0 {
			t.Errorf("警告も部分エラーも無いべき: warnings=%q partial=%q", v.Warnings, v.PartialErrors)
		}
		if v.RateLimit != rl || v.SpentUSD != 0.01 || !v.FixtureMode {
			t.Errorf("opt の値は入るべき: RateLimit=%+v SpentUSD=%v FixtureMode=%v", v.RateLimit, v.SpentUSD, v.FixtureMode)
		}
	})
}
