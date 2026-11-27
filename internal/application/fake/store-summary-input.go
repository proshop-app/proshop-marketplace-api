package fake

import (
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"

	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

type storeSummaryInput struct {
	category     string
	rawCategory  string
	ratingGroup  string
	name         string
	image        string
	price        money.Money
	sales        int64
	ratings      int64
	average      float64
	rawPublished bool
	candidate    bool
	canBeVisible bool
}

func summaryInput(state catalog.ProductState) storeSummaryInput {
	if state.Deleted {
		return storeSummaryInput{}
	}
	var input storeSummaryInput
	if raw := state.Card.Marketplace; raw != nil && raw.Published {
		input.rawPublished = true
		input.rawCategory = raw.CategoryID
	}
	if !state.HasPrice || !state.Card.Publication.Published {
		return input
	}
	input.candidate = true
	input.category = state.Card.Publication.CategoryID
	input.price = state.Price
	if state.Card.Visible {
		input.canBeVisible = true
		input.ratingGroup = catalog.RatingGroup(state)
		input.name = state.Card.Name
		input.image = state.Card.Image
		input.sales = state.SalesCount
		input.ratings = state.RatingsCount
		input.average = state.AverageRating
	}
	return input
}
