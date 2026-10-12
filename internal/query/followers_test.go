package query

import (
	"strings"
	"testing"

	"github.com/shutx-net/seekx/internal/xapi"
)

// testdata のフィクスチャと同じ値。見積り対象の ID と、1 ページ目が返す next_token（16 文字）。
// 検証がこれらを弾くと、フィクスチャモードの見積り → 取得 → ページングが通らなくなる。
const (
	fixtureFollowersUserID = "20000001"
	fixtureFollowersToken  = "FIXTUREPAGE2ZZZZ"
)

// followersRuleIDs は errs のルール ID を返された順に "," でつなぐ。
// 判定はルール ID で行う（既存の TestValidate と同じ）。期待しないルールが混ざっても落ちるよう、
// 「含むか」ではなく並び全体を比べるために使う。
func followersRuleIDs(errs []ValidationError) string {
	ids := make([]string, 0, len(errs))
	for _, e := range errs {
		ids = append(ids, e.RuleID)
	}
	return strings.Join(ids, ",")
}

func TestNormalizeFollowersUsername(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"@nasa", "nasa"},
		{"＠nasa", "nasa"}, // 日本語入力のままの全角 ＠
		{" nasa ", "nasa"},
		{"　＠nasa　", "nasa"}, // 前後の全角空白も除く
		{"@", ""},
		{"", ""},
		{"nasa", "nasa"},      // 対照: @ が無ければそのまま
		{"@Sample", "Sample"}, // 大文字小文字は変えない
		{"@@nasa", "@nasa"},   // 落とすのは 1 つだけ
		{"＠＠nasa", "＠nasa"},
		{"@＠nasa", "＠nasa"}, // 半角と全角を合わせても 1 つだけ
		{"na@sa", "na@sa"},  // 先頭以外の @ は落とさない
	}
	for _, tc := range cases {
		if got := NormalizeFollowersUsername(tc.in); got != tc.want {
			t.Errorf("NormalizeFollowersUsername(%q) は %q になるべき（実際: %q）", tc.in, tc.want, got)
		}
	}
}

func TestValidateFollowersLookup(t *testing.T) {
	cases := []struct {
		name      string
		form      FollowersForm
		wantRules []string // 空なら成功（エラーが 1 件も無いこと）
	}{
		{
			name: "成功: ユーザー名と 1 ページの人数",
			form: FollowersForm{Username: "nasa", MaxResults: 100},
		},
		{
			name: "成功: 先頭の @ は省略できる",
			form: FollowersForm{Username: "@nasa", MaxResults: 100},
		},
		{
			name: "成功: 先頭の全角 ＠ も省略できる",
			form: FollowersForm{Username: "＠nasa", MaxResults: 100},
		},
		{
			name: "成功: 英大文字・数字・アンダースコアは使える",
			form: FollowersForm{Username: "Sample_Star_01", MaxResults: 100},
		},
		{
			name: "成功: 15 文字は通るべき",
			form: FollowersForm{Username: strings.Repeat("a", 15), MaxResults: 100},
		},
		{
			name: "成功: 先頭の @ は文字数に数えない（@ + 15 文字）",
			form: FollowersForm{Username: "@" + strings.Repeat("a", 15), MaxResults: 100},
		},
		{
			name:      "U01: ユーザー名が空",
			form:      FollowersForm{MaxResults: 100},
			wantRules: []string{"U01"},
		},
		{
			name:      "U01: 空白だけ（全角空白を含む）",
			form:      FollowersForm{Username: " 　", MaxResults: 100},
			wantRules: []string{"U01"},
		},
		{
			name:      "U01: @ だけ（落とすと空になる）",
			form:      FollowersForm{Username: "@", MaxResults: 100},
			wantRules: []string{"U01"},
		},
		{
			name:      "U02: 記号を含む（a-b）",
			form:      FollowersForm{Username: "a-b", MaxResults: 100},
			wantRules: []string{"U02"},
		},
		{
			name:      "U02: 日本語",
			form:      FollowersForm{Username: "日本語", MaxResults: 100},
			wantRules: []string{"U02"},
		},
		{
			name:      "U02: 16 文字",
			form:      FollowersForm{Username: strings.Repeat("a", 16), MaxResults: 100},
			wantRules: []string{"U02"},
		},
		{
			name:      "U02: @ が 2 つ（落とすのは 1 つだけ）",
			form:      FollowersForm{Username: "@@nasa", MaxResults: 100},
			wantRules: []string{"U02"},
		},
		{
			name:      "U02: 途中の空白",
			form:      FollowersForm{Username: "na sa", MaxResults: 100},
			wantRules: []string{"U02"},
		},
		{
			name:      "U02: パスを壊す文字（../）",
			form:      FollowersForm{Username: "../nasa", MaxResults: 100},
			wantRules: []string{"U02"},
		},
		{
			name: "成功: 人数 1（下限）",
			form: FollowersForm{Username: "nasa", MaxResults: 1},
		},
		{
			name: "成功: 人数 1000（上限）",
			form: FollowersForm{Username: "nasa", MaxResults: 1000},
		},
		{
			name:      "U04: 人数 0 も範囲外（未指定とみなしてはいけない）",
			form:      FollowersForm{Username: "nasa", MaxResults: 0},
			wantRules: []string{"U04"},
		},
		{
			name:      "U04: 人数 -1（数として読めない値）",
			form:      FollowersForm{Username: "nasa", MaxResults: -1},
			wantRules: []string{"U04"},
		},
		{
			name:      "U04: 人数 1001",
			form:      FollowersForm{Username: "nasa", MaxResults: 1001},
			wantRules: []string{"U04"},
		},
		{
			name: "成功: 見積りでは ID とトークンを見ない",
			form: FollowersForm{Username: "nasa", MaxResults: 100, UserID: "12a", PaginationToken: "short"},
		},
		{
			name:      "エラーは全件まとめて返す（U01 と U04）",
			form:      FollowersForm{MaxResults: 0},
			wantRules: []string{"U01", "U04"},
		},
		{
			name:      "エラーは全件まとめて返す（U02 と U04）",
			form:      FollowersForm{Username: "a-b", MaxResults: 1001},
			wantRules: []string{"U02", "U04"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateFollowersLookup(tc.form)
			want := strings.Join(tc.wantRules, ",")
			if want == "" {
				if len(errs) > 0 {
					t.Fatalf("エラーになってはいけない: %v", ValidationErrors(errs).Messages())
				}
				return
			}
			if got := followersRuleIDs(errs); got != want {
				t.Errorf("ルール %s のエラーになるべき。実際: %v", want, ValidationErrors(errs).Messages())
			}
		})
	}
}

func TestValidateFollowersFetch(t *testing.T) {
	const (
		id    = fixtureFollowersUserID
		token = fixtureFollowersToken
	)
	if len(token) != 16 {
		t.Fatalf("前提が崩れている: %q は下限ちょうどの 16 文字であるべき", token)
	}

	cases := []struct {
		name      string
		form      FollowersForm
		wantRules []string // 空なら成功（エラーが 1 件も無いこと）
	}{
		{
			name: "成功: ID と人数だけ（ユーザー名なし・最初のページ）",
			form: FollowersForm{UserID: id, MaxResults: 100},
		},
		{
			name: "成功: ユーザー名とトークンあり",
			form: FollowersForm{Username: "sample_star", UserID: id, MaxResults: 100, PaginationToken: token},
		},
		{
			name: "成功: ユーザー名の先頭の @ は省略できる",
			form: FollowersForm{Username: "@sample_star", UserID: id, MaxResults: 100},
		},
		{
			name: "成功: ユーザー名が @ だけなら無いものとして扱う",
			form: FollowersForm{Username: "@", UserID: id, MaxResults: 100},
		},
		{
			name:      "U02: ユーザー名があって不正",
			form:      FollowersForm{Username: "a-b", UserID: id, MaxResults: 100},
			wantRules: []string{"U02"},
		},
		{
			name:      "U02: ユーザー名が 16 文字",
			form:      FollowersForm{Username: strings.Repeat("a", 16), UserID: id, MaxResults: 100},
			wantRules: []string{"U02"},
		},
		{
			name:      "U03: ID が空",
			form:      FollowersForm{MaxResults: 100},
			wantRules: []string{"U03"},
		},
		{
			name:      "U03: 数字以外を含む（12a）",
			form:      FollowersForm{UserID: "12a", MaxResults: 100},
			wantRules: []string{"U03"},
		},
		{
			name:      "U03: 20 桁",
			form:      FollowersForm{UserID: strings.Repeat("1", 20), MaxResults: 100},
			wantRules: []string{"U03"},
		},
		{
			name: "成功: 19 桁は通るべき",
			form: FollowersForm{UserID: strings.Repeat("9", 19), MaxResults: 100},
		},
		{
			name: "成功: 1 桁",
			form: FollowersForm{UserID: "1", MaxResults: 100},
		},
		{
			name:      "U03: 全角数字",
			form:      FollowersForm{UserID: "２００００００１", MaxResults: 100},
			wantRules: []string{"U03"},
		},
		{
			name:      "U03: 負の数",
			form:      FollowersForm{UserID: "-1", MaxResults: 100},
			wantRules: []string{"U03"},
		},
		{
			name:      "U03: パスを壊す文字（../1）",
			form:      FollowersForm{UserID: "../1", MaxResults: 100},
			wantRules: []string{"U03"},
		},
		{
			name:      "U03: 前後の空白は補正しない（送る値そのものを確かめる）",
			form:      FollowersForm{UserID: " " + id + " ", MaxResults: 100},
			wantRules: []string{"U03"},
		},
		{
			name:      "U03: 末尾の改行（$ が行末にマッチしてはいけない）",
			form:      FollowersForm{UserID: id + "\n", MaxResults: 100},
			wantRules: []string{"U03"},
		},
		{
			name: "成功: 人数 1（下限）",
			form: FollowersForm{UserID: id, MaxResults: 1},
		},
		{
			name: "成功: 人数 1000（上限）",
			form: FollowersForm{UserID: id, MaxResults: 1000},
		},
		{
			name:      "U04: 人数 0",
			form:      FollowersForm{UserID: id, MaxResults: 0},
			wantRules: []string{"U04"},
		},
		{
			name:      "U04: 人数 -1",
			form:      FollowersForm{UserID: id, MaxResults: -1},
			wantRules: []string{"U04"},
		},
		{
			name:      "U04: 人数 1001",
			form:      FollowersForm{UserID: id, MaxResults: 1001},
			wantRules: []string{"U04"},
		},
		{
			name: "成功: トークン 16 文字（下限・testdata の next_token と同じ）",
			form: FollowersForm{UserID: id, MaxResults: 100, PaginationToken: token},
		},
		{
			name:      "U05: 短すぎる（short）",
			form:      FollowersForm{UserID: id, MaxResults: 100, PaginationToken: "short"},
			wantRules: []string{"U05"},
		},
		{
			name:      "U05: 15 文字（下限の 1 つ手前）",
			form:      FollowersForm{UserID: id, MaxResults: 100, PaginationToken: strings.Repeat("A", 15)},
			wantRules: []string{"U05"},
		},
		{
			name: "成功: 小文字の長いトークン（X の形式が変わっても弾かない）",
			form: FollowersForm{UserID: id, MaxResults: 100, PaginationToken: "b26v89c19zqg8o3fpdg4tp9gw8nxqxhj7o4j0iwt0x2gs"},
		},
		{
			name: "成功: _ と - を含むトークン",
			form: FollowersForm{UserID: id, MaxResults: 100, PaginationToken: "abc_DEF-123_xyz-7890"},
		},
		{
			name: "成功: トークン 256 文字（上限）",
			form: FollowersForm{UserID: id, MaxResults: 100, PaginationToken: strings.Repeat("A", 256)},
		},
		{
			name:      "U05: トークン 257 文字",
			form:      FollowersForm{UserID: id, MaxResults: 100, PaginationToken: strings.Repeat("A", 257)},
			wantRules: []string{"U05"},
		},
		{
			name:      "エラーは全件まとめて返す（U02・U03・U04・U05）",
			form:      FollowersForm{Username: "a-b", UserID: "12a", MaxResults: 0, PaginationToken: "short"},
			wantRules: []string{"U02", "U03", "U04", "U05"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateFollowersFetch(tc.form)
			want := strings.Join(tc.wantRules, ",")
			if want == "" {
				if len(errs) > 0 {
					t.Fatalf("エラーになってはいけない: %v", ValidationErrors(errs).Messages())
				}
				return
			}
			if got := followersRuleIDs(errs); got != want {
				t.Errorf("ルール %s のエラーになるべき。実際: %v", want, ValidationErrors(errs).Messages())
			}
		})
	}

	// 長さは範囲内（16〜256）でも、英数字と _ - 以外を含むトークンは弾く。パスやフィクスチャの
	// ファイル名に混ぜると別のファイルを指したり、別の文字に化けたりするため。
	t.Run("U05: 長さが範囲内でも文字種が違えば弾くべき", func(t *testing.T) {
		for _, tok := range []string{
			"../../etc/passwd",  // パスを壊す（ちょうど 16 文字）
			"FIXTUREPAGE2 ZZZ",  // 途中の空白
			token + " ",         // 末尾の空白（補正しない）
			token + "\n",        // 末尾の改行（$ が行末にマッチしてはいけない）
			"FIXTUREPAGE2%2FZZ", // パーセントエンコード（どこかで余計にデコードされると / に化ける）
			"FIXTUREPAGE2.ZZZ",  // ドット（.. で親ディレクトリを指せる）
			"ＦＩＸＴＵＲＥＰＡＧＥ２ＺＺＺＺ", // 全角の英数字
		} {
			// 前提: 長さだけで弾かれる値では、文字種の判定を確かめたことにならない。
			if n := len([]rune(tok)); n < xapi.PaginationTokenMinLen || n > 256 {
				t.Fatalf("前提が崩れている: %q は %d 文字で、長さだけで弾かれてしまう", tok, n)
			}
			errs := ValidateFollowersFetch(FollowersForm{UserID: id, MaxResults: 100, PaginationToken: tok})
			if got := followersRuleIDs(errs); got != "U05" {
				t.Errorf("トークン %q は U05 になるべき。実際: %v", tok, ValidationErrors(errs).Messages())
			}
		}
	})
}

// TestFollowersValidationMessages は画面に出る文言とフィールド名を固定する。
// U04 は xapi の境界値から組むので、数値が文言に正しく入ることもここで確かめる。
func TestFollowersValidationMessages(t *testing.T) {
	cases := []struct {
		name, rule, field, msg string
		errs                   []ValidationError
	}{
		{
			name: "U01", rule: "U01", field: "username",
			msg:  "ユーザー名を入力してください。",
			errs: ValidateFollowersLookup(FollowersForm{MaxResults: 100}),
		},
		{
			name: "U02（@ を落とした値を出す）", rule: "U02", field: "username",
			msg:  "ユーザー名「na-sa」が不正です。半角英数字とアンダースコアのみ、15 文字以内です（先頭の @ は省略できます）。",
			errs: ValidateFollowersLookup(FollowersForm{Username: "@na-sa", MaxResults: 100}),
		},
		{
			name: "U03", rule: "U03", field: "id",
			msg:  "ユーザー ID が不正です（数字のみ・19 桁以内）。見積り画面のリンクから開き直してください。",
			errs: ValidateFollowersFetch(FollowersForm{UserID: "12a", MaxResults: 100}),
		},
		{
			name: "U04（範囲は xapi の境界値から組む）", rule: "U04", field: "max",
			msg:  "1 ページの人数は 1〜1000 の整数で指定してください。",
			errs: ValidateFollowersLookup(FollowersForm{Username: "nasa", MaxResults: 1001}),
		},
		{
			name: "U05", rule: "U05", field: "token",
			msg:  "ページングトークンが不正です。最初のページから取得し直してください。",
			errs: ValidateFollowersFetch(FollowersForm{UserID: fixtureFollowersUserID, MaxResults: 100, PaginationToken: "short"}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.errs) != 1 {
				t.Fatalf("エラーはちょうど 1 件であるべき: %v", ValidationErrors(tc.errs).Messages())
			}
			e := tc.errs[0]
			if e.RuleID != tc.rule || e.Field != tc.field {
				t.Errorf("ルール ID とフィールド名: got %s / %s, want %s / %s", e.RuleID, e.Field, tc.rule, tc.field)
			}
			if e.Message != tc.msg {
				t.Errorf("文言が違う:\n got  %q\n want %q", e.Message, tc.msg)
			}
			// 画面には Messages() の "[U0x] …" の形で出る。
			if got, want := ValidationErrors(tc.errs).Messages()[0], "["+tc.rule+"] "+tc.msg; got != want {
				t.Errorf("Messages() の形が違う:\n got  %q\n want %q", got, want)
			}
		})
	}
}

func TestFollowersPageSizeDefault(t *testing.T) {
	// フォームの既定の選択肢と、見積り・ページングの金額（100 人 = $1.00）の前提。
	if FollowersPageSizeDefault != 100 {
		t.Errorf("1 ページの人数の既定は 100 人であるべき: got %d", FollowersPageSizeDefault)
	}
	// 既定値が検証を通らないと、人数を選ばずに見積るだけで U04 になってしまう。
	if errs := ValidateFollowersLookup(FollowersForm{Username: "nasa", MaxResults: FollowersPageSizeDefault}); len(errs) > 0 {
		t.Errorf("既定の人数は見積りの検証を通るべき: %v", ValidationErrors(errs).Messages())
	}
	if errs := ValidateFollowersFetch(FollowersForm{UserID: fixtureFollowersUserID, MaxResults: FollowersPageSizeDefault}); len(errs) > 0 {
		t.Errorf("既定の人数は取得の検証を通るべき: %v", ValidationErrors(errs).Messages())
	}
}

func TestBuildUserLookupParams(t *testing.T) {
	t.Run("先頭の @ と前後の空白を落とし、大文字小文字は変えずに渡すべき", func(t *testing.T) {
		for _, in := range []string{"Sample", "@Sample", "＠Sample", " @Sample "} {
			p, err := BuildUserLookupParams(FollowersForm{Username: in, MaxResults: 100})
			if err != nil {
				t.Fatalf("%q でエラーになってはいけない: %v", in, err)
			}
			if p.Username != "Sample" {
				t.Errorf("%q は %q として渡すべき: got %q", in, "Sample", p.Username)
			}
			if got, want := strings.Join(p.UserFields, ","), strings.Join(xapi.UserProfileFieldsRequested, ","); got != want {
				t.Errorf("UserFields はプロフィール用の一覧であるべき:\n got  %s\n want %s", got, want)
			}
		}
	})

	t.Run("検証に失敗したら ValidationErrors を返し、パラメータはゼロ値にするべき", func(t *testing.T) {
		cases := []struct {
			name string
			form FollowersForm
			want string
		}{
			{"ユーザー名が空", FollowersForm{MaxResults: 100}, "U01"},
			{"ユーザー名が不正", FollowersForm{Username: "a-b", MaxResults: 100}, "U02"},
			// 人数はユーザー検索には送らないが、課金されるユーザー検索の前に確かめる。
			{"人数が範囲外", FollowersForm{Username: "nasa", MaxResults: 0}, "U04"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				p, err := BuildUserLookupParams(tc.form)
				if err == nil {
					t.Fatal("エラーになるべき")
				}
				verrs, ok := err.(ValidationErrors)
				if !ok {
					t.Fatalf("ValidationErrors であるべき: %T", err)
				}
				if got := followersRuleIDs(verrs); got != tc.want {
					t.Errorf("ルール %s のエラーになるべき。実際: %v", tc.want, verrs.Messages())
				}
				// ゼロ値なら、エラーを見落として Client に渡しても空のユーザー名で弾かれ、API に届かない。
				if p.Username != "" || p.UserFields != nil {
					t.Errorf("失敗時はゼロ値を返すべき（途中まで埋めた値を返してはいけない）: %+v", p)
				}
			})
		}
	})
}

func TestBuildFollowersParams(t *testing.T) {
	t.Run("ID・人数・トークンをそのまま渡し、UserFields はプロフィール用の一覧にするべき", func(t *testing.T) {
		p, err := BuildFollowersParams(FollowersForm{
			Username:        "@sample_star",
			UserID:          fixtureFollowersUserID,
			MaxResults:      1000,
			PaginationToken: fixtureFollowersToken,
		})
		if err != nil {
			t.Fatalf("エラーになってはいけない: %v", err)
		}
		if p.UserID != fixtureFollowersUserID {
			t.Errorf("UserID: got %q, want %q", p.UserID, fixtureFollowersUserID)
		}
		if p.MaxResults != 1000 {
			t.Errorf("MaxResults は指定どおり渡すべき（既定値に置き換えてはいけない）: got %d, want 1000", p.MaxResults)
		}
		if p.PaginationToken != fixtureFollowersToken {
			t.Errorf("PaginationToken: got %q, want %q", p.PaginationToken, fixtureFollowersToken)
		}
		if got, want := strings.Join(p.UserFields, ","), strings.Join(xapi.UserProfileFieldsRequested, ","); got != want {
			t.Errorf("UserFields はプロフィール用の一覧であるべき:\n got  %s\n want %s", got, want)
		}
	})

	t.Run("ユーザー名とトークンが無くても組み立てられ、人数 1 もそのまま渡すべき", func(t *testing.T) {
		p, err := BuildFollowersParams(FollowersForm{UserID: fixtureFollowersUserID, MaxResults: 1})
		if err != nil {
			t.Fatalf("エラーになってはいけない: %v", err)
		}
		if p.MaxResults != 1 {
			t.Errorf("MaxResults: got %d, want 1（既定の 100 に置き換えてはいけない）", p.MaxResults)
		}
		if p.PaginationToken != "" {
			t.Errorf("トークンが空なら空のまま渡すべき（最初のページ）: got %q", p.PaginationToken)
		}
	})

	t.Run("検証に失敗したら ValidationErrors を返し、パラメータはゼロ値にするべき", func(t *testing.T) {
		valid := FollowersForm{
			Username:        "sample_star",
			UserID:          fixtureFollowersUserID,
			MaxResults:      100,
			PaginationToken: fixtureFollowersToken,
		}
		cases := []struct {
			name   string
			modify func(*FollowersForm)
			want   string
		}{
			{"ユーザー名が不正", func(f *FollowersForm) { f.Username = "a-b" }, "U02"},
			{"ID が不正", func(f *FollowersForm) { f.UserID = "12a" }, "U03"},
			{"人数が範囲外", func(f *FollowersForm) { f.MaxResults = 1001 }, "U04"},
			{"トークンが不正", func(f *FollowersForm) { f.PaginationToken = "../../etc/passwd" }, "U05"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				f := valid
				tc.modify(&f)
				p, err := BuildFollowersParams(f)
				if err == nil {
					t.Fatal("エラーになるべき")
				}
				verrs, ok := err.(ValidationErrors)
				if !ok {
					t.Fatalf("ValidationErrors であるべき: %T", err)
				}
				if got := followersRuleIDs(verrs); got != tc.want {
					t.Errorf("ルール %s のエラーになるべき。実際: %v", tc.want, verrs.Messages())
				}
				// ゼロ値なら、エラーを見落として Client に渡しても空の ID で弾かれ、API に届かない。
				if p.UserID != "" || p.MaxResults != 0 || p.PaginationToken != "" || p.UserFields != nil {
					t.Errorf("失敗時はゼロ値を返すべき（途中まで埋めた値を返してはいけない）: %+v", p)
				}
			})
		}

		// 対照: 変更前のフォームは通る（上の失敗が、変えた 1 項目だけによることを確かめる）。
		if _, err := BuildFollowersParams(valid); err != nil {
			t.Fatalf("対照のフォームはエラーになってはいけない: %v", err)
		}
	})
}
