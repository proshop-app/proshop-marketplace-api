package http

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const maxResolveBody = 4 << 10

var errReferenceUnavailable = errs.New(errs.Unavailable, "reference.unavailable", "общий реестр недоступен")

type referenceHandlers struct{ api ports.ReferenceAPI }

// referenceReader открывает только чтение. Служебные права записи браузеру
// не передаются: оператор проверяется локально и повторно владельцем реестра.
func referenceReader(token string, guarded, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		presented, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if found && token != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1 {
			next(w, r)
			return
		}
		guarded(w, r)
	}
}

func (h referenceHandlers) handler(resource string, write bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.api == nil {
			writeError(w, r, errReferenceUnavailable)
			return
		}
		if len(r.URL.RawQuery) > maxBodyBytes {
			writeError(w, r, ErrInvalidQuery)
			return
		}
		limit := int64(maxBodyBytes)
		if resource == "resolve" {
			limit = maxResolveBody
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		if err != nil || len(body) > 0 && !json.Valid(body) {
			writeError(w, r, ErrInvalidBody)
			return
		}
		response, err := h.api.Do(r.Context(), ports.ReferenceRequest{
			Method:     r.Method,
			Dictionary: r.PathValue("dict"),
			Resource:   strings.ReplaceAll(resource, "{id}", url.PathEscape(r.PathValue("id"))),
			Query:      r.URL.RawQuery,
			Body:       body,
			IfMatch:    r.Header.Get("If-Match"),
			Actor:      principalFrom(r).ID,
			Write:      write,
		})
		if err != nil {
			writeError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if response.ETag != "" {
			w.Header().Set("ETag", response.ETag)
		}
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	}
}
