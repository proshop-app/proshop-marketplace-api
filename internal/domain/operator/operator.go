// Package operator — служебная учётная запись платформы и её сессия.
package operator

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Role — единственная роль с доступом к операторским операциям.
const Role = "operator"

// Principal — безопасное представление учётной записи, без секретов.
type Principal struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

// Session — сессия с абсолютным сроком и отпечатком конфигурации доступа.
type Session struct {
	TokenHash      string
	Principal      Principal
	CredentialHash string
	ExpiresAt      time.Time
}

// Require проверяет роль также при непосредственном вызове сценария.
func Require(p Principal) error {
	if p.ID == "" {
		return ErrUnauthorized
	}
	if p.Role != Role {
		return ErrForbidden
	}
	return nil
}

// Hash — только отпечаток секрета попадает в хранилище.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
