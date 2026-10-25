package operator

import "github.com/proshop-app/proshop-marketplace/internal/domain/operator"

// credentialSubject меняется при смене пароля/локального ID, не выдаёт hub контакты.
func credentialSubject(cfg Config) string {
	return operator.Hash(cfg.Principal.ID + "\x00" + cfg.PasswordHash)
}
