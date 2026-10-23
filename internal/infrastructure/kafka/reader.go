// Package kafka — чтение топиков установки на kafka-go: своя группа
// консьюмера, ручной коммит после обработки.
package kafka

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// Config — брокеры, группа и полные имена топиков.
type Config struct {
	Brokers []string
	GroupID string
	Topics  []string
	Logger  *slog.Logger
}

// Reader — чтение группой с ручным коммитом. Новая группа начинает с
// самого раннего сообщения: проекция собирается из истории топиков.
type Reader struct {
	reader *kafkago.Reader
}

// NewReader — читатель группы.
func NewReader(cfg Config) *Reader {
	return &Reader{reader: kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:               cfg.Brokers,
		GroupID:               cfg.GroupID,
		GroupTopics:           cfg.Topics,
		MinBytes:              1,
		MaxBytes:              10 << 20,
		MaxWait:               500 * time.Millisecond,
		StartOffset:           kafkago.FirstOffset,
		CommitInterval:        0,
		WatchPartitionChanges: true,
		ErrorLogger: kafkago.LoggerFunc(func(format string, args ...any) {
			cfg.Logger.Warn("kafka: "+fmt.Sprintf(format, args...), slog.String("group", cfg.GroupID))
		}),
	})}
}

// Fetch — следующее сообщение без коммита.
func (r *Reader) Fetch(ctx context.Context) (kafkago.Message, error) {
	return r.reader.FetchMessage(ctx)
}

// Commit фиксирует позицию после сообщения.
func (r *Reader) Commit(ctx context.Context, message kafkago.Message) error {
	return r.reader.CommitMessages(ctx, message)
}

// Close закрывает читатель и выходит из группы.
func (r *Reader) Close() error {
	return r.reader.Close()
}

// Ping проверяет, что хотя бы один брокер принимает соединение.
func Ping(ctx context.Context, brokers []string) error {
	var lastErr error

	for _, broker := range brokers {
		dialer := net.Dialer{Timeout: 2 * time.Second}

		conn, err := dialer.DialContext(ctx, "tcp", strings.TrimSpace(broker))
		if err == nil {
			return conn.Close()
		}

		lastErr = err
	}

	return fmt.Errorf("брокеры недоступны: %w", lastErr)
}
