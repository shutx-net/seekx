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

// UserLookupCost はユーザー検索 1 回の金額（User: Read のユーザー 1 人分）を返す。
// フォロワー取得の見積りボタンのラベルと、セッション累計への計上に使う。
// ハンドラが xapi の単価を直接参照しないための入口（main.go の file_layout）。
// 存在しない・凍結されたユーザーは data が返らず課金されないので、計上はユーザーが返ったときだけにする。
func UserLookupCost() float64 { return xapi.CostPerUserRead }

// FollowersCostFor はフォロワー n 人分の金額（Following/Followers: Read）を返す。
// 課金は返ったユーザーの人数単位なので、ページングのラベル（最大額）には max_results を、
// 実費（セッション累計への計上）には実際に返った人数を渡す。
// 0 以下は 0 を返す（負の金額でセッション累計が減らないようにする）。
func FollowersCostFor(n int) float64 {
	if n <= 0 {
		return 0
	}
	return float64(n) * xapi.CostPerFollowsRead
}

// FollowersEstimate はフォロワー数からのフォロワー取得の見積り。
//
// 金額は UTC の 1 日内の重複排除（同じユーザーを同じ日に取り直しても課金されにくい）を考慮しない
// 上限側の値。重複排除はソフト保証で必ず効くとは限らないので、安くなる見込みを金額に織り込まない。
// フォロワー数は取得までの間にも増減するため、実費の厳密な上限ではない。
// 見積りのためのユーザー検索の金額（UserLookupCost）は含めない。
type FollowersEstimate struct {
	Total      int  // フォロワー数（public_metrics.followers_count）。不明なら 0
	TotalKnown bool // フォロワー数を取得できたか（public_metrics が欠けていれば false）
	PageSize   int  // 1 ページの人数（max_results）
	Pages      int  // 全員を取得するのに要るページ数（端数のページも 1 ページに数える）。不明なら 0
	FirstPage  int  // 最初のページで返る人数の見込み。不明なら PageSize（上限側に倒す）

	FirstPageUSD float64 // 最初のページの金額の見込み（FirstPage 人分）
	PageMaxUSD   float64 // 1 ページの最大額（PageSize 人分）。総数に依らない（取得ボタンとページングのラベル用）
	TotalUSD     float64 // 全員を取得したときの金額（Total 人分）。不明なら 0
}

// EstimateFollowers はフォロワー数（total）と 1 ページの人数（pageSize）からフォロワー取得の
// ページ数と金額を見積る。known はフォロワー数を取得できたか（public_metrics があったか）。
//
// 不明なときは total を使わず（Total・Pages・TotalUSD は 0）、最初のページは pageSize 人が返るものとして
// 上限側で見積る。負の total も不明として扱う（0 人と見なすと最初のページを少なく見積もるため）。
// pageSize が 0 以下（検証を経ずに渡った値）でもゼロ除算せず、ページ数と 1 ページの金額を 0 にする。
func EstimateFollowers(total int, known bool, pageSize int) FollowersEstimate {
	if pageSize < 0 {
		pageSize = 0
	}
	if total < 0 {
		known = false
	}
	e := FollowersEstimate{
		TotalKnown: known,
		PageSize:   pageSize,
		PageMaxUSD: FollowersCostFor(pageSize),
	}
	if !known {
		e.FirstPage = pageSize
		e.FirstPageUSD = e.PageMaxUSD
		return e
	}
	e.Total = total
	if pageSize > 0 {
		e.Pages = total / pageSize
		if total%pageSize != 0 {
			e.Pages++
		}
	}
	e.FirstPage = min(total, pageSize)
	e.FirstPageUSD = FollowersCostFor(e.FirstPage)
	e.TotalUSD = FollowersCostFor(total)
	return e
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
//
// 月次 cap は post read の上限なので、投稿を返さない経路（ユーザー検索・フォロワー取得）は
// これを直接呼ばず、GateAllowsFor に ReadUsers を渡して判定する。
func GateAllows(cap CapStatus, cfg config.Config) (bool, string) {
	if cap.Level == CapBlock && !cfg.AllowOverCap {
		return false, fmt.Sprintf(
			"月次上限の %.1f%% を消費しているため実行を停止しました（%d / %d post reads）。cap は毎月 %d 日にリセットされます。続行するには SEEKX_ALLOW_OVER_CAP=1 を設定して再起動してください。",
			cap.Percent(), cap.Usage, cap.Cap, cap.ResetDay)
	}
	return true, ""
}

// ReadKind は課金経路が読み取るリソースの種類。月次 cap ガードを掛けるかどうかを分けるために使う。
type ReadKind int

const (
	// ReadPosts は投稿の読み取り（投稿の検索）。月次 cap ガードの対象。
	// ゼロ値にしてあるのは、種類を渡し忘れたときにガードが効く側へ倒すため。
	ReadPosts ReadKind = iota
	// ReadUsers はユーザーの読み取り（ユーザー検索・フォロワー取得）。月次 cap ガードの対象外。
	ReadUsers
)

// GateAllowsFor は読み取りの種類ごとに課金経路を通してよいかを判定する。
// ReadUsers は常に (true, "") を返し、それ以外（ReadPosts と未知の値）は GateAllows に委ねる。
//
// 【ユーザーの読み取りを月次 cap で止めない】月次 cap（/2/usage/tweets の project_cap）は
// post read の上限で、ユーザー検索とフォロワー取得は投稿を返さない（expansions を付けられない型に
// してある。xapi.UserLookupParams / xapi.FollowersParams）ので cap を消費しない。Block 中に止めても
// cap は減らず、関係の無い理由で正当な操作を止めるだけになる。
// 支出は、クリックごとの金額表示とセッション累計で見せる（セッション予算の上限は廃止済み。
// 理由は GateAllows のコメント）。
func GateAllowsFor(kind ReadKind, cap CapStatus, cfg config.Config) (bool, string) {
	if kind == ReadUsers {
		return true, ""
	}
	return GateAllows(cap, cfg)
}
