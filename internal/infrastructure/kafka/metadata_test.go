package kafka

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestMetadataHonorsCancellationAndDeadline(t *testing.T) {
	for _, check := range []string{"reader-cancel", "reader-deadline", "ping"} {
		t.Run(check, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			accepted := make(chan net.Conn, 1)
			go func() {
				conn, err := listener.Accept()
				if err == nil {
					accepted <- conn
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if check == "ping" {
					done <- Ping(ctx, []string{listener.Addr().String()})
					return
				}
				reader := NewReader(Config{Brokers: []string{listener.Addr().String()}})
				_, err := reader.existing(ctx)
				done <- err
			}()
			conn := <-accepted
			defer func() { _ = conn.Close() }()
			if check == "reader-cancel" {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil || (!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)) {
					t.Fatalf("молчащий брокер не здоров; операция слушает контекст: %v", err)
				}
			case <-time.After(time.Second):
				_ = conn.Close()
				<-done
				t.Fatal("чтение метаданных зависло после отмены/срока")
			}
		})
	}
}
