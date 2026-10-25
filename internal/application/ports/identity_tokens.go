package ports

import (
	"context"
	"time"
)

// IdentityAccess — JWT для ответа и отдельный секрет продления; в JSON не сериализуется.
type IdentityAccess struct {
	Token           string
	Key             string
	LocalID         string
	AccessExpiresAt time.Time
	ExpiresAt       time.Time
}

// IdentityTokens — выдача токенов hub и локальная проверка JWT без refresh-хранилища.
type IdentityTokens interface {
	Open(ctx context.Context, id string) (IdentityAccess, error)
	Authenticate(ctx context.Context, token string) (IdentityAccess, error)
	Refresh(ctx context.Context, key string) (IdentityAccess, error)
	Bootstrap(ctx context.Context, token, key string) (IdentityAccess, error)
	Logout(ctx context.Context, key string) error
}

// HubClaims — уже проверенная подпись, границы клиента и срок JWT.
type HubClaims struct {
	Subject   string
	ExpiresAt time.Time
	IssuedAt  time.Time
}

// HubTokens — результат обмена; Access не сохраняется в хранилище.
type HubTokens struct {
	Access  string
	Refresh string
	Claims  HubClaims
}

// IdentityHub — доверенный backend-клиент одного realm marketplace.
type IdentityHub interface {
	Open(ctx context.Context, localID string) (HubTokens, error)
	Claims(ctx context.Context, token string, allowExpired bool) (HubClaims, error)
	Refresh(ctx context.Context, token, subject string) (HubTokens, error)
	Revoke(ctx context.Context, token string) error
}

// IdentityMappings — неизменяемое соответствие hub subject и локального покупателя.
type IdentityMappings interface {
	Bind(ctx context.Context, subject string, id string) error
	Find(ctx context.Context, subject string) (string, error)
}

// IdentityRefresh — только зашифрованный refresh и метаданные его ротации.
type IdentityRefresh struct {
	LocalID   string
	Subject   string
	Encrypted string
	ExpiresAt time.Time
}

// IdentityRefreshStore — атомарная ротация; незавершённый обмен нельзя повторять.
type IdentityRefreshStore interface {
	Save(ctx context.Context, hash string, record IdentityRefresh) error
	Claim(ctx context.Context, hash, owner string, expected string) (IdentityRefresh, error)
	Finish(ctx context.Context, hash, owner string, record IdentityRefresh) error
	Delete(ctx context.Context, hash string) (IdentityRefresh, error)
}

// RefreshCipher — аутентифицированное шифрование с привязкой к записи/клиенту.
type RefreshCipher interface {
	Seal(plaintext, binding string) (string, error)
	Open(ciphertext, binding string) (string, error)
}
