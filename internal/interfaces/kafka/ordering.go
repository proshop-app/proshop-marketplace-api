package kafka

import (
	"encoding/json"
	"strings"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

type orderingKind uint8

const (
	orderPartition orderingKind = iota
	orderProduct
	orderTenant
	orderOrder
)

// Вид входит в ключ: покупатель, товар и запись реестра не разделяют очередь случайно.
type orderingKey struct {
	kind      orderingKind
	tenant    string
	entity    string
	topic     string
	partition int
}

func messageKey(message kafkago.Message, prefix string) orderingKey {
	topic := strings.TrimPrefix(message.Topic, prefixOf(prefix))
	var key orderingKey
	switch topic {
	case TopicProductProjected, TopicProductProjectedBulk:
		key = pairedKey(message)
	case TopicStockItemUpdated:
		key = signalKey(message)
	case TopicOrderStatusChanged:
		key = orderKey(message)
	default:
		if strings.HasPrefix(topic, "platform.tenant.") {
			key = registryKey(message)
		}
	}
	if key.kind != orderPartition {
		return key
	}
	return orderingKey{kind: orderPartition, topic: message.Topic, partition: message.Partition}
}

func pairedKey(message kafkago.Message) orderingKey {
	tenant, entity, err := projectionKey(message)
	if err != nil {
		return orderingKey{}
	}
	kind := orderProduct
	_, err = ids.ParseProductID(entity)
	if _, tenantErr := ids.ParseTenantID(tenant); tenantErr != nil || err != nil {
		return orderingKey{}
	}
	return orderingKey{kind: kind, tenant: tenant, entity: entity}
}

func signalKey(message kafkago.Message) orderingKey {
	var value struct {
		ProductID string `json:"productId"`
	}
	if json.Unmarshal(message.Value, &value) != nil {
		return orderingKey{}
	}
	tenant := tenantHeader(message)
	_, tenantErr := ids.ParseTenantID(tenant)
	_, productErr := ids.ParseProductID(value.ProductID)
	if tenantErr != nil || productErr != nil {
		return orderingKey{}
	}
	return orderingKey{kind: orderProduct, tenant: tenant, entity: value.ProductID}
}

func registryKey(message kafkago.Message) orderingKey {
	var value struct {
		TenantID string `json:"tenantId"`
	}
	if json.Unmarshal(message.Value, &value) != nil || value.TenantID == "" {
		return orderingKey{}
	}
	tenant, err := resolveTenant(value.TenantID, tenantHeader(message))
	if err != nil {
		return orderingKey{}
	}
	return orderingKey{kind: orderTenant, tenant: string(tenant)}
}

func (k orderingKey) group() orderingKey {
	if k.kind == orderPartition {
		return k
	}
	return orderingKey{kind: orderTenant, tenant: k.tenant}
}
