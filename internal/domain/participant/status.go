package participant

// Status — полнота настроек участия магазина.
type Status string

const (
	// StatusOnboarding — магазин ещё не передал обязательные настройки участия.
	StatusOnboarding Status = "onboarding"
	// StatusConnected — настройки участия получены с сервера.
	StatusConnected Status = "connected"
)
