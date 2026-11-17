package kafka

import (
	"testing"

	kafkago "github.com/segmentio/kafka-go"
)

func TestMessageKeysUnifyProductTopicsAndKeepOthersSequential(t *testing.T) {
	product := orderingKey{tenant: "t1", product: "p1"}
	cases := []struct {
		topic string
		key   string
		body  string
		want  orderingKey
	}{
		{TopicProductProjected, `["t1", "p1"]`, "", product},
		{TopicProductProjected, `["t1","p1"]`, "", product},
		{TopicStockItemUpdated, `"stock-1"`, `{"productId":"p1"}`, product},
		{TopicProductPriceUpdated, `"p1"`, `{"id":"p1"}`, product},
		{TopicTenantCreated, `"t1"`, "", orderingKey{}},
		{TopicTenantUpdated, `"t1"`, "", orderingKey{}},
		{TopicTenantSuspended, `"t1"`, "", orderingKey{}},
		{TopicTenantResumed, `"t1"`, "", orderingKey{}},
		{TopicTenantDeleted, `"t1"`, "", orderingKey{}},
		{TopicPriceContextChanged, `["t1","buyer"]`, "", orderingKey{}},
		{TopicProductProjected, `null`, "", orderingKey{}},
		{TopicStockItemUpdated, `"stock-1"`, "{", orderingKey{}},
	}
	for _, tc := range cases {
		t.Run(tc.topic+tc.key+tc.body, func(t *testing.T) {
			message := kafkago.Message{
				Topic:   "shop." + tc.topic,
				Key:     []byte(tc.key),
				Value:   []byte(tc.body),
				Headers: []kafkago.Header{{Key: HeaderTenant, Value: []byte("t1")}},
			}
			if got := messageKey(message, "shop"); got != tc.want {
				t.Fatalf("ключ: %+v, ожидался %+v", got, tc.want)
			}
		})
	}
}
