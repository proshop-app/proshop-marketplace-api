package http

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

type dictionaryBindingInput struct {
	Mode         *string   `json:"mode"`
	Dictionaries *[]string `json:"dictionaries"`
}

type dictionaryBindingsResponse struct {
	Version            int64                        `json:"version"`
	DictionaryBindings []taxonomy.DictionaryBinding `json:"dictionaryBindings"`
}

func (h *taxonomyHandlers) dictionaryBindings(w http.ResponseWriter, r *http.Request) {
	current, err := h.service.Current(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	if taxonomyCached(w, r, current.Version) {
		return
	}
	rules := current.DictionaryBindings
	if rules == nil {
		rules = []taxonomy.DictionaryBinding{}
	}
	writeJSON(w, http.StatusOK, dictionaryBindingsResponse{Version: current.Version, DictionaryBindings: rules})
}

func (h *taxonomyHandlers) putDictionaryBinding(w http.ResponseWriter, r *http.Request) {
	expected, category, err := bindingRequestScope(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var input dictionaryBindingInput
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	if input.Mode == nil || input.Dictionaries == nil {
		writeError(w, r, taxonomy.ErrInvalid.WithMessage("нужны mode и массив dictionaries"))
		return
	}
	version, err := h.service.PutDictionaryBinding(
		r.Context(),
		taxonomy.DictionaryBinding{
			CategoryID:    category,
			AttributeCode: r.PathValue("attributeCode"),
			Mode:          *input.Mode,
			Dictionaries:  *input.Dictionaries,
		}, expected)
	writeBindingVersion(w, r, version, err)
}

func (h *taxonomyHandlers) deleteDictionaryBinding(w http.ResponseWriter, r *http.Request) {
	expected, category, err := bindingRequestScope(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	version, err := h.service.DeleteDictionaryBinding(r.Context(), category, r.PathValue("attributeCode"), expected)
	writeBindingVersion(w, r, version, err)
}

func bindingRequestScope(r *http.Request) (int64, string, error) {
	raw := r.Header.Get("If-Match")
	if len(raw) < len(`"taxonomy-0"`) {
		return 0, "", taxonomy.ErrInvalid.WithMessage("нужен If-Match текущей версии")
	}
	version, err := strconv.ParseInt(raw[len(`"taxonomy-`):len(raw)-1], 10, 64)
	if err != nil || version < 0 || raw != `"taxonomy-`+strconv.FormatInt(version, 10)+`"` {
		return 0, "", taxonomy.ErrInvalid.WithMessage("нужен If-Match текущей версии")
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, "", taxonomy.ErrInvalid.WithMessage("неверный query области подсказок")
	}
	category := ""
	for key, values := range query {
		if key != "categoryId" || len(values) != 1 || values[0] == "" {
			return 0, "", taxonomy.ErrInvalid.WithMessage("допустим только один непустой categoryId")
		}
		category = values[0]
	}
	return version, category, nil
}

func writeBindingVersion(w http.ResponseWriter, r *http.Request, version int64, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", `"taxonomy-`+strconv.FormatInt(version, 10)+`"`)
	w.Header().Set("X-Taxonomy-Version", strconv.FormatInt(version, 10))
	writeJSON(w, http.StatusOK, map[string]int64{"version": version})
}
