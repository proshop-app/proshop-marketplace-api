package participant_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	app "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
)

func TestCardCandidatesRankingAndPagination(t *testing.T) {
	_, service := seedStoreCard(t)
	page, err := service.CardCandidates(context.Background(), "t1", app.CardCandidatesQuery{Limit: 2})
	if err != nil || page.Total != 4 || page.Page != 1 || page.Limit != 2 {
		t.Fatal(page, err)
	}
	got := []string{page.Items[0].ProductID, page.Items[1].ProductID}
	if !reflect.DeepEqual(got, []string{"p1", "p4"}) {
		t.Fatal(got)
	}
	page, err = service.CardCandidates(context.Background(), "t1", app.CardCandidatesQuery{Page: 2, Limit: 2})
	if err != nil || len(page.Items) != 2 || page.Items[0].ProductID != "p3" {
		t.Fatal(page, err)
	}
	huge := int(^uint(0) >> 1)
	page, err = service.CardCandidates(context.Background(), "t1", app.CardCandidatesQuery{Page: huge})
	if err != nil || page.Items == nil || len(page.Items) != 0 || page.Total != 4 {
		t.Fatal(page, err)
	}
}

func TestCardCandidatesCurrentVisibilityAndHydration(t *testing.T) {
	store, service := seedStoreCard(t)
	store.SeedVerdict(moderation.VerdictState{TenantID: "t1", ProductID: "p3", Status: moderation.Rejected})
	product, _ := store.Product("t1", "p2")
	product.Card.Name = "Красная сумка"
	store.SeedProduct(product)
	foreign := product
	foreign.TenantID = "other"
	foreign.ProductID = "foreign"
	store.SeedProduct(foreign)
	page, err := service.CardCandidates(context.Background(), "t1", app.CardCandidatesQuery{Query: "КРАСНАЯ"})
	if err != nil || page.Total != 1 || page.Items[0].ProductID != "p2" {
		t.Fatal(page, err)
	}
	page, err = service.CardCandidates(context.Background(), "t1", app.CardCandidatesQuery{
		IDs: []string{"p2", "p3", "p1"},
	})
	if err != nil || page.Total != 2 || page.Items[0].ProductID != "p2" || page.Items[1].ProductID != "p1" {
		t.Fatal(page, err)
	}
	page, err = service.CardCandidates(context.Background(), "t1", app.CardCandidatesQuery{IDs: []string{"foreign"}})
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
	state, _ := store.Participant("t1")
	state.Enabled = false
	state.Stage = participant.StageDisconnected
	store.SeedParticipant(state)
	page, err = service.CardCandidates(context.Background(), "t1", app.CardCandidatesQuery{})
	if err != nil || page.Total != 0 {
		t.Fatal(page, err)
	}
}

func TestCardCandidatesInvalidQuery(t *testing.T) {
	_, service := seedStoreCard(t)
	for _, query := range []app.CardCandidatesQuery{
		{Page: -1},
		{Limit: 21},
		{Query: strings.Repeat("я", app.CardCandidatesQueryMax+1)},
		{Query: "товар", IDs: []string{"p1"}},
		{IDs: []string{"p1", "p1"}},
		{IDs: []string{"p1", "p2", "p3", "p4"}},
		{IDs: []string{"$invalid"}},
	} {
		_, err := service.CardCandidates(context.Background(), "t1", query)
		if !errors.Is(err, app.ErrInvalidCardCandidates) {
			t.Fatal(query, err)
		}
	}
}
