package searchapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

const capacityResponseLimit = 384 << 10

type deferredOffer struct {
	TenantID  string `json:"tenantId"`
	ProductID string `json:"productId"`
	Part      string `json:"part"`
	Reason    string `json:"reason"`
}

type capacityResponse struct {
	Code           string          `json:"code"`
	DeferredOffers json.RawMessage `json:"deferredOffers"`
}

func (x *Index) put(ctx context.Context, path string, docs []ports.OfferDocument) error {
	result := &ports.OfferCapacityError{UnconfirmedFrom: len(docs)}
	for offset := 0; offset < len(docs); {
		batch, err := nextOfferBatch(docs[offset:])
		if err == nil {
			var deferred []ids.OfferID
			deferred, err = x.putOfferBatch(ctx, path, batch)
			result.Deferred = append(result.Deferred, deferred...)
		}
		if err != nil {
			if len(result.Deferred) == 0 {
				return err
			}
			result.UnconfirmedFrom = offset
			result.UnconfirmedError = err
			break
		}
		offset += len(batch.Offers)
	}
	if len(result.Deferred) > 0 {
		return result
	}
	return nil
}

func (x *Index) putOfferBatch(ctx context.Context, path string, batch offerBatch) ([]ids.OfferID, error) {
	var capacity *capacityResponse
	if path == "/offers" {
		capacity = new(capacityResponse)
	}
	status, err := x.requestWithCapacity(ctx, http.MethodPost, path, batch, nil, capacity)
	if status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge {
		return nil, ErrInvalid.WithMessage(fmt.Sprintf("поиск отверг предложения: HTTP %d", status))
	}
	if capacity != nil && capacity.Code == "GENERATION_CAPACITY" {
		return batchDeferredOffers(batch.Offers, capacity.DeferredOffers), nil
	}
	return nil, err
}

func readCapacityResponse(body io.Reader) capacityResponse {
	encoded, err := io.ReadAll(io.LimitReader(body, capacityResponseLimit+1))
	if err != nil || len(encoded) > capacityResponseLimit {
		return capacityResponse{}
	}
	var result capacityResponse
	if json.Unmarshal(encoded, &result) != nil {
		return capacityResponse{}
	}
	return result
}

// Без полного и однозначного списка весь подпакет остаётся на повтор.
func batchDeferredOffers(offers []offerDocument, raw json.RawMessage) []ids.OfferID {
	sent := make([]ids.OfferID, 0, len(offers))
	remaining := make(map[ids.OfferID]bool, len(offers))
	for _, offer := range offers {
		key := ids.OfferID{Tenant: ids.TenantID(offer.TenantID), Product: ids.ProductID(offer.ProductID)}
		sent = append(sent, key)
		remaining[key] = true
	}
	var deferred []deferredOffer
	if json.Unmarshal(raw, &deferred) != nil || len(deferred) == 0 || len(deferred) > len(offers) {
		return sent
	}
	keys := make([]ids.OfferID, 0, len(deferred))
	for _, offer := range deferred {
		key := ids.OfferID{Tenant: ids.TenantID(offer.TenantID), Product: ids.ProductID(offer.ProductID)}
		if !remaining[key] || offer.Part == "" || offer.Reason == "" {
			return sent
		}
		delete(remaining, key)
		keys = append(keys, key)
	}
	return keys
}

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
