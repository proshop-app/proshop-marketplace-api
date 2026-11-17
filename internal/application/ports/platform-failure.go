package ports

// PlatformServerFailure отличает отказ соединения или шлюза от сбоя конкретного магазина.
// Адаптер сохраняет исходную ошибку и её договор через Unwrap.
type PlatformServerFailure struct {
	Cause error
}

func (e *PlatformServerFailure) Error() string {
	return e.Cause.Error()
}

// Unwrap сохраняет код ошибки и проверку errors.Is.
func (e *PlatformServerFailure) Unwrap() error {
	return e.Cause
}
