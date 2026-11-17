package kafka

import (
	"context"
	"fmt"
	"strings"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

func readPartitions(ctx context.Context, brokers []string, timeout time.Duration) ([]kafkago.Partition, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastErr error
	for _, broker := range brokers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		partitions, err := brokerPartitions(ctx, strings.TrimSpace(broker), timeout)
		if err == nil {
			return partitions, nil
		}
		lastErr = err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Deadline сокета может сработать раньше callback таймера контекста.
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return nil, context.DeadlineExceeded
	}
	return nil, fmt.Errorf("метаданные брокеров недоступны: %w", lastErr)
}

func brokerPartitions(ctx context.Context, broker string, timeout time.Duration) ([]kafkago.Partition, error) {
	dialer := kafkago.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", broker)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	// Conn.ReadPartitions не принимает контекст. Закрытие прерывает и чтение,
	// и запись при отмене; deadline ограничивает всю операцию после DialContext.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	return conn.ReadPartitions()
}
