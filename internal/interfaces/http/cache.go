package http

import (
	"net/http"
	"strings"
)

// publicCached проверяет слабые и сильные ETag для публичных GET.
func publicCached(w http.ResponseWriter, r *http.Request, etag string) bool {
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	for _, candidate := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		candidate = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(candidate), "W/"))
		if candidate == etag || candidate == "*" {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
	}
	return false
}
