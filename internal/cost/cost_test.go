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
		if !nearly(xapi.CostPerFollowsRead, 0.010) {
			t.Errorf("follows read: %v", xapi.CostPerFollowsRead)
		}
		if !nearly(UserLookupCost(), 0.010) {
			t.Errorf("UserLookupCost はユーザー 1 人分（user read）であるべき: got %v", UserLookupCost())
		}
	})
}

// TestEstimateFollowers はフォロワー取得の見積り（ページ数・最初のページ・金額）を固定する。
// ページ数は端数のページも 1 ページに数え、金額は重複排除を考慮しない上限側の値にする。
// 各ケースで全フィールドを比べ、どれか 1 つだけ誤って変えても通らないようにする。
func TestEstimateFollowers(t *testing.T) {
	cases := []struct {
		name     string
		total    int
		known    bool
		pageSize int
		want     FollowersEstimate
	}{
		{
			name:  "12,345 人を 100 人ずつなら 124 ページ（端数も 1 ページに数える）",
			total: 12345, known: true, pageSize: 100,
			want: FollowersEstimate{Total: 12345, TotalKnown: true, PageSize: 100, Pages: 124,
				FirstPage: 100, FirstPageUSD: 1.00, PageMaxUSD: 1.00, TotalUSD: 123.45},
		},
		{
			name:  "1 ページに満たなければ最初のページは総数の分だけ",
			total: 50, known: true, pageSize: 100,
			want: FollowersEstimate{Total: 50, TotalKnown: true, PageSize: 100, Pages: 1,
				FirstPage: 50, FirstPageUSD: 0.50, PageMaxUSD: 1.00, TotalUSD: 0.50},
		},
		{
			name:  "0 人ならページ数も金額も 0（1 ページの最大額は総数に依らない）",
			total: 0, known: true, pageSize: 100,
			want: FollowersEstimate{Total: 0, TotalKnown: true, PageSize: 100, Pages: 0,
				FirstPage: 0, FirstPageUSD: 0, PageMaxUSD: 1.00, TotalUSD: 0},
		},
		{
			name:  "割り切れるときは切り上げない",
			total: 1000, known: true, pageSize: 1000,
			want: FollowersEstimate{Total: 1000, TotalKnown: true, PageSize: 1000, Pages: 1,
				FirstPage: 1000, FirstPageUSD: 10.00, PageMaxUSD: 10.00, TotalUSD: 10.00},
		},
		{
			name:  "フォロワー数が不明なら総数を使わず、最初のページは 1 ページ分が返るものとして見込む",
			total: 12345, known: false, pageSize: 100,
			want: FollowersEstimate{Total: 0, TotalKnown: false, PageSize: 100, Pages: 0,
				FirstPage: 100, FirstPageUSD: 1.00, PageMaxUSD: 1.00, TotalUSD: 0},
		},
		{
			name:  "負のフォロワー数は不明として扱う（0 人と見なして少なく見積もってはいけない）",
			total: -1, known: true, pageSize: 100,
			want: FollowersEstimate{Total: 0, TotalKnown: false, PageSize: 100, Pages: 0,
				FirstPage: 100, FirstPageUSD: 1.00, PageMaxUSD: 1.00, TotalUSD: 0},
		},
		{
			name:  "1 ページの人数が 0 でもゼロ除算しない",
			total: 100, known: true, pageSize: 0,
			want: FollowersEstimate{Total: 100, TotalKnown: true, PageSize: 0, Pages: 0,
				FirstPage: 0, FirstPageUSD: 0, PageMaxUSD: 0, TotalUSD: 1.00},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EstimateFollowers(tc.total, tc.known, tc.pageSize)
			w := tc.want
			if got.Total != w.Total {
				t.Errorf("Total: got %d, want %d", got.Total, w.Total)
			}
			if got.TotalKnown != w.TotalKnown {
				t.Errorf("TotalKnown: got %v, want %v", got.TotalKnown, w.TotalKnown)
			}
			if got.PageSize != w.PageSize {
				t.Errorf("PageSize: got %d, want %d", got.PageSize, w.PageSize)
			}
			if got.Pages != w.Pages {
				t.Errorf("Pages: got %d, want %d", got.Pages, w.Pages)
			}
			if got.FirstPage != w.FirstPage {
				t.Errorf("FirstPage: got %d, want %d", got.FirstPage, w.FirstPage)
			}
			if !nearly(got.FirstPageUSD, w.FirstPageUSD) {
				t.Errorf("FirstPageUSD: got %v, want %v", got.FirstPageUSD, w.FirstPageUSD)
			}
			if !nearly(got.PageMaxUSD, w.PageMaxUSD) {
				t.Errorf("PageMaxUSD: got %v, want %v", got.PageMaxUSD, w.PageMaxUSD)
			}
			if !nearly(got.TotalUSD, w.TotalUSD) {
				t.Errorf("TotalUSD: got %v, want %v", got.TotalUSD, w.TotalUSD)
			}
		})
	}
}

// TestFollowersCostFor はフォロワーの人数から金額への換算（1 人 $0.010）を固定する。
// 0 人のケースだけでは「0 以下は 0」の分岐を消しても通ってしまうので、負の人数も置く。
func TestFollowersCostFor(t *testing.T) {
	cases := []struct {
		name string
		n    int
		want float64
	}{
		{"負の人数は 0（セッション累計を減らしてはいけない）", -1, 0},
		{"0 人は 0", 0, 0},
		{"1 人は $0.010", 1, 0.010},
		{"10 人は $0.10", 10, 0.10},
		{"100 人は $1.00", 100, 1.00},
		{"1,000 人は $10.00", 1000, 10.00},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FollowersCostFor(tc.n); !nearly(got, tc.want) {
				t.Errorf("FollowersCostFor(%d): got %v, want %v", tc.n, got, tc.want)
			}
		})
	}
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

// TestGateAllowsFor は月次 cap ガードを読み取りの種類で分ける方針を固定する。
// 月次 cap は post read の上限なので、投稿を返さないユーザーの読み取りは Block 中でも止めない。
// 投稿の読み取りは GateAllows と同じ判定・同じ理由のまま変えない（対照群）。
func TestGateAllowsFor(t *testing.T) {
	normal := EvaluateCap(usageResp(42, 2000000, 12))
	warn := EvaluateCap(usageResp(1700000, 2000000, 12))
	block := EvaluateCap(usageResp(1920000, 2000000, 12))
	unknown := EvaluateCap(nil)
	if normal.Level != CapNormal || warn.Level != CapWarn || block.Level != CapBlock {
		t.Fatalf("前提の cap 段階が崩れている: normal=%v warn=%v block=%v", normal.Level, warn.Level, block.Level)
	}
	baseCfg := config.Config{}
	overCapCfg := config.Config{AllowOverCap: true}

	cases := []struct {
		name   string
		kind   ReadKind
		cap    CapStatus
		cfg    config.Config
		wantOK bool
	}{
		{"cap Block でもユーザーの読み取りは止めない", ReadUsers, block, baseCfg, true},
		{"cap Block なら投稿の読み取りは止める", ReadPosts, block, baseCfg, false},
		{"Normal ならユーザーの読み取りは許可", ReadUsers, normal, baseCfg, true},
		{"Normal なら投稿の読み取りも許可", ReadPosts, normal, baseCfg, true},
		{"SEEKX_ALLOW_OVER_CAP=1 なら投稿の読み取りの停止は従来どおり解除される", ReadPosts, block, overCapCfg, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := GateAllowsFor(tc.kind, tc.cap, tc.cfg)
			if ok != tc.wantOK {
				t.Fatalf("許可: got %v, want %v（理由: %q）", ok, tc.wantOK, reason)
			}
			if ok && reason != "" {
				t.Errorf("許可するときの理由は空であるべき: %q", reason)
			}
			if !ok && !strings.Contains(reason, "月次上限") {
				t.Errorf("停止の理由に「月次上限」を含むべき: %q", reason)
			}
		})
	}

	t.Run("投稿の読み取りは GateAllows と同じ判定と理由を返す", func(t *testing.T) {
		for _, cs := range []CapStatus{normal, warn, block, unknown} {
			for _, cfg := range []config.Config{baseCfg, overCapCfg} {
				wantOK, wantReason := GateAllows(cs, cfg)
				ok, reason := GateAllowsFor(ReadPosts, cs, cfg)
				if ok != wantOK || reason != wantReason {
					t.Errorf("level=%v allowOverCap=%v: got (%v, %q), want (%v, %q)",
						cs.Level, cfg.AllowOverCap, ok, reason, wantOK, wantReason)
				}
			}
		}
	})

	t.Run("種類のゼロ値と未知の値はガードが効く側に倒す", func(t *testing.T) {
		var zero ReadKind
		if zero != ReadPosts {
			t.Errorf("ReadKind のゼロ値は ReadPosts であるべき: got %d", zero)
		}
		for _, k := range []ReadKind{zero, ReadKind(99)} {
			if ok, _ := GateAllowsFor(k, block, baseCfg); ok {
				t.Errorf("ReadKind(%d) を cap Block で許可してはいけない", k)
			}
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
