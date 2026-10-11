package xapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recordedRequest は偽サーバが受けたリクエストのうち、テストで確かめる部分。
type recordedRequest struct {
	path  string
	query url.Values
	auth  string
}

// newRecordingClient は、受けたリクエストを記録して body を 200 で返す偽サーバに向いた Client を作る。
// 記録は偽サーバのゴルーチンが書くので、返す関数はロックを取って写しを返す。
func newRecordingClient(t *testing.T, body string) (*Client, func() []recordedRequest) {
	t.Helper()
	var (
		mu  sync.Mutex
		got []recordedRequest
	)
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, recordedRequest{path: r.URL.Path, query: r.URL.Query(), auth: r.Header.Get("Authorization")})
		mu.Unlock()
		w.Header().Set("x-rate-limit-limit", "300")
		w.Header().Set("x-rate-limit-remaining", "299")
		w.Header().Set("x-rate-limit-reset", "1786000000")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	return c, func() []recordedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedRequest(nil), got...)
	}
}

// sortedQueryKeys はクエリのキーを並べ替えてカンマでつなぐ（送るキーを過不足なく比べるため）。
func sortedQueryKeys(v url.Values) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// requireAPIError は err が want の APIError であることを確かめて返す。
func requireAPIError(t *testing.T, err error, want ErrorKind) *APIError {
	t.Helper()
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("APIError であるべき: %T %v", err, err)
	}
	if ae.Kind != want {
		t.Errorf("Kind: got %v（%s）, want %v（%s）", ae.Kind, ae.Kind.Title(), want, want.Title())
	}
	return ae
}

// TestUserByUsernameRequest はユーザー検索のリクエストの形を固定する。
// 送るクエリは user.fields だけ。【expansions を送ってはいけない】ピン留め投稿などが includes に
// 入ると post read として課金され得る。
func TestUserByUsernameRequest(t *testing.T) {
	const body = `{"data":{"id":"20000001","name":"サンプル 星子","username":"sample_star",` +
		`"public_metrics":{"followers_count":12345,"following_count":210,"tweet_count":9876,"listed_count":55}}}`

	c, requests := newRecordingClient(t, body)
	resp, rl, err := c.UserByUsername(context.Background(), UserLookupParams{
		Username:   "sample_star",
		UserFields: UserProfileFieldsRequested,
	})
	if err != nil {
		t.Fatalf("予期しないエラー: %v", err)
	}

	got := requests()
	if len(got) != 1 {
		t.Fatalf("API はちょうど 1 回呼ばれるべき: got %d", len(got))
	}
	r := got[0]
	if r.path != "/users/by/username/sample_star" {
		t.Errorf("path: got %q, want %q", r.path, "/users/by/username/sample_star")
	}
	if want := strings.Join(UserProfileFieldsRequested, ","); r.query.Get("user.fields") != want {
		t.Errorf("user.fields: got %q, want %q", r.query.Get("user.fields"), want)
	}
	if _, ok := r.query["expansions"]; ok {
		t.Errorf("expansions を送ってはいけない: got %q", r.query.Get("expansions"))
	}
	if keys := sortedQueryKeys(r.query); keys != "user.fields" {
		t.Errorf("送るクエリは user.fields だけであるべき: got %q", keys)
	}
	if r.auth != "Bearer test-token-not-a-real-secret" {
		t.Errorf("Authorization ヘッダが付いていない: %q", r.auth)
	}

	if resp.Data == nil {
		t.Fatal("data があるのに Data が nil になってはいけない")
	}
	if resp.Data.ID != "20000001" || resp.Data.Username != "sample_star" {
		t.Errorf("ID / Username: got %q / %q", resp.Data.ID, resp.Data.Username)
	}
	if resp.Data.PublicMetrics == nil || resp.Data.PublicMetrics.FollowersCount != 12345 {
		t.Errorf("public_metrics.followers_count を読めるべき: got %+v", resp.Data.PublicMetrics)
	}
	if rl == nil || rl.Limit != 300 || rl.Remaining != 299 {
		t.Errorf("レートリミットのヘッダを読むべき: got %+v", rl)
	}

	t.Run("大文字小文字はそのまま送る（小文字に揃えるのはフィクスチャのファイル名だけ）", func(t *testing.T) {
		c, requests := newRecordingClient(t, body)
		if _, _, err := c.UserByUsername(context.Background(), UserLookupParams{Username: "Sample_Star"}); err != nil {
			t.Fatalf("予期しないエラー: %v", err)
		}
		got := requests()
		if len(got) != 1 {
			t.Fatalf("API はちょうど 1 回呼ばれるべき: got %d", len(got))
		}
		if got[0].path != "/users/by/username/Sample_Star" {
			t.Errorf("path: got %q, want %q", got[0].path, "/users/by/username/Sample_Star")
		}
		// 対照: UserFields が空なら user.fields も送らない（検索の searchValues と同じく、渡された値だけを送る）。
		if keys := sortedQueryKeys(got[0].query); keys != "" {
			t.Errorf("UserFields が空ならクエリを付けないべき: got %q", keys)
		}
	})
}

// TestFollowersRequest はフォロワー取得のリクエストの形を固定する。
// 送るのは max_results・pagination_token・user.fields だけ。【expansions を送ってはいけない】
// （理由は TestUserByUsernameRequest と同じ）。
func TestFollowersRequest(t *testing.T) {
	const body = `{"data":[{"id":"30000001","username":"sample_fan01"},{"id":"30000002","username":"sample_fan02"}],` +
		`"meta":{"result_count":2,"next_token":"FIXTUREPAGE3ZZZZ","previous_token":"FIXTUREPAGE1ZZZZ"}}`
	full := FollowersParams{
		UserID:          "20000001",
		MaxResults:      100,
		PaginationToken: "FIXTUREPAGE2ZZZZ",
		UserFields:      UserProfileFieldsRequested,
	}

	t.Run("人数・トークン・user.fields を送り、expansions は送らない", func(t *testing.T) {
		c, requests := newRecordingClient(t, body)
		resp, rl, err := c.Followers(context.Background(), full)
		if err != nil {
			t.Fatalf("予期しないエラー: %v", err)
		}
		got := requests()
		if len(got) != 1 {
			t.Fatalf("API はちょうど 1 回呼ばれるべき: got %d", len(got))
		}
		r := got[0]
		if r.path != "/users/20000001/followers" {
			t.Errorf("path: got %q, want %q", r.path, "/users/20000001/followers")
		}
		if r.query.Get("max_results") != "100" {
			t.Errorf("max_results: got %q, want %q", r.query.Get("max_results"), "100")
		}
		if r.query.Get("pagination_token") != "FIXTUREPAGE2ZZZZ" {
			t.Errorf("pagination_token: got %q, want %q", r.query.Get("pagination_token"), "FIXTUREPAGE2ZZZZ")
		}
		if want := strings.Join(UserProfileFieldsRequested, ","); r.query.Get("user.fields") != want {
			t.Errorf("user.fields: got %q, want %q", r.query.Get("user.fields"), want)
		}
		if _, ok := r.query["expansions"]; ok {
			t.Errorf("expansions を送ってはいけない: got %q", r.query.Get("expansions"))
		}
		if keys := sortedQueryKeys(r.query); keys != "max_results,pagination_token,user.fields" {
			t.Errorf("送るクエリは max_results・pagination_token・user.fields だけであるべき: got %q", keys)
		}
		if r.auth != "Bearer test-token-not-a-real-secret" {
			t.Errorf("Authorization ヘッダが付いていない: %q", r.auth)
		}

		if len(resp.Data) != 2 || resp.Data[0].Username != "sample_fan01" || resp.Data[1].Username != "sample_fan02" {
			t.Errorf("data を順に読めるべき: got %+v", resp.Data)
		}
		if resp.Meta.NextToken != "FIXTUREPAGE3ZZZZ" || resp.Meta.PreviousToken != "FIXTUREPAGE1ZZZZ" {
			t.Errorf("meta のトークンを読めるべき: got %+v", resp.Meta)
		}
		if rl == nil || rl.Limit != 300 || rl.Remaining != 299 {
			t.Errorf("レートリミットのヘッダを読むべき: got %+v", rl)
		}
	})

	t.Run("人数が 0・トークンが空なら max_results と pagination_token を送らない", func(t *testing.T) {
		c, requests := newRecordingClient(t, body)
		p := full
		p.MaxResults = 0
		p.PaginationToken = ""
		if _, _, err := c.Followers(context.Background(), p); err != nil {
			t.Fatalf("予期しないエラー: %v", err)
		}
		got := requests()
		if len(got) != 1 {
			t.Fatalf("API はちょうど 1 回呼ばれるべき: got %d", len(got))
		}
		if keys := sortedQueryKeys(got[0].query); keys != "user.fields" {
			t.Errorf("送るクエリは user.fields だけであるべき: got %q", keys)
		}
	})

	t.Run("連続して呼んでもペーシングしない（full-archive の 1 req/sec の対象外）", func(t *testing.T) {
		// {} はユーザー検索・フォロワー取得・検索のどのレスポンス型にも読める。
		c, requests := newRecordingClient(t, `{}`)
		frozen := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
		var slept []time.Duration
		// 時計を止める。ペーシングの対象なら 2 回目で必ず待つ。
		c.pacer.now = func() time.Time { return frozen }
		c.pacer.sleep = func(d time.Duration) { slept = append(slept, d) }

		for i := 0; i < 2; i++ {
			if _, _, err := c.Followers(context.Background(), full); err != nil {
				t.Fatalf("予期しないエラー: %v", err)
			}
			if _, _, err := c.UserByUsername(context.Background(), UserLookupParams{Username: "sample_star"}); err != nil {
				t.Fatalf("予期しないエラー: %v", err)
			}
		}
		if len(slept) != 0 {
			t.Errorf("ユーザー検索・フォロワー取得で待ってはいけない: %v", slept)
		}
		if n := len(requests()); n != 4 {
			t.Errorf("4 回とも API に届くべき: got %d", n)
		}

		// 対照: 同じ仕掛けで search/all は 2 回目に待つ（時計の差し替えが効いていることの確認）。
		p := testParams()
		p.Endpoint = EndpointAll
		for i := 0; i < 2; i++ {
			if _, _, err := c.SearchAll(context.Background(), p); err != nil {
				t.Fatalf("予期しないエラー: %v", err)
			}
		}
		if len(slept) != 1 {
			t.Errorf("対照の search/all は 2 回目に 1 回だけ待つべき: %v", slept)
		}
	})
}

// TestUserPathRejectsUnsafeSegments は、パスに埋める値にパスを壊す文字があれば API を呼ばずに
// KindInvalidQuery を返すことを固定する。形式（15 文字以内など）の検証は internal/query の責務で、
// ここは / . % 空白などを防御的に弾くだけ。
func TestUserPathRejectsUnsafeSegments(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	ctx := context.Background()

	wantRejected := func(t *testing.T, err error, subject string, before int32) {
		t.Helper()
		ae := requireAPIError(t, err, KindInvalidQuery)
		if !strings.Contains(ae.Detail, subject) || !strings.Contains(ae.Detail, "API は呼び出していません") {
			t.Errorf("Detail に「%s」と「API は呼び出していません」を含めるべき: %q", subject, ae.Detail)
		}
		if n := calls.Load() - before; n != 0 {
			t.Errorf("不正な値で API を呼んではいけない: %d 回呼ばれた", n)
		}
	}

	for _, u := range []string{"", "../x", "a/b", "a b", "名前", "a.b", "a%2Fb", "a?b", "@sample_star", "sample_star\n"} {
		t.Run(fmt.Sprintf("ユーザー名 %q", u), func(t *testing.T) {
			before := calls.Load()
			resp, rl, err := c.UserByUsername(ctx, UserLookupParams{Username: u, UserFields: UserProfileFieldsRequested})
			wantRejected(t, err, "ユーザー名", before)
			if resp != nil || rl != nil {
				t.Errorf("弾いたときはレスポンスもレートリミットも nil であるべき: %+v / %+v", resp, rl)
			}
		})
	}
	for _, id := range []string{"12a", "", "../1", "1/2", "１２３", "-1", "1 2"} {
		t.Run(fmt.Sprintf("ユーザー ID %q", id), func(t *testing.T) {
			before := calls.Load()
			resp, rl, err := c.Followers(ctx, FollowersParams{UserID: id, MaxResults: 100, UserFields: UserProfileFieldsRequested})
			wantRejected(t, err, "ユーザー ID", before)
			if resp != nil || rl != nil {
				t.Errorf("弾いたときはレスポンスもレートリミットも nil であるべき: %+v / %+v", resp, rl)
			}
		})
	}

	t.Run("対照: 正しいユーザー名と ID は API に届く", func(t *testing.T) {
		before := calls.Load()
		if _, _, err := c.UserByUsername(ctx, UserLookupParams{Username: "sample_star"}); err != nil {
			t.Fatalf("予期しないエラー: %v", err)
		}
		if _, _, err := c.Followers(ctx, FollowersParams{UserID: "20000001"}); err != nil {
			t.Fatalf("予期しないエラー: %v", err)
		}
		if n := calls.Load() - before; n != 2 {
			t.Errorf("正しい値なら API に 2 回届くべき: got %d", n)
		}
	})

	t.Run("フィクスチャモードでもファイルを探す前に弾く", func(t *testing.T) {
		// 素通りすると、ファイルを探して見つからない KindDecode になる。
		fc := NewClient(Options{FixtureDir: t.TempDir()}, discardLogger())
		_, _, err := fc.UserByUsername(ctx, UserLookupParams{Username: "../x"})
		requireAPIError(t, err, KindInvalidQuery)
		_, _, err = fc.Followers(ctx, FollowersParams{UserID: "12a"})
		requireAPIError(t, err, KindInvalidQuery)
	})
}

// TestFixtureCandidates はフィクスチャモードで、ユーザー名・ID・ページングトークンから応答ファイルを
// 選ぶ規則を固定する。環境変数を増やさずに、ユーザー名で状態（鍵・0 人・存在しない）を、
// トークンでページを切り替えるための仕組み（client.go の fixtureCandidates）。
func TestFixtureCandidates(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "fixtures")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 中身の username で、どのファイルを読んだかを見分ける。
	files := map[string]string{
		"users_by_username.json":                `{"data":{"id":"1","username":"default_user"}}`,
		"users_by_username_fx_x.json":           `{"data":{"id":"2","username":"fx_x"}}`,
		"users_followers.json":                  `{"data":[{"id":"100","username":"default_page"}],"meta":{"result_count":1}}`,
		"users_followers_42.json":               `{"data":[{"id":"142","username":"followers_of_42"}],"meta":{"result_count":1}}`,
		"users_followers_TOKENTOKENTOKEN1.json": `{"data":[{"id":"200","username":"token_page"}],"meta":{"result_count":1}}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// フィクスチャのディレクトリの外に置く。トークンでここを指せてはいけない。
	outside := `{"data":[{"id":"999","username":"outside"}],"meta":{"result_count":1}}`
	if err := os.WriteFile(filepath.Join(root, "outside.json"), []byte(outside), 0o600); err != nil {
		t.Fatal(err)
	}

	c := NewClient(Options{FixtureDir: dir}, discardLogger())
	if c.httpClient != nil {
		t.Fatal("フィクスチャモードでは HTTP クライアントを作ってはいけない（D-22）")
	}
	ctx := context.Background()

	lookups := []struct {
		name, username, want string
	}{
		{"個別のファイルがあればそれを使う（大文字混じりでも小文字のファイル名に当たる）", "FX_X", "fx_x"},
		{"小文字で指定しても同じ個別のファイル", "fx_x", "fx_x"},
		{"個別のファイルが無ければ既定のファイル", "unknown_name", "default_user"},
	}
	for _, tc := range lookups {
		t.Run("ユーザー検索: "+tc.name, func(t *testing.T) {
			resp, rl, err := c.UserByUsername(ctx, UserLookupParams{Username: tc.username})
			if err != nil {
				t.Fatalf("予期しないエラー: %v", err)
			}
			if rl != nil {
				t.Errorf("フィクスチャモードでは RateLimit は nil であるべき: %+v", rl)
			}
			if resp.Data == nil || resp.Data.Username != tc.want {
				t.Errorf("%q で読むファイルが違う: got %+v, want username %q", tc.username, resp.Data, tc.want)
			}
		})
	}

	pages := []struct {
		name, id, token, want string
	}{
		{"ID の個別のファイルがあればそれを使う", "42", "", "followers_of_42"},
		{"ID の個別のファイルが無ければ既定のファイル", "7", "", "default_page"},
		{"トークンがあればトークンのファイル（ID の個別のファイルより優先する）", "42", "TOKENTOKENTOKEN1", "token_page"},
		{"トークンのファイルは ID に依らない", "7", "TOKENTOKENTOKEN1", "token_page"},
	}
	for _, tc := range pages {
		t.Run("フォロワー取得: "+tc.name, func(t *testing.T) {
			resp, _, err := c.Followers(ctx, FollowersParams{UserID: tc.id, PaginationToken: tc.token})
			if err != nil {
				t.Fatalf("予期しないエラー: %v", err)
			}
			if len(resp.Data) != 1 || resp.Data[0].Username != tc.want {
				t.Errorf("ID %q・トークン %q で読むファイルが違う: got %+v, want username %q", tc.id, tc.token, resp.Data, tc.want)
			}
		})
	}

	t.Run("ユーザー検索: 個別のファイルがあるのに読めなければ、既定のファイルに落としてはいけない", func(t *testing.T) {
		// 存在するが読めない個別のファイルの代わりに、同じ名前のディレクトリを置く。
		// 既定に落ちると、個別のフィクスチャが使われていないことに気付けない。
		if err := os.Mkdir(filepath.Join(dir, "users_by_username_fx_broken.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		resp, _, err := c.UserByUsername(ctx, UserLookupParams{Username: "fx_broken"})
		ae := requireAPIError(t, err, KindDecode)
		if resp != nil {
			t.Errorf("既定のファイルを代わりに読んではいけない: got %+v", resp.Data)
		}
		if !strings.Contains(ae.Detail, "users_by_username_fx_broken.json") {
			t.Errorf("Detail に読めなかった個別のファイル名を出すべき: %q", ae.Detail)
		}
	})

	t.Run("フォロワー取得: トークンのファイルが無ければエラーにし、1 ページ目に落としてはいけない", func(t *testing.T) {
		resp, _, err := c.Followers(ctx, FollowersParams{UserID: "42", PaginationToken: "UNKNOWNTOKEN0001"})
		ae := requireAPIError(t, err, KindDecode)
		if resp != nil {
			t.Errorf("別のファイルを代わりに読んではいけない: got %+v", resp.Data)
		}
		if !strings.Contains(ae.Detail, "users_followers_UNKNOWNTOKEN0001.json") || !strings.Contains(ae.Detail, "読み込めません") {
			t.Errorf("Detail に探したファイル名を出すべき: %q", ae.Detail)
		}
	})

	for _, token := range []string{"../x", "x/../../outside", "a.b", "a b", "%2e%2e"} {
		t.Run(fmt.Sprintf("フォロワー取得: ファイル名にできないトークン %q は読む前に弾く", token), func(t *testing.T) {
			resp, _, err := c.Followers(ctx, FollowersParams{UserID: "42", PaginationToken: token})
			ae := requireAPIError(t, err, KindDecode)
			if resp != nil {
				t.Errorf("ファイルを読んではいけない（ディレクトリの外を含む）: got %+v", resp.Data)
			}
			// 「読み込めません」は探して見つからなかったときの文言。読む前に弾いたことと区別する。
			if !strings.Contains(ae.Detail, "ページングトークン") || strings.Contains(ae.Detail, "読み込めません") {
				t.Errorf("トークンの文字種で弾いたことを Detail に出すべき: %q", ae.Detail)
			}
		})
	}

	t.Run("候補の順序", func(t *testing.T) {
		withFile := NewClient(Options{FixtureDir: dir, FixtureFile: "override.json"}, discardLogger())
		cases := []struct {
			name string
			c    *Client
			path string
			q    url.Values
			want string // 試す順にカンマでつなぐ
		}{
			{"検索は SEEKX_FIXTURE_FILE だけ", withFile, pathSearchRecent, nil, "override.json"},
			{"SEEKX_FIXTURE_FILE が無ければ検索は従来の名前", c, pathSearchAll, nil, "search_all.json"},
			{"counts/all は counts_recent.json にフォールバックする", c, pathCountsAll, nil, "counts_all.json,counts_recent.json"},
			{"usage は従来の名前", c, pathUsage, nil, "usage.json"},
			{"ユーザー検索は個別（小文字）→ 既定", c, pathUsersByUsername + "FX_X", nil, "users_by_username_fx_x.json,users_by_username.json"},
			{"SEEKX_FIXTURE_FILE はユーザー検索に効かない", withFile, pathUsersByUsername + "FX_X", nil, "users_by_username_fx_x.json,users_by_username.json"},
			{"フォロワー取得（トークンなし）は ID → 既定", c, "/users/42/followers", url.Values{"max_results": {"100"}}, "users_followers_42.json,users_followers.json"},
			{"フォロワー取得（トークンあり）はトークンのファイルだけ", c, "/users/42/followers", url.Values{"pagination_token": {"TOKENTOKENTOKEN1"}}, "users_followers_TOKENTOKENTOKEN1.json"},
			{"SEEKX_FIXTURE_FILE はフォロワー取得に効かない", withFile, "/users/42/followers", nil, "users_followers_42.json,users_followers.json"},
		}
		for _, tc := range cases {
			got, err := tc.c.fixtureCandidates(tc.path, tc.q)
			if err != nil {
				t.Errorf("%s: 予期しないエラー: %v", tc.name, err)
				continue
			}
			if strings.Join(got, ",") != tc.want {
				t.Errorf("%s: got %v, want %s", tc.name, got, tc.want)
			}
		}
	})

	t.Run("パスに埋めた値がファイル名にできなければ KindDecode（メソッドの検証を経ない呼び出しへの備え）", func(t *testing.T) {
		for _, path := range []string{pathUsersByUsername + "../x", pathUsersByUsername + "a/b", "/users/../1/followers", "/users//followers"} {
			got, err := c.fixtureCandidates(path, nil)
			if err == nil {
				t.Errorf("%q は弾くべき: got %v", path, got)
				continue
			}
			requireAPIError(t, err, KindDecode)
		}
	})
}

// TestUserCallsLogging はユーザー検索・フォロワー取得の api call ログを固定する。
// est_usd は返った人数 × 単価で、ユーザーが返らなければ 0（課金なし）。
// 【トークンと Authorization をログ・エラーに出してはいけない】（TestClientNoTokenInLogs と同じ）。
func TestUserCallsLogging(t *testing.T) {
	const secret = "SUPER-SECRET-TOKEN-VALUE-0123456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/by/username/sample_star":
			_, _ = w.Write([]byte(`{"data":{"id":"20000001","username":"sample_star"}}`))
		case "/users/by/username/fx_notfound":
			_, _ = w.Write([]byte(`{"errors":[{"title":"Not Found Error","type":"https://api.twitter.com/2/problems/resource-not-found"}]}`))
		case "/users/20000001/followers":
			_, _ = w.Write([]byte(`{"data":[{"id":"30000001"},{"id":"30000002"},{"id":"30000003"}],"meta":{"result_count":3}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"title":"Invalid Request","detail":"bad"}`))
		}
	}))
	defer srv.Close()

	var buf bytes.Buffer
	c := NewClient(Options{BearerToken: secret, APIBaseURL: srv.URL},
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	c.sleep = func(time.Duration) {}
	ctx := context.Background()

	if _, _, err := c.UserByUsername(ctx, UserLookupParams{Username: "sample_star"}); err != nil {
		t.Fatalf("予期しないエラー: %v", err)
	}
	if _, _, err := c.UserByUsername(ctx, UserLookupParams{Username: "fx_notfound"}); err != nil {
		t.Fatalf("errors だけの 200 をエラーにしてはいけない（扱いは呼び出し側が決める）: %v", err)
	}
	if _, _, err := c.Followers(ctx, FollowersParams{UserID: "20000001", MaxResults: 100}); err != nil {
		t.Fatalf("予期しないエラー: %v", err)
	}
	_, _, errBad := c.Followers(ctx, FollowersParams{UserID: "20000002"})
	if errBad == nil {
		t.Fatal("400 はエラーになるべき")
	}

	type apiCall struct {
		Msg         string  `json:"msg"`
		Endpoint    string  `json:"endpoint"`
		ResultCount int     `json:"result_count"`
		EstUSD      float64 `json:"est_usd"`
	}
	var calls []apiCall
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec apiCall
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("ログの行を JSON として読めない: %v: %s", err, line)
		}
		if rec.Msg == "api call" {
			calls = append(calls, rec)
		}
	}
	want := []apiCall{
		{Msg: "api call", Endpoint: "/users/by/username/sample_star", ResultCount: 1, EstUSD: CostPerUserRead},
		{Msg: "api call", Endpoint: "/users/by/username/fx_notfound", ResultCount: 0, EstUSD: 0},
		{Msg: "api call", Endpoint: "/users/20000001/followers", ResultCount: 3, EstUSD: 3 * CostPerFollowsRead},
	}
	if len(calls) != len(want) {
		t.Fatalf("api call のログは %d 行のはず: got %d（%+v）", len(want), len(calls), calls)
	}
	for i, w := range want {
		g := calls[i]
		if g.Endpoint != w.Endpoint || g.ResultCount != w.ResultCount || math.Abs(g.EstUSD-w.EstUSD) > 1e-9 {
			t.Errorf("api call[%d]: got %+v, want %+v", i, g, w)
		}
	}

	logged := buf.String()
	if strings.Contains(logged, secret) {
		t.Error("ログにトークンが出ている")
	}
	if strings.Contains(strings.ToLower(logged), "authorization") {
		t.Error("ログに Authorization が出ている")
	}
	if strings.Contains(errBad.Error(), secret) {
		t.Error("エラーメッセージにトークンが出ている")
	}
	if strings.Contains(errBad.Error(), srv.URL) {
		t.Error("エラーメッセージにリクエスト URL が出ている")
	}
}
