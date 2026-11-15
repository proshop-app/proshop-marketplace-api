package searchapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
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
