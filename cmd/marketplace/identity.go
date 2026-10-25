package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"time"

	appidentity "github.com/proshop-app/proshop-marketplace/internal/application/identity"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/identityhub"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
)

func (s *server) identityTokens(clientID, secret, realm string, ttl time.Duration) (*appidentity.Tokens, error) {
	cfg := s.cfg.Identity
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.CAFile != "" {
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("системные CA недоступны")
		}

		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil || !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("IDENTITY_CA_FILE не содержит доступный CA")
		}

		transport.TLSClientConfig = &tls.Config{
			RootCAs:    roots,
			MinVersion: tls.VersionTLS12,
		}
	}

	hubConfig := identityhub.Config{
		Realm:    realm,
		URL:      cfg.URL,
		ClientID: clientID,
		Secret:   secret,
	}
	hub := identityhub.New(hubConfig, &http.Client{
		Transport: transport,
		Timeout:   cfg.Timeout,
	})
	issuer := cfg.URL + "/realms/" + realm
	cipher, err := identityhub.NewCipher(cfg.EncryptionKey, issuer+":"+clientID)
	if err != nil {
		return nil, fmt.Errorf("ключ шифрования refresh некорректен")
	}

	return appidentity.NewTokens(
		hub,
		postgres.NewIdentityMappings(s.store.pool, issuer, clientID),
		redis.NewIdentityRefreshes(s.cache, keyPrefix+clientID+":"),
		cipher,
		random.IDs{},
		clock.System{},
		ttl,
	), nil
}
