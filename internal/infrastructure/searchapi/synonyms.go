package searchapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/synonyms"
)

const (
	synonymValidationPath = "/api/v1/internal/search/synonyms/validate"
	synonymReplyLimit     = 32 << 10
)

// SynonymValidator запрашивает каталожные пробы под полномочиями читателя.
type SynonymValidator struct {
	endpoint Index
}

// SynonymValidator использует транспорт поиска с отдельным токеном читателя реестра.
func (x *Index) SynonymValidator(readToken string) *SynonymValidator {
	endpoint := *x
	endpoint.base = strings.TrimSuffix(x.base, apiPath)
	endpoint.referenceBearer = readToken
	return &SynonymValidator{endpoint: endpoint}
}

// Validate запрашивает каталожную проверку вне контура покупательской выдачи.
func (x *SynonymValidator) Validate(
	ctx context.Context,
	batch ports.SynonymValidationRequest,
) (ports.SynonymValidationResponse, error) {
	if x.endpoint.referenceBearer == "" || batch.BatchID == "" ||
		len(batch.Rules) == 0 || len(batch.Rules) > synonyms.MaxBatch {
		return ports.SynonymValidationResponse{}, ErrInvalid
	}
	var reply ports.SynonymValidationResponse
	_, err := x.endpoint.request(ctx, http.MethodPost, synonymValidationPath, batch, &reply)
	return reply, err
}
