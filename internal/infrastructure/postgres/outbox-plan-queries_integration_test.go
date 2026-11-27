//go:build integration

package postgres

const (
	ClaimTasksPlanSQL       = claimTasks
	ReadyLanesPlanSQL       = selectReadyLanes
	FailedTasksPlanSQL      = selectFailedTasks
	RequeueFailedPlanSQL    = requeueFailedTasks
	OutboxRetryStatsPlanSQL = selectOutboxRetryStats
)
