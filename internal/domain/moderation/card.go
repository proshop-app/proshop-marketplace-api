package moderation

import (
	"encoding/json"
	"slices"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

// ReviewCard использует опубликованные атрибуты и действующую цену проекции.
type ReviewCard struct {
	Name         string                     `json:"name"`
	Description  string                     `json:"description"`
	Image        string                     `json:"image"`
	Price        int64                      `json:"price"`
	BasePrice    int64                      `json:"basePrice"`
	CategoryID   string                     `json:"categoryId"`
	CategoryPath []string                   `json:"categoryPath"`
	Attributes   map[string]json.RawMessage `json:"attributes"`
	Seller       ReviewSeller               `json:"seller"`
	Publication  taxonomy.Publication       `json:"publication"`
}

// ReviewSeller ограничивает сведения о продавце публичными полями.
type ReviewSeller struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func cloneCard(card *ReviewCard) *ReviewCard {
	if card == nil {
		return nil
	}
	result := *card
	result.CategoryPath = slices.Clone(card.CategoryPath)
	result.Attributes = cloneAttributes(card.Attributes)
	result.Publication.CategoryPath = slices.Clone(card.Publication.CategoryPath)
	result.Publication.Attributes = cloneAttributes(card.Publication.Attributes)
	result.Publication.Issues = slices.Clone(card.Publication.Issues)
	return &result
}

func cloneAttributes(values map[string]json.RawMessage) map[string]json.RawMessage {
	if values == nil {
		return nil
	}
	result := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		result[key] = slices.Clone(value)
	}
	return result
}
