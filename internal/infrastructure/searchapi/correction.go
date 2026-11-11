package searchapi

import (
	"strings"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

func fromCorrection(value *offerCorrection) (*ports.OfferCorrection, error) {
	if value == nil {
		return nil, nil
	}

	if strings.TrimSpace(value.Query) == "" {
		return nil, ErrUnavailable.WithMessage("поиск вернул пустой исправленный запрос")
	}

	switch value.Kind {
	case "typo", "layout", "translit", "partial":
		return &ports.OfferCorrection{
			Query: value.Query,
			Kind:  value.Kind,
		}, nil
	default:
		return nil, ErrUnavailable.WithMessage("поиск вернул неизвестный вид исправления запроса")
	}
}
