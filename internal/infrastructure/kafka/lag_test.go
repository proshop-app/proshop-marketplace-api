package kafka

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	kafkago "github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/protocol/listoffsets"
)

// offsetsTransport отвечает концом журнала и считает запросы к брокеру.
type offsetsTransport struct {
	ends  map[int32]int64
	calls int
}

type lagRecorder struct {
	lags      map[int]int64
	forgotten []int
}

func (t *offsetsTransport) RoundTrip(context.Context, net.Addr, kafkago.Request) (kafkago.Response, error) {
	t.calls++
	partitions := make([]listoffsets.ResponsePartition, 0, len(t.ends))
	for partition, end := range t.ends {
		partitions = append(partitions, listoffsets.ResponsePartition{
			Partition: partition,
			Timestamp: kafkago.LastOffset,
			Offset:    end,
		})
	}
	return &listoffsets.Response{Topics: []listoffsets.ResponseTopic{{Topic: "shop.t", Partitions: partitions}}}, nil
}

func (r *lagRecorder) ObserveLag(_ string, partition int, lag int64) { r.lags[partition] = lag }

func (r *lagRecorder) ForgetLag(_ string, partition int) {
	r.forgotten = append(r.forgotten, partition)
}

func TestLagCountsFromGroupPositionInOneBrokerRequest(t *testing.T) {
	transport := &offsetsTransport{ends: map[int32]int64{0: 120, 1: 40, 2: 9}}
	recorder := &lagRecorder{lags: map[int]int64{}}
	reader := NewReader(Config{Brokers: []string{"localhost:9092"}, Lag: recorder})
	reader.client.Transport = transport
	session := &readSession{positions: map[string]map[int]*atomic.Int64{"shop.t": {
		0: startPosition(100),
		1: startPosition(kafkago.FirstOffset),
		2: startPosition(9),
	}}}

	// Окно партиции 0 полно, чтение стоит: отставание растёт от зафиксированной позиции.
	reader.observeLag(context.Background(), session)
	if transport.calls != 1 || recorder.lags[0] != 20 || recorder.lags[2] != 0 {
		t.Fatalf("одним запросом: конец журнала минус позиция группы: %+v, запросов %d", recorder.lags, transport.calls)
	}
	if _, ok := recorder.lags[1]; ok {
		t.Fatal("без позиции группы отставание не придумывается")
	}

	session.positions["shop.t"][1].CompareAndSwap(unknownPosition, 30)
	session.advance(map[string]map[int]int64{"shop.t": {0: 110, 2: 3}})
	reader.observeLag(context.Background(), session)
	if recorder.lags[0] != 10 || recorder.lags[1] != 10 || recorder.lags[2] != 0 {
		t.Fatalf("фиксация двигает позицию только вперёд: %+v", recorder.lags)
	}

	session.forgetLag(recorder)
	if len(recorder.forgotten) != 3 {
		t.Fatalf("ушедшие партиции убираются из метрики: %v", recorder.forgotten)
	}
}
