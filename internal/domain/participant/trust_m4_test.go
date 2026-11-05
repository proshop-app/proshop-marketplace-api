package participant_test

import (
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func TestDemotionRestartsProbation(t *testing.T) {
	now := time.Now()
	p := participant.Restore(participant.State{TenantID: "t1", Trust: participant.Trusted, ApprovedOffers: 50})
	changed, err := p.SetTrust(participant.Probation, now)
	if err != nil || !changed || p.State().ApprovedOffers != 0 {
		t.Fatal(p.State(), err)
	}
	p.ApproveOffer(2, now)
	if p.State().Trust != participant.Probation {
		t.Fatal(p.State())
	}
	if changed, err := p.SetTrust(participant.Probation, now); err != nil || changed {
		t.Fatal("повтор доверия изменил состояние", err)
	}
	p.ApproveOffer(2, now)
	if p.State().Trust != participant.Trusted || p.State().ApprovedOffers != 2 {
		t.Fatal(p.State())
	}
}

func TestAutomaticPromotionActor(t *testing.T) {
	now := time.Now()
	p := participant.Restore(participant.State{TenantID: "t1", Trust: participant.Probation})
	p.ApproveOffer(1, now)
	d := p.PromotionDecision("00000000-0000-4000-8000-000000000001", now)
	if d.Actor != "system" || d.Operator.ID != "" || d.Reason != "" || d.ReasonMessage != "" ||
		d.Note != "Одобрено 1 предложений" || d.Value != participant.Trusted || !d.At.Equal(now) {
		t.Fatal(d)
	}
}
