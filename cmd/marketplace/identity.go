package main

import (
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"time"

	appidentity "github.com/proshop-app/proshop-marketplace/internal/application/identity"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/clock"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/httpclient"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/identityhub"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/redis"
)

const (
	buyerRealm    = "marketplace"
	operatorRealm = "platform"
	// identityConns — вызовы identity-hub с реплики: вход, обмен refresh и обновление
	// ключей; обычная проверка access идёт по подписи без identity-hub.
	identityConns = 32
)

func (s *server) identityIssuer(realm string) string {
	return s.cfg.Identity.URL + "/realms/" + realm
}

func (s *server) identityMappings(realm, clientID string) ports.IdentityMappings {
	return postgres.NewIdentityMappings(s.store.pool(ports.DatabaseRead), s.identityIssuer(realm), clientID)
}

func (s *server) identityTokens(
	clientID, secret, realm string,
	ttl time.Duration,
	mappings ports.IdentityMappings,
) (*appidentity.Tokens, error) {
	cfg := s.cfg.Identity
	roots, err := identityRoots(cfg.CAFile)
	if err != nil {
		return nil, err
	}

	hubConfig := identityhub.Config{
		Realm:    realm,
		URL:      cfg.URL,
		ClientID: clientID,
		Secret:   secret,
	}
	hub := identityhub.New(hubConfig, httpclient.Internal(httpclient.Config{
		Conns:   identityConns,
		Timeout: cfg.Timeout,
		RootCAs: roots,
	}))
	issuer := s.identityIssuer(realm)
	cipher, err := identityhub.NewCipher(cfg.EncryptionKey, issuer+":"+clientID)
	if err != nil {
		return nil, fmt.Errorf("ключ шифрования refresh некорректен")
	}

	return appidentity.NewTokens(
		hub,
		mappings,
		redis.NewIdentityRefreshes(s.cache, keyPrefix+clientID+":"),
		cipher,
		random.IDs{},
		clock.System{},
		ttl,
	), nil
}

// identityRoots — системные CA и УЦ identity-hub из IDENTITY_CA_FILE; без файла — системные.
func identityRoots(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("системные CA недоступны")
	}
	// Путь задаёт развёртывание (IDENTITY_CA_FILE), не пользователь.
	pem, err := os.ReadFile(filepath.Clean(path))
	if err != nil || !roots.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("IDENTITY_CA_FILE не содержит доступный CA")
	}
	return roots, nil
}
