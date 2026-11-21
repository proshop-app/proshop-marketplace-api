package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	appbuyer "github.com/proshop-app/proshop-marketplace/internal/application/buyer"
	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/application/searchfeedback"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	feedbackReceipt = "03af0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab"
	feedbackBuyer   = ids.UUID("b0000000-0000-4000-8000-000000000001")
	feedbackClick   = `{"kind":"click","receipt":"` + feedbackReceipt + `","offerId":"seller-a~p42"}`
	feedbackShow    = `{"kind":"impression","receipt":"` + feedbackReceipt + `","offerIds":["seller-a~p42"]}`
)

type feedbackHTTPEnv struct {
	handler http.Handler
	index   *fake.PromotionIndex
	limiter *fake.Limiter
	tokens  *feedbackTokens
	sink    *feedbackSink
	key     string
	token   string
}

type feedbackTokens struct {
	ports.IdentityTokens
	stall bool
	err   error
	calls int
}

type feedbackSink struct {
	events []ports.SearchFeedback
	err    error
}

func (a *feedbackTokens) Authenticate(ctx context.Context, token string) (ports.IdentityAccess, error) {
	a.calls++
	if a.stall {
		<-ctx.Done()
		return ports.IdentityAccess{}, ctx.Err()
	}
	if a.err != nil {
		return ports.IdentityAccess{}, a.err
	}
	return a.IdentityTokens.Authenticate(ctx, token)
}

func (s *feedbackSink) Feedback(_ context.Context, event ports.SearchFeedback) (bool, error) {
	s.events = append(s.events, event)
	return s.err == nil, s.err
}

func newFeedbackHTTP(t *testing.T) feedbackHTTPEnv {
	t.Helper()
	ctx := context.Background()
	clock := fake.NewClock(time.Now())
	store := fake.NewStore()
	if err := store.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return tx.Buyers().Add(ctx, buyer.New(feedbackBuyer, "79000000001", clock.Now()))
	}); err != nil {
		t.Fatal(err)
	}
	tokens := &feedbackTokens{IdentityTokens: fake.NewIdentityTokens(clock)}
	access, err := tokens.Open(ctx, string(feedbackBuyer))
	if err != nil {
		t.Fatal(err)
	}
	document := ports.OfferDocument{ID: "seller-a~p42", TenantID: "seller-a", ProductID: "p42", Visible: true}
	index := &fake.PromotionIndex{
		Index: fake.NewIndex(),
		Result: ports.OfferPage{
			Receipt: feedbackReceipt,
			Items:   []ports.OfferDocument{document},
			Total:   1,
		},
	}
	if err := index.Put(ctx, []ports.OfferDocument{document}); err != nil {
		t.Fatal(err)
	}
	keys := searchfeedback.NewBuyerKeys(strings.Repeat("k", 32))
	sink, limiter := &feedbackSink{}, fake.NewLimiter()
	handler := NewHandler(Deps{
		RequestTimeout:         time.Second,
		ReferenceImportTimeout: time.Second,
		Auth:                   appbuyer.NewAuth(store, nil, tokens, nil, nil, clock, appbuyer.AuthConfig{}),
		Catalog:                appcatalog.NewQueries(store, index, nil, nil, nil, clock),
		SearchFeedback:         searchfeedback.NewFeedback(sink, limiter),
		SearchBuyerKeys:        keys,
		TrustedProxies:         1,
		Registerer:             prometheus.NewRegistry(),
		Logger:                 slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return feedbackHTTPEnv{
		handler: handler,
		index:   index,
		limiter: limiter,
		tokens:  tokens,
		sink:    sink,
		key:     keys.Key(feedbackBuyer),
		token:   access.Token,
	}
}

func (e feedbackHTTPEnv) send(method, path, body, cookie, bearer, ip string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set(moneyUnitHeader, "RUB")
	request.Header.Set("X-Forwarded-For", ip)
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response := httptest.NewRecorder()
	e.handler.ServeHTTP(response, request)
	return response
}

func TestCatalogSearchObservation(t *testing.T) {
	e := newFeedbackHTTP(t)
	const route = "/api/v1/catalog/offers"
	const observed = route + "?q=товар&observe=1&offset=20&track=1"
	response := e.send(http.MethodGet, observed, "", e.token, "", "")
	assertObservation(t, response, feedbackReceipt, "private, no-store")
	query := e.index.Query
	if !query.Observe || query.TrackQuery || query.Offset != 20 || query.BuyerKey != e.key {
		t.Fatalf("поиск: %+v", e.index.Query)
	}
	if strings.Contains(response.Body.String(), e.key) || e.tokens.calls != 1 {
		t.Fatal("псевдоним должен оставаться служебным, проверка access выполняется один раз")
	}
	response = e.send(http.MethodGet, observed, "", "", "", "")
	assertObservation(t, response, feedbackReceipt, "private, no-store")
	if e.index.Query.BuyerKey != "" || e.tokens.calls != 1 {
		t.Fatal("гость не проверяет identity")
	}
	for _, path := range []string{
		route + "?q=товар",
		route + "?observe=1&q=%20",
		route + "?ids=seller-a~p42&observe=1",
	} {
		e.index.Query = ports.OfferQuery{}
		cache := "private, no-store"
		if path == route+"?q=товар" {
			cache = publicCatalogCacheControl
		}
		response = e.send(http.MethodGet, path, "", e.token, "", "")
		assertObservation(t, response, "", cache)
		if e.index.Query.Observe || e.index.Query.BuyerKey != "" || e.tokens.calls != 1 {
			t.Fatal(path, e.index.Query, e.tokens.calls)
		}
	}
}

func TestCatalogSearchObservationSurvivesIdentityAndReceiptFailures(t *testing.T) {
	e := newFeedbackHTTP(t)
	const path = "/api/v1/catalog/offers?q=товар&observe=1"
	for _, token := range []string{"invalid", e.token} {
		if token == e.token {
			e.tokens.err = errors.New("identity недоступен")
		}
		response := e.send(http.MethodGet, path, "", "", token, "")
		assertObservation(t, response, feedbackReceipt, "private, no-store")
		if e.index.Query.BuyerKey != "" {
			t.Fatal("выдача должна продолжиться в гостевом режиме")
		}
	}
	e.tokens.stall = true
	assertObservation(t, e.send(http.MethodGet, path, "", "", e.token, ""), feedbackReceipt, "private, no-store")
	if e.index.Query.BuyerKey != "" {
		t.Fatal("таймаут identity не должен мешать гостевой выдаче")
	}
	e.tokens.stall = false
	e.index.Result.Receipt = ""
	assertObservation(t, e.send(http.MethodGet, path, "", "", e.token, ""), "", "private, no-store")
	e.index.Result = ports.OfferPage{Receipt: feedbackReceipt}
	assertObservation(t, e.send(http.MethodGet, path, "", "", "", ""), "", "private, no-store")
	for _, suffix := range []string{"", "0", "true", "1&observe=1"} {
		response := e.send(http.MethodGet, "/api/v1/catalog/offers?q=товар&observe="+suffix, "", e.token, "", "")
		if response.Code != http.StatusBadRequest {
			t.Fatal(response.Code, response.Body.String())
		}
	}
}

func assertObservation(t *testing.T, response *httptest.ResponseRecorder, receipt, cache string) {
	t.Helper()
	var body offerPageJSON
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != cache || body.Receipt != receipt {
		t.Fatal(response.Code, response.Header(), response.Body.String())
	}
	if receipt == "" && strings.Contains(response.Body.String(), `"receipt"`) {
		t.Fatal("пустая квитанция не должна быть в JSON")
	}
}

func TestSearchFeedbackHTTP(t *testing.T) {
	e := newFeedbackHTTP(t)
	const path = "/api/v1/search/feedback"
	const address = "198.51.100.2"
	assertFeedback(t, e.send(http.MethodPost, path, feedbackShow, e.token, "", address), true)
	if e.tokens.calls != 0 || e.limiter.Calls() != 1 || len(e.sink.events) != 1 || e.sink.events[0].BuyerKey != "" {
		t.Fatal(e.tokens.calls, e.limiter.Calls(), e.sink.events)
	}
	assertFeedback(t, e.send(http.MethodPost, path, feedbackClick, "", e.token, address), true)
	if e.tokens.calls != 1 || e.sink.events[1].BuyerKey != e.key || e.sink.events[1].OfferID != "seller-a~p42" {
		t.Fatal(e.tokens.calls, e.sink.events)
	}
	e.tokens.err = errors.New("identity недоступен")
	assertFeedback(t, e.send(http.MethodPost, path, feedbackClick, e.token, "", address), true)
	if e.sink.events[2].BuyerKey != "" {
		t.Fatal(e.sink.events)
	}
	e.sink.err = errors.New("поиск недоступен")
	assertFeedback(t, e.send(http.MethodPost, path, feedbackClick, "", "", address), false)
	e.sink.err = nil
	for range 116 {
		assertFeedback(t, e.send(http.MethodPost, path, feedbackShow, "", "", address), true)
	}
	assertFeedback(t, e.send(http.MethodPost, path, feedbackClick, e.token, "", address), false)
	if len(e.sink.events) != 120 || e.tokens.calls != 2 {
		t.Fatal("лишний запрос дошёл до identity или поиска", e.tokens.calls, len(e.sink.events))
	}
	assertFeedback(t, e.send(http.MethodPost, path, feedbackShow, "", "", "198.51.100.3"), true)
	e.limiter.Err = errors.New("Redis недоступен")
	assertFeedback(t, e.send(http.MethodPost, path, feedbackShow, "", "", "198.51.100.3"), false)
	if len(e.sink.events) != 121 {
		t.Fatal("при отказе лимитера поиск вызван", len(e.sink.events))
	}
}

func TestSearchFeedbackRejectsMalformedBeforeDependencies(t *testing.T) {
	e := newFeedbackHTTP(t)
	const path = "/api/v1/search/feedback"
	const limit = searchfeedback.MaxBodyBytes
	const showPrefix = `{"kind":"impression","receipt":"` + feedbackReceipt + `","offerIds":[`
	for _, body := range []string{
		`{}`,
		`null`,
		feedbackClick + ` {}`,
		strings.Replace(feedbackClick, feedbackReceipt, "bad", 1),
		strings.Replace(feedbackClick, "seller-a~p42", "p42", 1),
		strings.Replace(feedbackClick, `"kind":"click"`, `"kind":"purchase"`, 1),
		strings.TrimSuffix(feedbackClick, "}") + `,"buyerKey":"spoof"}`,
		strings.TrimSuffix(feedbackClick, "}") + `,"offerIds":null}`,
		strings.TrimSuffix(feedbackShow, "}") + `,"offerId":null}`,
		showPrefix + `]}`,
		showPrefix + strings.Repeat(`"seller-a~p42",`, 48) + `"seller-a~p42"]}`,
		feedbackShow + strings.Repeat(" ", limit-len(feedbackShow)+1),
	} {
		response := e.send(http.MethodPost, path, body, e.token, "", "")
		if response.Code != http.StatusBadRequest {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	if e.tokens.calls != 0 || e.limiter.Calls() != 0 || len(e.sink.events) != 0 {
		t.Fatal(e.tokens.calls, e.limiter.Calls(), len(e.sink.events))
	}
	id := `"` + strings.Repeat("a", 128) + "~" + strings.Repeat("b", 128) + `"`
	body := showPrefix + strings.Repeat(id+`,`, 47) + id + `]}`
	body += strings.Repeat(" ", limit-len(body))
	assertFeedback(t, e.send(http.MethodPost, path, body, e.token, "", ""), true)
	if len(e.sink.events[0].OfferIDs) != 48 || e.limiter.Calls() != 1 {
		t.Fatal("48 предложений на границе размера должны пройти за один вызов")
	}
}

func assertFeedback(t *testing.T, response *httptest.ResponseRecorder, accepted bool) {
	t.Helper()
	want, _ := json.Marshal(searchFeedbackResponse{Data: accepted})
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != string(want) ||
		response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal(response.Code, response.Header(), response.Body.String())
	}
}
