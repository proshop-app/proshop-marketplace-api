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
	w.Header().Set("X-Taxonomy-Version", strconv.FormatInt(version, 10))
	return publicCached(w, r, etag)
}

func (h *taxonomyHandlers) categories(w http.ResponseWriter, r *http.Request) {
	h.readPart(w, r, "categories", false)
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
	match := r.Header.Get("If-Match")
	expected, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(match, `"taxonomy-`), `"`), 10, 64)
	if err != nil || expected < 0 || match != `"taxonomy-`+strconv.FormatInt(expected, 10)+`"` {
		writeError(w, r, taxonomy.ErrInvalid.WithMessage("нужен If-Match текущей версии"))
		return
	}
	d, err := taxonomyfile.DecodePart(http.MaxBytesReader(w, r.Body, maxTaxonomyBody), r.PathValue("part"))
	var version int64
	if err == nil {
		version, err = h.service.LoadPart(r.Context(), r.PathValue("part"), d, expected)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", `"taxonomy-`+strconv.FormatInt(version, 10)+`"`)
	w.Header().Set("X-Taxonomy-Version", strconv.FormatInt(version, 10))
	writeJSON(w, http.StatusOK, map[string]int64{"version": version})
}

func (h *taxonomyHandlers) current(w http.ResponseWriter, r *http.Request) {
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

func (h *taxonomyHandlers) attributes(w http.ResponseWriter, r *http.Request) {
	h.readPart(w, r, "attributes", false)
}

func (h *taxonomyHandlers) schemas(w http.ResponseWriter, r *http.Request) {
	h.readPart(w, r, "schemas", false)
}

func (h *taxonomyHandlers) download(w http.ResponseWriter, r *http.Request) {
	h.readPart(w, r, r.PathValue("part"), true)
}

func (h *taxonomyHandlers) readPart(w http.ResponseWriter, r *http.Request, part string, asYAML bool) {
	if part != "attributes" && part != "schemas" && part != "categories" && part != "dictionary-bindings" {
		writeError(w, r, taxonomy.ErrInvalid.WithMessage("неизвестная часть таксономии"))
		return
	}
	d, err := h.service.Current(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	if taxonomyCached(w, r, d.Version) {
		return
	}
	if asYAML {
		body, err := taxonomyfile.EncodePart(d, part)
		if err != nil {
			writeError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		w.Header().Set("Content-Disposition", `attachment; filename="`+part+`.yaml"`)
		_, _ = w.Write(body)
		return
	}
	var value any
	switch part {
	case "attributes":
		value = d.Attributes
	case "schemas":
		value = d.Schemas
	case "categories":
		value = d.Categories
	case "dictionary-bindings":
		value = d.DictionaryBindings
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": d.Version, part: value})
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
