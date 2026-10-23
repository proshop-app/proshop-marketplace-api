package http

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
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
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

type httpMetrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
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
	registerer.MustRegister(m.requests, m.duration)

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
		ctx := context.WithValue(context.WithValue(r.Context(), keyRequestID, id), keyLogger, requestLogger)
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
			requestLogger.Info(
				"http",
				slog.String("method", r.Method),
				slog.String("route", route),
				slog.Int("status", recorder.status),
				slog.Duration("duration", elapsed),
			)
		}()

		next.ServeHTTP(recorder, r)
	})
}

// operatorOnly пропускает запрос со служебным токеном оператора в заголовке
// Authorization: Bearer. Сравнение — за постоянное время.
func operatorOnly(token string, next http.HandlerFunc) http.HandlerFunc {
	expected := []byte(token)

	return func(w http.ResponseWriter, r *http.Request) {
		presented, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found || subtle.ConstantTimeCompare([]byte(presented), expected) != 1 {
			writeError(w, r, ErrUnauthorized)

			return
		}

		next(w, r)
	}
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
