package http

import (
	"net/http"
	"strconv"
	"strings"

	appsynonyms "github.com/proshop-app/proshop-marketplace/internal/application/synonyms"
	"github.com/proshop-app/proshop-marketplace/internal/domain/synonyms"
)

const (
	synonymCommandsBody = 256 << 10
	operatorSynonymPath = "/api/v1/operator/reference/search-synonyms"
	synonymSnapshotPath = "/api/v1/internal/reference/search-synonyms/snapshot"
)

type synonymHandlers struct {
	service *appsynonyms.Service
}

type synonymChange struct {
	ExpectedRevision *int64 `json:"expectedRevision"`
	Reason           string `json:"reason"`
}

type synonymDisable struct {
	synonymChange
	Enabled *bool `json:"enabled"`
}

type synonymStopList struct {
	synonymChange
	BlockedTerms []string `json:"blockedTerms"`
}

func (h synonymHandlers) snapshot(w http.ResponseWriter, r *http.Request) {
	etag := r.Header.Get("If-None-Match")
	rev, body, err := h.service.Snapshot(r.Context(), synonymRevision(etag))
	if err != nil {
		writeError(w, r, err)
		return
	}
	current := synonymETag(rev)
	w.Header().Set("ETag", current)
	w.Header().Set("Cache-Control", "private, no-cache")
	if etagMatches(etag, current) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func synonymRevision(etag string) int64 {
	value := strings.TrimPrefix(strings.TrimSpace(etag), "W/")
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		rev, err := strconv.ParseInt(value[1:len(value)-1], 10, 64)
		if err == nil && synonymETag(rev) == value {
			return rev
		}
	}
	return -1
}

func synonymETag(revision int64) string {
	return `"` + strconv.FormatInt(revision, 10) + `"`
}

func (h synonymHandlers) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, r, synonyms.ErrInvalid)
			return
		}
		limit = parsed
	}
	result, err := h.service.List(r.Context(), query.Get("after"), limit)
	writeSynonymResult(w, r, result, err)
}

func (h synonymHandlers) disable(w http.ResponseWriter, r *http.Request) {
	var data synonymDisable
	if err := decodeLimited(w, r, &data, synonymCommandsBody); err != nil {
		writeError(w, r, err)
		return
	}
	if data.Enabled == nil || *data.Enabled || data.ExpectedRevision == nil {
		writeError(w, r, synonyms.ErrInvalid)
		return
	}
	rev, err := h.service.Disable(r.Context(), r.PathValue("id"), data.command(r))
	writeSynonymResult(w, r, map[string]int64{"revision": rev}, err)
}

func (h synonymHandlers) stopList(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.StopList(r.Context())
	writeSynonymResult(w, r, result, err)
}

func (h synonymHandlers) replaceStopList(w http.ResponseWriter, r *http.Request) {
	var data synonymStopList
	if err := decodeLimited(w, r, &data, synonymCommandsBody); err != nil {
		writeError(w, r, err)
		return
	}
	if data.ExpectedRevision == nil {
		writeError(w, r, synonyms.ErrInvalid)
		return
	}
	result, err := h.service.ReplaceStopList(r.Context(), data.BlockedTerms, data.command(r))
	writeSynonymResult(w, r, result, err)
}

func (c synonymChange) command(r *http.Request) appsynonyms.OperatorChange {
	return appsynonyms.OperatorChange{
		ExpectedRevision: *c.ExpectedRevision,
		Actor:            principalFrom(r).ID,
		Reason:           c.Reason,
	}
}

func writeSynonymResult(w http.ResponseWriter, r *http.Request, result any, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}
