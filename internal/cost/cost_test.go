package cost

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shutx-net/seekx/internal/config"
	"github.com/shutx-net/seekx/internal/xapi"
)

func loadFixture(t *testing.T, name string, out any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../../testdata", name))
	if err != nil {
		t.Fatalf("フィクスチャ %s の読み込みに失敗: %v", name, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("フィクスチャ %s の Unmarshal に失敗: %v", name, err)
	}
}

// usageResp は usage レスポンスを組み立てる。xapi.UsageResponse の数値フィールドは
// 非公開型（flexInt）なので、実レスポンスと同じ JSON デコード経路で作る。
func usageResp(usage, cap, resetDay int) *xapi.UsageResponse {
	body := fmt.Sprintf(`{"data":{"project_id":"1","project_cap":%d,"project_usage":%d,"cap_reset_day":%d}}`, cap, usage, resetDay)
	u := &xapi.UsageResponse{}
	if err := json.Unmarshal([]byte(body), u); err != nil {
		panic(err) // テスト内で組み立てる固定 JSON なので起こらない
	}
	return u
}

func TestEstimateSearch(t *testing.T) {
	cases := []struct {
		name         string
		total        int
		maxResults   int
		wantFetch    int
		wantPostCost float64
		wantTotalMin float64
		wantTotalMax float64
	}{
		{"総件数 < max_results なら総件数", 4, 10, 4, 0.020, 0.020, 0.060},
		{"総件数 > max_results なら max_results", 10000, 10, 10, 0.050, 0.050, 0.150},
		{"総件数 0", 0, 10, 0, 0, 0, 0},
		{"max_results 100", 10000, 100, 100, 0.500, 0.500, 1.500},
		{"ちょうど同数", 10, 10, 10, 0.050, 0.050, 0.150},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := EstimateSearch(tc.total, tc.maxResults)
			if e.WillFetch != tc.wantFetch {
				t.Errorf("WillFetch: got %d, want %d", e.WillFetch, tc.wantFetch)
			}
			if !nearly(e.PostCostUSD, tc.wantPostCost) {
				t.Errorf("PostCostUSD: got %v, want %v", e.PostCostUSD, tc.wantPostCost)
			}
			if !nearly(e.TotalMinUSD, tc.wantTotalMin) {
				t.Errorf("TotalMinUSD: got %v, want %v", e.TotalMinUSD, tc.wantTotalMin)
			}
			if !nearly(e.TotalMaxUSD, tc.wantTotalMax) {
				t.Errorf("TotalMaxUSD: got %v, want %v", e.TotalMaxUSD, tc.wantTotalMax)
			}
			if e.TotalMatched != tc.total {
				t.Errorf("TotalMatched: got %d", e.TotalMatched)
			}
		})
	}

	t.Run("単価が仕様どおり", func(t *testing.T) {
		if xapi.CostPerPostRead != 0.005 {
			t.Errorf("post read: %v", xapi.CostPerPostRead)
		}
		if xapi.CostPerUserRead != 0.010 {
			t.Errorf("user read: %v", xapi.CostPerUserRead)
		}
		if xapi.CostPerCountsRecent != 0.005 {
			t.Errorf("counts recent: %v", xapi.CostPerCountsRecent)
		}
		if xapi.CostPerCountsAll != 0.010 {
			t.Errorf("counts all: %v", xapi.CostPerCountsAll)
		}
		if CountsCostFor(xapi.EndpointRecent) != 0.005 || CountsCostFor(xapi.EndpointAll) != 0.010 {
			t.Error("CountsCostFor の対応が不正")
		}
		if MaxCostFor(10) != 0.05 || MaxCostFor(100) != 0.5 || MaxCostFor(0) != 0 {
			t.Errorf("MaxCostFor: %v %v %v", MaxCostFor(10), MaxCostFor(100), MaxCostFor(0))
		}
	})
}

func nearly(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}

func TestEvaluateCap(t *testing.T) {
	cases := []struct {
		name      string
		usage     int
		cap       int
		wantLevel CapLevel
	}{
		{"79.9% は Normal", 1598000, 2000000, CapNormal},
		{"ちょうど 80% は Warn", 1600000, 2000000, CapWarn},
		{"94.9% は Warn", 1898000, 2000000, CapWarn},
		{"ちょうど 95% は Block", 1900000, 2000000, CapBlock},
		{"96% は Block", 1920000, 2000000, CapBlock},
		{"0% は Normal", 0, 2000000, CapNormal},
		{"100% 超も Block", 2100000, 2000000, CapBlock},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := EvaluateCap(usageResp(tc.usage, tc.cap, 12))
			if cs.Level != tc.wantLevel {
				t.Errorf("Level: got %v, want %v（ratio=%v）", cs.Level, tc.wantLevel, cs.Ratio)
			}
			if !cs.Known {
				t.Error("Known であるべき")
			}
			if cs.ResetDay != 12 {
				t.Errorf("ResetDay: got %d", cs.ResetDay)
			}
		})
	}

	t.Run("cap 0 でゼロ除算しない", func(t *testing.T) {
		cs := EvaluateCap(usageResp(100, 0, 12))
		if cs.Cap != MonthlyPostCapDefault {
			t.Errorf("既定 cap にフォールバックすべき: got %d", cs.Cap)
		}
		if cs.Ratio < 0 || cs.Ratio > 1 {
			t.Errorf("Ratio が異常: %v", cs.Ratio)
		}
		if cs.Level != CapNormal {
			t.Errorf("Level: %v", cs.Level)
		}
	})

	t.Run("usage が nil でも落ちない", func(t *testing.T) {
		cs := EvaluateCap(nil)
		if cs.Known {
			t.Error("Known は false")
		}
		if cs.Cap != MonthlyPostCapDefault {
			t.Errorf("Cap: got %d", cs.Cap)
		}
		if cs.IsBlock() {
			t.Error("取得できない場合は Block にしない（検索を止めない）")
		}
	})

	t.Run("Percent と分岐ヘルパ", func(t *testing.T) {
		cs := EvaluateCap(usageResp(1600000, 2000000, 12))
		if !nearly(cs.Percent(), 80.0) {
			t.Errorf("Percent: got %v", cs.Percent())
		}
		if !cs.IsWarn() || cs.IsBlock() {
			t.Errorf("分岐: warn=%v block=%v", cs.IsWarn(), cs.IsBlock())
		}
	})

	t.Run("文字列で返る project_usage も読める（R-04）", func(t *testing.T) {
		var u xapi.UsageResponse
		loadFixture(t, "usage_string_usage.json", &u)
		cs := EvaluateCap(&u)
		if cs.Level != CapBlock {
			t.Errorf("1920000/2000000 = 96%% は Block: got %v (ratio=%v)", cs.Level, cs.Ratio)
		}
	})
}

func TestGateAllows(t *testing.T) {
	baseCfg := config.Config{}

	t.Run("通常は許可", func(t *testing.T) {
		ok, reason := GateAllows(EvaluateCap(usageResp(42, 2000000, 12)), baseCfg)
		if !ok {
			t.Errorf("許可されるべき: %s", reason)
		}
		if reason != "" {
			t.Errorf("理由は空: %q", reason)
		}
	})

	t.Run("cap Block は停止する", func(t *testing.T) {
		ok, reason := GateAllows(EvaluateCap(usageResp(1920000, 2000000, 12)), baseCfg)
		if ok {
			t.Fatal("停止すべき")
		}
		if !strings.Contains(reason, "月次上限") {
			t.Errorf("理由: %q", reason)
		}
		if !strings.Contains(reason, "SEEKX_ALLOW_OVER_CAP") {
			t.Errorf("解除方法を案内すべき: %q", reason)
		}
	})

	t.Run("SEEKX_ALLOW_OVER_CAP=1 で cap Block を解除できる", func(t *testing.T) {
		cfg := baseCfg
		cfg.AllowOverCap = true
		ok, reason := GateAllows(EvaluateCap(usageResp(1920000, 2000000, 12)), cfg)
		if !ok {
			t.Errorf("解除されるべき: %s", reason)
		}
	})

	t.Run("cap Warn は停止しない", func(t *testing.T) {
		ok, _ := GateAllows(EvaluateCap(usageResp(1700000, 2000000, 12)), baseCfg)
		if !ok {
			t.Error("Warn では止めない")
		}
	})

	t.Run("usage 不明でも検索は止めない", func(t *testing.T) {
		ok, reason := GateAllows(EvaluateCap(nil), baseCfg)
		if !ok {
			t.Errorf("止めるべきではない: %s", reason)
		}
	})
}

func TestSessionSpend(t *testing.T) {
	s := &SessionSpend{}
	s.Add(0.005)
	s.Add(0.050)
	if !nearly(s.Total(), 0.055) {
		t.Errorf("Total: got %v", s.Total())
	}
	if s.CallCount() != 2 {
		t.Errorf("Calls: got %d", s.CallCount())
	}
}

// TestSessionSpendConcurrent は -race で実行して sync.Mutex の健全性を確認する。
func TestSessionSpendConcurrent(t *testing.T) {
	s := &SessionSpend{}
	var wg sync.WaitGroup
	const n = 200
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Add(0.005)
			_ = s.Total()
			_ = s.CallCount()
		}()
	}
	wg.Wait()
	if s.CallCount() != n {
		t.Errorf("Calls: got %d, want %d", s.CallCount(), n)
	}
	if !nearly(s.Total(), 0.005*n) {
		t.Errorf("Total: got %v, want %v", s.Total(), 0.005*n)
	}
}
