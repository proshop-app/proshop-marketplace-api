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

func TestRemovingUnreachableShopPreservesHealthyOrderAndRequiresConsent(t *testing.T) {
	e := setup(t)
	purchase := confirmedPurchase(t, e)
	ctx := context.Background()
	e.platform.FailCreate("t1", errors.New("timeout"))
	started, err := beginAndExecute(ctx, e, buyerID, purchase.ID, orderContact)
	if err != nil {
		t.Fatal(err)
	}
	healthy := started.Groups[1].OrderID
	e.clock.Advance(appcheckout.CreationTimeout)
	if err := resumeAndExecute(ctx, e); err != nil {
		t.Fatal(err)
	}
	state, _ := e.service.Get(ctx, buyerID, purchase.ID)
	if state.Status != checkout.StatusDraft || state.Total != purchase.Total || state.Groups[0].Excluded() {
		t.Fatal(state)
	}
	if _, err := e.service.Confirm(ctx, buyerID, purchase.ID); !errors.Is(err, checkout.ErrSellerUnavailable) {
		t.Fatal("срок нельзя продлить новым confirm", err)
	}
	removed, err := e.service.RemoveGroup(ctx, buyerID, purchase.ID, "t1")
	if err != nil || removed.Status != checkout.StatusDraft || removed.Total.Kopecks() != 35000 {
		t.Fatal(removed, err)
	}
	if _, err := e.service.Pay(ctx, buyerID, purchase.ID); !errors.Is(err, checkout.ErrTransition) {
		t.Fatal("нужно новое согласие", err)
	}
	repeated, err := e.service.RemoveGroup(ctx, buyerID, purchase.ID, "t1")
	if err != nil || repeated.Total != removed.Total {
		t.Fatal(repeated, err)
	}
	final := reconfirmPurchase(t, e, purchase.ID)
	if final.Groups[1].OrderID != healthy || len(e.platform.OrderRequests("t2")) != 1 {
		t.Fatal(final)
	}
	if err := e.service.ConfirmPayment(ctx, buyerID, purchase.ID); err != nil {
		t.Fatal(err)
	}
	if calls := e.platform.StubPaymentCalls(); len(calls) != 1 || calls[0] != "t2" {
		t.Fatal(calls)
	}
}
