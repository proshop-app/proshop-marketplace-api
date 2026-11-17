package kafka

import (
	"encoding/json"
	"strings"

	kafkago "github.com/segmentio/kafka-go"
)

// Нулевой ключ оставляет остальные топики и повреждённые ключи последовательными.
type orderingKey struct {
	tenant  string
	product string
}

func messageKey(message kafkago.Message, prefix string) orderingKey {
	switch strings.TrimPrefix(message.Topic, prefixOf(prefix)) {
	case TopicProductProjected:
		tenant, product, err := projectionKey(message)
		if err == nil && tenant != "" && product != "" {
			return orderingKey{tenant: tenant, product: product}
		}
	case TopicStockItemUpdated, TopicProductPriceUpdated:
		var value struct {
			ID        string `json:"id"`
			ProductID string `json:"productId"`
		}
		if json.Unmarshal(message.Value, &value) != nil {
			return orderingKey{}
		}
		product := value.ID
		if strings.TrimPrefix(message.Topic, prefixOf(prefix)) == TopicStockItemUpdated {
			product = value.ProductID
		}
		if tenant := tenantHeader(message); tenant != "" && product != "" {
			return orderingKey{tenant: tenant, product: product}
		}
	}
	return orderingKey{}
}
