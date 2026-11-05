package http

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const (
	// maxResolveBody — потолок тела resolve из контракта реестра.
	maxResolveBody = 4 << 10
	// scopeGlobal — общий реестр; частные записи магазинов — следующий этап.
	scopeGlobal = "global"
	// unknownDictionary — метка метрики для запроса, чей словарь не подтверждён.
	unknownDictionary = "unknown"
)

var (
	// ErrIfMatchRequired — правка записи без ревизии, на которую она опирается.
	ErrIfMatchRequired = errs.New(
		errs.Invalid,
		"reference.if_match_required",
		"нужен заголовок If-Match с ревизией записи",
	)
	// ErrScopeUnsupported — снимок частного слоя магазина появится вместе с частными записями.
	ErrScopeUnsupported = errs.New(errs.Invalid, "reference.scope_unsupported", "поддерживается только scope=global")
	// referenceSources — кто может завести запись через API; поставка идёт командой.
	referenceSources = map[reference.Source]bool{
		reference.SourceOperator: true,
		reference.SourceCatalog:  true,
	}
)

type referenceHandlers struct {
	service  *appreference.Service
	resolves *prometheus.CounterVec
}

type referenceAliasJSON struct {
	ID     ids.UUID            `json:"id"`
	Text   string              `json:"text"`
	Key    string              `json:"key"`
	Kind   reference.AliasKind `json:"kind"`
	Locale string              `json:"locale,omitempty"`
	Source reference.Source    `json:"source"`
	Weight int16               `json:"weight"`
}

type referenceEntryJSON struct {
	ID          ids.UUID             `json:"id"`
	Dictionary  string               `json:"dictionary"`
	Canonical   string               `json:"canonical"`
	Key         string               `json:"key"`
	Status      reference.Status     `json:"status"`
	MergedInto  ids.UUID             `json:"mergedInto,omitempty"`
	Source      reference.Source     `json:"source"`
	Occurrences int64                `json:"occurrences"`
	Revision    int64                `json:"revision"`
	Metadata    json.RawMessage      `json:"metadata"`
	Aliases     []referenceAliasJSON `json:"aliases"`
	CreatedAt   time.Time            `json:"createdAt"`
	UpdatedAt   time.Time            `json:"updatedAt"`
}

type snapshotAliasJSON struct {
	Text string              `json:"text"`
	Key  string              `json:"key"`
	Kind reference.AliasKind `json:"kind"`
}

type snapshotEntryJSON struct {
	Directions []string            `json:"directions"`
	Order      int                 `json:"order"`
	Label      string              `json:"label,omitempty"`
	ID         ids.UUID            `json:"id"`
	Canonical  string              `json:"canonical"`
	Key        string              `json:"key"`
	Status     reference.Status    `json:"status"`
	MergedInto ids.UUID            `json:"mergedInto,omitempty"`
	Aliases    []snapshotAliasJSON `json:"aliases"`
}

type snapshotMetadata struct {
	Directions []string `json:"directions"`
	Order      int      `json:"order"`
	Label      string   `json:"label"`
}

type discoveryJSON struct {
	SchemaVersion int                           `json:"schemaVersion"`
	Dictionaries  []appreference.DictionaryView `json:"dictionaries"`
}

type snapshotJSON struct {
	Kind          reference.Kind      `json:"kind"`
	SchemaVersion int                 `json:"schemaVersion"`
	Dictionary    string              `json:"dictionary"`
	Revision      int64               `json:"revision"`
	Normalizer    string              `json:"normalizer"`
	Full          bool                `json:"full"`
	Entries       []snapshotEntryJSON `json:"entries"`
	Removed       []ids.UUID          `json:"removed"`
}

type resolveRequest struct {
	Text     string `json:"text"`
	TenantID string `json:"tenantId,omitempty"`
}

type matchJSON struct {
	EntryID    ids.UUID         `json:"entryId"`
	Canonical  string           `json:"canonical"`
	AliasKey   string           `json:"aliasKey"`
	Status     reference.Status `json:"status"`
	Scope      string           `json:"scope"`
	Confidence string           `json:"confidence,omitempty"`
	Weight     float64          `json:"weight"`
}

type resolveJSON struct {
	SchemaVersion int         `json:"schemaVersion"`
	Dictionary    string      `json:"dictionary"`
	Revision      int64       `json:"revision"`
	Normalizer    string      `json:"normalizer"`
	Key           string      `json:"key"`
	Match         *matchJSON  `json:"match,omitempty"`
	Candidates    []matchJSON `json:"candidates"`
}

type aliasRequest struct {
	Text   string              `json:"text"`
	Kind   reference.AliasKind `json:"kind,omitempty"`
	Locale string              `json:"locale,omitempty"`
	Weight int16               `json:"weight,omitempty"`
	Reason string              `json:"reason,omitempty"`
}

type createEntryRequest struct {
	Canonical string           `json:"canonical"`
	Status    reference.Status `json:"status,omitempty"`
	Source    reference.Source `json:"source,omitempty"`
	Aliases   []aliasRequest   `json:"aliases,omitempty"`
	Metadata  json.RawMessage  `json:"metadata,omitempty"`
	Reason    string           `json:"reason,omitempty"`
}

type updateEntryRequest struct {
	Canonical *string           `json:"canonical,omitempty"`
	Status    *reference.Status `json:"status,omitempty"`
	Metadata  json.RawMessage   `json:"metadata,omitempty"`
	Reason    string            `json:"reason,omitempty"`
}

type mergeRequest struct {
	Into   ids.UUID `json:"into"`
	Reason string   `json:"reason,omitempty"`
}

type entryResponse struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Revision      int64               `json:"revision"`
	Created       *bool               `json:"created,omitempty"`
	Changed       *bool               `json:"changed,omitempty"`
	Entry         referenceEntryJSON  `json:"entry"`
	Resolved      *referenceEntryJSON `json:"resolved,omitempty"`
	Alias         *referenceAliasJSON `json:"alias,omitempty"`
}

type entriesResponse struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Revision      int64                `json:"revision"`
	Total         int                  `json:"total"`
	Entries       []referenceEntryJSON `json:"entries"`
}

// referenceReader пускает к чтению реестра по токену читателя без прав
// оператора; любой другой доступ проверяет операторская защита guarded.
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

func newReferenceHandlers(service *appreference.Service, registerer prometheus.Registerer) *referenceHandlers {
	h := &referenceHandlers{
		service: service,
		resolves: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "marketplace_reference_resolve_total",
				Help: "Разрешения написаний реестра: match, candidates, miss, invalid, error.",
			},
			[]string{"dictionary", "result"},
		),
	}
	if registerer == nil {
		return h
	}
	// маршруты собираются не один раз на реестр метрик: счётчик берётся уже заведённый
	var already prometheus.AlreadyRegisteredError
	switch err := registerer.Register(h.resolves); {
	case errors.As(err, &already):
		if existing, ok := already.ExistingCollector.(*prometheus.CounterVec); ok {
			h.resolves = existing
		}
	case err != nil:
		panic(err)
	}
	return h
}

func (h *referenceHandlers) snapshot(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if scope := query.Get("scope"); scope != "" && scope != scopeGlobal {
		writeError(w, r, ErrScopeUnsupported)
		return
	}
	var since *int64
	if raw := query.Get("since"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			writeError(w, r, ErrInvalidQuery.WithMessage("since — неотрицательная ревизия"))
			return
		}
		since = &value
	}
	snapshot, err := h.service.Snapshot(r.Context(), r.PathValue("dict"), since)
	if err != nil {
		writeError(w, r, err)
		return
	}
	body := snapshotJSON{
		Kind:          snapshot.Kind,
		SchemaVersion: appreference.SchemaVersion,
		Dictionary:    snapshot.Dictionary,
		Revision:      snapshot.Revision,
		Normalizer:    snapshot.Normalizer,
		Full:          snapshot.Full,
		Entries:       make([]snapshotEntryJSON, 0, len(snapshot.Entries)),
		Removed:       snapshot.Removed,
	}
	for _, entry := range snapshot.Entries {
		item := snapshotEntryJSON{
			ID:         entry.ID,
			Canonical:  entry.CanonicalName,
			Key:        entry.CanonicalKey,
			Status:     entry.Status,
			MergedInto: entry.MergedInto,
			Aliases:    make([]snapshotAliasJSON, 0, len(entry.Aliases)),
		}
		var metadata snapshotMetadata
		_ = json.Unmarshal(entry.Metadata, &metadata)
		item.Directions, item.Order, item.Label = metadata.Directions, metadata.Order, metadata.Label
		if item.Directions == nil {
			item.Directions = []string{}
		}
		for _, alias := range entry.Aliases {
			item.Aliases = append(item.Aliases, snapshotAliasJSON{Text: alias.Text, Key: alias.Key, Kind: alias.Kind})
		}
		body.Entries = append(body.Entries, item)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, body)
}

func (h *referenceHandlers) resolve(w http.ResponseWriter, r *http.Request) {
	dictionary := r.PathValue("dict")
	var request resolveRequest
	err := decodeLimited(w, r, &request, maxResolveBody)
	if err == nil && request.TenantID != "" {
		// частный слой магазина — этап частных записей; формат проверяется уже сейчас
		_, err = ids.ParseTenantID(request.TenantID)
	}
	if err != nil {
		// словарь ещё не проверен: адрес не становится меткой
		h.resolves.WithLabelValues(unknownDictionary, "invalid").Inc()
		writeError(w, r, err)
		return
	}
	resolution, err := h.service.Resolve(r.Context(), dictionary, request.Text)
	if err != nil {
		h.countResolve(dictionary, err)
		writeError(w, r, err)
		return
	}
	body := resolveJSON{
		SchemaVersion: appreference.SchemaVersion,
		Dictionary:    resolution.Dictionary,
		Revision:      resolution.Revision,
		Normalizer:    resolution.Normalizer,
		Key:           resolution.Key,
		Candidates:    make([]matchJSON, 0, len(resolution.Candidates)),
	}
	result := "miss"
	if resolution.Match != nil {
		match := matchOf(*resolution.Match)
		body.Match, result = &match, "match"
	}
	for _, candidate := range resolution.Candidates {
		body.Candidates = append(body.Candidates, matchOf(candidate))
	}
	if body.Match == nil && len(body.Candidates) > 0 {
		result = "candidates"
	}
	h.resolves.WithLabelValues(dictionary, result).Inc()
	writeJSON(w, http.StatusOK, body)
}

func (h *referenceHandlers) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := reference.ListFilter{Status: reference.Status(query.Get("status"))}
	for name, target := range map[string]*int{"limit": &filter.Limit, "offset": &filter.Offset} {
		if raw := query.Get(name); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				writeError(w, r, ErrInvalidQuery)
				return
			}
			*target = value
		}
	}
	page, err := h.service.List(r.Context(), r.PathValue("dict"), filter)
	if err != nil {
		writeError(w, r, err)
		return
	}
	body := entriesResponse{
		SchemaVersion: appreference.SchemaVersion,
		Revision:      page.Revision,
		Total:         page.Total,
		Entries:       make([]referenceEntryJSON, 0, len(page.Entries)),
	}
	for _, entry := range page.Entries {
		body.Entries = append(body.Entries, entryOf(entry))
	}
	writeJSON(w, http.StatusOK, body)
}

func (h *referenceHandlers) get(w http.ResponseWriter, r *http.Request) {
	view, err := h.service.Get(r.Context(), r.PathValue("dict"), ids.UUID(r.PathValue("id")))
	if err != nil {
		writeError(w, r, err)
		return
	}
	body := entryResponse{
		SchemaVersion: appreference.SchemaVersion,
		Revision:      view.Revision,
		Entry:         entryOf(view.Entry),
	}
	if view.Resolved != nil {
		resolved := entryOf(*view.Resolved)
		body.Resolved = &resolved
	}
	writeEntry(w, http.StatusOK, body)
}

func (h *referenceHandlers) create(w http.ResponseWriter, r *http.Request) {
	var request createEntryRequest
	if err := readJSON(w, r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	if request.Status == "" {
		request.Status = reference.StatusCandidate
	}
	if request.Source == "" {
		request.Source = reference.SourceOperator
	}
	if !referenceSources[request.Source] {
		writeError(w, r, reference.ErrInvalid.WithMessage("source — operator или catalog"))
		return
	}
	specs := make([]appreference.AliasSpec, 0, len(request.Aliases))
	for _, alias := range request.Aliases {
		specs = append(specs, specOf(alias))
	}
	result, err := h.service.Create(r.Context(), appreference.CreateCommand{
		Dictionary: r.PathValue("dict"),
		Name:       request.Canonical,
		Status:     request.Status,
		Aliases:    specs,
		Metadata:   request.Metadata,
		Meta:       metaOf(r, request.Source, request.Reason),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	created := result.Created
	writeEntry(w, status, entryResponse{
		SchemaVersion: appreference.SchemaVersion,
		Revision:      result.Revision,
		Created:       &created,
		Entry:         entryOf(result.Entry),
	})
}

func (h *referenceHandlers) update(w http.ResponseWriter, r *http.Request) {
	ifMatch, err := revisionOf(r)
	var request updateEntryRequest
	if err == nil {
		err = readJSON(w, r, &request)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := h.service.Update(r.Context(), appreference.UpdateCommand{
		Dictionary: r.PathValue("dict"),
		ID:         ids.UUID(r.PathValue("id")),
		IfMatch:    ifMatch,
		Name:       request.Canonical,
		Status:     request.Status,
		Metadata:   request.Metadata,
		Meta:       metaOf(r, reference.SourceOperator, request.Reason),
	})
	h.writeChanged(w, r, result, err, http.StatusOK)
}

func (h *referenceHandlers) merge(w http.ResponseWriter, r *http.Request) {
	ifMatch, err := revisionOf(r)
	var request mergeRequest
	if err == nil {
		err = readJSON(w, r, &request)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := h.service.Merge(r.Context(), appreference.MergeCommand{
		Dictionary: r.PathValue("dict"),
		ID:         ids.UUID(r.PathValue("id")),
		Into:       request.Into,
		IfMatch:    ifMatch,
		Meta:       metaOf(r, reference.SourceOperator, request.Reason),
	})
	h.writeChanged(w, r, result, err, http.StatusOK)
}

func (h *referenceHandlers) addAlias(w http.ResponseWriter, r *http.Request) {
	var request aliasRequest
	if err := readJSON(w, r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := h.service.AddAlias(r.Context(), appreference.AddAliasCommand{
		Dictionary: r.PathValue("dict"),
		EntryID:    ids.UUID(r.PathValue("id")),
		Alias:      specOf(request),
		Meta:       metaOf(r, reference.SourceOperator, request.Reason),
	})
	status := http.StatusOK
	if result.Changed {
		status = http.StatusCreated
	}
	h.writeChanged(w, r, result, err, status)
}

func (h *referenceHandlers) removeAlias(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.RemoveAlias(r.Context(), appreference.RemoveAliasCommand{
		Dictionary: r.PathValue("dict"),
		AliasID:    ids.UUID(r.PathValue("id")),
		Meta:       metaOf(r, reference.SourceOperator, r.URL.Query().Get("reason")),
	})
	h.writeChanged(w, r, result, err, http.StatusOK)
}

func (h *referenceHandlers) writeChanged(
	w http.ResponseWriter,
	r *http.Request,
	result appreference.EntryResult,
	err error,
	status int,
) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	changed := result.Changed
	body := entryResponse{
		SchemaVersion: appreference.SchemaVersion,
		Revision:      result.Revision,
		Changed:       &changed,
		Entry:         entryOf(result.Entry),
	}
	if result.Alias != nil {
		alias := aliasOf(*result.Alias)
		body.Alias = &alias
	}
	writeEntry(w, status, body)
}

// countResolve — отказ resolve по классу ошибки. Метка словаря ставится,
// только когда сценарий уже нашёл словарь (invalid и conflict); иначе адрес
// стал бы меткой и раздул кардинальность метрики.
func (h *referenceHandlers) countResolve(dictionary string, err error) {
	switch errs.CodeOf(err) {
	case errs.Invalid:
		h.resolves.WithLabelValues(dictionary, "invalid").Inc()
	case errs.Conflict:
		h.resolves.WithLabelValues(dictionary, "error").Inc()
	case errs.NotFound:
		h.resolves.WithLabelValues(unknownDictionary, "invalid").Inc()
	default:
		h.resolves.WithLabelValues(unknownDictionary, "error").Inc()
	}
}

// writeEntry отдаёт запись с её ревизией в ETag: её клиент возвращает в If-Match.
func writeEntry(w http.ResponseWriter, status int, body entryResponse) {
	w.Header().Set("ETag", `"`+strconv.FormatInt(body.Entry.Revision, 10)+`"`)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, body)
}

// revisionOf — ревизия записи из If-Match: "12", W/"12" или 12.
func revisionOf(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		return 0, ErrIfMatchRequired
	}
	raw = strings.Trim(strings.TrimPrefix(raw, "W/"), `"`)
	revision, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || revision < 1 {
		return 0, ErrIfMatchRequired.WithMessage("If-Match — ревизия записи из ETag")
	}
	return revision, nil
}

// decodeLimited — строгий разбор тела со своим потолком размера.
func decodeLimited(w http.ResponseWriter, r *http.Request, out any, limit int64) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return ErrInvalidBody.WithMessage("тело больше " + strconv.FormatInt(limit, 10) + " байт")
		}
		return ErrInvalidBody.Wrap(err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalidBody.WithMessage("после объекта в теле есть лишнее")
	}
	return nil
}

func metaOf(r *http.Request, source reference.Source, reason string) reference.Meta {
	return reference.Meta{Actor: principalFrom(r).ID, Source: source, Reason: reason}
}

func specOf(request aliasRequest) appreference.AliasSpec {
	return appreference.AliasSpec{
		Text:   request.Text,
		Kind:   request.Kind,
		Locale: request.Locale,
		Weight: request.Weight,
	}
}

func matchOf(match appreference.Match) matchJSON {
	return matchJSON{
		EntryID:    match.EntryID,
		Canonical:  match.Canonical,
		AliasKey:   match.AliasKey,
		Status:     match.Status,
		Scope:      scopeGlobal,
		Confidence: match.Confidence,
		Weight:     match.Weight,
	}
}

func entryOf(state reference.EntryState) referenceEntryJSON {
	entry := referenceEntryJSON{
		ID:          state.ID,
		Dictionary:  state.Dictionary,
		Canonical:   state.CanonicalName,
		Key:         state.CanonicalKey,
		Status:      state.Status,
		MergedInto:  state.MergedInto,
		Source:      state.Source,
		Occurrences: state.Occurrences,
		Revision:    state.Revision,
		Metadata:    state.Metadata,
		Aliases:     make([]referenceAliasJSON, 0, len(state.Aliases)),
		CreatedAt:   state.CreatedAt,
		UpdatedAt:   state.UpdatedAt,
	}
	for _, alias := range state.Aliases {
		entry.Aliases = append(entry.Aliases, aliasOf(alias))
	}
	return entry
}

func aliasOf(alias reference.AliasState) referenceAliasJSON {
	return referenceAliasJSON{
		ID:     alias.ID,
		Text:   alias.Text,
		Key:    alias.Key,
		Kind:   alias.Kind,
		Locale: alias.Locale,
		Source: alias.Source,
		Weight: alias.Weight,
	}
}
