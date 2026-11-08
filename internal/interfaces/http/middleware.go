package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	headerRequestID = "X-Request-Id"
	maxBodyBytes    = 64 << 10
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type contextKey int

const (
	keyRequestID contextKey = iota
	keyLogger
	keyBuyer
	keyFacts
)

// requestFacts — что обработчики узнали о запросе; observe дописывает это
// в строку журнала запроса.
type requestFacts struct {
	buyerID       ids.UUID
	catalogFacets *catalogFacetFacts
}

type catalogFacetFacts struct {
	selector   string
	class      string
	total      int
	attributes int
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

type httpMetrics struct {
	catalogOffers *prometheus.CounterVec
	requests      *prometheus.CounterVec
	duration      *prometheus.HistogramVec
}

// RequestID — идентификатор запроса из контекста.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(keyRequestID).(string)

	return id
}

func loggerFrom(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(keyLogger).(*slog.Logger); ok {
		return logger
	}

	return slog.Default()
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func newHTTPMetrics(registerer prometheus.Registerer) *httpMetrics {
	m := &httpMetrics{
		catalogOffers: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_catalog_offers_total",
				Help: "Успешные HTTP-выдачи каталога по выбору фасетов.",
			},
			[]string{"selector"},
		),
		requests: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_http_requests_total",
				Help: "Запросы HTTP по маршруту и коду ответа.",
			},
			[]string{"route", "code"},
		),
		duration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "marketplace_http_request_duration_seconds",
				Help:    "Длительность запросов HTTP по маршруту.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"route"},
		),
	}
	registerer.MustRegister(m.requests, m.duration, m.catalogOffers)
	for _, selector := range []string{"all", "none", "partial"} {
		m.catalogOffers.WithLabelValues(selector)
	}

	return m
}

// observe — идентификатор запроса, журнал, метрики и перехват паники.
func observe(logger *slog.Logger, metrics *httpMetrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()

		id := r.Header.Get(headerRequestID)
		if !requestIDPattern.MatchString(id) {
			id = newRequestID()
		}

		requestLogger := logger.With(slog.String("requestId", id))
		facts := &requestFacts{}

		ctx := context.WithValue(r.Context(), keyRequestID, id)
		ctx = context.WithValue(ctx, keyLogger, requestLogger)
		ctx = context.WithValue(ctx, keyFacts, facts)
		r = r.WithContext(ctx)
		w.Header().Set(headerRequestID, id)

		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		defer func() {
			if recovered := recover(); recovered != nil {
				requestLogger.Error("http: паника обработчика", slog.Any("panic", recovered))
				writeJSON(
					recorder,
					http.StatusInternalServerError,
					errorBody{Error: errorDetail{Code: "internal", Message: "внутренняя ошибка"}},
				)
			}

			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}

			elapsed := time.Since(started)
			metrics.requests.WithLabelValues(route, strconv.Itoa(recorder.status)).Inc()
			metrics.duration.WithLabelValues(route).Observe(elapsed.Seconds())
			attrs := []any{
				slog.String("method", r.Method),
				slog.String("route", route),
				slog.Int("status", recorder.status),
				slog.Duration("duration", elapsed),
			}
			if facts.buyerID != "" {
				attrs = append(attrs, slog.String("buyerId", string(facts.buyerID)))
			}
			if facts.catalogFacets != nil {
				if recorder.status == http.StatusOK {
					metrics.catalogOffers.WithLabelValues(facts.catalogFacets.class).Inc()
				}
				attrs = append(
					attrs,
					slog.String("catalogFacetSelector", facts.catalogFacets.selector),
					slog.Int("catalogFacetCount", facts.catalogFacets.total),
					slog.Int("catalogAttributeFacetCount", facts.catalogFacets.attributes),
				)
			}

			requestLogger.Info("http", attrs...)
		}()

		next.ServeHTTP(recorder, r)
	})
}

func newRequestID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}

	return hex.EncodeToString(buf)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// readJSON разбирает тело строго: неизвестные поля и мусор после объекта — ошибка.
func readJSON(w http.ResponseWriter, r *http.Request, out any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(out); err != nil {
		return ErrInvalidBody.Wrap(err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrInvalidBody.WithMessage("после объекта в теле есть лишнее")
	}

	return nil
}

// rememberBuyer отмечает покупателя запроса для строки журнала запроса.
func rememberBuyer(ctx context.Context, buyerID ids.UUID) {
	if facts, ok := ctx.Value(keyFacts).(*requestFacts); ok {
		facts.buyerID = buyerID
	}
}
