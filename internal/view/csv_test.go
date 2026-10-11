package view

// csv_test.go は CSV の要件（BOM・RFC 4180 のクォート・CSV インジェクション対策・ファイル名）を固定する。
// 期待値はここに文字列で書く（csv.go の定数を参照すると、定数ごと誤って変えても通ってしまう）。

import (
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shutx-net/seekx/internal/xapi"
)

// csvBOM は UTF-8 の BOM（U+FEFF）のバイト列。Excel はこの 3 バイトで UTF-8 と判定する。
const csvBOM = "\xef\xbb\xbf"

// csvWantHeader は見出し行（CRLF まで）。列の名前と並びを固定する。
const csvWantHeader = "ユーザーID,ユーザー名,表示名,プロフィールURL,自己紹介,場所,Webサイト," +
	"フォロワー数,フォロー数,投稿数,リスト数,認証済み,認証の種類,鍵アカウント,アカウント作成日時,プロフィール画像URL\r\n"

// csvLine はクォート済みのセルをカンマでつなぎ、CRLF を付ける（期待値を書くため）。
func csvLine(cells ...string) string {
	return strings.Join(cells, ",") + "\r\n"
}

// descriptionOnlyLine は自己紹介（raw はクォート済みのセル）だけを持つユーザーの行の期待値。
// ほかのセルは空で、真偽値の 2 列だけが false になる。
func descriptionOnlyLine(raw string) string {
	return csvLine("", "", "", "", raw, "", "", "", "", "", "", "false", "", "false", "", "")
}

// readCSV は FollowersCSVFile の出力から BOM を外して読み戻す。
// 行ごとの列数が揃っていなければ encoding/csv の Reader がエラーにする。
func readCSV(t *testing.T, s string) [][]string {
	t.Helper()
	if !strings.HasPrefix(s, csvBOM) {
		t.Fatalf("BOM で始まるべき: %q", s)
	}
	recs, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(s, csvBOM))).ReadAll()
	if err != nil {
		t.Fatalf("RFC 4180 の CSV として読めるべき: %v\n%q", err, s)
	}
	return recs
}

func TestFollowersCSVFile(t *testing.T) {
	t.Run("0 人なら BOM と見出し行だけ", func(t *testing.T) {
		for _, users := range [][]ViewUser{nil, {}} {
			if got, want := FollowersCSVFile(users), csvBOM+csvWantHeader; got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		}
	})

	t.Run("1 人 1 行を渡した順に、見出しの列の順で書く（真偽値は true / false、作成日時は ISO）", func(t *testing.T) {
		// CreatedAtDisplay と CreatedAtISO を別の日付にして、表示用の日付を書いていないことも確かめる。
		// 真偽値の 2 列は 2 人で逆にして、列の取り違えを検出する。
		users := []ViewUser{
			{
				ID:               "30000002",
				Name:             "Sample Blue",
				Username:         "sample_fan02",
				ProfileURL:       "https://x.com/sample_fan02",
				ProfileImageURL:  "https://pbs.twimg.com/profile_images/3000000000000000002/fan02_200x200.jpg",
				Description:      "Travel photos and coffee.",
				Location:         "大阪",
				URL:              "https://t.co/fixture03",
				Verified:         true,
				VerifiedType:     "blue",
				Protected:        false,
				CreatedAtDisplay: "2018-03-05",
				CreatedAtISO:     "2018-03-04T15:30:00Z",
				Metrics:          ViewUserMetrics{Followers: 5400, Following: 310, Posts: 12000, Listed: 21, HasMetrics: true},
			},
			{
				ID:               "30000003",
				Name:             "鍵付きのファン",
				Username:         "sample_fan03",
				ProfileURL:       "https://x.com/sample_fan03",
				ProfileImageURL:  "https://pbs.twimg.com/profile_images/3000000000000000003/fan03_200x200.jpg",
				VerifiedType:     "none",
				Protected:        true,
				CreatedAtDisplay: "2020-09-10",
				CreatedAtISO:     "2020-09-10T00:00:00Z",
				Metrics:          ViewUserMetrics{Followers: 15, Following: 42, Posts: 230, Listed: 0, HasMetrics: true},
			},
		}
		want := csvBOM + csvWantHeader +
			csvLine("30000002", "sample_fan02", "Sample Blue", "https://x.com/sample_fan02", "Travel photos and coffee.", "大阪",
				"https://t.co/fixture03", "5400", "310", "12000", "21", "true", "blue", "false", "2018-03-04T15:30:00Z",
				"https://pbs.twimg.com/profile_images/3000000000000000002/fan02_200x200.jpg") +
			csvLine("30000003", "sample_fan03", "鍵付きのファン", "https://x.com/sample_fan03", "", "",
				"", "15", "42", "230", "0", "false", "none", "true", "2020-09-10T00:00:00Z",
				"https://pbs.twimg.com/profile_images/3000000000000000003/fan03_200x200.jpg")
		if got := FollowersCSVFile(users); got != want {
			t.Errorf("CSV が違う:\n got  %q\n want %q", got, want)
		}
	})

	t.Run("カンマ・ダブルクォート・改行を含むセルは \"…\" で囲み、\" は \"\" にする", func(t *testing.T) {
		cases := []struct{ name, in, wantCell string }{
			{"カンマ", "Osaka, Japan", `"Osaka, Japan"`},
			{"ダブルクォート", `He said "hi"`, `"He said ""hi"""`},
			{"LF の改行は CRLF にする", "1 行目\n2 行目", "\"1 行目\r\n2 行目\""},
			{"CRLF の改行は CR を二重にしない", "1 行目\r\n2 行目", "\"1 行目\r\n2 行目\""},
			{"単独の CR も改行として残す（encoding/csv は CR を捨てるので先に直す）", "1 行目\r2 行目", "\"1 行目\r\n2 行目\""},
			{"クォートの要らないセルは囲まない（対照）", "猫と写真が好きです。", "猫と写真が好きです。"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got := FollowersCSVFile([]ViewUser{{Description: tc.in}})
				if want := csvBOM + csvWantHeader + descriptionOnlyLine(tc.wantCell); got != want {
					t.Errorf("got %q, want %q", got, want)
				}
			})
		}
	})

	t.Run("= + - @ タブ CR で始まるセルの先頭に ' を付ける", func(t *testing.T) {
		cases := []struct{ name, in, wantCell string }{
			{"= で始まる（HYPERLINK）", `=HYPERLINK("https://example.com")`, `"'=HYPERLINK(""https://example.com"")"`},
			{"+ で始まる", "+81 3-1234-5678", "'+81 3-1234-5678"},
			{"- で始まる", "-", "'-"},
			{"@ で始まる", "@sample_star さんのファンです", "'@sample_star さんのファンです"},
			{"タブで始まる", "\t=1+1", "'\t=1+1"},
			{"CR で始まる（改行をそろえる前に判定する）", "\r=1+1", "\"'\r\n=1+1\""},
			{"途中の = や + は触らない（対照）", "1+1=2", "1+1=2"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got := FollowersCSVFile([]ViewUser{{Description: tc.in}})
				if want := csvBOM + csvWantHeader + descriptionOnlyLine(tc.wantCell); got != want {
					t.Errorf("got %q, want %q", got, want)
				}
			})
		}
	})

	t.Run("指標が不明なら 4 つの数値を空セルにする（0 にしない）", func(t *testing.T) {
		// 不明な側にはあえて 0 以外の値を入れ、値ではなく HasMetrics で決めていることを確かめる。
		unknown := ViewUser{ID: "30000006", Metrics: ViewUserMetrics{Followers: 1, Following: 2, Posts: 3, Listed: 4}}
		zero := ViewUser{ID: "30000005", Metrics: ViewUserMetrics{HasMetrics: true}}
		want := csvBOM + csvWantHeader +
			csvLine("30000006", "", "", "", "", "", "", "", "", "", "", "false", "", "false", "", "") +
			csvLine("30000005", "", "", "", "", "", "", "0", "0", "0", "0", "false", "", "false", "", "")
		if got := FollowersCSVFile([]ViewUser{unknown, zero}); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("各行の列数は見出しと同じ", func(t *testing.T) {
		if got, want := len(followersCSVRecord(ViewUser{})), len(followersCSVHeader); got != want {
			t.Errorf("行の列数 %d が見出しの列数 %d と違う", got, want)
		}
	})

	t.Run("testdata の 1 ページ目は見出しと 6 行になる", func(t *testing.T) {
		var resp xapi.FollowersResponse
		loadFixture(t, "users_followers.json", &resp)
		v := BuildFollowersView(&resp, ViewOptions{Loc: jst(t)})
		out := FollowersCSVFile(v.Users)

		if n := strings.Count(out, csvBOM); n != 1 {
			t.Errorf("BOM は先頭に 1 つだけであるべき: got %d 個", n)
		}
		if !strings.HasPrefix(out, csvBOM+csvWantHeader) {
			t.Errorf("BOM と見出し行で始まるべき: %q", out)
		}
		// 行の区切りもセル内の改行も CRLF だけ。CRLF を除いたあとに CR / LF が残ってはいけない。
		if rest := strings.ReplaceAll(out, "\r\n", ""); strings.ContainsAny(rest, "\r\n") {
			t.Errorf("CRLF 以外の改行（裸の CR / LF）があってはいけない: %q", out)
		}
		if !strings.HasSuffix(out, "\r\n") {
			t.Errorf("最後の行も CRLF で終わるべき: %q", out)
		}

		recs := readCSV(t, out)
		if len(recs) != 7 {
			t.Fatalf("見出し + 6 行であるべき: got %d 行", len(recs))
		}
		col := map[string]int{}
		for i, h := range recs[0] {
			col[h] = i
		}
		byID := map[string][]string{}
		var ids []string
		for _, r := range recs[1:] {
			id := r[col["ユーザーID"]]
			ids = append(ids, id)
			byID[id] = r
		}
		if got, want := strings.Join(ids, ","), "30000001,30000002,30000003,30000004,30000005,30000006"; got != want {
			t.Errorf("行の並び: got %s, want %s（表示と同じ順であるべき）", got, want)
		}

		// 読み戻した値（Reader はセル内の CRLF を LF にして返す）。
		checks := []struct{ id, column, want string }{
			{"30000001", "自己紹介", "猫と写真が好きです。\n東京在住。"},
			{"30000001", "フォロワー数", "120"},
			{"30000001", "投稿数", "4567"},
			{"30000001", "Webサイト", "https://t.co/fixture02"},
			{"30000001", "プロフィールURL", "https://x.com/sample_fan01"},
			{"30000001", "アカウント作成日時", "2015-06-07T00:00:00Z"},
			{"30000001", "プロフィール画像URL", "https://pbs.twimg.com/profile_images/3000000000000000001/fan01_200x200.jpg"},
			{"30000002", "投稿数", "12000"}, // post_count 由来
			{"30000002", "場所", "Osaka, Japan"},
			{"30000002", "認証済み", "true"},
			{"30000002", "認証の種類", "blue"},
			{"30000003", "鍵アカウント", "true"},
			{"30000003", "自己紹介", ""},
			{"30000004", "表示名", `'=HYPERLINK("https://example.com")`},
			{"30000004", "自己紹介", "'@sample_star さんのファンです"},
			{"30000004", "場所", "'-"},
			// CSV は HTML ではないので、HTML のエスケープはしない（</script> 対策は JSON に埋めるテンプレート側）。
			{"30000005", "表示名", "<img src=x onerror=alert(1)>"},
			{"30000005", "自己紹介", "</script><script>alert(1)</script>\n2 行目"},
			{"30000005", "フォロワー数", "0"}, // 0 人は空ではなく 0
			{"30000005", "フォロー数", "5"},
			{"30000005", "プロフィール画像URL", "https://abs.twimg.com/sticky/default_profile_images/default_profile_200x200.png"},
			{"30000006", "フォロワー数", ""}, // public_metrics の欠落は空
			{"30000006", "フォロー数", ""},
			{"30000006", "投稿数", ""},
			{"30000006", "リスト数", ""},
			{"30000006", "認証の種類", "government"},
		}
		for _, ck := range checks {
			i, ok := col[ck.column]
			if !ok {
				t.Fatalf("見出しに %q が無い: %q", ck.column, recs[0])
			}
			if got := byID[ck.id][i]; got != ck.want {
				t.Errorf("%s の %s: got %q, want %q", ck.id, ck.column, got, ck.want)
			}
		}

		// クォートの形は読み戻すと見えなくなるので、出力そのもので確かめる。
		for _, raw := range []string{
			`"'=HYPERLINK(""https://example.com"")"`,
			"\"猫と写真が好きです。\r\n東京在住。\"",
			`"Osaka, Japan"`,
		} {
			if !strings.Contains(out, raw) {
				t.Errorf("出力に %q が含まれるべき", raw)
			}
		}
	})
}

func TestNeutralizeCell(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"= で始まる", "=1+1", "'=1+1"},
		{"= で始まる（HYPERLINK）", `=HYPERLINK("https://example.com")`, `'=HYPERLINK("https://example.com")`},
		{"+ で始まる", "+81 3-1234-5678", "'+81 3-1234-5678"},
		{"- で始まる", "-", "'-"},
		{"- で始まる数（指標は負にならないので実害はない）", "-1", "'-1"},
		{"@ で始まる", "@sample_star", "'@sample_star"},
		{"タブで始まる", "\t=1+1", "'\t=1+1"},
		{"CR で始まる", "\r=1+1", "'\r=1+1"},
		// ここから対照（触らないもの）。
		{"空文字", "", ""},
		{"途中の =", "a=b", "a=b"},
		{"途中の + - @", "猫+犬-鳥@家", "猫+犬-鳥@家"},
		{"数値", "4567", "4567"},
		{"0", "0", "0"},
		{"ISO の日時（途中に - がある）", "2015-06-07T00:00:00Z", "2015-06-07T00:00:00Z"},
		{"URL", "https://t.co/fixture02", "https://t.co/fixture02"},
		{"日本語で始まる", "猫=1", "猫=1"},
		{"' で始まるものに ' を重ねない", "'=1+1", "'=1+1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := neutralizeCell(tc.in); got != tc.want {
				t.Errorf("neutralizeCell(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFollowersCSVFilename(t *testing.T) {
	loc := jst(t)
	at := time.Date(2026, 8, 13, 9, 0, 0, 0, loc)

	cases := []struct {
		name         string
		username, id string
		at           time.Time
		want         string
	}{
		{"ユーザー名と日時から作る", "sample_star", "20000001", at, "followers_sample_star_20260813-090000.csv"},
		{"時分秒を 24 時間制で書く", "sample_star", "20000001", time.Date(2026, 8, 13, 21, 5, 7, 0, loc), "followers_sample_star_20260813-210507.csv"},
		{"渡された時刻のタイムゾーンのまま書く（UTC に直さない）", "sample_star", "20000001", at.UTC(), "followers_sample_star_20260813-000000.csv"},
		{"ユーザー名が空なら ID を使う", "", "20000001", at, "followers_20000001_20260813-090000.csv"},
		{"英数字と _ 以外を落とす", "../sample star/星.csv", "", at, "followers_samplestarcsv_20260813-090000.csv"},
		{"OS がファイル名に使えない文字を落とす", `a<b>c:d"e|f?g*h\i`, "", at, "followers_abcdefghi_20260813-090000.csv"},
		{"先頭の @ を落とす", "@sample_star", "20000001", at, "followers_sample_star_20260813-090000.csv"},
		{"記号だけのユーザー名なら ID を使う", "@@@", "20000001", at, "followers_20000001_20260813-090000.csv"},
		{"ID の記号も落とす", "", "2000/0001", at, "followers_20000001_20260813-090000.csv"},
		{"どちらも空なら日時だけ（_ を重ねない）", "", "", at, "followers_20260813-090000.csv"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FollowersCSVFilename(tc.username, tc.id, tc.at); got != tc.want {
				t.Errorf("FollowersCSVFilename(%q, %q, %v) = %q, want %q", tc.username, tc.id, tc.at, got, tc.want)
			}
		})
	}
}

// TestCSVExportJSON は CSVExport の JSON のキーを固定する。
// テンプレートがこの形で <script type="application/json"> に埋め、ブラウザの csv.js が
// data.filename と data.csv を読む。
func TestCSVExportJSON(t *testing.T) {
	b, err := json.Marshal(CSVExport{Filename: "followers_sample_star_20260813-090000.csv", Text: "a,b\r\n"})
	if err != nil {
		t.Fatalf("JSON にできるべき: %v", err)
	}
	if got, want := string(b), `{"filename":"followers_sample_star_20260813-090000.csv","csv":"a,b\r\n"}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
