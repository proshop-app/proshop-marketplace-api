package kafka

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

var (
	// ErrTenantMissing — у сообщения нет магазина ни в ключе, ни в теле, ни в заголовке.
	ErrTenantMissing = errs.New(errs.Invalid, "kafka.tenant_missing", "у сообщения нет магазина")
	// ErrTenantMismatch — магазин в ключе, теле и заголовке расходится.
	ErrTenantMismatch = errs.New(errs.Invalid, "kafka.tenant_mismatch", "магазин в ключе, теле и заголовке расходится")
	// ErrInvalidPayload — сообщение не разбирается.
	ErrInvalidPayload = errs.New(errs.Invalid, "kafka.invalid_payload", "сообщение не разбирается")
	// ErrUnknownTopic — для топика нет обработчика.
	ErrUnknownTopic = errs.New(errs.Invalid, "kafka.unknown_topic", "для топика нет обработчика")
)
