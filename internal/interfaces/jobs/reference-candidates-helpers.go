package jobs

import "log/slog"

func (r *ReferenceCandidatesRunner) failed(code string, err error) {
	r.failures.Inc()
	r.logger.Warn("реестр: сбор кандидатов не завершён", slog.String("dictionary", code), slog.Any("error", err))
}
