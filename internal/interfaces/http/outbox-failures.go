package http

import (
	"net/http"
	"strconv"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

type requeueTasksJSON struct {
	Kind  string `json:"kind"`
	After int64  `json:"after,string"`
	Limit int    `json:"limit"`
}

type requeuedTasksJSON struct {
	IDs   []string `json:"ids"`
	After string   `json:"after"`
}

func (h *operatorHandlers) outboxFailures(w http.ResponseWriter, r *http.Request) {
	after, limit := int64(0), 50
	if raw := r.URL.Query().Get("after"); raw != "" {
		var err error
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, r, ErrInvalidQuery)
			return
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeError(w, r, ErrInvalidQuery)
			return
		}
	}
	items, err := h.dispatcher.Failures(r.Context(), after, limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *operatorHandlers) requeueTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("taskId"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, r, ErrInvalidQuery)
		return
	}
	h.requeue(w, r, ports.TaskRequeue{ID: id, Limit: 1})
}

func (h *operatorHandlers) requeueTasks(w http.ResponseWriter, r *http.Request) {
	body := requeueTasksJSON{Limit: 100}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	h.requeue(w, r, ports.TaskRequeue{Kind: body.Kind, After: body.After, Limit: body.Limit})
}

func (h *operatorHandlers) requeue(w http.ResponseWriter, r *http.Request, request ports.TaskRequeue) {
	items, err := h.dispatcher.Requeue(r.Context(), request)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result := requeuedTasksJSON{IDs: []string{}, After: strconv.FormatInt(request.After, 10)}
	for _, id := range items {
		result.After = strconv.FormatInt(id, 10)
		result.IDs = append(result.IDs, result.After)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}
