package http

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type referenceDelayedBody struct {
	delay   time.Duration
	content *strings.Reader
}

func (b *referenceDelayedBody) Read(target []byte) (int, error) {
	if b.delay > 0 {
		time.Sleep(b.delay)
		b.delay = 0
	}
	return b.content.Read(target)
}

// Настоящие deadlines соединения проверяются через тот же observe/statusRecorder,
// что и в приложении. Загрузка и обработка дольше обычных таймаутов сервера.
func TestReferenceImportBudgetExtendsOnlyItsConnectionDeadlines(t *testing.T) {
	ordinaryTimeout := 25 * time.Millisecond
	delayed := 3 * ordinaryTimeout
	deadlines := make(chan time.Duration, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /import", referenceImportBudget(func(w http.ResponseWriter, r *http.Request) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			http.Error(w, "нет общего бюджета", http.StatusInternalServerError)
			return
		}
		deadlines <- time.Until(deadline)
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "{}" {
			http.Error(w, "загрузка оборвана", http.StatusBadRequest)
			return
		}
		time.Sleep(delayed)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("GET /regular", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(delayed)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewUnstartedServer(observe(logger, newHTTPMetrics(prometheus.NewRegistry()), mux))
	server.Config.ReadTimeout = ordinaryTimeout
	server.Config.WriteTimeout = ordinaryTimeout
	server.Start()
	t.Cleanup(server.Close)
	body := &referenceDelayedBody{delay: delayed, content: strings.NewReader("{}")}
	response, err := server.Client().Post(server.URL+"/import", "application/json", body)
	if err != nil {
		t.Fatal("импорт потерял deadline через statusRecorder", err)
	}
	var result map[string]bool
	err = json.NewDecoder(response.Body).Decode(&result)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !result["ok"] {
		t.Fatal(response.StatusCode, result, err)
	}
	remaining := <-deadlines
	if remaining < referenceImportTimeout-time.Second || remaining > referenceImportTimeout {
		t.Fatal("у импорта неверный бюджет", remaining)
	}
	if server.Config.ReadTimeout != ordinaryTimeout || server.Config.WriteTimeout != ordinaryTimeout {
		t.Fatal("глобальные таймауты изменились")
	}
	// Следующий запрос на том же клиенте снова ограничен обычным WriteTimeout.
	regular, err := server.Client().Get(server.URL + "/regular")
	if err == nil {
		_ = regular.Body.Close()
		t.Fatal("обычный маршрут получил увеличенный deadline", regular.StatusCode)
	}
}

func TestReferenceImportBudgetRespectsEarlierContextAndReports503(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	handler := referenceImportBudget(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		writeReferenceImportError(w, r, r.Context().Err())
	})
	request := httptest.NewRequest(http.MethodPost, "/import", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	var body errorBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusServiceUnavailable || body.Error.Code != "reference.import_timeout" {
		t.Fatal(recorder.Code, body)
	}
}
