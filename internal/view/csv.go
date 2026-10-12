package view

// csv.go は表示中のフォロワーを CSV ファイルの中身（文字列）にする。
//
// 要件は「ブラウザ側で CSV にして保存」だが、整形はこのサーバ側の純粋関数で行い、
// ファイル化と保存だけをブラウザ（cmd/seekx/static/js/csv.js）に任せる。理由:
//   - BOM・クォート・無害化を Go のテストで固定できる
//   - JS を最小に保つ方針（D-55）に合う
//   - 追加の API 呼び出しもサーバ側の保存も無い（描画のたびに作って捨てる）

import (
	"encoding/csv"
	"strconv"
	"strings"
	"time"
)

// CSVExport はテンプレートが <script type="application/json"> に埋める CSV のデータ。
// JSON のキー（filename / csv）はブラウザ側の csv.js が読む名前なので変えない。
//
// html/template はこの要素の中を JS の文脈として扱い、値を JSON にして < > & を Unicode エスケープに
// 置き換えるので、表示名や自己紹介に </script> があっても要素から抜け出せない。CSV の CR も
// JSON の \r として保たれる。data 属性に入れると、ブラウザが HTML を読むときに CR を LF に
// 直してしまう（実測済み）ため使わない。
type CSVExport struct {
	Filename string `json:"filename"`
	Text     string `json:"csv"`
}

// utf8BOM は先頭に付けるバイト順マーク。無いと Excel が UTF-8 と判定せず、日本語が文字化けする。
const utf8BOM = "\ufeff"

// followersCSVHeader は見出し行。列の並びは followersCSVRecord と一致させる。
var followersCSVHeader = []string{
	"ユーザーID", "ユーザー名", "表示名", "プロフィールURL", "自己紹介", "場所", "Webサイト",
	"フォロワー数", "フォロー数", "投稿数", "リスト数",
	"認証済み", "認証の種類", "鍵アカウント", "アカウント作成日時", "プロフィール画像URL",
}

// csvLineBreaks はセル内の改行を LF にそろえる。
//
// encoding/csv は UseCRLF のとき、クォートしたセル内の LF を CRLF にして CR は捨てる（CRLF の CR を
// 二重にしないため。実測済み）。単独の CR まで捨てると前後の行がつながるので、先に LF にしておく。
// ブラウザも HTML を読むときに CR を LF に直すので、カードの見た目ともそろう。
var csvLineBreaks = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// FollowersCSVFile は表示中のフォロワーを CSV ファイルの中身にする（0 人なら見出し行だけ）。
//
//   - 先頭に UTF-8 の BOM を付ける（Excel で開いたときに文字化けさせない）
//   - クォートは RFC 4180 に従う（encoding/csv）。行の区切りもセル内の改行も CRLF
//   - 各セルを neutralizeCell で無害化する（CSV インジェクション対策）
//   - 指標が不明なユーザーは 4 つの数値を空セルにする（0 にすると「0 人」と区別できない）
//   - 真偽値は true / false、作成日時は CreatedAtISO（UTC の RFC 3339）
func FollowersCSVFile(users []ViewUser) string {
	var b strings.Builder
	b.WriteString(utf8BOM)
	w := csv.NewWriter(&b)
	w.UseCRLF = true
	writeCSVRow(w, followersCSVHeader)
	for _, u := range users {
		writeCSVRow(w, followersCSVRecord(u))
	}
	w.Flush()
	// w.Error() は見ない。csv.Writer がエラーを返すのは区切り文字が不正なときと書き込み先が
	// 失敗したときだけで、既定の区切り文字で strings.Builder に書く限りどちらも起こらない。
	return b.String()
}

// writeCSVRow は各セルを無害化し、改行をそろえてから 1 行書く。
func writeCSVRow(w *csv.Writer, cells []string) {
	row := make([]string, len(cells))
	for i, c := range cells {
		// 無害化を先にする。改行を先にそろえると、CR で始まるセルが LF 始まりになって判定を素通りする。
		row[i] = csvLineBreaks.Replace(neutralizeCell(c))
	}
	_ = w.Write(row) // エラーにならない理由は FollowersCSVFile のコメントを参照
}

// followersCSVRecord は 1 人分の行を followersCSVHeader の順に並べる。
func followersCSVRecord(u ViewUser) []string {
	var followers, following, posts, listed string // 指標が不明なら空セルのまま
	if u.Metrics.HasMetrics {
		followers = strconv.Itoa(u.Metrics.Followers)
		following = strconv.Itoa(u.Metrics.Following)
		posts = strconv.Itoa(u.Metrics.Posts)
		listed = strconv.Itoa(u.Metrics.Listed)
	}
	return []string{
		u.ID,
		u.Username,
		u.Name,
		u.ProfileURL,
		u.Description,
		u.Location,
		u.URL,
		followers,
		following,
		posts,
		listed,
		strconv.FormatBool(u.Verified),
		u.VerifiedType,
		strconv.FormatBool(u.Protected),
		u.CreatedAtISO,
		u.ProfileImageURL,
	}
}

// neutralizeCell は、表計算ソフトが数式として解釈し得るセルの先頭に ' を付ける（CSV インジェクション対策）。
//
// 表示名・自己紹介・場所は誰でも書けるので、=HYPERLINK(...) のような値が CSV を開いた人の環境で
// 数式として動かないようにする。対象は OWASP の推奨と同じく = + - @ タブ CR で始まるセル。
// 数式になるのは先頭の文字だけなので、途中の文字は触らない。
func neutralizeCell(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// FollowersCSVFilename は保存するファイル名 followers_<ユーザー名>_<日時>.csv を返す。
//
// ユーザー名が無いとき（ID だけで開いたページ）は ID を使い、どちらも無ければ日時だけにする。
// 日時は at をそのまま書く（表示タイムゾーンへの変換は呼び出し側。time.Now() を呼ばないため、
// 時刻は引数で受け取る）。ユーザー名と ID はリクエストの URL から来る値なので、ASCII の英数字と _
// 以外を落とし、パス区切りや OS がファイル名に使えない文字を入れない。
func FollowersCSVFilename(username, id string, at time.Time) string {
	name := filenameSafe(username)
	if name == "" {
		name = filenameSafe(id)
	}
	stamp := at.Format("20060102-150405")
	if name == "" {
		return "followers_" + stamp + ".csv"
	}
	return "followers_" + name + "_" + stamp + ".csv"
}

// filenameSafe は s から ASCII の英数字と _ だけを残す。
func filenameSafe(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i] // マルチバイト文字のバイトはすべて 0x80 以上なので、まとめて落ちる
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' {
			b.WriteByte(c)
		}
	}
	return b.String()
}
