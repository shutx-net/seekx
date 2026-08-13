package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempEnv(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
		t.Fatalf("一時 .env の作成に失敗: %v", err)
	}
	return p
}

func TestLoadDotEnv(t *testing.T) {
	cases := []struct {
		name     string
		contents string
		want     map[string]string
	}{
		{
			name:     "単純なキーと値",
			contents: "A=1\nB=2\n",
			want:     map[string]string{"A": "1", "B": "2"},
		},
		{
			name:     "二重引用符を除去する",
			contents: "A=\"hello world\"\n",
			want:     map[string]string{"A": "hello world"},
		},
		{
			name:     "単一引用符を除去する",
			contents: "A='hello'\n",
			want:     map[string]string{"A": "hello"},
		},
		{
			name:     "前後で異なる引用符は除去しない",
			contents: "A='hello\"\n",
			want:     map[string]string{"A": "'hello\""},
		},
		{
			name:     "コメント行と空行をスキップする",
			contents: "# comment\n\n   \nA=1\n#B=2\n",
			want:     map[string]string{"A": "1"},
		},
		{
			name:     "値に = を含む場合は最初の = のみで分割する",
			contents: "A=b=c=d\n",
			want:     map[string]string{"A": "b=c=d"},
		},
		{
			name:     "export 接頭辞を除去する",
			contents: "export A=1\n",
			want:     map[string]string{"A": "1"},
		},
		{
			name:     "先頭行の BOM を除去する",
			contents: "\ufeffA=1\nB=2\n",
			want:     map[string]string{"A": "1", "B": "2"},
		},
		{
			name:     "= が無い行はスキップする",
			contents: "GARBAGE\nA=1\n",
			want:     map[string]string{"A": "1"},
		},
		{
			name:     "重複キーは後勝ち",
			contents: "A=1\nA=2\n",
			want:     map[string]string{"A": "2"},
		},
		{
			name:     "値に # を含む場合はリテラルとして保持する（インラインコメントは解釈しない）",
			contents: "A=abc#def\n",
			want:     map[string]string{"A": "abc#def"},
		},
		{
			name:     "エスケープシーケンスは解釈しない",
			contents: `A=line1\nline2` + "\n",
			want:     map[string]string{"A": `line1\nline2`},
		},
		{
			name:     "変数展開は行わない",
			contents: "A=1\nB=${A}\n",
			want:     map[string]string{"A": "1", "B": "${A}"},
		},
		{
			name:     "キーと値の周囲の空白を除去する",
			contents: "  A  =  1  \n",
			want:     map[string]string{"A": "1"},
		},
		{
			name:     "空の値を許容する",
			contents: "A=\nB=\"\"\n",
			want:     map[string]string{"A": "", "B": ""},
		},
		{
			name:     "= の左辺が空の行はスキップする",
			contents: "=1\nA=2\n",
			want:     map[string]string{"A": "2"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := loadDotEnv(writeTempEnv(t, tc.contents))
			if err != nil {
				t.Fatalf("予期しないエラー: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("キー数が違う: got %d (%v), want %d (%v)", len(got), keysOf(got), len(tc.want), keysOf(tc.want))
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("キー %q: got %q, want %q", k, got[k], v)
				}
			}
		})
	}

	t.Run("ファイルが存在しない場合はエラーにしない", func(t *testing.T) {
		got, err := loadDotEnv(filepath.Join(t.TempDir(), "does-not-exist"))
		if err != nil {
			t.Fatalf("エラーになってはいけない: %v", err)
		}
		if got != nil {
			t.Errorf("nil マップを期待したが %v", got)
		}
	})

	t.Run("= が無い行の行番号を報告する", func(t *testing.T) {
		_, skipped, err := parseDotEnvFile(writeTempEnv(t, "A=1\nGARBAGE\nB=2\nMORE GARBAGE\n"))
		if err != nil {
			t.Fatalf("予期しないエラー: %v", err)
		}
		want := []int{2, 4}
		if len(skipped) != len(want) {
			t.Fatalf("スキップ行数が違う: got %v, want %v", skipped, want)
		}
		for i := range want {
			if skipped[i] != want[i] {
				t.Errorf("skipped[%d]: got %d, want %d", i, skipped[i], want[i])
			}
		}
	})
}

func keysOf(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func TestLookupEnvPrecedence(t *testing.T) {
	dot := map[string]string{"SEEKX_TEST_KEY": "from-dotenv", "SEEKX_ONLY_DOT": "dot"}

	if got := lookupEnv(dot, "SEEKX_TEST_KEY"); got != "from-dotenv" {
		t.Errorf("os.Getenv 未設定時は .env の値: got %q", got)
	}

	t.Setenv("SEEKX_TEST_KEY", "from-os")
	if got := lookupEnv(dot, "SEEKX_TEST_KEY"); got != "from-os" {
		t.Errorf("os.Getenv が優先されるべき: got %q", got)
	}

	t.Setenv("SEEKX_TEST_KEY", "")
	if got := lookupEnv(dot, "SEEKX_TEST_KEY"); got != "from-dotenv" {
		t.Errorf("os.Getenv が空なら .env にフォールバック: got %q", got)
	}

	if got := lookupEnv(dot, "SEEKX_MISSING_EVERYWHERE"); got != "" {
		t.Errorf("未定義キーは空文字: got %q", got)
	}
}

func TestMaskToken(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "(未設定)"},
		{"a", "a... (1 chars)"},
		{"abc", "abc... (3 chars)"},
		{"abcd", "abcd... (4 chars)"},
		{"abcdefghijklmnop", "abcd... (16 chars)"},
		{"あいうえおかきくけこ", "あいうえ... (10 chars)"},
	}
	for _, tc := range cases {
		if got := maskToken(tc.in); got != tc.want {
			t.Errorf("maskToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// マスク結果に元のトークン全体が含まれないこと。
	tok := "AAAABBBBCCCCDDDD"
	if got := maskToken(tok); got == tok || len(got) > len(tok)+20 {
		t.Errorf("マスクが不十分: %q", got)
	}
}

func TestPortResolution(t *testing.T) {
	empty := func(string) string { return "" }
	if got := resolvePort(empty); got != DefaultPort {
		t.Errorf("PORT 未設定なら DefaultPort: got %q, want %q", got, DefaultPort)
	}
	if got := resolvePort(func(k string) string {
		if k == "PORT" {
			return "9999"
		}
		return ""
	}); got != "9999" {
		t.Errorf("PORT 設定時はその値: got %q", got)
	}
	if DefaultPort != "5050" {
		t.Errorf("DefaultPort は devcontainer.json の forwardPorts と一致していなければならない: got %q", DefaultPort)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	if err := os.WriteFile(p, []byte("BEARER_TOKEN=dummy-token-for-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SEEKX_ENV_FILE", p)
	// 実環境の値がテストに漏れ込まないようにする。
	for _, k := range []string{"PORT", "BEARER_TOKEN", "SEEKX_API_BASE", "SEEKX_FIXTURE_DIR",
		"SEEKX_FIXTURE_FILE", "SEEKX_SESSION_BUDGET_USD", "SEEKX_ALLOW_OVER_CAP",
		"SEEKX_MAX_RESULTS_CAP", "SEEKX_TZ", "SEEKX_LOG_LEVEL"} {
		t.Setenv(k, "")
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("予期しないエラー: %v", err)
	}
	if cfg.Port != DefaultPort {
		t.Errorf("Port: got %q, want %q", cfg.Port, DefaultPort)
	}
	if cfg.APIBaseURL != "https://api.x.com/2" {
		t.Errorf("APIBaseURL: got %q", cfg.APIBaseURL)
	}
	if cfg.SessionBudgetUSD != 5.00 {
		t.Errorf("SessionBudgetUSD: got %v", cfg.SessionBudgetUSD)
	}
	if cfg.MaxResultsCap != 100 {
		t.Errorf("MaxResultsCap: got %v", cfg.MaxResultsCap)
	}
	if cfg.DisplayTZ != "Asia/Tokyo" {
		t.Errorf("DisplayTZ: got %q", cfg.DisplayTZ)
	}
	if cfg.FixtureMode() {
		t.Error("既定ではフィクスチャモードではない")
	}
	if cfg.Location().String() != "Asia/Tokyo" {
		t.Errorf("Location: got %q", cfg.Location())
	}
}

func TestLoadConfigMissingToken(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	if err := os.WriteFile(p, []byte("# トークンなし\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SEEKX_ENV_FILE", p)
	t.Setenv("BEARER_TOKEN", "")
	t.Setenv("SEEKX_FIXTURE_DIR", "")

	if _, err := LoadConfig(); err == nil {
		t.Fatal("BEARER_TOKEN 未設定はエラーになるべき")
	}

	// フィクスチャモードならトークン無しでも起動できる（D-22）。
	t.Setenv("SEEKX_FIXTURE_DIR", "testdata")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("フィクスチャモードではトークン不要: %v", err)
	}
	if !cfg.FixtureMode() {
		t.Error("FixtureMode が true になるべき")
	}
}
