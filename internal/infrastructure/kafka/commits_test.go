package kafka

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/protocol/offsetcommit"
)

type commitTransport struct {
	request *offsetcommit.Request
	failure int16
	stall   bool
}

func (t *commitTransport) RoundTrip(
	ctx context.Context,
	_ net.Addr,
	request kafkago.Request,
) (kafkago.Response, error) {
	t.request = request.(*offsetcommit.Request)
	if t.stall {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &offsetcommit.Response{Topics: []offsetcommit.ResponseTopic{{
		Name:       "topic",
		Partitions: []offsetcommit.ResponsePartition{{PartitionIndex: 0, ErrorCode: t.failure}},
	}}}, nil
}

func TestCommitOffsetsKeepsFencingAndPartitionErrors(t *testing.T) {
	transport := &commitTransport{failure: int16(kafkago.IllegalGeneration)}
	reader := NewReader(Config{Brokers: []string{"localhost:9092"}})
	reader.client.Transport = transport
	generation := &kafkago.Generation{ID: 7, GroupID: "group", MemberID: "member"}
	err := reader.commitOffsets(context.Background(), generation, map[string]map[int]int64{"topic": {0: 3}})
	if !errors.Is(err, kafkago.IllegalGeneration) {
		t.Fatalf("ошибка партиции потеряна: %v", err)
	}
	request := transport.request
	if request.GenerationID != 7 || request.MemberID != "member" || request.GroupID != "group" ||
		request.Topics[0].Partitions[0].CommittedOffset != 3 {
		t.Fatalf("коммит утратил привязку к поколению: %+v", request)
	}
}

func TestCommitOffsetsHonoursDeadlineOnStalledBroker(t *testing.T) {
	reader := NewReader(Config{Brokers: []string{"localhost:9092"}})
	reader.client.Transport = &commitTransport{stall: true}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := reader.commitOffsets(ctx, &kafkago.Generation{}, map[string]map[int]int64{"topic": {0: 1}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("зависший брокер не прервался по сроку финального коммита: %v", err)
	}
}
