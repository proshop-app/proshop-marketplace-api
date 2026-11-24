package kafka

import (
	"testing"

	kafkago "github.com/segmentio/kafka-go"
)

func TestMessageKeysKeepOnlyRequiredOrdering(t *testing.T) {
	product := orderingKey{kind: orderProduct, tenant: "t1", entity: "p1"}
	tenant := orderingKey{kind: orderTenant, tenant: "t1"}
	cases := []struct {
		topic string
		key   string
		body  string
		want  orderingKey
	}{
		{TopicProductProjected, `["t1", "p1"]`, "", product},
		{TopicProductProjected, `["t1","p1"]`, "", product},
		{TopicProductProjectedBulk, `["t1","p1"]`, "", product},
		{TopicStockItemUpdated, `"stock-1"`, `{"productId":"p1"}`, product},
		// Цены сервер не публикует: неизвестная тема упорядочивается по партиции.
		{"catalog.product.price-updated", `"p1"`, `{"id":"p1"}`, orderingKey{}},
		{TopicTenantCreated, `"t1"`, `{"tenantId":"t1"}`, tenant},
		{TopicTenantUpdated, `"t1"`, `{"tenantId":"t1"}`, tenant},
		{TopicTenantSuspended, `"t1"`, `{"tenantId":"t1"}`, tenant},
		{TopicTenantResumed, `"t1"`, `{"tenantId":"t1"}`, tenant},
		{TopicTenantDeleted, `"t1"`, `{"tenantId":"t1"}`, tenant},
		{"platform.tenant.future", `"t1"`, `{"tenantId":"t1"}`, tenant},
		{TopicProductProjected, `null`, "", orderingKey{}},
		{TopicProductProjectedBulk, `null`, "", orderingKey{}},
		{TopicStockItemUpdated, `"stock-1"`, "{", orderingKey{}},
		{TopicTenantUpdated, "", "{", orderingKey{}},
		{TopicTenantCreated, "", `{"tenantId":"t2"}`, orderingKey{}},
		{"future.topic", "", "", orderingKey{}},
	}
	for _, tc := range cases {
		t.Run(tc.topic+tc.key+tc.body, func(t *testing.T) {
			message := kafkago.Message{
				Topic:     "shop." + tc.topic,
				Partition: 3,
				Key:       []byte(tc.key),
				Value:     []byte(tc.body),
				Headers:   []kafkago.Header{{Key: HeaderTenant, Value: []byte("t1")}},
			}
			want := tc.want
			if want.kind == orderPartition {
				want = orderingKey{kind: orderPartition, topic: message.Topic, partition: message.Partition}
			}
			if got := messageKey(message, "shop"); got != want {
				t.Fatalf("ключ: %+v, ожидался %+v", got, want)
			}
		})
	}
}

func TestOrderingKindsAndFallbackPartitionsDoNotCollide(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	keys := []orderingKey{
		{kind: orderProduct, tenant: "t1", entity: id},
		{kind: orderTenant, tenant: "t1"},
		{kind: orderPartition, topic: "a", partition: 0},
		{kind: orderPartition, topic: "a", partition: 1},
		{kind: orderPartition, topic: "b", partition: 0},
	}
	seen := make(map[orderingKey]bool)
	for _, key := range keys {
		if seen[key] {
			t.Fatal("разные виды или партиции попали в одну цепочку", key)
		}
		seen[key] = true
	}
}
