package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/shutx-net/seekx/internal/config"
	"github.com/shutx-net/seekx/internal/xapi"
)

// postForm は設定フォームの送信を模す（guard.go を通すヘッダ付き）。
func postForm(target string, form url.Values) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Host = "localhost:" + config.DefaultPort
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", "http://localhost:"+config.DefaultPort)
	return req
}

const sampleToken = "AAAAAAAAAAAAAAAAAAAAAsampletoken0123456789%2Fabc"

// TestSettingsTokenNeverRendered は設定したトークンが画面に出ないことを確認する。
// ページを読めた相手にトークンを渡さないことが、認証を持たないこのサーバの前提
// （設定画面の prefill という「普通の親切心」が事故になる）。
func TestSettingsTokenNeverRendered(t *testing.T) {
	app := testApp(t, config.Config{APIBaseURL: "http://127.0.0.1:1/unused"})
	router := newRouter(app)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, postForm(settingsTokenPath, url.Values{"bearer": {sampleToken}}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("設定後は 303 で戻すべき: status=%d body=%s", rec.Code, truncate(rec.Body.String(), 300))
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, newTestRequest(http.MethodGet, settingsPath+"?saved=1", nil))
	body := rec.Body.String()

	if rec.Code != http.StatusOK {
		t.Fatalf("設定画面が開けない: status=%d", rec.Code)
	}
	if strings.Contains(body, sampleToken) {
		t.Error("トークンの値が HTML に描画されている")
	}
	// 先頭 4 文字と文字数のマスク表示だけが出る。
	if !strings.Contains(body, config.MaskToken(sampleToken)) {
		t.Errorf("マスク表示が無い: %s", truncate(body, 400))
	}
	// 入力欄に値を prefill しない。
	if strings.Contains(body, `name="bearer" value=`) {
		t.Error("入力欄に値が prefill されている")
	}
	if !strings.Contains(body, `type="password"`) {
		t.Error("入力欄が type=password になっていない")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("設定画面は no-store にすべき: %q", got)
	}
}

// TestSettingsTokenNotInURL はトークンが URL に載らないことを確認する。
// リダイレクト先・履歴・ログに値を残さないため（D-23 の例外）。
func TestSettingsTokenNotInURL(t *testing.T) {
	app := testApp(t, config.Config{APIBaseURL: "http://127.0.0.1:1/unused"})
	router := newRouter(app)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, postForm(settingsTokenPath, url.Values{"bearer": {sampleToken}}))

	loc := rec.Header().Get("Location")
	if strings.Contains(loc, sampleToken) {
		t.Errorf("リダイレクト先にトークンが載っている: %q", loc)
	}
	if loc != settingsPath+"?saved=1" {
		t.Errorf("リダイレクト先: got %q", loc)
	}

	// GET で設定する経路は存在しない。
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, newTestRequest(http.MethodGet, settingsTokenPath+"?bearer="+sampleToken, nil))
	if rec.Code == http.StatusOK {
		t.Error("GET でトークンを設定できてはいけない")
	}
}

// TestSettingsTokenTakesEffect は設定したトークンが実際に使われることと、
// クリアで .env 由来の値へ戻ることを確認する。
func TestSettingsTokenTakesEffect(t *testing.T) {
	var gotAuth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/tweets/search/") {
			gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"data":[],"meta":{"result_count":0}}`))
	}))
	defer srv.Close()

	app := testApp(t, config.Config{BearerToken: "env-token", APIBaseURL: srv.URL})
	router := newRouter(app)
	const search = "/search?kw=%E7%8C%AB&max=10&mode=execute"

	router.ServeHTTP(httptest.NewRecorder(), newTestRequest(http.MethodGet, search, nil))

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, postForm(settingsTokenPath, url.Values{"bearer": {"ui-token"}}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("トークンを設定できない: status=%d", rec.Code)
	}
	router.ServeHTTP(httptest.NewRecorder(), newTestRequest(http.MethodGet, search, nil))

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, postForm(settingsClearPath, nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("トークンを削除できない: status=%d", rec.Code)
	}
	router.ServeHTTP(httptest.NewRecorder(), newTestRequest(http.MethodGet, search, nil))

	want := []string{"Bearer env-token", "Bearer ui-token", "Bearer env-token"}
	if len(gotAuth) != len(want) {
		t.Fatalf("検索の呼び出し回数: got %d, want %d（%v）", len(gotAuth), len(want), gotAuth)
	}
	for i := range want {
		if gotAuth[i] != want[i] {
			t.Errorf("%d 回目の Authorization: got %q, want %q", i+1, gotAuth[i], want[i])
		}
	}
}

// TestSettingsTokenValidation は不正な入力を弾くことを確認する。
// 特に改行はヘッダインジェクションになるため必ず落とす。
func TestSettingsTokenValidation(t *testing.T) {
	cases := []struct {
		name  string
		value string
		ok    bool
	}{
		{"通常のトークン", sampleToken, true},
		{"前後の空白は落とす", "  " + sampleToken + "\n", true},
		{"空", "", false},
		{"空白のみ", "   ", false},
		{"改行を含む（ヘッダインジェクション）", "abc\r\nX-Evil: 1", false},
		{"タブを含む", "abc\tdef", false},
		{"途中に空白を含む", "abc def", false},
		{"日本語を含む", "全角ダミー値XYZ", false}, // 画面の文言と偶然一致しない値にする
		{"長すぎる", strings.Repeat("a", maxTokenLen+1), false},
		{"上限ちょうど", strings.Repeat("a", maxTokenLen), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := testApp(t, config.Config{APIBaseURL: "http://127.0.0.1:1/unused"})
			router := newRouter(app)

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, postForm(settingsTokenPath, url.Values{"bearer": {tc.value}}))

			if tc.ok {
				if rec.Code != http.StatusSeeOther {
					t.Errorf("受け付けるべき: status=%d body=%s", rec.Code, truncate(rec.Body.String(), 200))
				}
				return
			}
			if rec.Code != http.StatusBadRequest {
				t.Errorf("弾くべき: status=%d", rec.Code)
			}
			// エラー画面に入力値を出さない（P-TOKEN-LEAK）。
			if tc.value != "" && strings.TrimSpace(tc.value) != "" &&
				strings.Contains(rec.Body.String(), strings.TrimSpace(tc.value)) {
				t.Error("エラー画面に入力値が出ている")
			}
		})
	}
}

// TestSettingsTokenChangeResetsSessionAndUsage はトークン差し替え時に
// セッション累計と usage キャッシュを捨てることを確認する。
// 別アカウントの消費が混ざると累計と cap の表示が狂う。
func TestSettingsTokenChangeResetsSessionAndUsage(t *testing.T) {
	usageCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/usage/tweets" {
			usageCalls++
			_, _ = w.Write([]byte(`{"data":{"project_usage":"1000","project_cap":"2000000","cap_reset_day":12}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[],"meta":{"result_count":0}}`))
	}))
	defer srv.Close()

	app := testApp(t, config.Config{BearerToken: "env-token", APIBaseURL: srv.URL})
	router := newRouter(app)

	app.session.Add(1.23)
	router.ServeHTTP(httptest.NewRecorder(), newTestRequest(http.MethodGet, "/", nil))
	if usageCalls != 1 {
		t.Fatalf("usage 呼び出し: got %d, want 1", usageCalls)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, postForm(settingsTokenPath, url.Values{"bearer": {"ui-token"}}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("トークンを設定できない: status=%d", rec.Code)
	}

	if got := app.session.Total(); got != 0 {
		t.Errorf("セッション累計がリセットされていない: got %v", got)
	}
	if got := app.session.CallCount(); got != 0 {
		t.Errorf("呼び出し回数がリセットされていない: got %d", got)
	}

	// 60 秒キャッシュを持ち越さず、新しいトークンで取り直す。
	router.ServeHTTP(httptest.NewRecorder(), newTestRequest(http.MethodGet, "/", nil))
	if usageCalls != 2 {
		t.Errorf("usage キャッシュが破棄されていない: usageCalls=%d", usageCalls)
	}
}

// TestSettingsSameTokenKeepsSession は同じ値を入れ直しただけなら
// セッション累計を維持することを確認する（表示を無言でリセットしない）。
func TestSettingsSameTokenKeepsSession(t *testing.T) {
	app := testApp(t, config.Config{BearerToken: sampleToken, APIBaseURL: "http://127.0.0.1:1/unused"})
	router := newRouter(app)
	app.session.Add(1.23)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, postForm(settingsTokenPath, url.Values{"bearer": {sampleToken}}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d", rec.Code)
	}
	if got := app.session.Total(); got != 1.23 {
		t.Errorf("同じトークンならセッション累計は維持されるべき: got %v", got)
	}
}

// TestSearchWithoutTokenDoesNotCallAPI はトークン未設定のまま検索したときに
// API を呼ばずに案内を出すことを確認する。
func TestSearchWithoutTokenDoesNotCallAPI(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"data":[],"meta":{"result_count":0}}`))
	}))
	defer srv.Close()

	app := testApp(t, config.Config{APIBaseURL: srv.URL}) // BearerToken 空
	router := newRouter(app)

	for _, mode := range []string{"estimate", "execute"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, newTestRequest(http.MethodGet, "/search?kw=%E7%8C%AB&max=10&mode="+mode, nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: トークン未設定なら止めるべき: status=%d", mode, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), settingsPath) {
			t.Errorf("%s: 設定画面への導線が無い", mode)
		}
	}
	if calls != 0 {
		t.Errorf("トークン未設定で API を呼んではいけない（usage 含む）: calls=%d", calls)
	}

	// フォーム画面にも未設定であることが出る。
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, newTestRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), "Bearer Token が設定されていません") {
		t.Error("トークン未設定のバナーが出ていない")
	}
}

// TestFixtureModeNeedsNoToken はフィクスチャモードではトークン無しで
// 検索できることを確認する（D-22 を壊していないこと）。
func TestFixtureModeNeedsNoToken(t *testing.T) {
	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	router := newRouter(app)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, newTestRequest(http.MethodGet, "/search?kw=%E7%8C%AB&max=10&mode=execute", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("フィクスチャモードはトークン無しで検索できるべき: status=%d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Bearer Token が設定されていません") {
		t.Error("フィクスチャモードで未設定バナーを出してはいけない")
	}
}

// TestFixtureModeStaysFixtureAfterSetToken はトークンを設定しても
// フィクスチャモードのままであることを確認する。
// SetToken が課金経路を生やさないこと（D-22 の構造的保証）の回帰テスト。
func TestFixtureModeStaysFixtureAfterSetToken(t *testing.T) {
	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	router := newRouter(app)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, postForm(settingsTokenPath, url.Values{"bearer": {sampleToken}}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d", rec.Code)
	}
	if !app.client.FixtureMode() {
		t.Error("トークン設定でフィクスチャモードが解除されている")
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, newTestRequest(http.MethodGet, "/search?kw=%E7%8C%AB&max=10&mode=execute", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if got := app.session.Total(); got != 0 {
		t.Errorf("フィクスチャモードで課金を計上している: got %v", got)
	}
}

// TestSettingsRejectsCrossSitePost は他サイトからの設定変更を弾くことを確認する。
// トークンを勝手に差し替えられないこと（CSRF）。
func TestSettingsRejectsCrossSitePost(t *testing.T) {
	app := testApp(t, config.Config{BearerToken: "env-token", APIBaseURL: "http://127.0.0.1:1/unused"})
	router := newRouter(app)

	for _, path := range []string{settingsTokenPath, settingsClearPath} {
		req := postForm(path, url.Values{"bearer": {"attacker-token"}})
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Origin", "https://evil.example")

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: 他サイトからの POST は拒否すべき: status=%d", path, rec.Code)
		}
	}

	if tok, src := app.tokens.Effective(); tok != "env-token" || src != tokenSourceEnv {
		t.Errorf("トークンが差し替えられている: source=%v", src)
	}
}

// TestTokenStorePrecedence は tokenStore の優先順位とクリアの意味を固定する。
func TestTokenStorePrecedence(t *testing.T) {
	s := newTokenStore("env-token")

	if tok, src := s.Effective(); tok != "env-token" || src != tokenSourceEnv {
		t.Errorf("初期状態: got %q / %v", tok, src)
	}

	changed, err := s.Set("ui-token")
	if err != nil || !changed {
		t.Fatalf("Set: changed=%v err=%v", changed, err)
	}
	if tok, src := s.Effective(); tok != "ui-token" || src != tokenSourceUI {
		t.Errorf("UI 設定後: got %q / %v", tok, src)
	}

	if changed, _ := s.Set("ui-token"); changed {
		t.Error("同じ値の再設定は changed=false であるべき")
	}

	if !s.Clear() {
		t.Error("クリアは changed=true であるべき")
	}
	if tok, src := s.Effective(); tok != "env-token" || src != tokenSourceEnv {
		t.Errorf("クリア後は .env の値に戻るべき: got %q / %v", tok, src)
	}
	if s.Clear() {
		t.Error("2 回目のクリアは changed=false であるべき")
	}

	// .env が無い場合、クリアすると未設定に戻る。
	empty := newTokenStore("")
	if _, err := empty.Set("ui-token"); err != nil {
		t.Fatal(err)
	}
	empty.Clear()
	if tok, src := empty.Effective(); tok != "" || src != tokenSourceNone {
		t.Errorf(".env 無しのクリア後: got %q / %v", tok, src)
	}
}

// TestConcurrentTokenChange はトークンの差し替えと検索が同時に走ってもデータ競合が
// 起きないことを確認する（go test -race で意味を持つ）。
// トークンを実行時に可変にしたことで生まれた競合の回帰テスト。
func TestConcurrentTokenChange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/usage/tweets" {
			_, _ = w.Write([]byte(`{"data":{"project_usage":"1","project_cap":"2000000","cap_reset_day":12}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[],"meta":{"result_count":0}}`))
	}))
	defer srv.Close()

	app := testApp(t, config.Config{BearerToken: "env-token", APIBaseURL: srv.URL})
	router := newRouter(app)
	const search = "/search?kw=%E7%8C%AB&max=10&mode=execute"

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				router.ServeHTTP(httptest.NewRecorder(), newTestRequest(http.MethodGet, search, nil))
			}
		}()
	}
	for i := range 4 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range 20 {
				router.ServeHTTP(httptest.NewRecorder(),
					postForm(settingsTokenPath, url.Values{"bearer": {fmt.Sprintf("token-%d-%d", i, j)}}))
			}
		}(i)
	}
	wg.Wait()

	if !app.client.HasToken() {
		t.Error("差し替えの競合でトークンが失われている")
	}
}

// TestClientSetTokenKeepsFixtureMode は xapi 側の不変条件を直接確認する。
func TestClientSetTokenKeepsFixtureMode(t *testing.T) {
	c := xapi.NewClient(xapi.Options{FixtureDir: fixtureDir}, discardLogger())
	if c.HasToken() {
		t.Error("初期状態でトークンを持っている")
	}
	c.SetToken("t")
	if !c.HasToken() {
		t.Error("SetToken が反映されていない")
	}
	if !c.FixtureMode() {
		t.Error("SetToken でフィクスチャモードが解除された")
	}
}
