// Package identity — ошибки и отпечатки токенов независимо от локального профиля.
package identity

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/proshop-app/proshop-marketplace/pkg/errs"
)

var (
	// ErrUnauthorized — подпись, границы клиента или привязка аккаунта не подтверждены.
	ErrUnauthorized = errs.New(errs.Unauthorized, "buyer.unauthorized", "нужен вход пользователя")
	// ErrAccessExpired — подпись и границы верны, истёк только access.
	ErrAccessExpired = errs.New(errs.Unauthorized, "buyer.access_expired", "срок доступа истёк")
)

// HashToken — непрозрачный секрет никогда не входит в ключ хранилища.
func HashToken(token string) string {
	hash := sha256.Sum256([]byte(token))

	return hex.EncodeToString(hash[:])
}
