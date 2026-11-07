package http

import (
	"net/http/httptest"
	"testing"
)

// Адреса из документационных сетей: 198.51.100.0/24 подставляет клиент, 203.0.113.0/24 —
// настоящие клиенты, 10.0.0.0/24 — прокси установки.
func TestClientIP(t *testing.T) {
	for _, tc := range []struct {
		name      string
		trusted   int
		forwarded []string
		remote    string
		want      string
	}{
		{
			name:      "без настройки заголовок не читается",
			forwarded: []string{"198.51.100.66"},
			remote:    "10.0.0.3:41000",
			want:      "10.0.0.3",
		},
		{
			name:      "один прокси: подделанное левое звено не влияет",
			trusted:   1,
			forwarded: []string{"198.51.100.66, 203.0.113.7"},
			remote:    "10.0.0.3:41000",
			want:      "203.0.113.7",
		},
		{
			name:      "два прокси, как Caddy и cell-proxy на стенде",
			trusted:   2,
			forwarded: []string{"198.51.100.66, 203.0.113.7, 10.0.0.2"},
			remote:    "10.0.0.3:41000",
			want:      "203.0.113.7",
		},
		{
			name:      "три прокси",
			trusted:   3,
			forwarded: []string{"198.51.100.66, 203.0.113.7, 10.0.0.1, 10.0.0.2"},
			remote:    "10.0.0.3:41000",
			want:      "203.0.113.7",
		},
		{
			name:      "подделка в несколько звеньев не сдвигает клиента",
			trusted:   2,
			forwarded: []string{"198.51.100.1, 198.51.100.2, 198.51.100.3, 203.0.113.7, 10.0.0.2"},
			remote:    "10.0.0.3:41000",
			want:      "203.0.113.7",
		},
		{
			name:      "строки заголовка — одна цепочка",
			trusted:   2,
			forwarded: []string{"198.51.100.66, 203.0.113.7", "10.0.0.2"},
			remote:    "10.0.0.3:41000",
			want:      "203.0.113.7",
		},
		{
			name:      "цепочка короче — самое левое звено от серверного рендера",
			trusted:   2,
			forwarded: []string{"203.0.113.7"},
			remote:    "10.0.0.5:52000",
			want:      "203.0.113.7",
		},
		{
			name:    "за прокси без заголовка — RemoteAddr",
			trusted: 2,
			remote:  "10.0.0.5:52000",
			want:    "10.0.0.5",
		},
		{
			name:      "не адрес — ближайшее звено правее, а не пустой адрес без лимита",
			trusted:   2,
			forwarded: []string{"unknown, 10.0.0.2"},
			remote:    "10.0.0.3:41000",
			want:      "10.0.0.2",
		},
		{
			name:      "порт и IPv6 приводятся к адресу",
			trusted:   1,
			forwarded: []string{"[2001:db8::7]:443"},
			remote:    "[2001:db8::3]:41000",
			want:      "2001:db8::7",
		},
		{
			name:      "IPv4 в IPv6 — тот же адрес и тот же счётчик",
			trusted:   1,
			forwarded: []string{"::ffff:203.0.113.7"},
			remote:    "10.0.0.3:41000",
			want:      "203.0.113.7",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/v1/auth/call", nil)
			r.RemoteAddr = tc.remote
			for _, line := range tc.forwarded {
				r.Header.Add("X-Forwarded-For", line)
			}
			if got := clientIP(r, tc.trusted); got != tc.want {
				t.Fatalf("адрес клиента %q, ожидался %q", got, tc.want)
			}
		})
	}
}
