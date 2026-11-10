package http

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
)

type reviewDeadlineUOW struct {
	remaining time.Duration
}

func (u *reviewDeadlineUOW) Do(ctx context.Context, _ func(context.Context, ports.Tx) error) error {
	deadline, exists := ctx.Deadline()
	if exists {
		u.remaining = time.Until(deadline)
	}
	return context.DeadlineExceeded
}

func TestDictionaryRevisionParserUsesStrictSingleDictionaryETagIncludingZero(t *testing.T) {
	for _, header := range [][]string{
		nil,
		{""},
		{`"0"`},
		{`"reference-01"`},
		{`W/"reference-1"`},
		{"reference-1"},
		{`"reference--1"`},
		{`"reference-1"`, `"reference-1"`},
		{`"reference-1", "reference-2"`},
		{`"reference-9223372036854775808"`},
		{` "reference-1"`},
	} {
		request := httptest.NewRequest("POST", "/", nil)
		for _, value := range header {
			request.Header.Add("If-Match", value)
		}
		if _, err := dictionaryRevisionOf(request); err == nil {
			t.Fatal("нестрогий If-Match принят", header)
		}
	}
	request := httptest.NewRequest("POST", "/", nil)
	request.Header.Set("If-Match", `"reference-0"`)
	if revision, err := dictionaryRevisionOf(request); err != nil || revision != 0 {
		t.Fatal(revision, err)
	}
}

func TestReviewDeadlineIsBoundedBeforeOpeningUnitOfWork(t *testing.T) {
	uow := &reviewDeadlineUOW{}
	handler := NewHandler(Deps{
		Reference:     appreference.NewService(uow, nil, nil, nil, appreference.Config{}),
		OperatorToken: "token",
		Registerer:    prometheus.NewRegistry(),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	request := httptest.NewRequest(
		"POST",
		"/api/v1/operator/reference/b1/entries/review",
		strings.NewReader(`{"status":"confirmed","selection":{"mode":"allCandidates","excludedIds":[]}}`))
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("If-Match", `"reference-0"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || uow.remaining < 24*time.Second ||
		uow.remaining > referenceReviewTimeout ||
		!strings.Contains(response.Body.String(), "reference.review_timeout") {
		t.Fatal(response.Code, response.Body.String(), uow.remaining)
	}
}
