package http

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

type dictionarySourcesResponse struct {
	Version           int64                          `json:"version"`
	DictionarySources map[string]map[string][]string `json:"dictionarySources"`
}

type dictionarySourcesCache struct {
	mu      sync.Mutex
	version int64
	body    []byte
}

func (h *taxonomyHandlers) dictionarySources(w http.ResponseWriter, r *http.Request) {
	current, err := h.service.Current(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	version, body, err := h.sources.response(current)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if taxonomyCached(w, r, version) {
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(body)
}

func (c *dictionarySourcesCache) response(d taxonomy.Definition) (int64, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Версия монотонна: запоздавший запрос не заменяет уже готовый более новый снимок.
	if c.body != nil && d.Version <= c.version {
		return c.version, c.body, nil
	}
	body, err := json.Marshal(dictionarySourcesResponse{
		Version:           d.Version,
		DictionarySources: d.DictionarySources(),
	})
	if err != nil {
		return 0, nil, err
	}
	c.version, c.body = d.Version, append(body, '\n')
	return c.version, c.body, nil
}
