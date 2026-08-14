package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shutx-net/seekx/internal/config"
)

// guardRequest は Host と fetch metadata を明示したリクエストを作る。
// newTestRequest（render_test.go）と違い、ここでは検証対象そのものを組み立てる。
func guardRequest(target, host, secFetchSite, origin string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = host
	if secFetchSite != "" {
		req.Header.Set("Sec-Fetch-Site", secFetchSite)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}

// TestRequestGuardHost は Host ヘッダの許可・拒否を確認する（DNS rebinding 対策）。
func TestRequestGuardHost(t *testing.T) {
	localPort := "localhost:" + config.DefaultPort
	cases := []struct {
		name  string
		host  string
		allow bool
	}{
		{"localhost とポート", localPort, true},
		{"ポートなしの localhost", "localhost", true},
		{"大文字", "LOCALHOST:" + config.DefaultPort, true},
		{"末尾ドットの FQDN 形式", "localhost.:" + config.DefaultPort, true},
		{"IPv4 ループバック", "127.0.0.1:" + config.DefaultPort, true},
		{"IPv6 ループバックの角括弧付き", "[::1]:" + config.DefaultPort, true},
		{"転送で異なるポートになった場合も許可する", "localhost:18080", true},
		{"攻撃者のドメイン（DNS rebinding）", "evil.example:" + config.DefaultPort, false},
		{"LAN の IP アドレス", "192.168.1.5:" + config.DefaultPort, false},
		{"Host ヘッダ無し", "", false},
		{"許可ホストを含む別ドメイン", "localhost.evil.example", false},
	}

	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	router := newRouter(app)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, guardRequest("/", tc.host, "same-origin", ""))

			blocked := rec.Code == http.StatusForbidden
			if tc.allow && blocked {
				t.Errorf("許可されるべき Host が拒否された: host=%q body=%s", tc.host, truncate(rec.Body.String(), 200))
			}
			if !tc.allow && !blocked {
				t.Errorf("拒否されるべき Host が通った: host=%q status=%d", tc.host, rec.Code)
			}
		})
	}
}

// TestRequestGuardCrossSite は他サイト起点のリクエストを拒否することを確認する。
func TestRequestGuardCrossSite(t *testing.T) {
	localPort := "localhost:" + config.DefaultPort
	selfOrigin := "http://" + localPort

	cases := []struct {
		name         string
		secFetchSite string
		origin       string
		allow        bool
	}{
		{"アプリ内の遷移", "same-origin", selfOrigin, true},
		{"アドレスバー・ブックマークからの遷移", "none", "", true},
		{"大文字小文字を問わない", "Same-Origin", "", true},
		{"他サイトの img / form / リンク", "cross-site", "https://evil.example", false},
		{"localhost の別ポートのアプリ", "same-site", "http://localhost:3000", false},
		{"ヘッダ無し（curl や死活監視）", "", "", true},
		{"Sec-Fetch-Site 無しで Origin が一致", "", selfOrigin, true},
		{"Sec-Fetch-Site 無しで Origin が別サイト", "", "https://evil.example", false},
		{"Sec-Fetch-Site 無しで Origin が別ポート", "", "http://localhost:3000", false},
		{"Origin: null（sandbox iframe）", "", "null", false},
	}

	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	router := newRouter(app)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, guardRequest("/", localPort, tc.secFetchSite, tc.origin))

			blocked := rec.Code == http.StatusForbidden
			if tc.allow && blocked {
				t.Errorf("許可されるべきリクエストが拒否された: sec-fetch-site=%q origin=%q", tc.secFetchSite, tc.origin)
			}
			if !tc.allow && !blocked {
				t.Errorf("拒否されるべきリクエストが通った: sec-fetch-site=%q origin=%q status=%d",
					tc.secFetchSite, tc.origin, rec.Code)
			}
		})
	}
}

// TestRequestGuardBlocksCrossSiteBilling は他サイト起点の mode=execute が
// 課金経路へ到達しないことを確認する。認証が無いこのサーバの一番の実害
// （他サイトの <img> だけで課金が発火する）に対する回帰テスト。
//
// 同じ条件で same-origin なら API に到達することも同時に固定する。
// そうしないと「常に 403」という誤修正でもこのテストが通ってしまう。
func TestRequestGuardBlocksCrossSiteBilling(t *testing.T) {
	searchCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/tweets/search/") {
			searchCalls++
		}
		_, _ = w.Write([]byte(`{"data":[],"meta":{"result_count":0}}`))
	}))
	defer srv.Close()

	// 予算は十分（max=10 の最大コストは $0.15）。止まるとすれば guard の判断だけ。
	app := testApp(t, config.Config{BearerToken: "t", APIBaseURL: srv.URL, SessionBudgetUSD: 5.0})
	router := newRouter(app)
	const target = "/search?kw=%E7%8C%AB&max=10&mode=execute"
	localPort := "localhost:" + config.DefaultPort

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, guardRequest(target, localPort, "cross-site", "https://evil.example"))

	if rec.Code != http.StatusForbidden {
		t.Errorf("他サイト起点の課金リクエストは拒否されるべき: status=%d", rec.Code)
	}
	if searchCalls != 0 {
		t.Errorf("他サイト起点で検索 API を呼んではいけない: searchCalls=%d", searchCalls)
	}
	// 予算ゲートの 403 と取り違えていないことを本文で区別する。
	if !strings.Contains(rec.Body.String(), "SEEKX_ALLOWED_HOSTS") {
		t.Errorf("guard 以外の理由で 403 になっている可能性がある: %s", truncate(rec.Body.String(), 300))
	}

	// 対照群: 同一オリジンなら課金経路まで到達する。
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, guardRequest(target, localPort, "same-origin", "http://"+localPort))

	if rec.Code != http.StatusOK {
		t.Fatalf("同一オリジンの検索は通るべき: status=%d body=%s", rec.Code, truncate(rec.Body.String(), 300))
	}
	if searchCalls != 1 {
		t.Errorf("同一オリジンでは検索 API に到達するべき: searchCalls=%d", searchCalls)
	}
}

// TestRequestGuardRejectionKeepsSecurityHeaders は拒否した応答にも
// セキュリティヘッダが付くことを確認する（ミドルウェアの入れ子の順序）。
func TestRequestGuardRejectionKeepsSecurityHeaders(t *testing.T) {
	app := testApp(t, config.Config{FixtureDir: fixtureDir})
	router := newRouter(app)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, guardRequest("/", "evil.example", "cross-site", "https://evil.example"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("拒否されるべき: status=%d", rec.Code)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got == "" {
		t.Error("拒否応答に Content-Security-Policy が無い")
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("拒否応答の X-Content-Type-Options が nosniff でない: %q", got)
	}
}

// TestRequestGuardAllowedHostsConfig は SEEKX_ALLOWED_HOSTS で追加したホスト名が
// 通ること、既定の localhost が失われないことを確認する。
func TestRequestGuardAllowedHostsConfig(t *testing.T) {
	app := testApp(t, config.Config{
		FixtureDir:   fixtureDir,
		AllowedHosts: append(config.DefaultAllowedHosts(), "seekx.local"),
	})
	router := newRouter(app)

	for _, host := range []string{"seekx.local:" + config.DefaultPort, "localhost:" + config.DefaultPort} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, guardRequest("/", host, "same-origin", ""))
		if rec.Code == http.StatusForbidden {
			t.Errorf("許可したはずの Host が拒否された: %q", host)
		}
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, guardRequest("/", "evil.example", "same-origin", ""))
	if rec.Code != http.StatusForbidden {
		t.Errorf("許可していない Host が通った: status=%d", rec.Code)
	}
}

// TestRequestGuardEmptyAllowListFallsBackToDefaults は許可リストが空のときに
// 全許可へ倒れないことを確認する（設定の抜けが素通りに化けないこと）。
func TestRequestGuardEmptyAllowListFallsBackToDefaults(t *testing.T) {
	h := withRequestGuard(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }),
		nil, discardLogger())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, guardRequest("/", "evil.example", "same-origin", ""))
	if rec.Code != http.StatusForbidden {
		t.Errorf("許可リストが空のとき全許可に倒れている: status=%d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, guardRequest("/", "localhost:"+config.DefaultPort, "same-origin", ""))
	if rec.Code != http.StatusOK {
		t.Errorf("既定の localhost が拒否された: status=%d", rec.Code)
	}
}
