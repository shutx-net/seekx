package cost

// package cost はコスト見積り・月次 cap ガード・セッション累計コストを担う。
//
// 制約（file_layout）:
//   - HTTP 呼び出しを行わない（usage の取得は internal/xapi、判定はここ）
//   - テンプレートに依存しない
//
// 単価そのもの（CostPerPostRead など）は「API についての事実」なので
// internal/xapi に置いてある。ここはその単価を使う「方針」の側。

import (
	"fmt"
	"sync"

	"github.com/shutx-net/seekx/internal/config"
	"github.com/shutx-net/seekx/internal/xapi"
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
func EvaluateCap(u *xapi.UsageResponse) CapStatus {
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
	Buckets        []xapi.CountBucket
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
		PostCostUSD:    float64(willFetch) * xapi.CostPerPostRead,
		MaxUserCostUSD: float64(willFetch) * xapi.CostPerUserRead,
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
	return float64(maxResults) * xapi.CostPerPostRead
}

// CountsCostFor は counts 1 リクエストの単価を返す。
func CountsCostFor(e xapi.Endpoint) float64 {
	if e == xapi.EndpointAll {
		return xapi.CostPerCountsAll
	}
	return xapi.CostPerCountsRecent
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

// Reset は累計を 0 に戻す。Bearer Token を差し替えたときに使う。
// 別アカウントの支出を 1 つの累計に混ぜると、画面に出る「セッション累計」が
// どちらのアカウントの支出でもない値になる。
func (s *SessionSpend) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.USD = 0
	s.Calls = 0
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

// GateAllows は課金経路を通してよいかを判定する。月次 cap が Block のときだけ
// false と日本語理由を返す。
//
// セッション累計に対する上限は持たない。プロセス単位のブレーキは再起動で 0 に戻るため
// 支出の天井として機能せず、「止まったら再起動する」を促すだけだった。累計は表示に使う。
func GateAllows(cap CapStatus, cfg config.Config) (bool, string) {
	if cap.Level == CapBlock && !cfg.AllowOverCap {
		return false, fmt.Sprintf(
			"月次上限の %.1f%% を消費しているため実行を停止しました（%d / %d post reads）。cap は毎月 %d 日にリセットされます。続行するには SEEKX_ALLOW_OVER_CAP=1 を設定して再起動してください。",
			cap.Percent(), cap.Usage, cap.Cap, cap.ResetDay)
	}
	return true, ""
}
