package messaging_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const circleKind = "participant.settings.sync"

// Магазины со старейшей задачей упорядоченного вида в повторе не держат круг: полоса без
// готовой головы не считается готовой, курсор двигается и при пустом взятии.
func TestClaimCircleSkipsBlockedOrderedLanes(t *testing.T) {
	store := fake.NewStore()
	ctx := context.Background()
	for n := 1; n <= 8; n++ {
		tenant := ids.TenantID(fmt.Sprintf("a%d", n))
		enqueue(t, store, circleKind, tenant, string(tenant)+"-old", t0.Add(-time.Hour))
		enqueue(t, store, circleKind, tenant, string(tenant)+"-new", t0.Add(-time.Minute))
	}
	for n := 1; n <= 50; n++ {
		tenant := ids.TenantID(fmt.Sprintf("b%02d", n))
		enqueue(t, store, circleKind, tenant, string(tenant), t0.Add(-time.Minute))
	}
	// Старейшая задача каждого a-магазина в аренде: следующую брать нельзя.
	held, err := store.Claim(ctx, circleRequest("", 8, t0.Add(time.Hour)))
	if err != nil || len(held.Tasks) != 8 || held.Tasks[0].TenantID != "a1" {
		t.Fatal(held, err)
	}
	served := map[ids.TenantID]bool{}
	after := ids.TenantID("a0")
	for range 10 {
		claimed, err := store.Claim(ctx, circleRequest(after, 8, t0.Add(time.Hour)))
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range claimed.Tasks {
			if task.TenantID[0] == 'a' {
				t.Fatal("взята задача за арендованной головой", task)
			}
			served[task.TenantID] = true
		}
		after = claimed.Next
	}
	if len(served) != 50 {
		t.Fatalf("круг обслужил %d магазинов из 50", len(served))
	}
}

func circleRequest(after ids.TenantID, limit int, lease time.Time) ports.ClaimRequest {
	return ports.ClaimRequest{
		Now:          t0,
		Limit:        limit,
		PerTenant:    limit,
		AfterTenant:  after,
		OrderedKinds: []string{circleKind},
		LeaseUntil:   lease,
	}
}
