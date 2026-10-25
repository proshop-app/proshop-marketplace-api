package identity_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appidentity "github.com/proshop-app/proshop-marketplace/internal/application/identity"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/identity"
	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

var unavailable = errs.New(errs.Unavailable, "test.unavailable", "недоступно")

type hub struct {
	calls      atomic.Int32
	refreshErr error
	entered    chan struct{}
	release    chan struct{}
}

type mappings struct {
	err error
}

type cipher struct{}

type refreshStore struct {
	mu      sync.Mutex
	record  ports.IdentityRefresh
	owner   string
	deleted bool
}

func (h *hub) Open(_ context.Context, id string) (ports.HubTokens, error) {
	return ports.HubTokens{
		Access:  "jwt",
		Refresh: "refresh",
		Claims: ports.HubClaims{
			Subject:   id,
			ExpiresAt: time.Now().Add(time.Minute),
		},
	}, nil
}

func (h *hub) Claims(_ context.Context, token string, allowExpired bool) (ports.HubClaims, error) {
	if token == "forged" {
		return ports.HubClaims{}, identity.ErrUnauthorized
	}
	if !allowExpired && token == "expired" {
		return ports.HubClaims{}, identity.ErrAccessExpired
	}

	return ports.HubClaims{
		Subject:   "account-one",
		ExpiresAt: time.Now().Add(time.Minute),
	}, nil
}

func (h *hub) Refresh(_ context.Context, _ string, subject string) (ports.HubTokens, error) {
	h.calls.Add(1)
	if h.entered != nil {
		close(h.entered)
		<-h.release
	}
	if h.refreshErr != nil {
		return ports.HubTokens{}, h.refreshErr
	}

	return ports.HubTokens{
		Access:  "new-jwt",
		Refresh: "new-refresh",
		Claims: ports.HubClaims{
			Subject:   subject,
			ExpiresAt: time.Now().Add(time.Minute),
		},
	}, nil
}

func (*hub) Revoke(context.Context, string) error                          { return nil }
func (*mappings) Bind(context.Context, string, string) error               { return nil }
func (m *mappings) Find(_ context.Context, subject string) (string, error) { return subject, m.err }
func (cipher) Seal(plain, binding string) (string, error)                  { return plain + ":" + binding, nil }
func (cipher) Open(_ string, _ string) (string, error)                     { return "refresh", nil }

func (s *refreshStore) Save(_ context.Context, _ string, record ports.IdentityRefresh) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record = record
	s.deleted = false

	return nil
}

func (s *refreshStore) Claim(_ context.Context, _, owner, expected string) (ports.IdentityRefresh, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleted || (expected != "" && expected != s.record.LocalID) {
		return ports.IdentityRefresh{}, identity.ErrUnauthorized
	}
	if s.owner != "" {
		return ports.IdentityRefresh{}, appidentity.ErrRefreshBusy
	}
	s.owner = owner

	return s.record, nil
}

func (s *refreshStore) Finish(_ context.Context, _, owner string, record ports.IdentityRefresh) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleted || owner != s.owner {
		return identity.ErrUnauthorized
	}
	s.record = record
	s.owner = ""

	return nil
}

func (s *refreshStore) Delete(_ context.Context, _ string) (ports.IdentityRefresh, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleted {
		return ports.IdentityRefresh{}, identity.ErrUnauthorized
	}
	s.deleted = true

	return s.record, nil
}

func service(h *hub, m *mappings, s *refreshStore) *appidentity.Tokens {
	return appidentity.NewTokens(h, m, s, cipher{}, &fake.IDs{}, fake.NewClock(time.Now()), time.Hour)
}

func TestPreexchangeFailureCanRetryWithoutLosingRefresh(t *testing.T) {
	h, m, s := &hub{}, &mappings{}, &refreshStore{}
	tokens := service(h, m, s)
	access, err := tokens.Open(context.Background(), "account-one")
	if err != nil {
		t.Fatal(err)
	}
	m.err = unavailable
	if _, err := tokens.Refresh(context.Background(), access.Key); !errors.Is(err, unavailable) {
		t.Fatal("ошибка mapping должна остаться unavailable", err)
	}
	m.err = nil
	if _, err := tokens.Refresh(context.Background(), access.Key); err != nil || h.calls.Load() != 1 {
		t.Fatal("после восстановления повтор безопасен до первого обмена", err, h.calls.Load())
	}
}

func TestUnknownExchangeOutcomeNeverRetriesOldRefresh(t *testing.T) {
	h, m, s := &hub{refreshErr: unavailable}, &mappings{}, &refreshStore{}
	tokens := service(h, m, s)
	access, _ := tokens.Open(context.Background(), "account-one")
	if _, err := tokens.Refresh(context.Background(), access.Key); !errors.Is(err, unavailable) {
		t.Fatal(err)
	}
	if _, err := tokens.Refresh(context.Background(), access.Key); !errors.Is(err, appidentity.ErrRefreshBusy) {
		t.Fatal(err)
	}
	if h.calls.Load() != 1 {
		t.Fatal("старый refresh был отправлен повторно")
	}
}

func TestLogoutDuringRefreshCannotResurrectRecord(t *testing.T) {
	h := &hub{entered: make(chan struct{}), release: make(chan struct{})}
	tokens := service(h, &mappings{}, &refreshStore{})
	access, _ := tokens.Open(context.Background(), "account-one")
	done := make(chan error, 1)
	go func() {
		_, err := tokens.Refresh(context.Background(), access.Key)
		done <- err
	}()
	<-h.entered
	if err := tokens.Logout(context.Background(), access.Key); err != nil {
		t.Fatal(err)
	}
	close(h.release)
	if err := <-done; !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatal("Finish воскресил удалённую запись", err)
	}
	if _, err := tokens.Refresh(context.Background(), access.Key); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatal(err)
	}
}

func TestExpiredJWTAndForeignKeyDoNotTouchForeignRefresh(t *testing.T) {
	h, s := &hub{}, &refreshStore{}
	tokens := service(h, &mappings{}, s)
	access, _ := tokens.Open(context.Background(), "account-two")
	_, err := tokens.Bootstrap(context.Background(), "expired", access.Key)
	if !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatal(err)
	}
	if h.calls.Load() != 0 {
		t.Fatal("чужой refresh отправлен в hub")
	}
	if _, err := tokens.Refresh(context.Background(), access.Key); err != nil {
		t.Fatal("чужой bootstrap не должен портить запись", err)
	}
}
