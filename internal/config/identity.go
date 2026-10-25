package config

import (
	"encoding/base64"
	"net"
	"net/url"
	"strings"
	"time"
)

// Identity — отдельный backend-клиент покупателей marketplace.
type Identity struct {
	OperatorClientID string
	OperatorSecret   string
	URL              string
	ClientID         string
	Secret           string
	EncryptionKey    string
	CAFile           string
	Timeout          time.Duration
}

func (r *reader) identity() Identity {
	cfg := Identity{
		OperatorClientID: r.str("IDENTITY_OPERATOR_CLIENT_ID", ""),
		OperatorSecret:   r.str("IDENTITY_OPERATOR_SECRET", ""),
		URL:              strings.TrimRight(r.required("IDENTITY_HUB_URL"), "/"),
		ClientID:         r.required("IDENTITY_CLIENT_ID"),
		Secret:           r.required("IDENTITY_BACKEND_SECRET"),
		EncryptionKey:    r.required("IDENTITY_REFRESH_KEY"),
		CAFile:           r.str("IDENTITY_CA_FILE", ""),
		Timeout:          r.duration("IDENTITY_TIMEOUT", 5*time.Second),
	}

	parsed, err := url.Parse(cfg.URL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		r.fail("IDENTITY_HUB_URL", "ожидается доверенный HTTPS origin")
	} else {
		ip := net.ParseIP(parsed.Hostname())
		loopback := parsed.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
		if parsed.Path != "" || (parsed.Scheme != "https" && (parsed.Scheme != "http" || !loopback)) {
			r.fail("IDENTITY_HUB_URL", "HTTPS origin без пути; HTTP разрешён только на loopback")
		}
	}

	if len(cfg.Secret) < minOperatorTokenLength {
		r.fail("IDENTITY_BACKEND_SECRET", "не короче 32 символов")
	}
	if strings.ContainsAny(cfg.ClientID, "/?# \t\r\n") {
		r.fail("IDENTITY_CLIENT_ID", "не должен содержать путь, пробелы или параметры URL")
	}

	key, err := base64.StdEncoding.DecodeString(cfg.EncryptionKey)
	if err != nil || len(key) != 32 {
		r.fail("IDENTITY_REFRESH_KEY", "ожидается отдельный base64 ключ длиной 32 байта")
	}

	return cfg
}

func (r *reader) validateIdentityOperator(cfg Config) {
	if cfg.Operator.Email == "" {
		return
	}

	client := cfg.Identity.OperatorClientID
	secret := cfg.Identity.OperatorSecret
	if client == "" || strings.ContainsAny(client, "/?# \t\r\n") || client == cfg.Identity.ClientID {
		r.fail("IDENTITY_OPERATOR_CLIENT_ID", "нужен отдельный backend client для realm platform")
	}
	if len(secret) < minOperatorTokenLength || secret == cfg.Identity.Secret || secret == cfg.Operator.Token {
		r.fail("IDENTITY_OPERATOR_SECRET", "нужен отдельный credential длиной от 32 символов")
	}
}
