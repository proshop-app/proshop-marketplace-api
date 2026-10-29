package searchapi

import (
	"context"
	"net/http"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

type groupQuery struct {
	TenantID string `json:"tenantId"`
	GroupID  string `json:"groupId"`
}

type groupPage struct {
	Items     []offerDocument `json:"items"`
	Truncated bool            `json:"truncated"`
}

// Group возвращает видимые позиции группы одного продавца.
func (x *Index) Group(ctx context.Context, tenantID, groupID string) (ports.OfferGroup, error) {
	var result groupPage
	_, err := x.request(
		ctx,
		http.MethodPost,
		"/group",
		groupQuery{
			TenantID: tenantID,
			GroupID:  groupID,
		},
		&result,
	)
	if err != nil {
		return ports.OfferGroup{}, err
	}
	page := ports.OfferGroup{
		Items:     make([]ports.OfferDocument, 0, len(result.Items)),
		Truncated: result.Truncated,
	}
	for _, item := range result.Items {
		if item.TenantID != tenantID || item.VariantsID != groupID || !item.Visible {
			return ports.OfferGroup{}, ErrUnavailable.WithMessage("поиск вернул чужой или скрытый вариант")
		}
		doc, err := fromDocument(item)
		if err != nil {
			return ports.OfferGroup{}, err
		}
		page.Items = append(page.Items, doc)
	}

	return page, nil
}
