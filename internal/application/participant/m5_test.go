package participant_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func TestM5NotesAndPreviousValues(t *testing.T) {
	store, _, service := setup(t)
	service.WithProbationOffers(12)
	ctx := context.Background()
	view, err := service.Create(ctx, appparticipant.CreateCommand{TenantID: "t1", Name: "Магазин"})
	if err != nil || view.ProbationOffers != 12 {
		t.Fatal(view, err)
	}
	readyState, ok := store.Participant("t1")
	if !ok {
		t.Fatal("участник не найден")
	}
	readyState.StoreCard.Readiness.Ready = true
	store.SeedParticipant(readyState)

	if _, err := service.Enable(ctx, "t1", participant.Decision{}); err != nil {
		t.Fatal(err)
	}
	for _, note := range []string{"", " \n\t", strings.Repeat("я", 2001)} {
		d := participant.Decision{Note: note}
		if _, err := service.Disable(ctx, "t1", d); !errors.Is(err, participant.ErrInvalidNote) {
			t.Fatal(err)
		}
		if _, err := service.SetTrust(ctx, "t1", participant.Trusted, d); !errors.Is(err, participant.ErrInvalidNote) {
			t.Fatal(err)
		}
	}
	d := participant.Decision{Note: "  " + strings.Repeat("я", 2000) + "  "}
	if _, err := service.Disable(ctx, "t1", d); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetTrust(ctx, "t1", participant.Trusted, d); err != nil {
		t.Fatal(err)
	}
	page, err := service.ListDecisions(ctx, "t1", participant.Page{Limit: 10})
	if err != nil || page.Total != 4 {
		t.Fatal(page, err)
	}
	for _, record := range page.Items {
		expected := "candidate"
		if record.Action == "candidate" {
			expected = ""
		}
		if record.Action == "disable" {
			expected = "connected"
		}
		if record.Action == "trust" {
			expected = participant.Probation
		}
		if record.PreviousValue != expected {
			t.Fatal(record)
		}
	}
}
