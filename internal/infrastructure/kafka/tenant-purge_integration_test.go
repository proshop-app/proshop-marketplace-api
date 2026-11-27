//go:build integration

package kafka_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/messaging"
	appparticipant "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/participant"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/searchapi"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/kafka"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	purgeComplete           = `SELECT count(*) FROM marketplace_tenant_deletions WHERE tenant_id = 't0' AND complete`
	purgeDirectoryTombstone = `SELECT count(*) FROM shop_directory_changes WHERE tenant_id = 't0' AND card IS NULL`
	purgeCheckout           = `SELECT count(*) FROM checkouts WHERE id = md5('checkout')::uuid AND status = 'paid'`
	purgeCheckoutTask       = `SELECT count(*) FROM outbox WHERE tenant_id = 't0' AND kind = 'checkout.cancel_order'`
	purgeOrderSnapshot      = `SELECT count(*) FROM checkout_groups
		WHERE tenant_id = 't0' AND order_progress->>'OrderID' = 'order-0' AND lines->0->>'productId' = 'p0'`
	purgeProducts     = 1000
	seedPurgeProducts = `
		INSERT INTO catalog_products
		    (tenant_id, product_id, revision, deleted, card, price, base_price, projected_at, version, updated_at)
		SELECT 't0', 'p' || n, 1, false, '{"name":"Товар","visible":true,
		    "publication":{"published":true,"categoryId":"c1"},"marketplace":{"published":true,"categoryId":"c1"}}',
		    100, 100, now(), 1, now() FROM generate_series(0, $1 - 1) n`
	seedPurgeRows = `
		INSERT INTO offers (tenant_id, product_id, available, stock_at, price, price_at, version, updated_at)
		    SELECT tenant_id, product_id, 1, now(), 100, now(), 1, now() FROM catalog_products WHERE tenant_id = 't0';
		INSERT INTO moderation_verdicts
		    (tenant_id, product_id, revision, rules_revision, taxonomy_version, status, reasons,
		     decided_by, version, updated_at)
		    SELECT tenant_id, product_id, 1, 1, 1, 'allowed', '[]', 'rule', 1, now()
		    FROM catalog_products WHERE tenant_id = 't0';
		SELECT apply_store_summary_changes('t0', 3000);
		SELECT stage_store_summary_change('t0', 'p0');
		INSERT INTO store_cards (tenant_id, slug, name, offers, public, page_public, summary, header, card)
		    VALUES ('t0', 't0', 'Удаляемый магазин', 1000, true, true, '{}', '{}', '{}'),
		           ('t1', 't1', 'Другой магазин', 1, true, true, '{}', '{}', '{}');
		SELECT record_shop_directory_change('t0', shop_directory_card('t0', 't0', 'Удаляемый магазин', 1000));
		SELECT record_shop_directory_change('t1', shop_directory_card('t1', 't1', 'Другой магазин', 1));
		INSERT INTO store_card_categories (category_id, tenant_id, name, count) VALUES ('c1','t0','Категория',1000);
		INSERT INTO moderation_cases
		    (id, tenant_id, product_id, revision, source, status, reasons, note, opened_at, version)
		    SELECT md5(product_id)::uuid, tenant_id, product_id, 1, 'operator', 'allowed', '[]', '', now(), 1
		    FROM catalog_products WHERE tenant_id = 't0';
		INSERT INTO moderation_decisions (id, case_id, operator_id, operator_name, action, reason, note, revision, at)
		    SELECT id, id, 'o1', 'Оператор', 'allow', '', 'Решение', 1, now()
		    FROM moderation_cases WHERE tenant_id = 't0';
		INSERT INTO moderation_idempotency (key_hash, response, expires_at)
		    VALUES ('decision', '{"Result":{"tenantId":"t0"}}', now() + interval '1 day');
		INSERT INTO participant_decisions (id, tenant_id, operator_id, operator_name, action, value, reason, note, at)
		    VALUES (gen_random_uuid(), 't0', 'o1', 'Оператор', 'enable', '', '', '', now());
		INSERT INTO promotion_campaigns
		    (id, tenant_id, kind, placement_code, position, category_id, date_from, date_to, price,
		     status, version, state, created_at, updated_at, product_id)
		    VALUES (gen_random_uuid(), 't0', 'sponsored_slot', 'search_slot', 1, '',
		      '2026-11-02', '2026-11-29', 100, 'active', 1, '{}', now(), now(), 'p0');
		INSERT INTO promotion_views (campaign_id, day, kind, event_id)
		    SELECT id, current_date, 'view', gen_random_uuid() FROM promotion_campaigns, generate_series(1, 401);
		INSERT INTO promotion_counters (campaign_id, day, position, shard)
		    SELECT id, current_date, 1, 0 FROM promotion_campaigns;
		INSERT INTO promotion_decisions (campaign_id, at, actor, action)
		    SELECT id, now(), 'system', 'activate' FROM promotion_campaigns;
		INSERT INTO offer_index_tenant VALUES ('t0', 1, 1, 'p0', false);
		INSERT INTO taxonomy_revalidation_failures VALUES ('t0', 'p0', 1, 1, 1, 'ошибка', now());
		INSERT INTO buyers (id, phone, created_at) VALUES (md5('buyer')::uuid, '+79999999999', now());
		INSERT INTO buyer_shop_subscriptions (buyer_id, tenant_id) VALUES (md5('buyer')::uuid, 't0');
		INSERT INTO checkouts
		    (id, buyer_id, idempotency_key, status, goods_total, delivery_total, total, version, created_at, updated_at)
		    VALUES (md5('checkout')::uuid, md5('buyer')::uuid, 'r0', 'paid', 100, 0, 100, 1, now(), now());
		INSERT INTO checkout_groups (checkout_id, tenant_id, external_checkout_id, lines, goods, order_progress)
		    VALUES (md5('checkout')::uuid, 't0', 'r0', '[{"productId":"p0"}]', 100,
		          '{"Status":"paid","OrderID":"order-0"}');
		INSERT INTO outbox (kind, tenant_id, payload, next_at, created_at, failed_at, failure_reason)
		    SELECT 'catalog.offer.sync', 't0', '{}', now(), now(),
		        CASE WHEN n % 3 = 0 THEN now() END, CASE WHEN n % 3 = 0 THEN 'test' END
		        FROM generate_series(1, 1000) n;
		INSERT INTO outbox (kind, tenant_id, payload, next_at, created_at)
		    VALUES ('checkout.cancel_order', 't0', '{}', now(), now())`
)

func TestTenantPurgeThroughKafkaAndOutbox(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, queries, uow := intakeDatabase(t, measurePool)
	if _, err := pool.Exec(ctx, seedPurgeProducts, purgeProducts); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, seedPurgeRows); err != nil {
		t.Fatal(err)
	}
	before := purgeDirectory(ctx, t, uow, 0)
	index := seedPurgeIndex(ctx, t)

	prefix := unique("tenant-purge-")
	topic := prefix + "." + kafka.TopicTenantDeleted
	createTopics(t, topic)
	pipeline := newPipeline(t, pool, prefix, unique("group-"))
	stream := streamReader(ctx, t, pipeline.reader)
	produce(t, kafkago.Message{
		Topic: topic,
		Key:   []byte("t0"),
		Value: []byte(fmt.Sprintf(`{"tenantId":"t0","at":%q}`, time.Now().Format(time.RFC3339Nano))),
	})
	deletion, err := fetchMessage(ctx, stream)
	if err != nil {
		t.Fatal(err)
	}

	queries.statements.Store(0)
	if err := pipeline.consumer.Process(ctx, deletion.Message); err != nil {
		t.Fatal(err)
	}
	ingest := queries.statements.Load()
	delta := purgeDirectory(ctx, t, uow, before.Revision)
	if !slices.Equal(delta.Removed, []ids.TenantID{"t0"}) || len(delta.Items) != 0 {
		t.Fatalf("дельта справочника: %+v", delta)
	}
	queries.statements.Store(0)
	purger := appparticipant.NewTenantPurger(uow, index, clock.System{})
	runPurgeOutbox(ctx, t, pool, clock.System{}, purger)
	dispatch := queries.statements.Load()
	t.Logf("SQL на %d предложений: %d приём + %d outbox с чтением индекса и очисткой", purgeProducts, ingest, dispatch)

	assertPurgeRows(ctx, t, pool)
	assertPurgeSearch(ctx, t, index)
	repeatPurgeEvent(ctx, t, pipeline, deletion.Message)
	assertPurgeRows(ctx, t, pool)
}

func TestTenantPurgeKeepsActiveOutboxLease(t *testing.T) {
	ctx := context.Background()
	pool, _, uow := intakeDatabase(t, measurePool)
	now := fake.NewClock(time.Now().UTC().Truncate(time.Microsecond))
	queue := postgres.NewTaskQueue(postgres.Single(pool))
	const kind = "test.tenant.lease"
	const eventID = "purge-lease"
	enqueue := func(ctx context.Context, tx ports.Tx) error {
		// Старая версия могла отметить событие, оставив строки магазина и его очередь.
		if _, err := tx.Inbox().MarkProcessed(ctx, eventID, now.Now()); err != nil {
			return err
		}
		return tx.Outbox().EnqueueBatch(ctx, []ports.NewTask{
			{Kind: kind, TenantID: "t0", Payload: []byte("{}"), At: now.Now()},
			{Kind: kind, TenantID: "t1", Payload: []byte("{}"), At: now.Now()},
		})
	}
	if err := uow.Do(ctx, enqueue); err != nil {
		t.Fatal(err)
	}
	claim, err := queue.Claim(ctx, ports.ClaimRequest{
		Kinds:      []string{kind},
		Now:        now.Now(),
		Limit:      2,
		PerTenant:  1,
		LeaseUntil: now.Now().Add(time.Minute),
	})
	if err != nil || len(claim.Tasks) != 2 {
		t.Fatalf("аренда двух задач: %+v, %v", claim, err)
	}
	registry := appparticipant.NewRegistry(uow, now, random.IDs{})
	if err := registry.Apply(ctx, appparticipant.RegistryEvent{
		EventID: eventID,
		Event: participant.RegistryEvent{
			TenantID: "t0",
			Status:   participant.TenantDeleted,
			At:       now.Now(),
		},
	}); err != nil {
		t.Fatal(err)
	}
	purger := appparticipant.NewTenantPurger(uow, fake.NewIndex(), now)
	runPurgeOutbox(ctx, t, pool, clock.System{}, purger)

	resolved := make([]ports.TaskResolution, 0, len(claim.Tasks))
	for _, task := range claim.Tasks {
		resolved = append(resolved, ports.TaskResolution{ID: task.ID, Attempts: task.Attempts, At: now.Now()})
	}
	if err := queue.Resolve(ctx, resolved); err != nil {
		t.Fatalf("удаление сорвало подтверждение задач двух магазинов: %v", err)
	}

	now.Advance(time.Minute)
	runPurgeOutbox(ctx, t, pool, now, purger)
	assertPurgeCount(ctx, t, pool, `SELECT count(*) FROM outbox WHERE tenant_id = 't0'`, 0)
	assertPurgeCount(ctx, t, pool, purgeComplete, 1)
}

func purgeDirectory(ctx context.Context, t *testing.T, uow ports.UnitOfWork, revision int64) ports.ShopDirectory {
	t.Helper()
	var result ports.ShopDirectory
	if err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		var err error
		result, err = tx.StoreCards().Directory(ctx, revision)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func runPurgeOutbox(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, now ports.Clock, purger *appparticipant.TenantPurger,
) {
	t.Helper()
	queue := postgres.NewTaskQueue(postgres.Single(pool))
	dispatcher := messaging.NewDispatcher(
		queue,
		now,
		messaging.DispatcherConfig{
			Batch:        1,
			PerTenant:    1,
			Lease:        time.Minute,
			Kinds:        []string{ports.TaskPurgeTenant},
			OrderedKinds: []string{ports.TaskPurgeTenant},
		},
		map[string]messaging.Handler{ports.TaskPurgeTenant: purger.HandleTask},
	)
	for i := 0; i < 20; i++ {
		outcomes, _, _, err := dispatcher.RunWork(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(outcomes) == 0 {
			return
		}
		if outcomes[0].Err != nil {
			t.Fatal(outcomes[0].Err)
		}
	}
	t.Fatal("очистка не дошла до конца")
}

func assertPurgeRows(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for _, table := range []string{
		"tenants",
		"participants",
		"participant_decisions",
		"catalog_products",
		"offers",
		"store_cards",
		"store_card_categories",
		"store_card_cache_versions",
		"store_summary_products",
		"store_summary_counts",
		"store_summary_changes",
		"store_summary_change_heads",
		"store_summary_change_cursor",
		"store_summary_ratings",
		"moderation_cases",
		"moderation_verdicts",
		"moderation_verdict_log",
		"moderation_idempotency",
		"promotion_campaigns",
		"offer_index_tenant",
		"taxonomy_revalidation_failures",
		"buyer_shop_subscriptions",
	} {
		assertPurgeCount(ctx, t, pool, "SELECT count(*) FROM "+table+" WHERE tenant_id = 't0'", 0)
	}
	for _, table := range []string{
		"moderation_decisions",
		"promotion_decisions",
		"promotion_counters",
		"promotion_views",
	} {
		assertPurgeCount(ctx, t, pool, "SELECT count(*) FROM "+table, 0)
	}
	assertPurgeCount(ctx, t, pool, `SELECT count(*) FROM outbox WHERE tenant_id = 't0'`, 1)
	assertPurgeCount(ctx, t, pool, purgeCheckoutTask, 1)
	assertPurgeCount(ctx, t, pool, purgeCheckout, 1)
	assertPurgeCount(ctx, t, pool, purgeOrderSnapshot, 1)
	assertPurgeCount(ctx, t, pool, `SELECT count(*) FROM store_cards WHERE tenant_id = 't1' AND public`, 1)
	assertPurgeCount(ctx, t, pool, purgeDirectoryTombstone, 1)
	assertPurgeCount(ctx, t, pool, purgeComplete, 1)
}

func assertPurgeCount(ctx context.Context, t *testing.T, pool *pgxpool.Pool, query string, want int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, query).Scan(&count); err != nil || count != want {
		t.Fatalf("%s: число строк %d, ожидалось %d, ошибка %v", query, count, want, err)
	}
}

func repeatPurgeEvent(ctx context.Context, t *testing.T, pipeline *pipeline, message kafkago.Message) {
	t.Helper()
	if err := pipeline.consumer.Process(ctx, message); err != nil {
		t.Fatal(err)
	}
	message.Offset++
	if err := pipeline.consumer.Process(ctx, message); err != nil {
		t.Fatal(err)
	}
	for _, topic := range []string{kafka.TopicTenantCreated, kafka.TopicProductProjected, kafka.TopicStockItemUpdated} {
		late := intakeMessages("product", topic, 1)[0]
		switch topic {
		case kafka.TopicTenantCreated:
			late.Key = []byte("t0")
			at := late.Time.Format(time.RFC3339Nano)
			late.Value = []byte(fmt.Sprintf(`{"tenantId":"t0","slug":"shop","status":"active","at":%q}`, at))
		case kafka.TopicStockItemUpdated:
			late = intakeMessages("stock", topic, 1)[0]
		}
		if err := pipeline.consumer.Process(ctx, late); err != nil {
			t.Fatal(err)
		}
	}
	assertPurgeCount(ctx, t, pipeline.pool, countIntakeParked, 0)
}

func seedPurgeIndex(ctx context.Context, t *testing.T) ports.OfferIndex {
	t.Helper()
	var index ports.OfferIndex = fake.NewIndex()
	if address := os.Getenv("TEST_SEARCH_API_URL"); address != "" {
		parsed, err := url.Parse(address)
		if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" {
			t.Fatal("тест удаления требует поисковый fixture на отдельном loopback-порту")
		}
		client, err := searchapi.New(searchapi.Config{URL: address, Token: "uno-marketplace-integration-token-0001"})
		if err != nil {
			t.Fatal(err)
		}
		index = client
	}
	generation, err := index.NewGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	docs := make([]ports.OfferDocument, 0, purgeProducts+1)
	for i := 0; i < purgeProducts; i++ {
		docs = append(docs, purgeOffer("t0", fmt.Sprintf("p%d", i)))
	}
	docs = append(docs, purgeOffer("t1", "other"))
	if err := index.PutGeneration(ctx, generation, docs); err != nil {
		t.Fatal(err)
	}
	if err := index.Activate(ctx, generation); err != nil {
		t.Fatal(err)
	}
	assertIndexedOffers(ctx, t, index, "t0", purgeProducts)
	return index
}

func purgeOffer(tenant, product string) ports.OfferDocument {
	key := ids.OfferID{Tenant: ids.TenantID(tenant), Product: ids.ProductID(product)}
	return ports.OfferDocument{
		ID:         key.String(),
		TenantID:   tenant,
		ProductID:  product,
		Visible:    true,
		Listed:     true,
		Version:    1,
		Name:       "Товар",
		CategoryID: "c1",
		Price:      100,
		UpdatedAt:  time.Now(),
	}
}

func assertPurgeSearch(ctx context.Context, t *testing.T, index ports.OfferIndex) {
	t.Helper()
	if err := index.Put(ctx, []ports.OfferDocument{purgeOffer("t0", "p0")}); err != nil {
		t.Fatal(err)
	}
	assertIndexedOffers(ctx, t, index, "t0", 0)
	assertIndexedOffers(ctx, t, index, "t1", 1)
}

func assertIndexedOffers(ctx context.Context, t *testing.T, index ports.OfferIndex, tenant string, want int) {
	t.Helper()
	page, err := index.Search(ctx, ports.OfferQuery{TenantIDs: []string{tenant}, Limit: 1})
	if err != nil || page.Total != want {
		t.Fatalf("предложения %s в выдаче: %d, ожидалось %d, ошибка %v", tenant, page.Total, want, err)
	}
}
