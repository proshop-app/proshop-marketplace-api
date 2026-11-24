//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	appcart "github.com/proshop-app/proshop-marketplace/internal/application/cart"
	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

const (
	cartScaleOffers = 100
	// connectScaleParticipants делает магазины замера видимыми покупателю.
	connectScaleParticipants = `UPDATE participants
        SET stage = 'connected',
            enabled = true,
            delivery_settings = $1::jsonb,
            delivery_valid = true`
)

// TestCartAddAtScale замеряет добавление в корзину на настоящем PostgreSQL рядом с каталогом
// INTAKE_SCALE_CATALOG: медиану Add и операторов SQL на добавление. Поиск в замере не участвует.
func TestCartAddAtScale(t *testing.T) {
	if os.Getenv("CART_SCALE") != "1" {
		t.Skip("замер: CART_SCALE=1")
	}
	catalogSize, _ := strconv.Atoi(os.Getenv("INTAKE_SCALE_CATALOG"))
	ctx := context.Background()
	base := testPool(t)
	config := base.Config().Copy()
	counter := &statementCounter{}
	config.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uow := postgres.NewUnitOfWork(pool)
	projector := seedProjectionIntake(ctx, t, uow, pool, catalogSize)
	platform := seedCartScaleOffers(ctx, t, pool, projector)
	seedBuyer(t, uow, buyerA, "79990000002")
	service := appcart.NewService(uow, platform, random.IDs{}, fake.NewClock(time.Now()), time.Hour)
	durations := make([]time.Duration, 0, cartScaleOffers)
	counter.statements.Store(0)
	for n := range cartScaleOffers {
		offer := cartScaleOffer(n)
		if os.Getenv("CART_SCALE_TRACE") == "1" && (n == 0 || n == cartScaleOffers-1) {
			counter.print.Store(200)
		}
		started := time.Now()
		if _, err := service.Add(ctx, buyerA, offer, 1); err != nil {
			t.Fatal(err)
		}
		durations = append(durations, time.Since(started))
		counter.print.Store(0)
	}
	statements := float64(counter.statements.Load()) / cartScaleOffers
	slices.Sort(durations)
	median, p95 := durations[len(durations)/2], durations[len(durations)*95/100]
	summary := "MEASURE cart catalog=%d adds=%d median=%s p95=%s sql=%.1f/add"
	t.Logf(summary, catalogSize, cartScaleOffers, median, p95, statements)
}

func seedCartScaleOffers(
	ctx context.Context,
	t *testing.T,
	pool *pgxpool.Pool,
	projector *appcatalog.Projector,
) *fake.Platform {
	t.Helper()
	events := make([]appcatalog.ProductEvent, 0, cartScaleOffers)
	for n := range cartScaleOffers {
		events = append(events, intakeScaleEvent(n, 1, "cart-"))
	}
	if err := projector.ApplyProducts(ctx, events); err != nil {
		t.Fatal(err)
	}
	carriers := []string{}
	for _, carrier := range delivery.Carriers() {
		carriers = append(carriers, string(carrier.Code))
	}
	settings, err := json.Marshal(participant.DeliverySettings{Regions: []string{"RU-MOW"}, Carriers: carriers})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, connectScaleParticipants, string(settings)); err != nil {
		t.Fatal(err)
	}
	platform := fake.NewPlatform()
	for n := range cartScaleOffers {
		offer := cartScaleOffer(n)
		item := ports.ItemCheck{ProductID: offer.Product, Exists: true, Available: 5, Price: money.FromKopecks(10000)}
		platform.SetItem(offer.Tenant, item)
	}
	return platform
}

func cartScaleOffer(n int) ids.OfferID {
	snapshot := intakeScaleEvent(n, 1, "cart-").Snapshot
	return ids.OfferID{Tenant: snapshot.TenantID, Product: snapshot.ProductID}
}
