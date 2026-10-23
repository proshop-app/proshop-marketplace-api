// Package password — проверка Argon2id PHC-строк с ограниченными параметрами.
package password

import (
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Verifier использует профиль RFC 9106: 64 MiB, три прохода, четыре линии.
// Параметры фиксированы, поэтому ошибочная конфигурация не выделит гигабайты.
type Verifier struct{}

// Validate проверяет формат при запуске процесса, до первого запроса.
func Validate(hash string) error {
	_, _, err := parse(hash)
	return err
}

// Verify сравнивает результат за постоянное время.
func (Verifier) Verify(password, hash string) bool {
	salt, expected, err := parse(hash)
	if err != nil {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func parse(hash string) ([]byte, []byte, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" ||
		parts[2] != "v=19" || parts[3] != "m=65536,t=3,p=4" {
		return nil, nil, fmt.Errorf("нужен Argon2id v=19 m=65536,t=3,p=4")
	}
	salt, saltErr := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	key, keyErr := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if saltErr != nil || keyErr != nil || len(salt) < 16 || len(salt) > 64 || len(key) != 32 {
		return nil, nil, fmt.Errorf("Argon2id: соль 16–64 байта и хеш 32 байта в base64 без padding")
	}
	return salt, key, nil
}
