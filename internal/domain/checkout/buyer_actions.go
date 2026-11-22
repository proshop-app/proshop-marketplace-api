package checkout

import (
	"strings"
	"time"
	"unicode/utf8"
)

// ReturnReason — причина возврата, которую выбирает покупатель.
type ReturnReason string

// ReturnRequestStatus — решение по заявке; согласие магазина — статус заказа returned.
type ReturnRequestStatus string

// Словарь заявки на возврат по договору с сервером магазина.
const (
	ReturnNotFit         ReturnReason = "not_fit"
	ReturnDefective      ReturnReason = "defective"
	ReturnNotAsDescribed ReturnReason = "not_as_described"
	ReturnChangedMind    ReturnReason = "changed_mind"

	ReturnRequested ReturnRequestStatus = "requested"
	ReturnRejected  ReturnRequestStatus = "rejected"

	// CancelledBeforeShipment — причина returned у оплаченного заказа, отменённого до отправки.
	CancelledBeforeShipment = "cancelled_before_shipment"

	maxReturnComment = 500
)

// ReturnRequest — заявка покупателя на возврат из состояния магазина.
type ReturnRequest struct {
	Status      ReturnRequestStatus `json:"status"`
	Reason      ReturnReason        `json:"reason"`
	RequestedAt time.Time           `json:"requestedAt"`
	ResolvedAt  *time.Time          `json:"resolvedAt,omitempty"`
	Comment     string              `json:"comment,omitempty"`
}

// CancellationByBuyer решает, нужна ли команда магазину. Повтор состоявшейся отмены — не ошибка;
// заказ у перевозчика не отменяется, неоплаченный убирают из покупки отдельной командой.
func (g Group) CancellationByBuyer() (bool, error) {
	if g.OrderID == "" || g.Abandoned {
		return false, ErrOrderNotCancellable
	}
	switch g.EffectiveStatus() {
	case GroupPaid, GroupAssembled:
		return true, nil
	case GroupReturned:
		if g.Reason == CancelledBeforeShipment {
			return false, nil
		}
	}
	return false, ErrOrderNotCancellable
}

// ReturnByBuyer решает, нужна ли заявка магазину: одна, по полученному заказу, повтор открытой — не ошибка.
func (g Group) ReturnByBuyer() (bool, error) {
	if g.OrderID == "" || g.Abandoned || g.EffectiveStatus() != GroupDelivered {
		return false, ErrReturnUnavailable
	}
	if g.ReturnRequest == nil {
		return true, nil
	}
	if g.ReturnRequest.Status == ReturnRequested {
		return false, nil
	}
	return false, ErrReturnUnavailable
}

// ValidReturnReason ограничивает причину словарём договора.
func ValidReturnReason(reason ReturnReason) bool {
	switch reason {
	case ReturnNotFit, ReturnDefective, ReturnNotAsDescribed, ReturnChangedMind:
		return true
	}
	return false
}

// knownReturnRequest принимает заявку магазина только по договору; объяснение обрезается по пределу.
func knownReturnRequest(value *ReturnRequest) *ReturnRequest {
	if value == nil || value.RequestedAt.IsZero() || !ValidReturnReason(value.Reason) ||
		(value.Status != ReturnRequested && value.Status != ReturnRejected) {
		return nil
	}
	result := cloneReturnRequest(value)
	result.Comment = strings.TrimSpace(result.Comment)
	if utf8.RuneCountInString(result.Comment) > maxReturnComment {
		result.Comment = string([]rune(result.Comment)[:maxReturnComment])
	}
	return result
}

func cloneReturnRequest(value *ReturnRequest) *ReturnRequest {
	if value == nil {
		return nil
	}
	result := *value
	if value.ResolvedAt != nil {
		at := *value.ResolvedAt
		result.ResolvedAt = &at
	}
	return &result
}
