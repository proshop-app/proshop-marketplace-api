package ports

import (
	"context"
	"encoding/json"
)

// ModelKeyCount содержит покрытие ключами по направлению и источнику.
type ModelKeyCount struct {
	Direction string  `json:"direction"`
	Source    string  `json:"source"`
	Offers    int64   `json:"offers"`
	Shared    int64   `json:"shared"`
	Share     float64 `json:"share"`
}

// ModelKeySample содержит пример совпадения между продавцами.
type ModelKeySample struct {
	ModelKey string          `json:"modelKey"`
	Source   string          `json:"source"`
	Offers   json.RawMessage `json:"offers"`
}

// ModelKeyReport содержит покрытие и примеры межмагазинных совпадений.
type ModelKeyReport struct {
	Counts  []ModelKeyCount  `json:"counts"`
	Samples []ModelKeySample `json:"samples"`
}

// ModelKeyStats предоставляет статистику опубликованных ключей модели.
type ModelKeyStats interface {
	Report(context.Context, bool) (ModelKeyReport, error)
}
