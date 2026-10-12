package xapi

// users_fixture_test.go は testdata/ のユーザー検索・フォロワー取得のフィクスチャを、フィクスチャモードの
// Client（fixtureCandidates の解決規則）を通して読み、中身を固定する。
//
// フィクスチャは課金ゼロで画面を確かめるためのもので、internal/view と cmd/seekx のテストや
// DEVELOPERS.md が、ここで固定する値（ID・人数・トークン・確認用のデータ）を前提にする。
// 確認用のデータ（XSS・CSV インジェクション・鍵アカウント・指標の欠落）を無害な値に直すと、
// それを使う側のテストは確かめる対象を失ったまま通ってしまうので、ここで先に落とす。

import (
	"context"
	"strings"
	"testing"
)

// フィクスチャのページングトークン。testdata/users_followers_<トークン>.json のファイル名でもある
// （トークンのファイル名は大文字小文字を区別する）。
const (
	fixturePage2Token = "FIXTUREPAGE2ZZZZ"
	fixturePage3Token = "FIXTUREPAGE3ZZZZ"
)

// userIDs は users の ID を順にカンマでつなぐ（並び順と過不足をまとめて比べるため）。
func userIDs(users []User) string {
	ids := make([]string, len(users))
	for i, u := range users {
		ids[i] = u.ID
	}
	return strings.Join(ids, ",")
}

// TestUserFixturesInTestdata は通常フロー（任意のユーザー名の見積り → 3 ページのフォロワー取得）の
// フィクスチャを固定する。見積り対象は 12,345 人（全件 $123.45・100 人/ページで 124 ページ）、
// フォロワーは 6 人 → 5 人 → 2 人で、最終ページに部分エラーがある。
func TestUserFixturesInTestdata(t *testing.T) {
	c := NewClient(Options{FixtureDir: fixtureDir}, discardLogger())
	ctx := context.Background()

	// page は見積り対象（20000001）のフォロワーを 1 ページ読む。token が空なら 1 ページ目。
	page := func(t *testing.T, token string) *FollowersResponse {
		t.Helper()
		resp, _, err := c.Followers(ctx, FollowersParams{
			UserID:          "20000001",
			MaxResults:      100,
			PaginationToken: token,
			UserFields:      UserProfileFieldsRequested,
		})
		if err != nil {
			t.Fatalf("トークン %q のページを読めるべき: %v", token, err)
		}
		return resp
	}

	t.Run("個別のファイルが無いユーザー名は見積り対象の sample_star になる", func(t *testing.T) {
		resp, _, err := c.UserByUsername(ctx, UserLookupParams{Username: "nasa", UserFields: UserProfileFieldsRequested})
		if err != nil {
			t.Fatalf("予期しないエラー: %v", err)
		}
		u := resp.Data
		if u == nil {
			t.Fatalf("既定のファイル（users_by_username.json）のユーザーが返るべき: errors=%+v", resp.Errors)
		}
		if u.ID != "20000001" || u.Username != "sample_star" {
			t.Errorf("ID / Username: got %q / %q, want %q / %q", u.ID, u.Username, "20000001", "sample_star")
		}
		if u.PublicMetrics == nil || u.PublicMetrics.FollowersCount != 12345 {
			t.Errorf("followers_count は 12345 であるべき（見積り 12,345 人・全件 $123.45・124 ページの前提）: got %+v", u.PublicMetrics)
		}
		if u.Protected {
			t.Error("見積り対象を鍵アカウントにしてはいけない（鍵の警告が出て、通常フローを確かめられない）")
		}
		if len(resp.Errors) != 0 {
			t.Errorf("errors があってはいけない: %+v", resp.Errors)
		}
	})

	t.Run("1 ページ目: 6 人と 2 ページ目のトークン", func(t *testing.T) {
		resp := page(t, "")
		if got, want := userIDs(resp.Data), "30000001,30000002,30000003,30000004,30000005,30000006"; got != want {
			t.Fatalf("ID の並び: got %s, want %s", got, want)
		}
		if resp.Meta.ResultCount != len(resp.Data) {
			t.Errorf("result_count は data の人数と一致するべき: got %d, want %d", resp.Meta.ResultCount, len(resp.Data))
		}
		if resp.Meta.NextToken != fixturePage2Token {
			t.Errorf("next_token: got %q, want %q", resp.Meta.NextToken, fixturePage2Token)
		}
		if resp.Meta.PreviousToken != "" {
			t.Errorf("1 ページ目に previous_token があってはいけない: got %q", resp.Meta.PreviousToken)
		}
		if len(resp.Errors) != 0 {
			t.Errorf("1 ページ目に errors があってはいけない: %+v", resp.Errors)
		}

		byID := map[string]User{}
		for _, u := range resp.Data {
			byID[u.ID] = u
		}
		// 各ユーザーの確認用のデータ。01 と 02 は投稿数の名前（tweet_count / post_count）で互いの対照になる。
		checks := []struct {
			id, want, purpose string
			ok                func(u User) bool
		}{
			{"30000001", "改行入りの description・location・url・tweet_count の指標をすべて持つ", "通常のカード", func(u User) bool {
				pm := u.PublicMetrics
				return u.Description == "猫と写真が好きです。\n東京在住。" && u.Location != "" && u.URL != "" &&
					pm != nil && pm.TweetCount != 0 && pm.PostCount == 0
			}},
			{"30000002", "認証済み（blue）で、投稿数を tweet_count ではなく post_count で持つ", "投稿数の互換経路", func(u User) bool {
				pm := u.PublicMetrics
				return u.Verified && u.VerifiedType == "blue" &&
					pm != nil && pm.TweetCount == 0 && pm.PostCount != 0 && pm.Posts() == pm.PostCount
			}},
			{"30000003", "鍵アカウントで、description が空である", "鍵の表示と自己紹介なし", func(u User) bool {
				return u.Protected && u.Description == ""
			}},
			{"30000004", "表示名・description・location がそれぞれ =、@、- で始まる", "CSV インジェクション", func(u User) bool {
				return u.Name == `=HYPERLINK("https://example.com")` &&
					u.Description == "@sample_star さんのファンです" && u.Location == "-"
			}},
			{"30000005", "表示名と description に HTML と </script> を含み、アイコンが abs.twimg.com の既定画像である", "XSS と既定アイコン", func(u User) bool {
				return u.Name == "<img src=x onerror=alert(1)>" &&
					u.Description == "</script><script>alert(1)</script>\n2 行目" &&
					u.ProfileImageURL == "https://abs.twimg.com/sticky/default_profile_images/default_profile_normal.png"
			}},
			{"30000006", "認証済み（government）で、public_metrics が欠落している", "指標不明", func(u User) bool {
				return u.Verified && u.VerifiedType == "government" && u.PublicMetrics == nil
			}},
		}
		for _, ck := range checks {
			u := byID[ck.id]
			if !ck.ok(u) {
				t.Errorf("%s（@%s）は%sべき（%sの確認用）: got %+v, public_metrics=%+v", ck.id, u.Username, ck.want, ck.purpose, u, u.PublicMetrics)
			}
		}

		// 指標の欠落は 30000006 だけ（画面の「指標を取得できませんでした」の警告が 1 人分になる前提）。
		var missing []string
		for _, u := range resp.Data {
			if u.PublicMetrics == nil {
				missing = append(missing, u.ID)
			}
		}
		if got := strings.Join(missing, ","); got != "30000006" {
			t.Errorf("public_metrics が欠落しているのは 30000006 だけであるべき: got %q", got)
		}
	})

	t.Run("2 ページ目: next_token でたどると 5 人と 3 ページ目のトークン", func(t *testing.T) {
		resp := page(t, fixturePage2Token)
		if got, want := userIDs(resp.Data), "30000007,30000008,30000009,30000010,30000011"; got != want {
			t.Fatalf("ID の並び（1 ページ目と重なってはいけない）: got %s, want %s", got, want)
		}
		if resp.Meta.ResultCount != len(resp.Data) {
			t.Errorf("result_count は data の人数と一致するべき: got %d, want %d", resp.Meta.ResultCount, len(resp.Data))
		}
		if resp.Meta.NextToken != fixturePage3Token {
			t.Errorf("next_token: got %q, want %q", resp.Meta.NextToken, fixturePage3Token)
		}
		// 【previous_token を付けない】1 ページ目はトークン無しで読むファイルなので、トークンで指すには
		// 同じ内容のファイルをもう 1 つ置くことになる。前へのリンクは 3 ページ目で確かめる。
		if resp.Meta.PreviousToken != "" {
			t.Errorf("2 ページ目に previous_token を付けてはいけない: got %q", resp.Meta.PreviousToken)
		}
		for _, u := range resp.Data {
			if u.PublicMetrics == nil {
				t.Errorf("2 ページ目は全員が public_metrics を持つべき: %s（@%s）", u.ID, u.Username)
			}
		}
		if len(resp.Errors) != 0 {
			t.Errorf("2 ページ目に errors があってはいけない: %+v", resp.Errors)
		}
	})

	t.Run("3 ページ目: 最終ページで、2 ページ目へ戻るトークンと部分エラーがある", func(t *testing.T) {
		resp := page(t, fixturePage3Token)
		if got, want := userIDs(resp.Data), "30000012,30000013"; got != want {
			t.Fatalf("ID の並び（前のページと重なってはいけない）: got %s, want %s", got, want)
		}
		if resp.Meta.ResultCount != len(resp.Data) {
			t.Errorf("result_count は data の人数と一致するべき: got %d, want %d", resp.Meta.ResultCount, len(resp.Data))
		}
		if resp.Meta.NextToken != "" {
			t.Errorf("最終ページに next_token があってはいけない: got %q", resp.Meta.NextToken)
		}
		if resp.Meta.PreviousToken != fixturePage2Token {
			t.Errorf("previous_token は 2 ページ目を指すべき: got %q, want %q", resp.Meta.PreviousToken, fixturePage2Token)
		}
		if len(resp.Errors) != 1 {
			t.Fatalf("部分エラーは 1 件であるべき: got %+v", resp.Errors)
		}
		if !resp.Errors[0].IsNotFound() {
			t.Errorf("部分エラーは resource-not-found であるべき: got type %q", resp.Errors[0].Type)
		}
		if resp.Errors[0].IsNotAuthorized() {
			t.Error("部分エラーを IsNotAuthorized と判定してはいけない")
		}
	})

	t.Run("トークンは入力検証を通る長さである", func(t *testing.T) {
		// 短いと、画面の前後リンクに載せたトークンが送る前の検証で弾かれ、ページをたどれない。
		for _, token := range []string{fixturePage2Token, fixturePage3Token} {
			if len(token) < PaginationTokenMinLen {
				t.Errorf("トークン %q は %d 文字以上であるべき: got %d 文字", token, PaginationTokenMinLen, len(token))
			}
		}
	})
}

// TestUserFixtureScenarios は例外の画面を確かめるフィクスチャを固定する。環境変数を増やさず、
// ユーザー名だけで切り替える（ユーザー検索は users_by_username_<小文字の名前>.json、フォロワー取得は
// ユーザー検索が返した ID の users_followers_<ID>.json）。
//
//   - fx_protected: 鍵アカウント。ユーザーは返るが、フォロワー取得は 200 で data が無く errors だけ
//     （App-only で読んだときの想定形で未実測。401 / 403 で返る場合は既存のエラー画面が扱う）
//   - fx_empty: フォロワー 0 人（errors ではない）
//   - fx_notfound: 存在しないユーザー（200 で data が無く errors だけ）
//
// この 3 つの名前は cmd/seekx の fixtureFollowerExamples と DEVELOPERS.md にも載る（変えるなら両方とも）。
func TestUserFixtureScenarios(t *testing.T) {
	c := NewClient(Options{FixtureDir: fixtureDir}, discardLogger())
	ctx := context.Background()

	lookup := func(t *testing.T, username string) *UserLookupResponse {
		t.Helper()
		resp, _, err := c.UserByUsername(ctx, UserLookupParams{Username: username, UserFields: UserProfileFieldsRequested})
		if err != nil {
			t.Fatalf("%q のユーザー検索をエラーにしてはいけない（errors だけの 200 も扱いは呼び出し側が決める）: %v", username, err)
		}
		return resp
	}
	// followersOf は、見積り画面の実行リンクと同じく、ユーザー検索が返した ID でフォロワーを読む。
	// ID とフィクスチャのファイル名がずれると既定のページ（6 人）に落ちるので、それも検出できる。
	followersOf := func(t *testing.T, u *User) *FollowersResponse {
		t.Helper()
		resp, _, err := c.Followers(ctx, FollowersParams{UserID: u.ID, MaxResults: 100, UserFields: UserProfileFieldsRequested})
		if err != nil {
			t.Fatalf("ID %s のフォロワー取得をエラーにしてはいけない: %v", u.ID, err)
		}
		return resp
	}

	t.Run("鍵アカウント: 大文字混じりの名前でも小文字のファイルに当たり、フォロワー取得は errors だけ", func(t *testing.T) {
		resp := lookup(t, "FX_Protected")
		u := resp.Data
		if u == nil {
			t.Fatalf("users_by_username_fx_protected.json のユーザーが返るべき: errors=%+v", resp.Errors)
		}
		if u.ID != "20000002" || u.Username != "fx_protected" {
			t.Fatalf("ID / Username: got %q / %q, want %q / %q（既定のファイルに落ちていないか）", u.ID, u.Username, "20000002", "fx_protected")
		}
		if !u.Protected {
			t.Error("protected: true であるべき（見積り画面の鍵アカウントの警告を確かめる）")
		}

		f := followersOf(t, u)
		if len(f.Data) != 0 {
			t.Errorf("鍵アカウントのフォロワーが返ってはいけない（既定のページに落ちていないか）: got %s", userIDs(f.Data))
		}
		if f.Meta.ResultCount != 0 || f.Meta.NextToken != "" {
			t.Errorf("meta はゼロ値であるべき: got %+v", f.Meta)
		}
		if len(f.Errors) != 1 {
			t.Fatalf("errors は 1 件であるべき: got %+v", f.Errors)
		}
		if !f.Errors[0].IsNotAuthorized() {
			t.Errorf("not-authorized-for-resource であるべき: got type %q", f.Errors[0].Type)
		}
		if f.Errors[0].IsNotFound() {
			t.Error("鍵アカウントのエラーを IsNotFound と判定してはいけない")
		}
	})

	t.Run("フォロワー 0 人: 指標は 0 で、フォロワー取得は 0 人（errors ではない）", func(t *testing.T) {
		resp := lookup(t, "fx_empty")
		u := resp.Data
		if u == nil {
			t.Fatalf("users_by_username_fx_empty.json のユーザーが返るべき: errors=%+v", resp.Errors)
		}
		if u.ID != "20000003" || u.Username != "fx_empty" {
			t.Fatalf("ID / Username: got %q / %q, want %q / %q（既定のファイルに落ちていないか）", u.ID, u.Username, "20000003", "fx_empty")
		}
		// 「0 人」と「不明」を区別する。public_metrics が欠落すると見積りが「不明」になり、0 人の画面を確かめられない。
		if u.PublicMetrics == nil {
			t.Fatal("public_metrics を持つべき（欠落すると 0 人ではなく不明になる）")
		}
		if u.PublicMetrics.FollowersCount != 0 {
			t.Errorf("followers_count は 0 であるべき: got %d", u.PublicMetrics.FollowersCount)
		}
		if u.Protected {
			t.Error("鍵アカウントにしてはいけない（鍵の警告が混ざり、0 人の画面だけを確かめられない）")
		}

		f := followersOf(t, u)
		if len(f.Data) != 0 || f.Meta.ResultCount != 0 {
			t.Errorf("フォロワーは 0 人であるべき（既定のページに落ちていないか）: got data=%s result_count=%d", userIDs(f.Data), f.Meta.ResultCount)
		}
		if f.Meta.NextToken != "" {
			t.Errorf("0 人のページに next_token があってはいけない: got %q", f.Meta.NextToken)
		}
		if len(f.Errors) != 0 {
			t.Errorf("0 人を errors にしてはいけない（取得できなかった画面になる）: %+v", f.Errors)
		}
	})

	t.Run("存在しないユーザー: data が無く errors（resource-not-found）だけ", func(t *testing.T) {
		resp := lookup(t, "fx_notfound")
		if resp.Data != nil {
			t.Fatalf("data があってはいけない（既定のファイルに落ちていないか）: got %+v", *resp.Data)
		}
		if len(resp.Errors) != 1 {
			t.Fatalf("errors は 1 件であるべき: got %+v", resp.Errors)
		}
		if !resp.Errors[0].IsNotFound() {
			t.Errorf("resource-not-found であるべき: got type %q", resp.Errors[0].Type)
		}
		if resp.Errors[0].IsNotAuthorized() {
			t.Error("存在しないユーザーのエラーを IsNotAuthorized と判定してはいけない")
		}
	})
}
