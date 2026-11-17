package ports

import "time"

// PlatformMetrics не раскрывает приложению реализацию Prometheus.
type PlatformMetrics interface {
	PlatformCall(operation, class, outcome string, elapsed time.Duration)
	PlatformQueueWait(class, outcome string, elapsed time.Duration)
}
