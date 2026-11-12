package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"gopkg.in/yaml.v3"

	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

const (
	maxReferenceImportBody       = 32 << 20
	maxReferenceImportDepth      = 64
	referenceImportTimeout       = 120 * time.Second
	referenceImportResponseGrace = 5 * time.Second
)

// ErrReferenceImportTimeout допускает безопасный повтор того же релиза и суммы.
var ErrReferenceImportTimeout = errs.New(
	errs.Unavailable,
	"reference.import_timeout",
	"время импорта истекло; повторите тот же файл, чтобы проверить результат",
)

type referenceImportResponse struct {
	SchemaVersion int                         `json:"schemaVersion"`
	Dictionaries  []appreference.ImportResult `json:"dictionaries"`
}

type referenceSummariesResponse struct {
	SchemaVersion int                              `json:"schemaVersion"`
	Dictionaries  []appreference.DictionarySummary `json:"dictionaries"`
}

func (h *referenceHandlers) summaries(w http.ResponseWriter, r *http.Request) {
	dictionaries, err := h.service.DictionarySummaries(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, referenceSummariesResponse{SchemaVersion: 1, Dictionaries: dictionaries})
}

func (h *referenceHandlers) importPackage(w http.ResponseWriter, r *http.Request) {
	input, err := decodeReferenceImport(w, r)
	if err != nil {
		writeReferenceImportError(w, r, err)
		return
	}
	if err := r.Context().Err(); err != nil {
		writeReferenceImportError(w, r, err)
		return
	}
	results, err := h.service.Import(r.Context(), principalFrom(r).ID, input)
	if err != nil {
		writeReferenceImportError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, referenceImportResponse{SchemaVersion: 1, Dictionaries: results})
}

// referenceImportBudget действует после проверки оператора: общий бюджет
// загрузки и транзакции не меняет короткие таймауты обычных HTTP-маршрутов.
func referenceImportBudget(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), referenceImportTimeout)
		defer cancel()
		deadline, _ := ctx.Deadline()
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
			writeError(w, r, err)
			return
		}
		// Запас на структурированный ответ после отмены транзакции по контексту.
		writeDeadline := deadline.Add(referenceImportResponseGrace)
		if err := controller.SetWriteDeadline(writeDeadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
			writeError(w, r, err)
			return
		}
		next(w, r.WithContext(ctx))
	}
}

func writeReferenceImportError(w http.ResponseWriter, r *http.Request, err error) {
	deadline, bounded := r.Context().Deadline()
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded) ||
		(bounded && !time.Now().Before(deadline)) {
		err = ErrReferenceImportTimeout
	}
	writeError(w, r, err)
}

func decodeReferenceImport(w http.ResponseWriter, r *http.Request) (reference.ImportPackage, error) {
	var input reference.ImportPackage
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && mediaType != "application/yaml" && mediaType != "text/yaml") {
		return input, ErrInvalidBody.WithMessage("нужен Content-Type application/json или application/yaml")
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxReferenceImportBody))
	if err != nil {
		return input, ErrInvalidBody.WithMessage("пакет не прочитан или превышает 32 МиБ")
	}
	if mediaType == "application/json" && !json.Valid(body) {
		return input, ErrInvalidBody.WithMessage("некорректный JSON")
	}
	node, err := referenceImportNode(body)
	if err != nil {
		return input, err
	}
	if mediaType != "application/json" {
		var value any
		if err := node.Decode(&value); err != nil {
			return input, ErrInvalidBody.Wrap(err)
		}
		body, err = json.Marshal(value)
		if err != nil {
			return input, ErrInvalidBody.Wrap(err)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, ErrInvalidBody.Wrap(err)
	}
	return input, nil
}

func referenceImportNode(body []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	var node yaml.Node
	if err := decoder.Decode(&node); err != nil {
		return nil, ErrInvalidBody.Wrap(err)
	}
	if err := validateImportNode(&node, 0); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidBody.WithMessage("нужен один документ без дополнительных данных")
	}
	return &node, nil
}

// validateImportNode запрещает неоднозначные ключи и раскрытие ссылок до разбора DTO.
func validateImportNode(node *yaml.Node, depth int) error {
	if depth > maxReferenceImportDepth || node.Anchor != "" || node.Kind == yaml.AliasNode {
		return ErrInvalidBody.WithMessage("слишком глубокий YAML, якоря и ссылки запрещены")
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Tag != "!!str" || seen[key.Value] {
				return ErrInvalidBody.WithMessage("ключи — уникальные строки")
			}
			seen[key.Value] = true
		}
	}
	if node.Kind == yaml.ScalarNode && node.Tag != "!!str" && node.Tag != "!!null" &&
		node.Tag != "!!int" && node.Tag != "!!float" && node.Tag != "!!bool" {
		return ErrInvalidBody.WithMessage("специальные теги YAML запрещены")
	}
	for _, child := range node.Content {
		if err := validateImportNode(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}
