package participant

// Status — подключение участника к приёму оплаты через платформу.
type Status string

const (
	// StatusOnboarding — магазин продавца у провайдера ещё не привязан: товары
	// видны в каталоге с пометкой, в checkout участник не попадает.
	StatusOnboarding Status = "onboarding"
	// StatusConnected — оператор ввёл идентификатор магазина продавца у провайдера.
	StatusConnected Status = "connected"
)
