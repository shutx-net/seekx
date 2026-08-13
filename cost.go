package main

// cost.go は単価定数・コスト見積り・月次 cap ガード・セッション累計コストを担う。
//
// 制約（file_layout）:
//   - HTTP 呼び出しを行わない（usage の取得は xclient.go、判定は cost.go）
//   - テンプレートに依存しない

import (
	"fmt"
	"sync"
)

// 単価（api_reference.cost_model）。
const (
	CostPerPostRead     = 0.005
	CostPerUserRead     = 0.010
	CostPerCountsRecent = 0.005
	CostPerCountsAll    = 0.010
)

// MonthlyPostCapDefault は project_cap を取得できなかった場合の既定値。
const MonthlyPostCapDefault = 2000000

// cap の閾値（D-28）。
const (
	capWarnRatio  = 0.80
	capBlockRatio = 0.95
)

// CapLevel は月次 cap の消費段階（D-28）。
type CapLevel int

const (
	CapNormal CapLevel = iota // usage/cap < 0.80
	CapWarn                   // 0.80 <= usage/cap < 0.95
	CapBlock                  // 0.95 <= usage/cap
)

// CapStatus は月次 post read cap の消費状況。
type CapStatus struct {
	Usage    int
	Cap      int
	Ratio    float64
	Level    CapLevel
	ResetDay int
	Known    bool // usage を取得できたか（フィクスチャ・エラー時は false）
}

// Percent は消費率（%）を返す。
func (c CapStatus) Percent() float64 { return c.Ratio * 100 }

// IsWarn / IsBlock はテンプレートからの分岐用。
func (c CapStatus) IsWarn() bool  { return c.Level == CapWarn }
func (c CapStatus) IsBlock() bool { return c.Level == CapBlock }

// EvaluateCap は usage レスポンスから CapStatus を作る。cap 0 のゼロ除算を避ける。
func EvaluateCap(u *UsageResponse) CapStatus {
	if u == nil {
		return CapStatus{Cap: MonthlyPostCapDefault}
	}
	cs := CapStatus{
		Usage:    u.Data.ProjectUsage.Int(),
		Cap:      u.Data.ProjectCap.Int(),
		ResetDay: u.Data.CapResetDay,
		Known:    true,
	}
	if cs.Cap <= 0 {
		cs.Cap = MonthlyPostCapDefault
	}
	if cs.Cap > 0 {
		cs.Ratio = float64(cs.Usage) / float64(cs.Cap)
	}
	switch {
	case cs.Ratio >= capBlockRatio:
		cs.Level = CapBlock
	case cs.Ratio >= capWarnRatio:
		cs.Level = CapWarn
	default:
		cs.Level = CapNormal
	}
	return cs
}

// Estimate は counts 結果からの見積り。
type Estimate struct {
	TotalMatched   int
	WillFetch      int
	PostCostUSD    float64
	MaxUserCostUSD float64
	TotalMinUSD    float64
	TotalMaxUSD    float64
	Granularity    string
	Buckets        []CountBucket
	CountsCostUSD  float64
}

// EstimateSearch は総件数と max_results から取得件数とコストを見積る。
// max_user_cost は「全件が別ユーザーで、かつ user read が課金される」最悪ケース（R-01 未解決）。
func EstimateSearch(total int, maxResults int) Estimate {
	if maxResults < 0 {
		maxResults = 0
	}
	willFetch := total
	if maxResults > 0 && total > maxResults {
		willFetch = maxResults
	}
	if willFetch < 0 {
		willFetch = 0
	}
	e := Estimate{
		TotalMatched:   total,
		WillFetch:      willFetch,
		PostCostUSD:    float64(willFetch) * CostPerPostRead,
		MaxUserCostUSD: float64(willFetch) * CostPerUserRead,
	}
	e.TotalMinUSD = e.PostCostUSD
	e.TotalMaxUSD = e.PostCostUSD + e.MaxUserCostUSD
	return e
}

// MaxCostFor は max_results だけから最大コスト（投稿分）を返す。フォームのボタンラベル用。
func MaxCostFor(maxResults int) float64 {
	if maxResults <= 0 {
		return 0
	}
	return float64(maxResults) * CostPerPostRead
}

// CountsCostFor は counts 1 リクエストの単価を返す。
func CountsCostFor(e Endpoint) float64 {
	if e == EndpointAll {
		return CostPerCountsAll
	}
	return CostPerCountsRecent
}

// SessionSpend はプロセス起動からの累計推定コスト。
type SessionSpend struct {
	mu    sync.Mutex
	USD   float64
	Calls int
}

func (s *SessionSpend) Add(usd float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.USD += usd
	s.Calls++
}

func (s *SessionSpend) Total() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.USD
}

func (s *SessionSpend) CallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Calls
}

// GateAllows は課金経路を通してよいかを判定する（cost_control.budget_guard）。
// cap Block / セッション予算超過 / 見積り最大額が残予算を超える場合に false と日本語理由を返す。
func GateAllows(cap CapStatus, session *SessionSpend, est Estimate, cfg Config) (bool, string) {
	if cap.Level == CapBlock && !cfg.AllowOverCap {
		return false, fmt.Sprintf(
			"月次上限の %.1f%% を消費しているため実行を停止しました（%d / %d post reads）。cap は毎月 %d 日にリセットされます。続行するには SEEKX_ALLOW_OVER_CAP=1 を設定して再起動してください。",
			cap.Percent(), cap.Usage, cap.Cap, cap.ResetDay)
	}

	spent := 0.0
	if session != nil {
		spent = session.Total()
	}
	budget := cfg.SessionBudgetUSD
	if budget <= 0 {
		return true, ""
	}
	if spent >= budget {
		return false, fmt.Sprintf(
			"このセッションで $%.2f を超えました（累計 $%.3f）。続けるには SEEKX_SESSION_BUDGET_USD を上げて再起動してください。",
			budget, spent)
	}
	if est.TotalMaxUSD > 0 && spent+est.TotalMaxUSD > budget {
		return false, fmt.Sprintf(
			"この検索の最大コスト $%.3f はセッション残予算 $%.3f を超えます（上限 $%.2f）。取得件数を減らすか SEEKX_SESSION_BUDGET_USD を上げてください。",
			est.TotalMaxUSD, budget-spent, budget)
	}
	return true, ""
}
