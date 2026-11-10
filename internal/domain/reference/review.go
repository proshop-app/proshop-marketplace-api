package reference

import "github.com/proshop-app/proshop-marketplace/pkg/ids"

// Лимиты и способы выбора массового решения: запрос никогда не обрезается молча.
const (
	MaxReviewEntries    = 10_000
	ReviewIDs           = "ids"
	ReviewAllCandidates = "allCandidates"
)

// ReviewSelection выбирает кандидатов явно либо все текущие, кроме исключённых.
type ReviewSelection struct {
	Mode        string
	IDs         []ids.UUID
	ExcludedIDs []ids.UUID
}

// ValidateReview проверяет решение и точную форму выбора до открытия транзакции.
func ValidateReview(status Status, selection ReviewSelection) error {
	if status != StatusConfirmed && status != StatusRejected {
		return ErrInvalid.WithMessage("массовое решение — confirmed или rejected")
	}
	var list []ids.UUID
	switch selection.Mode {
	case ReviewIDs:
		if len(selection.IDs) == 0 || selection.ExcludedIDs != nil {
			return ErrInvalid.WithMessage("ids требует непустой ids без excludedIds")
		}
		list = selection.IDs
	case ReviewAllCandidates:
		if selection.IDs != nil || selection.ExcludedIDs == nil {
			return ErrInvalid.WithMessage("allCandidates требует excludedIds без ids")
		}
		list = selection.ExcludedIDs
	default:
		return ErrInvalid.WithMessage("неизвестный способ выбора кандидатов")
	}
	if len(list) > MaxReviewEntries {
		return ErrLimit.WithMessage("выбор содержит больше 10000 идентификаторов")
	}
	seen := make(map[ids.UUID]bool, len(list))
	for _, id := range list {
		if _, err := ids.ParseUUID(string(id)); err != nil || seen[id] {
			return ErrInvalid.WithMessage("некорректный или повторный идентификатор выбора")
		}
		seen[id] = true
	}
	return nil
}
