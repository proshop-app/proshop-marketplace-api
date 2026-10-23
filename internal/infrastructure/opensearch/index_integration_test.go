//go:build integration

package opensearch_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/opensearch"
)

func clusterURL() string {
	if urls := os.Getenv("TEST_OPENSEARCH_URLS"); urls != "" {
		return strings.Split(urls, ",")[0]
	}

	return "http://localhost:29200"
}

func testIndex(t *testing.T) *opensearch.Index {
	t.Helper()

	urls := clusterURL()

	alias := fmt.Sprintf("test-offers-%d", time.Now().UnixNano())
	index, err := opensearch.New(opensearch.Config{URLs: []string{urls}, Alias: alias, Shards: 1, Replicas: 0})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := index.Ping(ctx); err != nil {
		t.Fatalf("кластер для тестов: %v", err)
	}

	t.Cleanup(func() {
		request, _ := http.NewRequest(http.MethodDelete, urls+"/"+alias+"-*", nil)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Errorf("уборка: %v", err)

			return
		}
		_ = response.Body.Close()

		if response.StatusCode >= http.StatusMultipleChoices {
			t.Errorf("уборка: %s", response.Status)
		}
	})

	return index
}

func offer(id string, name string, price int64, version int64) ports.OfferDocument {
	tenant, product, _ := strings.Cut(id, "~")

	return ports.OfferDocument{
		ID:                id,
		TenantID:          tenant,
		ProductID:         product,
		SellerSlug:        "slug-" + tenant,
		SellerName:        "Магазин " + tenant,
		Name:              name,
		Description:       "Описание товара",
		Image:             "/i.png",
		URL:               "/p",
		SKU:               "SKU-" + product,
		Categories:        []catalog.Category{{ID: "c1", Name: "Кухня", URL: "/c"}},
		Tags:              []string{"tag-1"},
		Price:             price,
		BasePrice:         price,
		Available:         3,
		InStock:           true,
		Visible:           true,
		Listed:            true,
		CheckoutAvailable: true,
		UpdatedAt:         time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC),
		Version:           version,
	}
}

func activate(t *testing.T, index *opensearch.Index) {
	t.Helper()

	ctx := context.Background()
	generation, err := index.NewGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Activate(ctx, generation); err != nil {
		t.Fatal(err)
	}
}

func TestIndexWriteSearchGet(t *testing.T) {
	index := testIndex(t)
	ctx := context.Background()

	if exists, err := index.Exists(ctx); err != nil || exists {
		t.Fatalf("алиаса ещё нет: %v, %v", exists, err)
	}
	activate(t, index)
	if exists, err := index.Exists(ctx); err != nil || !exists {
		t.Fatalf("алиас заведён: %v, %v", exists, err)
	}

	hidden := ports.OfferDocument{ID: "t2~p1", TenantID: "t2", ProductID: "p1", Version: 3, UpdatedAt: time.Now().UTC()}
	docs := []ports.OfferDocument{
		offer("t1~p1", "Чайник электрический", 250000, 2),
		offer("t1~p2", "Кружка керамическая", 50000, 2),
		offer("t3~p1", "Чайники заварочные", 120000, 2),
		hidden,
	}
	if err := index.Put(ctx, docs); err != nil {
		t.Fatal(err)
	}
	if err := index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}

	page, err := index.Search(ctx, ports.OfferQuery{Text: "чайник", Sort: ports.SortPriceAsc, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || page.Items[0].ID != "t3~p1" || page.Items[1].ID != "t1~p1" {
		t.Fatalf("поиск с морфологией и порядком по цене: %+v", page)
	}

	low, high := int64(40000), int64(60000)
	page, err = index.Search(
		ctx,
		ports.OfferQuery{PriceMin: &low, PriceMax: &high, CategoryID: "c1", Tag: "tag-1", InStock: true, Limit: 10},
	)
	if err != nil || page.Total != 1 || page.Items[0].ID != "t1~p2" {
		t.Fatalf("фильтры: %+v, %v", page, err)
	}

	page, err = index.Search(ctx, ports.OfferQuery{Limit: 10})
	if err != nil || page.Total != 3 {
		t.Fatalf("скрытое предложение не в выдаче: %+v, %v", page, err)
	}

	got, found, err := index.Get(ctx, "t1~p1")
	if err != nil || !found || got.Price != 250000 || got.Categories[0].Name != "Кухня" || got.Version != 2 {
		t.Fatalf("документ: %+v, %v, %v", got, found, err)
	}

	if _, found, err := index.Get(ctx, "t9~p9"); err != nil || found {
		t.Fatalf("нет документа: %v, %v", found, err)
	}
}

func TestIndexRejectsStaleVersion(t *testing.T) {
	index := testIndex(t)
	ctx := context.Background()
	activate(t, index)

	if err := index.Put(ctx, []ports.OfferDocument{offer("t1~p1", "Свежий", 100, 5)}); err != nil {
		t.Fatal(err)
	}
	if err := index.Put(ctx, []ports.OfferDocument{offer("t1~p1", "Устаревший", 1, 4)}); err != nil {
		t.Fatalf("устаревшая версия — не ошибка: %v", err)
	}

	got, _, _ := index.Get(ctx, "t1~p1")
	if got.Name != "Свежий" || got.Version != 5 {
		t.Fatalf("устаревший документ не перезаписывает свежий: %+v", got)
	}

	if err := index.Put(ctx, []ports.OfferDocument{offer("t1~p1", "Тот же", 100, 5)}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := index.Get(ctx, "t1~p1"); got.Name != "Тот же" {
		t.Fatalf("равная версия — тот же снимок, запись принимается: %+v", got)
	}
}

func TestIndexGenerationsSwap(t *testing.T) {
	index := testIndex(t)
	ctx := context.Background()
	activate(t, index)

	if err := index.Put(ctx, []ports.OfferDocument{offer("t1~old", "Старый", 100, 1)}); err != nil {
		t.Fatal(err)
	}

	generation, err := index.NewGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}

	fresh := []ports.OfferDocument{offer("t1~new", "Новый", 100, 1)}
	if err := index.PutGeneration(ctx, generation, fresh); err != nil {
		t.Fatal(err)
	}

	if _, found, _ := index.Get(ctx, "t1~new"); found {
		t.Fatal("до переключения поколение невидимо")
	}
	if err := index.Activate(ctx, generation); err != nil {
		t.Fatal(err)
	}

	if _, found, _ := index.Get(ctx, "t1~new"); !found {
		t.Fatal("после переключения видно новое поколение")
	}
	if _, found, _ := index.Get(ctx, "t1~old"); found {
		t.Fatal("старое поколение удалено")
	}

	orphan, err := index.NewGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := index.NewGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Activate(ctx, latest); err != nil {
		t.Fatal(err)
	}

	response, err := http.Get(clusterURL() + "/_cat/indices/" + orphan + "?format=json")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("брошенное поколение удаляется при переключении: %s", response.Status)
	}
}
