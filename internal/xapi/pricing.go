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
)
