package fake

import (
	"context"
	"sort"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Tiles возвращает затронутые плитки из тестовой проекции.
func (o offers) Tiles(_ context.Context, products []ids.OfferID, former []ports.TileKey) ([]ports.TileState, error) {
	selected := make(map[ports.TileKey]bool)
	changed := make(map[productKey]bool)
	for _, key := range former {
		selected[key] = true
	}
	for _, id := range products {
		key := productKey{id.Tenant, id.Product}
		if _, ok := o.d.products[key]; ok {
			selected[o.tileKey(key)] = true
			changed[key] = true
		}
	}
	return o.collectTiles(selected, changed, false), nil
}

// TilePage возвращает следующую порцию плиток в тестовой проекции.
func (o offers) TilePage(ctx context.Context, page ports.TilePage) ([]ports.TileState, error) {
	selected := make(map[ports.TileKey]bool)
	if page.TenantID != "" {
		page.After.TenantID = page.TenantID
	}
	var changed map[ports.TileKey]bool
	if !page.Since.IsZero() {
		states, err := o.ChangedSince(ctx, page.Since, ports.OfferKey{}, len(o.d.products))
		if err != nil {
			return nil, err
		}
		changed = make(map[ports.TileKey]bool)
		for _, state := range states {
			p := state.Product.State()
			changed[o.tileKey(productKey{p.TenantID, p.ProductID})] = true
		}
	}
	for key := range o.d.products {
		if _, ok := o.d.participants[key.tenant]; !ok || page.TenantID != "" && key.tenant != page.TenantID {
			continue
		}
		tile := o.tileKey(key)
		if tileBefore(page.After, tile) && (changed == nil || changed[tile]) {
			selected[tile] = true
		}
	}
	tiles := o.collectTiles(selected, nil, true)
	return tiles[:min(len(tiles), page.Limit)], nil
}

func (o offers) tileKey(key productKey) ports.TileKey {
	tile := o.d.products[key].Card.TileKey
	if tile == "" {
		tile = string(key.product)
	}
	return ports.TileKey{TenantID: key.tenant, Key: tile}
}

func tileBefore(a, b ports.TileKey) bool {
	if a.TenantID == b.TenantID {
		return a.Key < b.Key
	}
	return a.TenantID < b.TenantID
}

func (o offers) collectTiles(selected map[ports.TileKey]bool, changed map[productKey]bool, all bool) []ports.TileState {
	tiles := make([]ports.TileState, 0, len(selected))
	for key := range selected {
		if seller, ok := o.d.participants[key.TenantID]; ok {
			version := seller.Version + o.d.tenants[key.TenantID].Version
			tiles = append(tiles, ports.TileState{Key: key, Version: version, UpdatedAt: time.Unix(0, 0)})
		}
	}
	sort.Slice(tiles, func(i, j int) bool { return tileBefore(tiles[i].Key, tiles[j].Key) })
	indexes := make(map[ports.TileKey]int, len(tiles))
	for i := range tiles {
		indexes[tiles[i].Key] = i
	}
	for _, key := range o.keys(func(key productKey) bool { return selected[o.tileKey(key)] }) {
		i, ok := indexes[o.tileKey(key)]
		if !ok {
			continue
		}
		state := o.state(key)
		tile := &tiles[i]
		tile.Members = append(tile.Members, ports.TileMember{State: state, Changed: all || changed[key]})
		tile.Version += state.Product.Version()
		if state.Signals != nil {
			tile.Version += state.Signals.Version()
		}
		if state.Verdict != nil {
			tile.Version += state.Verdict.State().Version
		}
	}
	return tiles
}
