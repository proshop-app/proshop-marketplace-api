package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func productTileKey(product catalog.ProductState) string {
	if product.Card.TileKey != "" {
		return product.Card.TileKey
	}
	return string(product.ProductID)
}

func formerTileKey(id ids.ProductID) (string, bool) {
	return strings.CutPrefix(string(id), ports.TileDocumentPrefix)
}

func tileOfferID(key ports.TileKey) ids.OfferID {
	return ids.OfferID{Tenant: key.TenantID, Product: ids.ProductID(ports.TileDocumentPrefix + key.Key)}
}

func syncFormerTileTask(tenant ids.TenantID, key string, at time.Time) ports.NewTask {
	payload, _ := json.Marshal(taskPayload{TenantID: string(tenant), TileKey: key})
	digest := sha256.Sum256([]byte(key))
	return ports.NewTask{
		Kind:     TaskSyncOffer,
		TenantID: tenant,
		DedupKey: TaskSyncOffer + ":" + string(tenant) + "~tile~" + hex.EncodeToString(digest[:]),
		Payload:  payload,
		At:       at,
	}
}

func changedTileMembers(tiles []ports.TileState) []ports.OfferState {
	var states []ports.OfferState
	for _, tile := range tiles {
		for _, member := range tile.Members {
			if member.Changed {
				states = append(states, member.State)
			}
		}
	}
	return states
}

func (i *Indexer) indexDocuments(ctx context.Context, tiles []ports.TileState) ([]ports.OfferDocument, error) {
	// Ревизия плитки соответствует прочитанному снимку. Сохранение ключа
	// модели ставит следующую задачу с новой ревизией.
	cards := make([]ports.OfferDocument, len(tiles))
	for n, tile := range tiles {
		if tile.Version > 0 {
			cards[n] = tileDocument(tile)
		}
	}
	changed := changedTileMembers(tiles)
	ready, err := i.refreshModelKeys(ctx, changed)
	if err != nil {
		return nil, err
	}
	conflicts := make(map[ids.OfferID]bool, len(changed)-len(ready))
	if len(ready) != len(changed) {
		for _, state := range changed {
			conflicts[tileMemberID(state)] = true
		}
		for _, state := range ready {
			delete(conflicts, tileMemberID(state))
		}
	}
	readyCards := cards[:0]
	for n, card := range cards {
		if card.Tile != nil && !slices.ContainsFunc(tiles[n].Members, func(member ports.TileMember) bool {
			return conflicts[tileMemberID(member.State)]
		}) {
			readyCards = append(readyCards, card)
		}
	}
	return readyCards, nil
}

func tileMemberID(state ports.OfferState) ids.OfferID {
	product := state.Product.State()
	return ids.OfferID{Tenant: product.TenantID, Product: product.ProductID}
}

func tileDocument(tile ports.TileState) ports.OfferDocument {
	members := make([]ports.OfferDocument, 0, len(tile.Members))
	updatedAt := tile.UpdatedAt
	for _, member := range tile.Members {
		if doc, ok := Document(member.State); ok {
			if doc.UpdatedAt.After(updatedAt) {
				updatedAt = doc.UpdatedAt
			}
			if doc.Visible {
				members = append(members, doc)
			}
		}
	}
	key := tileOfferID(tile.Key)
	return ports.OfferDocument{
		TenantID:  string(key.Tenant),
		ProductID: string(key.Product),
		ID:        key.String(),
		Version:   tile.Version,
		Visible:   len(members) > 0,
		UpdatedAt: updatedAt,
		Tile:      &ports.TileDocument{Key: tile.Key.Key, Members: members},
	}
}
