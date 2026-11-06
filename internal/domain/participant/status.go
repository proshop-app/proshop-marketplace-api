package participant

// Stage — решение об участии магазина, независимое от настроек доставки.
type Stage string

// Стадии участия сохраняются в БД и публичном контракте.
const (
	StageCandidate    Stage = "candidate"
	StageConnected    Stage = "connected"
	StageReturned     Stage = "returned"
	StageDisconnected Stage = "disconnected"
)
