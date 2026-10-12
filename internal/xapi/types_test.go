package xapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fixtureDir はリポジトリ直下の testdata を指す（go test の cwd はパッケージディレクトリ）。
const fixtureDir = "../../testdata"

func loadFixture(t *testing.T, name string, out any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("フィクスチャ %s の読み込みに失敗: %v", name, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("フィクスチャ %s の Unmarshal に失敗: %v", name, err)
	}
}

func TestFlexInt(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    int
		wantErr bool
	}{
		{"文字列の数値", `"2000000"`, 2000000, false},
		{"素の数値", `2000000`, 2000000, false},
		{"null", `null`, 0, false},
		{"空文字", `""`, 0, false},
		{"不正値", `"abc"`, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var f flexInt
			err := json.Unmarshal([]byte(tc.in), &f)
			if tc.wantErr {
				if err == nil {
					t.Fatal("エラーになるべき")
				}
				return
			}
			if err != nil {
				t.Fatalf("予期しないエラー: %v", err)
			}
			if f.Int() != tc.want {
				t.Errorf("got %d, want %d", f.Int(), tc.want)
			}
		})
	}

	t.Run("usage フィクスチャ（数値）", func(t *testing.T) {
		var u UsageResponse
		loadFixture(t, "usage.json", &u)
		if u.Data.ProjectCap.Int() != 2000000 {
			t.Errorf("project_cap: got %d", u.Data.ProjectCap.Int())
		}
		if u.Data.ProjectUsage.Int() != 42 {
			t.Errorf("project_usage: got %d", u.Data.ProjectUsage.Int())
		}
		if u.Data.CapResetDay != 12 {
			t.Errorf("cap_reset_day: got %d", u.Data.CapResetDay)
		}
	})

	t.Run("usage フィクスチャ（project_usage が文字列）", func(t *testing.T) {
		var u UsageResponse
		loadFixture(t, "usage_string_usage.json", &u)
		if u.Data.ProjectUsage.Int() != 1920000 {
			t.Errorf("project_usage: got %d", u.Data.ProjectUsage.Int())
		}
	})
}

// TestCountsFieldNames は counts のフィールド名が tweet_count / total_tweet_count であることを固定する。
// ドキュメントの post_count / total_post_count に「修正」すると失敗する（DR-01 / P-COUNTS-NAMES）。
func TestCountsFieldNames(t *testing.T) {
	var c CountsResponse
	loadFixture(t, "counts_recent.json", &c)

	if c.Meta.TotalPostCount != 10000 {
		t.Errorf("meta.total_tweet_count が読めていない: got %d, want 10000", c.Meta.TotalPostCount)
	}
	if len(c.Data) != 7 {
		t.Fatalf("バケット 7 件: got %d", len(c.Data))
	}
	if c.Data[0].PostCount != 1204 {
		t.Errorf("data[0].tweet_count が読めていない: got %d, want 1204", c.Data[0].PostCount)
	}
	if c.Data[0].Start.IsZero() || c.Data[0].End.IsZero() {
		t.Error("バケットの start / end が読めていない")
	}

	// post_count / total_post_count という名前では読めないことを明示する。
	type wrongBucket struct {
		PostCount int `json:"post_count"`
	}
	type wrongResp struct {
		Data []wrongBucket `json:"data"`
		Meta struct {
			TotalPostCount int `json:"total_post_count"`
		} `json:"meta"`
	}
	b, err := os.ReadFile(filepath.Join(fixtureDir, "counts_recent.json"))
	if err != nil {
		t.Fatal(err)
	}
	var wrong wrongResp
	if err := json.Unmarshal(b, &wrong); err != nil {
		t.Fatal(err)
	}
	if wrong.Meta.TotalPostCount != 0 {
		t.Error("total_post_count は実レスポンスに存在しない（docs の誤り・DR-01）")
	}
	if len(wrong.Data) > 0 && wrong.Data[0].PostCount != 0 {
		t.Error("post_count は実レスポンスに存在しない（docs の誤り・DR-01）")
	}
}

// TestPostMetricsRepostCount は public_metrics のリポスト数フィールド名を固定する。
// 実 API（2026-08-13 実測）は repost_count を返す。retweet_count だけを見る実装にすると
// 値が常に 0 になり、エラーにならないため気付けない（R-03）。
//
// BuildView 経由での検証は internal/view の TestBuildViewRepostCount が担当する
// （xapi は internal/view を import できないため分割している）。
func TestPostMetricsRepostCount(t *testing.T) {
	t.Run("現行の repost_count を読む", func(t *testing.T) {
		var pm PostMetrics
		if err := json.Unmarshal([]byte(`{"repost_count":7,"reply_count":1,"like_count":2,"quote_count":3,"bookmark_count":4,"impression_count":5}`), &pm); err != nil {
			t.Fatal(err)
		}
		if pm.RepostCount != 7 {
			t.Errorf("RepostCount: got %d, want 7", pm.RepostCount)
		}
		if pm.Reposts() != 7 {
			t.Errorf("Reposts(): got %d, want 7", pm.Reposts())
		}
	})

	t.Run("レガシー名 retweet_count にフォールバックする", func(t *testing.T) {
		var pm PostMetrics
		if err := json.Unmarshal([]byte(`{"retweet_count":9}`), &pm); err != nil {
			t.Fatal(err)
		}
		if pm.RepostCount != 0 {
			t.Errorf("RepostCount は 0 のまま: got %d", pm.RepostCount)
		}
		if pm.Reposts() != 9 {
			t.Errorf("Reposts(): got %d, want 9（フォールバック）", pm.Reposts())
		}
	})

	t.Run("両方欠落なら 0", func(t *testing.T) {
		var pm PostMetrics
		if err := json.Unmarshal([]byte(`{"like_count":1}`), &pm); err != nil {
			t.Fatal(err)
		}
		if pm.Reposts() != 0 {
			t.Errorf("Reposts(): got %d", pm.Reposts())
		}
	})
}

// TestUserMetricsPosts はユーザーの投稿数のフィールド名の互換を固定する。
// tweet_count と post_count のどちらが返るかは未実測なので、どちらで返っても 0 にならないこと、
// 両方あれば post_count を優先することを確認する（PostMetrics.Reposts と同じ方針・R-03）。
func TestUserMetricsPosts(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"tweet_count だけなら tweet_count を使う", `{"followers_count":1,"tweet_count":9876}`, 9876},
		{"post_count だけなら post_count を使う", `{"followers_count":1,"post_count":321}`, 321},
		{"両方あれば post_count を優先する", `{"tweet_count":5,"post_count":7}`, 7},
		{"post_count が 0 なら tweet_count にフォールバックする", `{"tweet_count":5,"post_count":0}`, 5},
		{"両方欠落なら 0", `{"followers_count":1,"following_count":2,"listed_count":3}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m UserMetrics
			if err := json.Unmarshal([]byte(tc.in), &m); err != nil {
				t.Fatalf("Unmarshal に失敗: %v", err)
			}
			if got := m.Posts(); got != tc.want {
				t.Errorf("Posts(): got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestUserLookupResponseDecode はユーザー検索のレスポンスの 2 つの形を固定する。
// data は配列ではなくオブジェクト 1 件。存在しないユーザーは HTTP 200 で data が無く errors だけが返る。
func TestUserLookupResponseDecode(t *testing.T) {
	t.Run("data があればユーザー 1 件として読める", func(t *testing.T) {
		const in = `{"data":{"id":"20000001","name":"サンプル 星子","username":"sample_star","protected":false,` +
			`"public_metrics":{"followers_count":12345,"following_count":210,"tweet_count":9876,"listed_count":55}}}`
		var r UserLookupResponse
		if err := json.Unmarshal([]byte(in), &r); err != nil {
			t.Fatalf("Unmarshal に失敗: %v", err)
		}
		if r.Data == nil {
			t.Fatal("data があるのに Data が nil になってはいけない")
		}
		if r.Data.ID != "20000001" || r.Data.Username != "sample_star" {
			t.Errorf("ID / Username: got %q / %q", r.Data.ID, r.Data.Username)
		}
		if r.Data.PublicMetrics == nil || r.Data.PublicMetrics.FollowersCount != 12345 {
			t.Errorf("public_metrics.followers_count を読めるべき: got %+v", r.Data.PublicMetrics)
		}
		if len(r.Errors) != 0 {
			t.Errorf("errors が無いときは Errors が空であるべき: got %+v", r.Errors)
		}
	})

	t.Run("data が無く errors だけなら Data は nil で IsNotFound", func(t *testing.T) {
		const in = `{"errors":[{"value":"fx_notfound","detail":"Could not find user with username: [fx_notfound].",` +
			`"title":"Not Found Error","resource_type":"user","parameter":"username","resource_id":"fx_notfound",` +
			`"type":"https://api.twitter.com/2/problems/resource-not-found"}]}`
		var r UserLookupResponse
		if err := json.Unmarshal([]byte(in), &r); err != nil {
			t.Fatalf("Unmarshal に失敗: %v", err)
		}
		if r.Data != nil {
			t.Fatalf("data が無いときは Data が nil であるべき: got %+v", *r.Data)
		}
		if len(r.Errors) != 1 {
			t.Fatalf("errors は 1 件のはず: got %d", len(r.Errors))
		}
		if !r.Errors[0].IsNotFound() {
			t.Error("resource-not-found は IsNotFound と判定するべき")
		}
		if r.Errors[0].IsNotAuthorized() {
			t.Error("resource-not-found を IsNotAuthorized と判定してはいけない")
		}
		if got, want := r.Errors[0].Describe(), "Could not find user with username: [fx_notfound]."; got != want {
			t.Errorf("Describe(): got %q, want %q", got, want)
		}
	})
}

// TestProblemDetailKinds は errors[].type による判定を固定する。
// type のホストが api.twitter.com でも api.x.com でも同じに判定し、
// 途中に含むだけの type・区切りの / が無い type・title の文言では判定しない。
func TestProblemDetailKinds(t *testing.T) {
	cases := []struct {
		name              string
		typ               string
		title             string
		wantNotFound      bool
		wantNotAuthorized bool
	}{
		{"api.twitter.com の resource-not-found", "https://api.twitter.com/2/problems/resource-not-found", "Not Found Error", true, false},
		{"api.x.com の resource-not-found", "https://api.x.com/2/problems/resource-not-found", "Not Found Error", true, false},
		{"api.twitter.com の not-authorized-for-resource", "https://api.twitter.com/2/problems/not-authorized-for-resource", "Authorization Error", false, true},
		{"api.x.com の not-authorized-for-resource", "https://api.x.com/2/problems/not-authorized-for-resource", "Authorization Error", false, true},
		{"無関係な type（invalid-request）", "https://api.twitter.com/2/problems/invalid-request", "Invalid Request", false, false},
		{"無関係な type（client-forbidden）", "https://api.x.com/2/problems/client-forbidden", "Forbidden", false, false},
		{"resource-not-found を途中に含むだけ", "https://api.x.com/2/problems/resource-not-found/extra", "", false, false},
		{"resource-not-found の前に / が無い", "https://api.x.com/2/problems/no-resource-not-found", "", false, false},
		{"not-authorized-for-resource を途中に含むだけ", "https://api.x.com/2/problems/not-authorized-for-resource/extra", "", false, false},
		{"not-authorized-for-resource の前に / が無い", "https://api.x.com/2/problems/x-not-authorized-for-resource", "", false, false},
		{"type が空なら title が Not Found Error でも判定しない", "", "Not Found Error", false, false},
		{"type が空なら title が Authorization Error でも判定しない", "", "Authorization Error", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := ProblemDetail{Type: tc.typ, Title: tc.title}
			if got := p.IsNotFound(); got != tc.wantNotFound {
				t.Errorf("IsNotFound(%q): got %v, want %v", tc.typ, got, tc.wantNotFound)
			}
			if got := p.IsNotAuthorized(); got != tc.wantNotAuthorized {
				t.Errorf("IsNotAuthorized(%q): got %v, want %v", tc.typ, got, tc.wantNotAuthorized)
			}
		})
	}
}

// TestUserDecodesProfileFields はユーザー検索・フォロワー取得で要求するプロフィール項目を読めることを確認する。
// 検索の includes.users はこれらを要求しないので、欠落していてもゼロ値で読めること
// （User に項目を足しても検索を壊さないこと）もあわせて確認する。
func TestUserDecodesProfileFields(t *testing.T) {
	t.Run("description・location・url・protected・created_at・public_metrics を読める", func(t *testing.T) {
		const in = `{
  "id": "30000001",
  "name": "サンプル ファン",
  "username": "sample_fan01",
  "profile_image_url": "https://pbs.twimg.com/profile_images/3000000000000000001/fan01_normal.jpg",
  "verified": true,
  "verified_type": "blue",
  "description": "猫と写真が好きです。\n東京在住。",
  "location": "東京",
  "url": "https://t.co/fixture02",
  "protected": true,
  "created_at": "2015-06-07T08:09:10.000Z",
  "public_metrics": {"followers_count": 120, "following_count": 80, "tweet_count": 4567, "listed_count": 3}
}`
		var u User
		if err := json.Unmarshal([]byte(in), &u); err != nil {
			t.Fatalf("Unmarshal に失敗: %v", err)
		}
		// 既存の項目（対照）。プロフィール項目を足しても読めなくなってはいけない。
		if u.ID != "30000001" || u.Name != "サンプル ファン" || u.Username != "sample_fan01" {
			t.Errorf("ID / Name / Username: got %q / %q / %q", u.ID, u.Name, u.Username)
		}
		if !u.Verified || u.VerifiedType != "blue" {
			t.Errorf("Verified / VerifiedType: got %v / %q", u.Verified, u.VerifiedType)
		}
		if u.Description != "猫と写真が好きです。\n東京在住。" {
			t.Errorf("Description は改行を含めてそのまま読むべき: got %q", u.Description)
		}
		if u.Location != "東京" {
			t.Errorf("Location: got %q", u.Location)
		}
		if u.URL != "https://t.co/fixture02" {
			t.Errorf("URL: got %q", u.URL)
		}
		if !u.Protected {
			t.Error("protected: true を読めるべき")
		}
		if want := time.Date(2015, 6, 7, 8, 9, 10, 0, time.UTC); !u.CreatedAt.Equal(want) {
			t.Errorf("CreatedAt: got %v, want %v", u.CreatedAt, want)
		}
		if u.PublicMetrics == nil {
			t.Fatal("public_metrics があるのに PublicMetrics が nil になってはいけない")
		}
		pm := *u.PublicMetrics
		if pm.FollowersCount != 120 || pm.FollowingCount != 80 || pm.ListedCount != 3 {
			t.Errorf("followers_count / following_count / listed_count: got %d / %d / %d", pm.FollowersCount, pm.FollowingCount, pm.ListedCount)
		}
		if pm.Posts() != 4567 {
			t.Errorf("Posts(): got %d, want 4567", pm.Posts())
		}
	})

	t.Run("public_metrics が欠落したら PublicMetrics は nil（0 と区別する）", func(t *testing.T) {
		var u User
		if err := json.Unmarshal([]byte(`{"id":"30000006","name":"Sample Gov","username":"sample_gov","verified":true,"verified_type":"government"}`), &u); err != nil {
			t.Fatalf("Unmarshal に失敗: %v", err)
		}
		if u.PublicMetrics != nil {
			t.Errorf("public_metrics が無いときは nil であるべき（0 人として扱ってはいけない）: got %+v", *u.PublicMetrics)
		}
	})

	t.Run("検索の includes.users では要求していない項目がゼロ値になる", func(t *testing.T) {
		var r SearchResponse
		loadFixture(t, "search_recent.json", &r)
		if r.Includes == nil || len(r.Includes.Users) == 0 {
			t.Fatal("search_recent.json の includes.users が読めていない")
		}
		for _, u := range r.Includes.Users {
			if u.ID == "" || u.Username == "" || u.ProfileImageURL == "" {
				t.Errorf("既存の項目は従来どおり読めるべき: %+v", u)
			}
			if u.Description != "" || u.Location != "" || u.URL != "" || u.Protected || !u.CreatedAt.IsZero() || u.PublicMetrics != nil {
				t.Errorf("@%s: 要求していない項目はゼロ値であるべき: %+v", u.Username, u)
			}
		}
	})
}

// TestFollowersResponseDecode はフォロワー取得のレスポンスの JSON タグを固定する。
// data は配列、ページングは検索と同じ meta、部分成功エラーは errors に入る。
func TestFollowersResponseDecode(t *testing.T) {
	t.Run("data と meta を読める", func(t *testing.T) {
		const in = `{"data":[{"id":"30000001","username":"sample_fan01"},{"id":"30000002","username":"sample_fan02"}],` +
			`"meta":{"result_count":2,"next_token":"FIXTUREPAGE3ZZZZ","previous_token":"FIXTUREPAGE1ZZZZ"}}`
		var r FollowersResponse
		if err := json.Unmarshal([]byte(in), &r); err != nil {
			t.Fatalf("Unmarshal に失敗: %v", err)
		}
		if len(r.Data) != 2 {
			t.Fatalf("data は 2 人のはず: got %d", len(r.Data))
		}
		if r.Data[0].Username != "sample_fan01" || r.Data[1].Username != "sample_fan02" {
			t.Errorf("data を順に読めるべき: got %q, %q", r.Data[0].Username, r.Data[1].Username)
		}
		if r.Meta.ResultCount != 2 || r.Meta.NextToken != "FIXTUREPAGE3ZZZZ" || r.Meta.PreviousToken != "FIXTUREPAGE1ZZZZ" {
			t.Errorf("meta が読めていない: got %+v", r.Meta)
		}
		if len(r.Errors) != 0 {
			t.Errorf("errors が無いときは Errors が空であるべき: got %+v", r.Errors)
		}
	})

	t.Run("data も meta も無く errors だけでも読める", func(t *testing.T) {
		// 鍵アカウントのフォロワーを App-only で読んだときの想定形（未実測）。
		const in = `{"errors":[{"value":"20000002","detail":"Sorry, you are not authorized to see the user with id: [20000002].",` +
			`"title":"Authorization Error","resource_type":"user","parameter":"id","resource_id":"20000002",` +
			`"type":"https://api.twitter.com/2/problems/not-authorized-for-resource"}]}`
		var r FollowersResponse
		if err := json.Unmarshal([]byte(in), &r); err != nil {
			t.Fatalf("Unmarshal に失敗: %v", err)
		}
		if len(r.Data) != 0 || r.Meta.ResultCount != 0 || r.Meta.NextToken != "" {
			t.Errorf("data と meta はゼロ値であるべき: got data=%d meta=%+v", len(r.Data), r.Meta)
		}
		if len(r.Errors) != 1 {
			t.Fatalf("errors は 1 件のはず: got %d", len(r.Errors))
		}
		if !r.Errors[0].IsNotAuthorized() {
			t.Error("not-authorized-for-resource は IsNotAuthorized と判定するべき")
		}
		if r.Errors[0].IsNotFound() {
			t.Error("not-authorized-for-resource を IsNotFound と判定してはいけない")
		}
	})
}

// TestUserProfileFieldsCoverUser は User が読む項目を、ユーザー検索・フォロワー取得ですべて
// 要求していることを固定する。要求し忘れた項目は API が返さずゼロ値のまま表示され
// （「0 人」「自己紹介なし」に見える）、エラーにならないため気付けない。
// id は要求しなくても必ず返るので除く。
func TestUserProfileFieldsCoverUser(t *testing.T) {
	requested := map[string]bool{}
	for _, f := range UserProfileFieldsRequested {
		if requested[f] {
			t.Errorf("user.fields に %q が重複している", f)
		}
		requested[f] = true
	}

	decoded := map[string]string{} // JSON の名前 → Go のフィールド名
	typ := reflect.TypeOf(User{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name != "" && name != "-" {
			decoded[name] = f.Name
		}
	}
	// 対照: タグの読み取りが空振りすると下の確認が何も検査しなくなるので、既知の項目で確かめる。
	if decoded["username"] != "Username" || decoded["public_metrics"] != "PublicMetrics" {
		t.Fatalf("User の JSON タグを読めていない（判定の仕方が壊れている）: %v", decoded)
	}

	for name, field := range decoded {
		if name == "id" {
			continue
		}
		if !requested[name] {
			t.Errorf("User.%s（%s）を読むなら UserProfileFieldsRequested で要求するべき", field, name)
		}
	}
}

// TestUserParamsHaveNoExpansions は UserLookupParams / FollowersParams に expansions を
// 指定する手段が無いことを固定する。ピン留め投稿などを展開すると投稿が includes に入り
// post read として課金され得るため、型で付けられないようにしてある（request.go の【】）。
func TestUserParamsHaveNoExpansions(t *testing.T) {
	// expansionFields は名前に expansion を含むフィールドを返す。
	expansionFields := func(typ reflect.Type) []string {
		var out []string
		for i := 0; i < typ.NumField(); i++ {
			if name := typ.Field(i).Name; strings.Contains(strings.ToLower(name), "expansion") {
				out = append(out, name)
			}
		}
		return out
	}

	// 対照: 検索の SearchParams は Expansions を持つ。同じ判定で見つかることを先に確かめ、
	// 判定が常に「無い」と答える壊れ方をしていないことを固定する。
	if got := expansionFields(reflect.TypeOf(SearchParams{})); len(got) == 0 {
		t.Fatal("対照の SearchParams.Expansions が見つからない（判定の仕方が壊れている）")
	}

	for _, typ := range []reflect.Type{reflect.TypeOf(UserLookupParams{}), reflect.TypeOf(FollowersParams{})} {
		if got := expansionFields(typ); len(got) > 0 {
			t.Errorf("%s に expansions を指定できるフィールド %v を持たせてはいけない", typ.Name(), got)
		}
	}
}
