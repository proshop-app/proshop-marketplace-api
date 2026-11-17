package redis_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
)

func TestClientHonorsDeadlineDuringSocketRead(t *testing.T) {
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
	client, err := redis.NewClient("redis://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Ping(ctx).Err() }()
	conn := <-accepted
	defer func() { _ = conn.Close() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("молчащий Redis ответил успешно")
		}
	case <-time.After(time.Second):
		_ = conn.Close()
		<-done
		t.Fatal("срок контекста не прервал чтение сокета")
	}
}
