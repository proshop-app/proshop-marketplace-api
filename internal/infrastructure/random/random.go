// Package random — идентификаторы и секреты из криптографического генератора.
package random

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"

	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

// IDs — UUID версии 4 и токены по 256 бит.
type IDs struct{}

// NewUUID — случайный UUID версии 4.
func (IDs) NewUUID() ids.UUID {
	var b [16]byte
	fill(b[:])

	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	h := hex.EncodeToString(b[:])

	return ids.UUID(h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32])
}

// NewToken — 32 случайных байта в base64url без выравнивания.
func (IDs) NewToken() string {
	var b [32]byte
	fill(b[:])

	return base64.RawURLEncoding.EncodeToString(b[:])
}

// fill — системный генератор не отказывает; отказ — неисправность ядра, работать дальше нельзя.
func fill(b []byte) {
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
}
