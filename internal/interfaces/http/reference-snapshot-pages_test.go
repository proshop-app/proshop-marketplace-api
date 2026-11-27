package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

func TestReferenceSnapshotPageHTTPContract(t *testing.T) {
	store := fake.NewStore()
	store.SeedReferenceDictionary(reference.DictionaryState{
		Code:       "b1",
		Kind:       reference.KindOpen,
		Identity:   true,
		AliasKinds: reference.AliasKinds,
		Limits:     reference.DefaultLimits(),
	})
	clock := fake.NewClock(time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC))
	service := appreference.NewService(store, clock, &fake.IDs{}, nil, appreference.Config{})
	h := referenceHandlers{service: service}
	delivery := reference.Delivery{Dictionary: "b1", Version: 1, Checksum: strings.Repeat("b", 64)}
	for i := range 129 {
		delivery.Entries = append(delivery.Entries, reference.DeliveryEntry{
			Canonical: "Запись " + strconv.Itoa(i),
			Status:    reference.StatusConfirmed,
		})
	}
	if _, err := service.Apply(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	get := func(query string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/internal/reference/b1/snapshot"+query, nil)
		request.SetPathValue("dict", "b1")
		response := httptest.NewRecorder()
		h.snapshot(response, request)
		return response
	}
	response := get("")
	var first snapshotJSON
	if err := json.Unmarshal(response.Body.Bytes(), &first); err != nil || response.Code != http.StatusOK ||
		first.SchemaVersion != 2 || len(first.Entries) != 128 || first.NextCursor == "" {
		t.Fatal(response.Code, len(first.Entries), first.NextCursor, err)
	}
	cursor := url.QueryEscape(first.NextCursor)
	response = get("?cursor=" + cursor)
	var next snapshotJSON
	if err := json.Unmarshal(response.Body.Bytes(), &next); err != nil || response.Code != http.StatusOK ||
		next.Revision != first.Revision || len(next.Entries) != 1 || next.NextCursor != "" {
		t.Fatal(response.Code, next, err)
	}
	if invalid := get("?since=0&cursor=" + cursor); invalid.Code != http.StatusBadRequest {
		t.Fatal(invalid.Code, invalid.Body.String())
	}
}
