package xapi

// pricing.go は X API の公表単価（api_reference.cost_model）を定義する。
//
// 単価は「API についての事実」なのでここに置く。見積り・月次 cap ガード・
// セッション累計といった「方針」は internal/cost の責務である。
// Client 自身がログに est_usd を出すため、この値は xapi 内から参照できる必要がある
// （internal/cost を import すると xapi が葉でなくなる）。

// 単価（api_reference.cost_model）。
const (
	CostPerPostRead     = 0.005
	CostPerUserRead     = 0.010
	CostPerCountsRecent = 0.005
	CostPerCountsAll    = 0.010

	// CostPerFollowsRead は Following/Followers: Read の単価（一覧で返るユーザー 1 人あたり）。
	// User: Read（CostPerUserRead）と同額だが、料金表では別の行なので定数を分ける
	// （片方だけ改定されたときに、もう片方の見積りまで黙って変わらないようにする）。
	// Owned Reads（$0.001）はユーザーコンテキスト認証でだけ適用される単価で、
	// App-only の Bearer Token を使う seekx には適用されない。
	// 課金は返ったリソース単位で、UTC の 1 日内の同一リソースは重複排除される
	// （ソフト保証で、必ず排除されるとは限らない）。
	CostPerFollowsRead = 0.010
)
