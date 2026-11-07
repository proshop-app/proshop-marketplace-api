package buyer

import (
	"github.com/proshop-app/proshop-marketplace/internal/domain/identity"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

var (
	// ErrRegionLimit — исчерпан лимит обращений гостя к провайдеру региона.
	ErrRegionLimit = errs.New(errs.TooManyRequests, "buyer.region_limit", "превышен лимит подсказок региона")
	// ErrInvalidProfile — поля профиля не проходят проверку.
	ErrInvalidProfile = errs.New(errs.Invalid, "buyer.invalid_profile", "некорректный профиль")
	// ErrProfileConflict — профиль изменился после чтения.
	ErrProfileConflict = errs.New(errs.Conflict, "buyer.profile_conflict", "профиль изменён параллельно")
	// ErrCollectionLimit — достигнут лимит покупательского списка.
	ErrCollectionLimit = errs.New(errs.Conflict, "buyer.collection_limit", "достигнут лимит списка")
	// ErrInvalidTarget — целевая цена не может быть отрицательной.
	ErrInvalidTarget = errs.New(errs.Invalid, "buyer.invalid_target", "цель должна быть неотрицательной")

	// ErrAccessExpired — подпись и границы JWT верны, истёк только access.
	ErrAccessExpired = identity.ErrAccessExpired
	// ErrInvalidPhone — номер не похож на телефон.
	ErrInvalidPhone = errs.New(errs.Invalid, "buyer.invalid_phone", "некорректный номер телефона")
	// ErrNotFound — покупателя нет.
	ErrNotFound = errs.New(errs.NotFound, "buyer.not_found", "покупатель не найден")
	// ErrExists — покупатель с этим телефоном уже заведён.
	ErrExists = errs.New(errs.Conflict, "buyer.exists", "покупатель с этим телефоном уже есть")
	// ErrChallengeNotFound — проверки входа нет.
	ErrChallengeNotFound = errs.New(errs.NotFound, "buyer.challenge_not_found", "проверка входа не найдена")
	// ErrChallengeExpired — время на звонок вышло.
	ErrChallengeExpired = errs.New(
		errs.Conflict,
		"buyer.challenge_expired",
		"время на звонок вышло, начните вход заново",
	)
	// ErrChallengeUsed — по проверке уже выдана сессия.
	ErrChallengeUsed = errs.New(
		errs.Conflict,
		"buyer.challenge_used",
		"вход по этой проверке уже выполнен, начните вход заново",
	)
	// ErrChallengeNotConfirmed — звонка ещё не было.
	ErrChallengeNotConfirmed = errs.New(errs.Conflict, "buyer.challenge_pending", "звонок ещё не подтверждён")
	// ErrChallengeConflict — проверку изменили параллельно.
	ErrChallengeConflict = errs.New(
		errs.Conflict,
		"buyer.challenge_conflict",
		"проверка изменена параллельно, повторите запрос",
	)
	// ErrSessionNotFound — сессии нет или она истекла.
	ErrSessionNotFound = identity.ErrUnauthorized
	// ErrTooManyAttempts — превышен лимит попыток входа.
	ErrTooManyAttempts = errs.New(
		errs.TooManyRequests,
		"buyer.too_many_attempts",
		"слишком много попыток входа, попробуйте позже",
	)
)
