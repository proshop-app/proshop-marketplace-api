package checkout

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/application/searchfeedback"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
)

// enqueueSearchPurchases выполняется после условной записи группы в той же транзакции.
func (s *Service) enqueueSearchPurchases(ctx context.Context, tx ports.Tx, before, after checkout.State) error {
	if s.config.SearchBuyerKeys == nil {
		return nil
	}
	var buyerKey string
	var tasks []ports.NewTask
	for index, group := range after.Groups {
		if !group.Paid() || before.Groups[index].Paid() {
			continue
		}
		if buyerKey == "" {
			buyerKey = s.config.SearchBuyerKeys.Key(after.BuyerID)
		}
		task, err := searchPurchaseTask(after, group, buyerKey)
		if err != nil {
			return err
		}
		tasks = append(tasks, task)
	}
	if len(tasks) == 0 {
		return nil
	}
	return tx.Outbox().EnqueueBatch(ctx, tasks)
}

func searchPurchaseTask(state checkout.State, group checkout.Group, buyerKey string) (ports.NewTask, error) {
	eventID := "marketplace-paid:" + string(state.ID) + ":" +
		url.QueryEscape(string(group.TenantID)) + ":" + url.QueryEscape(group.OrderID)
	purchase := ports.SearchPurchase{
		Version:        searchfeedback.PurchaseVersion,
		EventID:        eventID,
		BuyerKey:       buyerKey,
		OrderCreatedAt: group.AwaitingSince.UTC(),
		PaidObservedAt: state.UpdatedAt.UTC(),
		OfferIDs:       paidOfferIDs(group.Lines),
	}
	payload, err := json.Marshal(purchase)
	if err != nil {
		return ports.NewTask{}, err
	}
	return ports.NewTask{
		Kind:     searchfeedback.TaskPurchase,
		TenantID: group.TenantID,
		DedupKey: eventID,
		Payload:  payload,
		At:       state.UpdatedAt,
	}, nil
}

func paidOfferIDs(lines []checkout.Line) []string {
	offers := make([]string, 0, len(lines))
	for _, line := range lines {
		if line.Quantity > 0 {
			offers = append(offers, line.Offer.String())
		}
	}
	return offers
}
