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
	held, err := store.Claim(ctx, circleRequest(ports.ClaimCursor{}, 8, t0.Add(time.Hour)))
	if err != nil || len(held.Tasks) != 8 || held.Tasks[0].TenantID != "a1" {
		t.Fatal(held, err)
	}
	served := map[ids.TenantID]bool{}
	after := ports.ClaimAfter("a0")
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

func circleRequest(after ports.ClaimCursor, limit int, lease time.Time) ports.ClaimRequest {
	return ports.ClaimRequest{
		Now:          t0,
		Limit:        limit,
		PerTenant:    limit,
		After:        after,
		OrderedKinds: []string{circleKind},
		LeaseUntil:   lease,
	}
}

// Общая задача площадки с пустым магазином, оставшаяся в очереди, не возвращает круг в начало:
// магазины за первыми ClaimLaneVisits полосами обслуживаются. Тот же сценарий на настоящей базе —
// TestClaimCirclePassesSharedTaskWithEmptyTenant в postgres.
func TestClaimCirclePassesSharedTaskWithEmptyTenant(t *testing.T) {
	store := fake.NewStore()
	ctx := context.Background()
	const sharedKind = "reference.changed"
	lease := t0.Add(time.Hour)
	for n := 1; n <= 300; n++ {
		tenant := ids.TenantID(fmt.Sprintf("m%03d", n))
		enqueue(t, store, circleKind, tenant, string(tenant)+"-old", t0.Add(-time.Hour))
		enqueue(t, store, circleKind, tenant, string(tenant)+"-new", t0.Add(-2*time.Minute))
	}
	enqueue(t, store, sharedKind, "", "shared", t0.Add(-time.Hour))
	// Головы всех полос и общая задача уходят в аренду: остаются в очереди несозревшими.
	held, after := 0, ports.ClaimCursor{}
	for range 8 {
		request := circleRequest(after, 200, lease)
		request.OrderedKinds = []string{circleKind, sharedKind}
		claimed, err := store.Claim(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		held, after = held+len(claimed.Tasks), claimed.Next
	}
	if held != 301 {
		t.Fatalf("в аренде %d задач из 301", held)
	}
	for n := 1; n <= 5; n++ {
		tenant := ids.TenantID(fmt.Sprintf("z%d", n))
		enqueue(t, store, circleKind, tenant, string(tenant), t0.Add(-time.Minute))
	}
	served := map[ids.TenantID]bool{}
	after = ports.ClaimCursor{}
	for range 12 {
		request := circleRequest(after, 8, lease)
		request.OrderedKinds = []string{circleKind, sharedKind}
		claimed, err := store.Claim(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range claimed.Tasks {
			if task.TenantID == "" || task.TenantID[0] == 'm' {
				t.Fatal("взята задача за арендованной головой", task)
			}
			served[task.TenantID] = true
		}
		after = claimed.Next
	}
	if len(served) != 5 {
		t.Fatalf("круг обслужил %d магазинов из 5: %v", len(served), served)
	}
}
