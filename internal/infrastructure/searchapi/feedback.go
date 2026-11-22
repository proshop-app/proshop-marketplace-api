package searchapi

import (
	"context"
	"fmt"
	"net/http"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const (
	feedbackLimit         = 16 << 10
	purchaseLimit         = 32 << 10
	behaviorResponseLimit = 1 << 10
)

var errPurchaseRejected = errs.New(errs.Invalid, "search.purchase_rejected", "поиск окончательно отклонил покупку")

type feedbackEvent struct {
	Kind     string   `json:"kind"`
	Receipt  string   `json:"receipt"`
	OfferID  string   `json:"offerId,omitempty"`
	OfferIDs []string `json:"offerIds,omitempty"`
	BuyerKey string   `json:"buyerKey,omitempty"`
}

type feedbackResult struct {
	Data bool `json:"data"`
}

type purchaseResult struct {
	Data struct {
		Attributed *int `json:"attributed"`
	} `json:"data"`
}

// Feedback передаёт событие в область площадки по существующему служебному допуску.
func (x *Index) Feedback(ctx context.Context, event ports.SearchFeedback) (bool, error) {
	var result feedbackResult
	status, err := x.requestWith(
		ctx,
		http.MethodPost,
		"/feedback",
		x.token,
		feedbackEvent(event),
		&result,
		bodyLimits{send: feedbackLimit, receive: behaviorResponseLimit},
		nil,
	)
	return status == http.StatusOK && result.Data, err
}

// Purchase повторяется при HTTP 503 и при потере сетевого ответа, сохраняя eventId.
func (x *Index) Purchase(ctx context.Context, event ports.SearchPurchase) error {
	if x.purchaseToken == "" {
		return errPurchaseRejected.WithMessage("нет допуска на запись покупок поиска")
	}
	var result purchaseResult
	status, err := x.requestWith(
		ctx,
		http.MethodPost,
		"/search-purchases",
		x.purchaseToken,
		event,
		&result,
		bodyLimits{send: purchaseLimit, receive: behaviorResponseLimit},
		nil,
	)
	switch status {
	case 0, http.StatusServiceUnavailable:
		return err
	case http.StatusOK:
		if err == nil && result.Data.Attributed != nil && *result.Data.Attributed >= 0 &&
			*result.Data.Attributed <= len(event.OfferIDs) {
			return nil
		}
		return errPurchaseRejected.WithMessage("поиск вернул некорректный результат покупки")
	default:
		return errPurchaseRejected.WithMessage(fmt.Sprintf("поиск отклонил покупку: HTTP %d", status))
	}
}
