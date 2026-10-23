package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
)

// PickupIndex — геоиндекс пунктов выдачи. Источник истины — таблица
// справочника; индекс восстанавливается из неё.
type PickupIndex struct {
	index *Index
	name  string
}

// NewPickupIndex — геоиндекс на том же кластере, что индекс предложений.
func NewPickupIndex(index *Index, name string) *PickupIndex {
	return &PickupIndex{index: index, name: name}
}

type pickupDocument struct {
	Carrier     string    `json:"carrier"`
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	Address     string    `json:"address"`
	City        string    `json:"city"`
	Location    geoPoint  `json:"location"`
	Schedule    string    `json:"schedule,omitempty"`
	RefreshedAt time.Time `json:"refreshedAt"`
}

type geoPoint struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

func pickupID(carrier delivery.Carrier, code string) string {
	return string(carrier) + ":" + code
}

// Ensure заводит индекс, если его нет.
func (x *PickupIndex) Ensure(ctx context.Context) (bool, error) {
	status, err := x.index.do(ctx, http.MethodHead, "/"+url.PathEscape(x.name), nil, nil, http.StatusNotFound)
	if err != nil || status == http.StatusOK {
		return false, err
	}

	keyword := map[string]any{"type": "keyword"}
	body := map[string]any{
		"settings": map[string]any{
			"number_of_shards":   x.index.shards,
			"number_of_replicas": x.index.replicas,
			"analysis": map[string]any{
				"normalizer": map[string]any{"lowercase": map[string]any{"type": "custom", "filter": []string{"lowercase"}}},
			},
		},
		"mappings": map[string]any{
			"dynamic": "strict",
			"properties": map[string]any{
				"carrier":     keyword,
				"code":        keyword,
				"name":        map[string]any{"type": "text"},
				"address":     map[string]any{"type": "text"},
				"city":        map[string]any{"type": "keyword", "normalizer": "lowercase"},
				"location":    map[string]any{"type": "geo_point"},
				"schedule":    map[string]any{"type": "keyword", "index": false},
				"refreshedAt": map[string]any{"type": "date"},
			},
		},
	}

	status, err = x.index.do(ctx, http.MethodPut, "/"+url.PathEscape(x.name), body, nil, http.StatusBadRequest)
	if err != nil || status == http.StatusOK {
		return status == http.StatusOK, err
	}

	// 400: индекс завёл соседний процесс между проверкой и созданием — или схема неверна
	if exists, err := x.index.do(ctx, http.MethodHead, "/"+url.PathEscape(x.name), nil, nil, http.StatusNotFound); err != nil || exists != http.StatusOK {
		return false, ErrUnavailable.WithMessage("индекс пунктов выдачи не заведён: " + x.name)
	}

	return false, nil
}

// Put пишет действующие пункты и удаляет закрытые.
func (x *PickupIndex) Put(ctx context.Context, points []delivery.PickupPoint) error {
	if len(points) == 0 {
		return nil
	}

	var body bytes.Buffer
	encoder := json.NewEncoder(&body)

	for _, p := range points {
		id := pickupID(p.Carrier, p.Code)

		if !p.Active {
			if err := encoder.Encode(map[string]any{"delete": map[string]any{"_index": x.name, "_id": id}}); err != nil {
				return fmt.Errorf("пачка пунктов: %w", err)
			}

			continue
		}

		if err := encoder.Encode(map[string]any{"index": map[string]any{"_index": x.name, "_id": id}}); err != nil {
			return fmt.Errorf("пачка пунктов: %w", err)
		}

		doc := pickupDocument{
			Carrier: string(p.Carrier), Code: p.Code, Name: p.Name, Address: p.Address, City: p.City,
			Location: geoPoint{Lat: p.Location.Lat, Lon: p.Location.Lon}, Schedule: p.Schedule, RefreshedAt: p.RefreshedAt.UTC(),
		}
		if err := encoder.Encode(doc); err != nil {
			return fmt.Errorf("пачка пунктов: %w", err)
		}
	}

	var response struct {
		Errors bool `json:"errors"`
		Items  []map[string]struct {
			ID     string `json:"_id"`
			Status int    `json:"status"`
			Error  *struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
			} `json:"error"`
		} `json:"items"`
	}

	if _, err := x.index.raw(ctx, http.MethodPost, "/_bulk", &body, "application/x-ndjson", &response); err != nil {
		return err
	}

	for _, item := range response.Items {
		for action, result := range item {
			if result.Error == nil || (action == "delete" && result.Status == http.StatusNotFound) {
				continue
			}

			return ErrUnavailable.WithMessage(fmt.Sprintf("пункт %s не записан: %s: %s", result.ID, result.Error.Type, result.Error.Reason))
		}
	}

	return nil
}

// Search — пункты перевозчика в прямоугольнике карты или городе.
func (x *PickupIndex) Search(ctx context.Context, query ports.PickupPointQuery) ([]delivery.PickupPoint, error) {
	filters := []any{map[string]any{"term": map[string]any{"carrier": string(query.Carrier)}}}

	if box := query.Area.BBox; box != nil {
		filters = append(filters, map[string]any{"geo_bounding_box": map[string]any{"location": map[string]any{
			"top_left":     map[string]any{"lat": box.MaxLat, "lon": box.MinLon},
			"bottom_right": map[string]any{"lat": box.MinLat, "lon": box.MaxLon},
		}}})
	} else {
		filters = append(filters, map[string]any{"term": map[string]any{"city": strings.TrimSpace(query.Area.City)}})
	}

	body := map[string]any{
		"query": map[string]any{"bool": map[string]any{"filter": filters}},
		"size":  query.Limit,
		"sort":  []any{map[string]any{"code": "asc"}},
	}

	var response struct {
		Hits struct {
			Hits []struct {
				Source pickupDocument `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}

	if _, err := x.index.do(ctx, http.MethodPost, "/"+url.PathEscape(x.name)+"/_search", body, &response); err != nil {
		return nil, err
	}

	points := make([]delivery.PickupPoint, 0, len(response.Hits.Hits))
	for _, hit := range response.Hits.Hits {
		doc := hit.Source
		points = append(points, delivery.PickupPoint{
			Carrier: delivery.Carrier(doc.Carrier), Code: doc.Code, Name: doc.Name, Address: doc.Address, City: doc.City,
			Location: delivery.Location{Lat: doc.Location.Lat, Lon: doc.Location.Lon}, Schedule: doc.Schedule,
			Active: true, RefreshedAt: doc.RefreshedAt,
		})
	}

	return points, nil
}

// RefreshPickups делает записанное видимым поиску сразу; нужен проверкам.
func (x *PickupIndex) RefreshPickups(ctx context.Context) error {
	_, err := x.index.do(ctx, http.MethodPost, "/"+url.PathEscape(x.name)+"/_refresh", nil, nil)

	return err
}
