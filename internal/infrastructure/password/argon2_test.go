package password_test

import (
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/password"
)

// Эталон получен независимой reference-реализацией libargon2.
const fixture = "$argon2id$v=19$m=65536,t=3,p=4$dGVzdC1zYWx0LTEyMzQ1Ng$dxebWRf1c60mv7EgTpWwlwTvY+VYM8pypB0T31qKRs4"

func TestArgon2idPHC(t *testing.T) {
	verifier := password.Verifier{}
	if err := password.Validate(fixture); err != nil {
		t.Fatal(err)
	}
	if !verifier.Verify("correct password", fixture) || verifier.Verify("wrong password", fixture) {
		t.Fatal("проверка Argon2id")
	}
	for _, hash := range []string{
		"",
		"$argon2id$v=19$m=999999999,t=3,p=4$salt$hash",
		"$argon2i$v=19$m=65536,t=3,p=4$a$b",
	} {
		if err := password.Validate(hash); err == nil || verifier.Verify("password", hash) {
			t.Fatal(hash)
		}
	}
}
