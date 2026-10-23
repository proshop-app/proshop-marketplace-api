package buyer

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

var (
	// ErrInvalidPhone — номер не похож на телефон.
	ErrInvalidPhone = errs.New(errs.Invalid, "buyer.invalid_phone", "некорректный номер телефона")
	// ErrNotFound — покупателя нет.
	ErrNotFound = errs.New(errs.NotFound, "buyer.not_found", "покупатель не найден")
	// ErrExists — покупатель с этим телефоном уже заведён.
	ErrExists = errs.New(errs.Conflict, "buyer.exists", "покупатель с этим телефоном уже есть")
	// ErrChallengeNotFound — проверки входа нет.
	ErrChallengeNotFound = errs.New(errs.NotFound, "buyer.challenge_not_found", "проверка входа не найдена")
	// ErrChallengeExpired — время на звонок вышло.
	ErrChallengeExpired = errs.New(errs.Conflict, "buyer.challenge_expired", "время на звонок вышло, начните вход заново")
	// ErrChallengeUsed — по проверке уже выдана сессия.
	ErrChallengeUsed = errs.New(errs.Conflict, "buyer.challenge_used", "вход по этой проверке уже выполнен, начните вход заново")
	// ErrChallengeNotConfirmed — звонка ещё не было.
	ErrChallengeNotConfirmed = errs.New(errs.Conflict, "buyer.challenge_pending", "звонок ещё не подтверждён")
	// ErrChallengeConflict — проверку изменили параллельно.
	ErrChallengeConflict = errs.New(errs.Conflict, "buyer.challenge_conflict", "проверка изменена параллельно, повторите запрос")
	// ErrSessionNotFound — сессии нет или она истекла.
	ErrSessionNotFound = errs.New(errs.Unauthorized, "buyer.unauthorized", "нужен вход покупателя")
	// ErrTooManyAttempts — превышен лимит попыток входа.
	ErrTooManyAttempts = errs.New(errs.TooManyRequests, "buyer.too_many_attempts", "слишком много попыток входа, попробуйте позже")
)
