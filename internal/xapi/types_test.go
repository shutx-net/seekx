package xapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
