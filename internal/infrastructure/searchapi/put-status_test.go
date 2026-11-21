package searchapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type putTransport struct {
	status int
	calls  int
	size   int
}

func (transport *putTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	defer func() {
		_ = request.Body.Close()
	}()
	var body offerBatch
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		return nil, err
	}
	transport.calls++
	transport.size = len(body.Offers)
	return &http.Response{
		StatusCode: transport.status,
		Body:       io.NopCloser(strings.NewReader("{}")),
		Header:     http.Header{},
	}, nil
}

func TestPutBatchClassifiesDocumentRejectionWithoutSockets(t *testing.T) {
	for _, status := range []int{204, 400, 413, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			transport := &putTransport{status: status}
			index := &Index{
				base:   "http://search.example.test/api/v1/internal/marketplace",
				client: &http.Client{Transport: transport},
			}
			docs := []ports.OfferDocument{
				{ID: "t1~p1", TenantID: "t1", ProductID: "p1", Version: 1},
				{ID: "t2~p2", TenantID: "t2", ProductID: "p2", Version: 1},
			}
			err := index.Put(context.Background(), docs)
			if transport.calls != 1 || transport.size != 2 {
				t.Fatalf("пачка должна идти одним HTTP-запросом: %+v", transport)
			}
			switch status {
			case 204:
				if err != nil {
					t.Fatal(err)
				}
			case 400, 413:
				if errs.CodeOf(err) != errs.Invalid {
					t.Fatalf("нужна изоляция документа: %v", err)
				}
			default:
				if errs.CodeOf(err) != errs.Unavailable {
					t.Fatalf("нужен повтор всей пачки: %v", err)
				}
			}
		})
	}
}

func TestSearchNetworkFailuresRemainRetryable(t *testing.T) {
	cause := errors.New("соединение разорвано")
	index := &Index{
		base: "http://search.example.test/api/v1/internal/marketplace",
		client: &http.Client{Transport: featureTransport(func(*http.Request) (*http.Response, error) {
			return nil, cause
		})},
	}
	err := index.Put(context.Background(), []ports.OfferDocument{{ID: "t1~p1", Version: 1}})
	if errs.CodeOf(err) != errs.Unavailable || !errors.Is(err, cause) {
		t.Fatal("сбой транспорта потерял класс или причину", err)
	}
}

func TestPutContinuesAfterCapacityAcrossByteBoundedBatches(t *testing.T) {
	const exact = `{"code":"GENERATION_CAPACITY","deferredOffers":[
		{"tenantId":"t1","productId":"p1","part":"marketplace-offers-test-p00-data","reason":"roots"}]}`
	for _, tc := range []struct {
		name       string
		body       string
		nextStatus int
		want       []ids.OfferID
		from       int
	}{
		{
			name:       "partial",
			body:       exact,
			nextStatus: http.StatusNoContent,
			want:       []ids.OfferID{{Tenant: "t1", Product: "p1"}},
			from:       3,
		},
		{
			name:       "legacy",
			body:       `{"code":"GENERATION_CAPACITY"}`,
			nextStatus: http.StatusNoContent,
			want: []ids.OfferID{
				{Tenant: "t1", Product: "p1"},
				{Tenant: "t2", Product: "p1"},
			},
			from: 3,
		},
		{
			name:       "invalid-list",
			body:       `{"code":"GENERATION_CAPACITY","deferredOffers":{}}`,
			nextStatus: http.StatusNoContent,
			want: []ids.OfferID{
				{Tenant: "t1", Product: "p1"},
				{Tenant: "t2", Product: "p1"},
			},
			from: 3,
		},
		{
			name:       "foreign-id",
			body:       strings.Replace(exact, "t1", "foreign", 1),
			nextStatus: http.StatusNoContent,
			want: []ids.OfferID{
				{Tenant: "t1", Product: "p1"},
				{Tenant: "t2", Product: "p1"},
			},
			from: 3,
		},
		{
			name:       "later-unconfirmed",
			body:       exact,
			nextStatus: http.StatusBadRequest,
			want:       []ids.OfferID{{Tenant: "t1", Product: "p1"}},
			from:       2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index, sizes := capacityBatchTestIndex(t, tc.body, tc.nextStatus)
			description := strings.Repeat("<", 400_000)
			docs := []ports.OfferDocument{
				{TenantID: "t1", ProductID: "p1", Version: 1, Description: description},
				{TenantID: "t2", ProductID: "p1", Version: 1},
				{TenantID: "t3", ProductID: "p1", Version: 1, Description: description},
			}
			err := index.Put(context.Background(), docs)
			if !reflect.DeepEqual(*sizes, []int{2, 1}) {
				t.Fatalf("два HTTP-подпакета [2, 1], получили %v", *sizes)
			}
			var partial *ports.OfferCapacityError
			if !errors.As(err, &partial) || !reflect.DeepEqual(partial.Deferred, tc.want) ||
				partial.UnconfirmedFrom != tc.from || errs.RetryKindOf(err) != errs.RetryTransient {
				t.Fatalf("неверный частичный результат: %+v, %v", partial, err)
			}
			if tc.nextStatus == http.StatusNoContent && partial.UnconfirmedError != nil ||
				tc.nextStatus == http.StatusBadRequest && errs.CodeOf(partial.UnconfirmedError) != errs.Invalid {
				t.Fatalf("ошибка хвоста пачки: %v", partial.UnconfirmedError)
			}
		})
	}
}

func capacityBatchTestIndex(t *testing.T, body string, nextStatus int) (*Index, *[]int) {
	t.Helper()
	var sizes []int
	transport := featureTransport(func(request *http.Request) (*http.Response, error) {
		defer func() {
			_ = request.Body.Close()
		}()
		var batch offerBatch
		if err := json.NewDecoder(request.Body).Decode(&batch); err != nil {
			return nil, err
		}
		if request.ContentLength > requestLimit {
			t.Fatalf("HTTP-запрос превышает предел: %d", request.ContentLength)
		}
		sizes = append(sizes, len(batch.Offers))
		if len(sizes) == 1 {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}
		return &http.Response{StatusCode: nextStatus, Body: http.NoBody}, nil
	})
	return &Index{base: "http://search.example.test", client: &http.Client{Transport: transport}}, &sizes
}
