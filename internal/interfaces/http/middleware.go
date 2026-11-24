package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
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
	status  int
	written bool
}

type httpMetrics struct {
	catalogOffers *prometheus.CounterVec
	requests      *prometheus.CounterVec
	duration      *prometheus.HistogramVec
	panics        *prometheus.CounterVec
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
	s.written = true
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(body []byte) (int, error) {
	s.written = true
	return s.ResponseWriter.Write(body)
}

// Unwrap сохраняет доступ ResponseController к дедлайнам соединения под наблюдением.
func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
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
		panics: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_http_panics_total",
				Help: "Паники обработчиков HTTP по маршруту; клиент получил 500 или оборванный ответ.",
			},
			[]string{"route"},
		),
	}
	registerer.MustRegister(m.requests, m.duration, m.catalogOffers, m.panics)
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
			abort := recoverHandler(recover(), recorder, r, requestLogger, metrics)
			logRequest(r, recorder, facts, time.Since(started), requestLogger, metrics)
			if abort {
				// net/http обрывает соединение без своего стека в журнале: стек уже записан здесь.
				panic(http.ErrAbortHandler)
			}
		}()

		next.ServeHTTP(recorder, r)
	})
}

// recoverHandler превращает панику обработчика в 500 со стеком в журнале и метрикой.
// true — ответ надо оборвать: обработчик сам прервал его через http.ErrAbortHandler
// (по смыслу стандартной библиотеки — без стека в журнале) либо паника случилась
// после начала ответа, и дописать JSON поверх уже отправленного нельзя.
func recoverHandler(
	recovered any,
	recorder *statusRecorder,
	r *http.Request,
	logger *slog.Logger,
	metrics *httpMetrics,
) bool {
	if recovered == nil {
		return false
	}
	if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
		return true
	}
	metrics.panics.WithLabelValues(routeOf(r)).Inc()
	logger.Error("http: паника обработчика", slog.Any("panic", recovered), slog.String("stack", string(debug.Stack())))
	if recorder.written {
		return true
	}
	writeJSON(
		recorder,
		http.StatusInternalServerError,
		errorBody{Error: errorDetail{Code: "internal", Message: "внутренняя ошибка"}},
	)
	return false
}

func routeOf(r *http.Request) string {
	if r.Pattern == "" {
		return "unmatched"
	}
	return r.Pattern
}

// logRequest пишет строку журнала и метрики запроса.
func logRequest(
	r *http.Request,
	recorder *statusRecorder,
	facts *requestFacts,
	elapsed time.Duration,
	logger *slog.Logger,
	metrics *httpMetrics,
) {
	route := routeOf(r)
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

	logger.Info("http", attrs...)
}

func newRequestID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}

	return hex.EncodeToString(buf)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		// Ошибка транспорта не должна отдавать 200 или частичный денежный ответ.
		status = http.StatusInternalServerError
		raw = []byte(`{"error":{"code":"internal","message":"внутренняя ошибка"}}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(raw, '\n'))
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
