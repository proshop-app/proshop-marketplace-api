package identityhub

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
)

// Cipher — AES-256-GCM; AAD включает client_id и отпечаток браузерного ключа.
type Cipher struct {
	aead   cipher.AEAD
	client string
}

// NewCipher требует отдельный ключ шифрования из окружения.
func NewCipher(encoded, client string) (*Cipher, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, ErrUnavailable
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrUnavailable
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrUnavailable
	}

	return &Cipher{
		aead:   aead,
		client: client,
	}, nil
}

// Seal использует новый случайный nonce при каждой записи refresh.
func (c *Cipher) Seal(plain, binding string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", ErrUnavailable
	}

	encrypted := c.aead.Seal(nonce, nonce, []byte(plain), []byte(c.client+":"+binding))

	return base64.RawStdEncoding.EncodeToString(encrypted), nil
}

// Open не допускает перенос ciphertext между записями и клиентами.
func (c *Cipher) Open(encoded, binding string) (string, error) {
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(raw) < c.aead.NonceSize()+c.aead.Overhead() {
		return "", ErrUnavailable
	}

	size := c.aead.NonceSize()
	plain, err := c.aead.Open(nil, raw[:size], raw[size:], []byte(c.client+":"+binding))
	if err != nil {
		return "", ErrUnavailable
	}

	return string(plain), nil
}
