package http

import (
	"net/http"
	"strings"
)

const publicCatalogCacheControl = "public, max-age=30"

// publicCached проверяет слабые и сильные ETag для публичных GET.
func publicCached(w http.ResponseWriter, r *http.Request, etag string) bool {
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(candidate), "W/"))
		if candidate == etag || candidate == "*" {
			return true
		}
	}
	return false
}
