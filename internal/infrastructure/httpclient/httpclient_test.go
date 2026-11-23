package httpclient_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/httpclient"
)

func TestClientReusesConnectionsWithinLimit(t *testing.T) {
	const conns = 4
	var opened atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			opened.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	client := httpclient.New(httpclient.Config{Conns: conns, Timeout: time.Second})

	for range 3 {
		var wave sync.WaitGroup
		for range 4 * conns {
			wave.Add(1)
			go func() {
				defer wave.Done()
				reply, err := client.Get(server.URL)
				if err != nil {
					t.Error(err)
					return
				}
				_ = reply.Body.Close()
			}()
		}
		wave.Wait()
	}

	if got := opened.Load(); got > conns {
		t.Fatalf("всплески переиспользуют не больше %d соединений, открыто %d", conns, got)
	}
}

func TestClientRefusesRedirectAndCutsHungServer(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hang" {
			<-release
			return
		}
		http.Redirect(w, r, "https://other.test/", http.StatusFound)
	}))
	defer server.Close()
	defer close(release)
	client := httpclient.New(httpclient.Config{Conns: 2, Timeout: 50 * time.Millisecond})

	reply, err := client.Get(server.URL + "/redirect")
	if err != nil || reply.StatusCode != http.StatusFound {
		t.Fatalf("переход по redirect уносит токен на чужой адрес: %v %v", reply, err)
	}
	_ = reply.Body.Close()

	started := time.Now()
	hung, err := client.Get(server.URL + "/hang")
	if err == nil {
		_ = hung.Body.Close()
	}
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("зависший сервер отрезается сроком вызова: %v за %s", err, time.Since(started))
	}
}
