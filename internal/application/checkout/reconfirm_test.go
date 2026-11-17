package checkout_test

import (
	"context"
	"errors"
	"testing"

	appcheckout "github.com/proshop-app/proshop-marketplace/internal/application/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/checkout"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func reconfirmPurchase(t *testing.T, e *env, id ids.UUID) checkout.State {
	t.Helper()
	ctx := context.Background()
	if _, err := e.service.Confirm(ctx, buyerID, id); err != nil {
		t.Fatal("новое подтверждение состава", err)
	}
	state, err := beginAndExecute(ctx, e, buyerID, id, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestReviewBeforePaymentPreservesHealthyOrderAndNeverChargesRemovedGroup(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	purchase := confirmedPurchase(t, e)
	state, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	healthyOrder := state.Groups[1].OrderID
	seller, _ := e.store.Participant("t1")
	seller.Stage = participant.StageDisconnected
	e.store.SeedParticipant(seller)
	reviewed, err := e.service.Pay(ctx, buyerID, purchase.ID)
	if err == nil || reviewed.Status != checkout.StatusDraft || reviewed.Total.Kopecks() != 35000 {
		t.Fatal("состав не возвращён на подтверждение", reviewed.Status, reviewed.Total, err)
	}
	if len(e.platform.StubPaymentCalls()) != 0 {
		t.Fatal("деньги до нового согласия")
	}
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	if len(e.platform.CancellationRequests("t1")) != 1 {
		t.Fatal("нет отмены снятого заказа")
	}
	state = reconfirmPurchase(t, e, purchase.ID)
	if state.Groups[1].OrderID != healthyOrder || len(e.platform.OrderRequests("t2")) != 1 {
		t.Fatal("здоровый заказ потерян или продублирован", state.Groups)
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, purchase.ID); err != nil {
		t.Fatal(err)
	}
	payments := e.platform.StubPaymentCalls()
	if len(payments) != 1 || payments[0] != "t2" {
		t.Fatal(payments)
	}
}

func TestTemporaryCreationRetriesOriginalIntentAfterReview(t *testing.T) {
	e := setup(t)
	purchase := confirmedPurchase(t, e)
	ctx := context.Background()
	e.platform.FailCreate("t1", errors.New("timeout"))
	if _, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(appcheckout.CreationTimeout)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	review, err := e.service.Get(ctx, buyerID, purchase.ID)
	if err != nil || review.Status != checkout.StatusDraft || review.Total != purchase.Total ||
		review.Groups[0].Excluded() {
		t.Fatal(review, err)
	}
	if _, err := e.service.QuoteGroup(ctx, buyerID, purchase.ID, "t1", "cdek", "other", "Город"); !errors.Is(
		err,
		checkout.ErrSnapshotChanged,
	) {
		t.Fatal("нельзя менять ПВЗ отправленного намерения", err)
	}
	e.platform.FailCreate("t1", nil)
	if _, err := e.service.Confirm(ctx, buyerID, purchase.ID); err != nil {
		t.Fatal(err)
	}
	otherContact := orderContact
	otherContact.Name = "Другой получатель"
	if _, err := e.service.CreateOrders(ctx, buyerID, purchase.ID, otherContact); !errors.Is(
		err,
		checkout.ErrSnapshotChanged,
	) {
		t.Fatal(err)
	}
	result, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	if err != nil || result.Status != checkout.StatusAwaitingPayment || result.Total != purchase.Total ||
		result.Groups[1].OrderID != review.Groups[1].OrderID || len(e.platform.OrderRequests("t2")) != 1 {
		t.Fatal(result, err)
	}
}
