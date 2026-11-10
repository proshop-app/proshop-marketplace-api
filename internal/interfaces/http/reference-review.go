package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const (
	maxReferenceReviewBody = 512 << 10
	referenceReviewTimeout = 25 * time.Second
)

// ErrReferenceReviewTimeout требует перечитать результат перед новым выбором, а не повторять решение автоматически.
var ErrReferenceReviewTimeout = errs.New(
	errs.Unavailable,
	"reference.review_timeout",
	"время решения истекло; перечитайте кандидатов перед новым выбором",
)

type referenceReviewSelection struct {
	Mode        string          `json:"mode"`
	IDs         json.RawMessage `json:"ids"`
	ExcludedIDs json.RawMessage `json:"excludedIds"`
}

type referenceReviewRequest struct {
	Status    reference.Status          `json:"status"`
	Selection *referenceReviewSelection `json:"selection"`
	Reason    json.RawMessage           `json:"reason"`
}

type referenceReviewResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	Dictionary    string `json:"dictionary"`
	Revision      int64  `json:"revision"`
	Changed       int    `json:"changed"`
}

func (h *referenceHandlers) review(w http.ResponseWriter, r *http.Request) {
	// Бюджет начинается до декодирования и ожидания блокировки словаря.
	ctx, cancel := context.WithTimeout(r.Context(), referenceReviewTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	revision, err := dictionaryRevisionOf(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	command, err := decodeReferenceReview(w, r)
	if err == nil {
		command.Dictionary = r.PathValue("dict")
		command.IfMatch = revision
		command.Meta.Actor = principalFrom(r).ID
		command.Meta.Source = reference.SourceOperator
	}
	var result appreference.ReviewResult
	if err == nil {
		result, err = h.service.Review(ctx, command)
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = ErrReferenceReviewTimeout
		}
		writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", dictionaryRevisionETag(result.Revision))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, referenceReviewResponse{
		SchemaVersion: appreference.SchemaVersion,
		Dictionary:    result.Dictionary,
		Revision:      result.Revision,
		Changed:       result.Changed,
	})
}

func dictionaryRevisionOf(r *http.Request) (int64, error) {
	values := r.Header.Values("If-Match")
	if len(values) != 1 {
		return 0, ErrIfMatchRequired.WithMessage("нужен один If-Match с головой словаря: reference-N в кавычках")
	}
	raw := values[0]
	if len(raw) < len(`"reference-0"`) {
		return 0, ErrIfMatchRequired
	}
	revision, err := strconv.ParseInt(raw[len(`"reference-`):len(raw)-1], 10, 64)
	if err != nil || revision < 0 || raw != dictionaryRevisionETag(revision) {
		return 0, ErrIfMatchRequired.WithMessage("If-Match — reference-N в кавычках из списка записей")
	}
	return revision, nil
}

func dictionaryRevisionETag(revision int64) string {
	return `"reference-` + strconv.FormatInt(revision, 10) + `"`
}

func decodeReferenceReview(w http.ResponseWriter, r *http.Request) (appreference.ReviewCommand, error) {
	var raw json.RawMessage
	if err := decodeLimited(w, r, &raw, maxReferenceReviewBody); err != nil {
		return appreference.ReviewCommand{}, err
	}
	// Общий строгий разбор документа запрещает повторные ключи до декодирования DTO.
	if _, err := referenceImportNode(raw); err != nil {
		return appreference.ReviewCommand{}, err
	}
	var request referenceReviewRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.Selection == nil {
		return appreference.ReviewCommand{}, ErrInvalidBody.WithMessage("нужны status и selection известной формы")
	}
	selection := reference.ReviewSelection{Mode: request.Selection.Mode}
	target := &selection.IDs
	source := request.Selection.IDs
	if selection.Mode == reference.ReviewAllCandidates {
		if request.Selection.IDs != nil {
			return appreference.ReviewCommand{}, reference.ErrInvalid
		}
		source = request.Selection.ExcludedIDs
		target = &selection.ExcludedIDs
	} else if request.Selection.ExcludedIDs != nil {
		return appreference.ReviewCommand{}, reference.ErrInvalid
	}
	if err := json.Unmarshal(source, target); err != nil || *target == nil {
		return appreference.ReviewCommand{}, ErrInvalidBody.WithMessage("выбор требует массив идентификаторов")
	}
	command := appreference.ReviewCommand{Status: request.Status, Selection: selection}
	if request.Reason != nil {
		if err := json.Unmarshal(request.Reason, &command.Meta.Reason); err != nil ||
			bytes.Equal(bytes.TrimSpace(request.Reason), []byte("null")) {
			return appreference.ReviewCommand{}, ErrInvalidBody.WithMessage("reason — строка")
		}
	}
	return command, nil
}
