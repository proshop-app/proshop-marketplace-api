package buyer

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// Session — сессия покупателя. Хранится по отпечатку токена: утечка
// хранилища не выдаёт сами токены.
type Session struct {
	TokenHash string
	BuyerID   ids.UUID
	CreatedAt time.Time
	ExpiresAt time.Time
}

// NewSession — сессия покупателя на ttl по непрозрачному токену.
func NewSession(token string, buyerID ids.UUID, now time.Time, ttl time.Duration) Session {
	return Session{TokenHash: HashToken(token), BuyerID: buyerID, CreatedAt: now, ExpiresAt: now.Add(ttl)}
}

// Expired — срок сессии вышел.
func (s Session) Expired(now time.Time) bool {
	return !now.Before(s.ExpiresAt)
}

// HashToken — отпечаток токена сессии.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))

	return hex.EncodeToString(sum[:])
}
