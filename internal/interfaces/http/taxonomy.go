package http

import (
	"net/http"
	"strconv"
	"strings"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/interfaces/taxonomyfile"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const maxTaxonomyBody = 2 << 20

type taxonomyHandlers struct {
	service *apptaxonomy.Service
	catalog *appcatalog.Queries
}

func taxonomyCached(w http.ResponseWriter, r *http.Request, version int64) bool {
	etag := `"taxonomy-` + strconv.FormatInt(version, 10) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Taxonomy-Version", strconv.FormatInt(version, 10))
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

func (h *taxonomyHandlers) categories(w http.ResponseWriter, r *http.Request) {
	d, err := h.service.Current(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	if taxonomyCached(w, r, d.Version) {
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *taxonomyHandlers) schema(w http.ResponseWriter, r *http.Request) {
	d, err := h.service.Current(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	category, _, ok := d.Category(r.PathValue("id"))
	if !ok {
		writeError(w, r, taxonomy.ErrCategory)
		return
	}
	if taxonomyCached(w, r, d.Version) {
		return
	}
	writeJSON(w, http.StatusOK, category)
}

func (h *taxonomyHandlers) version(w http.ResponseWriter, r *http.Request) {
	d, err := h.service.Current(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	if taxonomyCached(w, r, d.Version) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"version": d.Version})
}

func (h *taxonomyHandlers) load(w http.ResponseWriter, r *http.Request) {
	d, err := taxonomyfile.Decode(http.MaxBytesReader(w, r.Body, maxTaxonomyBody))
	if err == nil {
		err = h.service.Load(r.Context(), d)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("X-Taxonomy-Version", strconv.FormatInt(d.Version, 10))
	writeJSON(w, http.StatusOK, map[string]int64{"version": d.Version})
}

func (h *taxonomyHandlers) publication(w http.ResponseWriter, r *http.Request) {
	id, err := ids.ParseOfferID(r.PathValue("offerId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	publication, err := h.catalog.Publication(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, publication)
}
