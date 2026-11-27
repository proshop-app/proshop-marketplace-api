//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/errgroup"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	app "github.com/proshop-app/proshop-marketplace/internal/application/participant"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres/pgtest"
	transport "github.com/proshop-app/proshop-marketplace/internal/interfaces/http"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	seedDirectoryTenants = `
		WITH inserted AS (
		  INSERT INTO tenants (tenant_id, slug, status, event_at, version, updated_at)
		  SELECT 'shop-' || i, 'shop-' || i, 'active', $2, 1, $2 FROM generate_series(1, $1::integer) i
		  RETURNING tenant_id, slug
		)
		INSERT INTO participants (tenant_id, slug, name, stage, enabled, version, created_at, updated_at)
		SELECT tenant_id, slug, slug, 'connected', true, 1, $2, $2 FROM inserted`
	seedDirectoryCards = `
		INSERT INTO store_cards (tenant_id, slug, name, offers, public, summary, header, card)
		SELECT tenant_id, tenant_id, tenant_id, i, true, '{}'::jsonb, '{}'::jsonb,
		  jsonb_build_object('tenantId', tenant_id, 'slug', tenant_id, 'name', tenant_id, 'offers', i)
		FROM (SELECT i, 'shop-' || i AS tenant_id FROM generate_series(1, $1::integer) i) shops`
	directoryRevision     = `SELECT revision FROM shop_directory_revision`
	lockDirectoryRevision = directoryRevision + ` FOR SHARE`
	directoryLockWaits    = `SELECT count(*) FROM pg_stat_activity
        WHERE datname = current_database() AND wait_event_type = 'Lock'`
	countShopsWithOffers       = `SELECT count(*) FROM store_cards WHERE offers = $1`
	deleteDirectoryParticipant = `DELETE FROM participants WHERE tenant_id = $1`
	previousDirectoryMigration = 80
	directoryPath              = "/api/v1/shops/directory/snapshot"
	directoryPageSize          = 100
)

type directoryTraffic struct {
	requests int
	cards    int
	bytes    int
}

func TestShopDirectoryRevisionAtScale(t *testing.T) {
	for _, shops := range []int{1000, 5000} {
		t.Run(strconv.Itoa(shops), func(t *testing.T) {
			ctx := context.Background()
			pool := pgtest.Pool(t)
			uow := postgres.NewUnitOfWork(pool)
			handler := transport.NewHandler(transport.Deps{
				RequestTimeout:         time.Minute,
				ReferenceImportTimeout: time.Minute,
				Registerer:             prometheus.NewRegistry(),
				Logger:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
				Participants:           app.NewService(uow, fake.NewClock(at(0)), &fake.IDs{}),
			})
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := pool.Exec(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			if err := postgres.MigrateDownTo(ctx, pool, previousDirectoryMigration); err != nil {
				t.Fatal(err)
			}
			exec(seedDirectoryTenants, shops, at(0))
			exec(seedDirectoryCards, shops)
			if err := postgres.Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			record := directoryStoreRecord(1, 1)
			save := func() {
				t.Helper()
				if err := saveDirectoryStore(ctx, uow, record); err != nil {
					t.Fatal(err)
				}
			}
			save()

			var replicas [3]int64
			var master [3]map[int]string
			cycle := func(label string, expected int, check func(ports.ShopDirectory)) {
				t.Helper()
				var branch, baseline directoryTraffic
				for replica, since := range replicas {
					path := directoryPath
					if since != 0 {
						path += "?since=" + strconv.FormatInt(since, 10)
					}
					response := directoryRequest(t, handler, path, "")
					branch.requests++
					branch.bytes += response.Body.Len()
					switch response.Code {
					case http.StatusOK:
						var result ports.ShopDirectory
						if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil ||
							result.Revision <= since || len(result.Items) != expected || check == nil {
							t.Fatalf("%s: %v %s", label, err, response.Body.String())
						}
						check(result)
						replicas[replica] = result.Revision
						branch.cards += len(result.Items)
					case http.StatusNotModified:
						if response.Body.Len() != 0 || check != nil {
							t.Fatalf("%s: %d, тело %q", label, response.Code, response.Body.String())
						}
					default:
						t.Fatalf("%s: HTTP %d %s", label, response.Code, response.Body.String())
					}

					if master[replica] == nil {
						master[replica] = map[int]string{}
					}
					for offset := range (shops + directoryPageSize - 1) / directoryPageSize {
						path := fmt.Sprintf(
							"/api/v1/shops?limit=%d&offset=%d",
							directoryPageSize,
							offset*directoryPageSize,
						)
						page := directoryRequest(t, handler, path, master[replica][offset])
						baseline.requests++
						baseline.bytes += page.Body.Len()
						switch page.Code {
						case http.StatusOK:
							var body struct {
								Items []json.RawMessage `json:"items"`
							}
							if err := json.Unmarshal(page.Body.Bytes(), &body); err != nil || body.Items == nil {
								t.Fatal(err, page.Body.String())
							}
							master[replica][offset] = page.Header().Get("ETag")
							baseline.cards += len(body.Items)
						case http.StatusNotModified:
							if page.Body.Len() != 0 {
								t.Fatal(page.Body.String())
							}
						default:
							t.Fatal(page.Code, page.Body.String())
						}
					}
				}
				t.Logf(
					"%s, запросы/карточки/байты master → ветка: %d/%d/%d → %d/%d/%d",
					label,
					baseline.requests,
					baseline.cards,
					baseline.bytes,
					branch.requests,
					branch.cards,
					branch.bytes,
				)
			}
			cycle("холодный запуск", shops, func(full ports.ShopDirectory) {
				if len(full.Removed) != 0 {
					t.Fatal(full.Removed)
				}
			})
			cycle("без изменений", 0, nil)
			price := int64(20500)
			record.Summary.FromPrice, record.Card.FromPrice = &price, &price
			save()
			cycle("цена", 0, nil)
			record.Header.Offers, record.Card.Offers, record.Summary.Offers = 2, 2, 2
			save()
			cycle("предложения в пределах ступени", 0, nil)
			record.Header.Name, record.Card.Name = "Новый магазин", "Новый магазин"
			record.Header.Slug, record.Card.Slug = "new-shop", "new-shop"
			save()
			cycle("имя и маршрут", 1, func(changed ports.ShopDirectory) {
				if changed.Items[0].Name != "Новый магазин" || changed.Items[0].Slug != "new-shop" {
					t.Fatal(changed)
				}
			})
			record.Header.Offers, record.Card.Offers, record.Summary.Offers = 10000, 10000, 10000
			save()
			cycle("смена ступени", 1, func(changed ports.ShopDirectory) {
				if changed.Items[0].SizeTier != 3 {
					t.Fatal(changed)
				}
			})
			record.Public, record.PagePublic = false, false
			save()
			cycle("скрытие", 0, func(changed ports.ShopDirectory) {
				if len(changed.Removed) != 1 || changed.Removed[0] != "shop-1" {
					t.Fatal(changed)
				}
			})

			// Догоняющая реплика видит последнее состояние магазина и удаление
			// без скачивания остальных карточек за пропущенные циклы.
			oldest := replicas[0] - 3
			response := directoryRequest(t, handler, directoryPath+"?since="+strconv.FormatInt(oldest, 10), "")
			var delta ports.ShopDirectory
			if err := json.Unmarshal(response.Body.Bytes(), &delta); err != nil || response.Code != 200 ||
				len(delta.Items) != 0 || len(delta.Removed) != 1 {
				t.Fatal(response.Code, response.Body.String(), err)
			}
			var plan string
			row := pool.QueryRow(
				ctx,
				`EXPLAIN (FORMAT JSON) SELECT card FROM shop_directory_changes WHERE revision > $1`,
				replicas[0]-1,
			)
			if err := row.Scan(&plan); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(plan, "shop_directory_changes_revision") {
				t.Fatalf("дельта не использует индекс ревизии: %s", plan)
			}
		})
	}
}

func directoryStoreRecord(shop, offers int) ports.StoreRecord {
	id := fmt.Sprintf("shop-%d", shop)
	return ports.StoreRecord{
		Public:     true,
		PagePublic: true,
		Header: ports.ShopHeader{
			TenantID: ids.TenantID(id),
			Slug:     id,
			Name:     id,
			Offers:   offers,
		},
		Card: ports.ShopCard{
			TenantID: ids.TenantID(id),
			Slug:     id,
			Name:     id,
			Offers:   offers,
		},
		Summary: ports.StoreSummary{
			Offers: offers,
		},
	}
}

func saveDirectoryStore(ctx context.Context, uow ports.UnitOfWork, record ports.StoreRecord) error {
	return uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.StoreCards().Save(ctx, record)
	})
}

func readDirectory(t *testing.T, uow ports.UnitOfWork, since int64) ports.ShopDirectory {
	t.Helper()
	var result ports.ShopDirectory
	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		var err error
		result, err = tx.StoreCards().Directory(ctx, since)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func seedDirectoryStores(t *testing.T, pool *pgxpool.Pool, shops, offers int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), seedDirectoryTenants, shops, at(0)); err != nil {
		t.Fatal(err)
	}
	uow := postgres.NewUnitOfWork(pool)
	for shop := 1; shop <= shops; shop++ {
		if err := saveDirectoryStore(context.Background(), uow, directoryStoreRecord(shop, offers)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestShopDirectoryConcurrentChanges(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	const shops = 75
	seedDirectoryStores(t, pool, shops, 9)
	previous := readDirectory(t, uow, 0).Revision
	var writers sync.WaitGroup
	errors := make(chan error, shops)
	for shop := 1; shop <= shops; shop++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for _, offers := range []int{10, 100} {
				if err := saveDirectoryStore(ctx, uow, directoryStoreRecord(shop, offers)); err != nil {
					errors <- err
					return
				}
			}
		}()
	}
	writers.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	current := readDirectory(t, uow, previous)
	if current.Revision-previous != 2*shops || len(current.Items) != shops || len(current.Removed) != 0 {
		t.Fatalf(
			"потеря ревизий или карточек: %d → %d, карточек %d, удалений %d",
			previous,
			current.Revision,
			len(current.Items),
			len(current.Removed),
		)
	}
}

// Скрытая карточка уходит из справочника поиска той же транзакцией; повтор ревизию не двигает.
func TestShopDirectoryHiddenStoreIsRemoved(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	seedDirectoryStores(t, pool, 2, 9)
	previous := readDirectory(t, uow, 0).Revision
	hide := func(ctx context.Context, tx ports.Tx) error {
		return tx.StoreCards().Hide(ctx, "shop-1")
	}
	for range 2 {
		if err := uow.Do(ctx, hide); err != nil {
			t.Fatal(err)
		}
	}
	current := readDirectory(t, uow, previous)
	removed := len(current.Removed) == 1 && current.Removed[0] == "shop-1"
	if current.Revision != previous+1 || !removed || len(current.Items) != 0 {
		t.Fatalf("скрытый магазин остался в справочнике: %d → %+v", previous, current)
	}
}

func TestShopDirectoryOfferCountsDoNotSerializeStores(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.Pool(t)
	uow := postgres.NewUnitOfWork(pool)
	const shops = 100
	seedDirectoryStores(t, pool, shops, 20)

	writerConfig := pool.Config()
	writerConfig.MaxConns = shops
	writerPool, err := pgxpool.NewWithConfig(ctx, writerConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer writerPool.Close()

	guard, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = guard.Rollback(ctx)
	}()
	var previous int64
	if err := guard.QueryRow(ctx, lockDirectoryRevision).Scan(&previous); err != nil {
		t.Fatal(err)
	}

	// Все записи должны закончиться до первого commit, пока ревизия защищена от UPDATE.
	if err := concurrentDirectoryOfferUpdates(ctx, postgres.NewUnitOfWork(writerPool), shops, pool); err != nil {
		t.Fatal(err)
	}
	var stored int
	if err := pool.QueryRow(ctx, countShopsWithOffers, 21).Scan(&stored); err != nil || stored != shops {
		t.Fatal(stored, err)
	}
	unchanged := readDirectory(t, uow, previous)
	if unchanged.Revision != previous || len(unchanged.Items) != 0 || len(unchanged.Removed) != 0 {
		t.Fatal(unchanged)
	}
	if err := guard.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	record := directoryStoreRecord(1, 21)
	record.Header.Name, record.Card.Name = "Новое имя", "Новое имя"
	if err := saveDirectoryStore(ctx, uow, record); err != nil {
		t.Fatal(err)
	}
	renamed := readDirectory(t, uow, previous)
	if renamed.Revision != previous+1 || len(renamed.Items) != 1 ||
		renamed.Items[0].Name != record.Header.Name || renamed.Items[0].SizeTier != 2 {
		t.Fatal(renamed)
	}
	if _, err := pool.Exec(ctx, deleteDirectoryParticipant, record.Header.TenantID); err != nil {
		t.Fatal(err)
	}
	deleted := readDirectory(t, uow, renamed.Revision)
	if deleted.Revision != renamed.Revision+1 || len(deleted.Items) != 0 ||
		len(deleted.Removed) != 1 || deleted.Removed[0] != record.Header.TenantID {
		t.Fatal(deleted)
	}
	t.Logf(
		"%d транзакций, ожиданий блокировок: 0, обновлений ревизии: 0; новое имя: 1 обновление",
		shops,
	)
}

func concurrentDirectoryOfferUpdates(
	ctx context.Context,
	uow ports.UnitOfWork,
	shops int,
	observer *pgxpool.Pool,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	writers, work := errgroup.WithContext(ctx)
	ready := make(chan struct{}, shops)
	commit := make(chan struct{})
	for shop := 1; shop <= shops; shop++ {
		writers.Go(func() error {
			return uow.Do(work, func(ctx context.Context, tx ports.Tx) error {
				if err := tx.StoreCards().Save(ctx, directoryStoreRecord(shop, 21)); err != nil {
					return err
				}
				ready <- struct{}{}
				select {
				case <-commit:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		})
	}
	for range shops {
		select {
		case <-ready:
		case <-work.Done():
			return writers.Wait()
		}
	}
	var waiting int
	err := observer.QueryRow(work, directoryLockWaits).Scan(&waiting)
	if err == nil && waiting != 0 {
		err = fmt.Errorf("ожидания блокировок: %d", waiting)
	}
	if err != nil {
		cancel()
		_ = writers.Wait()
		return err
	}
	close(commit)
	return writers.Wait()
}

func directoryRequest(t *testing.T, handler http.Handler, path, etag string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	req.Header.Set("X-Marketplace-Money-Unit", "RUB")
	req.Header.Set("If-None-Match", etag)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}
