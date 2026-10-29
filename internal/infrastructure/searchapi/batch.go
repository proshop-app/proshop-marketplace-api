package searchapi

import (
	"encoding/json"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
)

// Размер учитывает JSON-экранирование, разделители и конверт запроса.
// Ограничиваем и число карточек, и байты: длинные описания не блокируют outbox.
func nextOfferBatch(docs []ports.OfferDocument) (offerBatch, error) {
	batch := offerBatch{
		Offers: make([]offerDocument, 0, min(len(docs), batchLimit)),
	}
	size := len(`{"offers":[]}`)

	for _, doc := range docs[:min(len(docs), batchLimit)] {
		offer, err := toDocument(doc)

		if err != nil {
			return offerBatch{}, err
		}
		encoded, err := json.Marshal(offer)

		if err != nil {
			return offerBatch{}, ErrInvalid.Wrap(err)
		}
		addition := len(encoded)

		if len(batch.Offers) > 0 {
			addition++
		}

		if size+addition > requestLimit {
			if len(batch.Offers) == 0 {
				return offerBatch{}, ErrInvalid.WithMessage("предложение превышает размер запроса поиска")
			}
			break
		}
		batch.Offers = append(batch.Offers, offer)
		size += addition
	}

	return batch, nil
}
