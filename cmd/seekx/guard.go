package main

// guard.go は HTTP リクエストの入口で「そのリクエストを受け付けてよいか」を判定する。
//
// なぜ必要か:
//   - GET /search?mode=execute は課金が発生する唯一の経路（main.go の executeSearch）。
//     このサーバは認証を持たないため、対策が無いと
//     他サイトに置かれた <img src="http://localhost:PORT/search?mode=execute&…"> を
//     ユーザーのブラウザが読み込むだけで課金が発火する。レスポンスは同一オリジンポリシーで
//     読めないが、課金そのものは成立してしまう。
//   - 攻撃者のドメインを 127.0.0.1 に解決させる DNS rebinding が成立すると、
//     攻撃者のページがこのアプリと同一オリジンになり、レスポンスまで読めてしまう。
//     ブラウザが送る Host ヘッダは攻撃者のドメインのままなので、そこで落とす。
//
// 制約（file_layout）:
//   - テンプレートを描画しない（拒否応答は text/plain 固定。UI の内側に入る前段のため）
//   - Config をそのまま受け取らない（必要な値だけを引数で受ける）
//   - Bearer Token に触れない
//   - ポート番号のリテラルを持たない（P-PORT-SSOT）

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/shutx-net/seekx/internal/config"
)

// guardRejectMessage は拒否時の応答本文。原因と復旧手順だけを書き、
// 許可リストの内容は書かない（設定を推測させないため）。
const guardRejectMessage = `このリクエストは受け付けられませんでした。

seekx は認証を持たないローカル専用ツールのため、他サイト起点のリクエストと、
許可していない Host ヘッダのリクエストを拒否します。

- ブラウザのアドレスバーから localhost で開き直してください。
- 別のホスト名で使う場合は SEEKX_ALLOWED_HOSTS に追加して再起動してください。
`

// withRequestGuard は Host 検証と cross-site 検証を行うミドルウェア。
//
// allowedHosts が空の場合は config.DefaultAllowedHosts() を使う。
// 「空＝全許可」にはしない（設定の抜けが素通りに化けるのを避ける）。
func withRequestGuard(next http.Handler, allowedHosts []string, log *slog.Logger) http.Handler {
	if len(allowedHosts) == 0 {
		allowedHosts = config.DefaultAllowedHosts()
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reason := rejectReason(r, allowedHosts); reason != "" {
			// 拒否理由の切り分けができるようヘッダの値も残す。
			// いずれも攻撃者が任意の値を入れられるので長さを切り詰める。
			log.Warn("リクエストを拒否しました",
				"reason", reason,
				"path", r.URL.Path,
				"host", truncateHeader(r.Host),
				"origin", truncateHeader(r.Header.Get("Origin")),
				"sec_fetch_site", truncateHeader(r.Header.Get("Sec-Fetch-Site")))
			http.Error(w, guardRejectMessage, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rejectReason は拒否理由を返す。受け付けてよい場合は空文字列を返す。
func rejectReason(r *http.Request, allowedHosts []string) string {
	if !hostAllowed(r.Host, allowedHosts) {
		return "host-not-allowed"
	}
	if crossSite(r) {
		return "cross-site"
	}
	return ""
}

// hostAllowed は Host ヘッダのホスト名部分が許可リストにあるかを返す。
//
// ポート番号は比較しない。devcontainer のポート転送でホスト側とコンテナ側の
// ポートが食い違い得るためで、DNS rebinding 対策として意味を持つのはホスト名の側。
func hostAllowed(host string, allowed []string) bool {
	h := config.NormalizeHostname(host)
	if h == "" {
		return false // Host ヘッダ無しは許可しない
	}
	for _, a := range allowed {
		if h == config.NormalizeHostname(a) {
			return true
		}
	}
	return false
}

// crossSite は他サイト起点のリクエストかどうかを返す。
//
// 一次情報は Sec-Fetch-Site（現代のブラウザは常に送る）:
//
//	same-origin … アプリ内の遷移・フォーム送信・静的アセット → 許可
//	none        … アドレスバー入力やブックマークからの遷移   → 許可
//	same-site   … 同一サイトの別オリジン（localhost の別ポート）→ 拒否
//	cross-site  … 他サイトの <img> / form / リンク           → 拒否
//
// Fetch Metadata の標準的な方針は cross-site でも「トップレベル遷移かつ GET」は
// 許可する（他サイトからの被リンクを壊さないため）。seekx はここで意図的に外れる。
// 課金経路が GET のトップレベル遷移そのもの（/search?mode=execute）であり、
// 他サイトから正当にリンクされる場面が無いため、許可すると穴が塞がらない。
//
// Sec-Fetch-Site が無い場合は Origin にフォールバックする。どちらも無い場合は許可する
// （curl や死活監視などブラウザ以外からの呼び出し。CSRF はブラウザを踏み台にする攻撃なので、
// ヘッダを自由に作れる相手をここで止めても意味がない）。
func crossSite(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))) {
	case "same-origin", "none":
		return false
	case "":
		// Origin へフォールバックする。
	default:
		return true
	}

	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	if strings.EqualFold(origin, "null") {
		return true // sandbox 属性付き iframe や data: URL 由来
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return true
	}
	// Origin はこのアプリ自身のページ URL に由来するため、ポートまで一致するのが正常。
	// ホスト名だけの比較にすると localhost の別ポートのアプリからの攻撃を通してしまう。
	return !strings.EqualFold(u.Host, r.Host)
}

// truncateHeader はログに載せるヘッダ値を切り詰める。
func truncateHeader(s string) string {
	const max = 120
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
