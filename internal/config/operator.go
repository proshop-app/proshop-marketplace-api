package config

import (
	"encoding/json"
	"net/mail"
	"net/url"
	"strings"

	"github.com/proshop-app/proshop-marketplace/pkg/regions"
)

func (r *reader) validateOperator(cfg Operator) {
	if cfg.Email == "" && cfg.PasswordHash == "" && cfg.Origin == "" {
		return
	}
	address, err := mail.ParseAddress(cfg.Email)
	if err != nil || address.Address != cfg.Email {
		r.fail("OPERATOR_EMAIL", "нужен email учётной записи платформы")
	}
	if cfg.PasswordHash == "" {
		r.fail("OPERATOR_PASSWORD_HASH", "обязателен вместе с OPERATOR_EMAIL")
	}
	origin, err := url.Parse(cfg.Origin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil ||
		origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" || strings.HasSuffix(cfg.Origin, "/") {
		r.fail("OPERATOR_ORIGIN", "нужен HTTPS origin панели без пути, например https://admin.example")
	}
	if cfg.SessionTTL.Hours() > 24 {
		r.fail("OPERATOR_SESSION_TTL", "не больше 24h")
	}
}

func (r *reader) regionCodes(name string) map[string]string {
	raw := r.str(name, "{}")
	var codes map[string]string
	if err := json.Unmarshal([]byte(raw), &codes); err != nil {
		r.fail(name, "нужен JSON-объект код зоны → ISO")
	}
	for key, value := range codes {
		if key == "" || !regions.ValidRU(value) {
			r.fail(name, "значения — существующие ISO 3166-2 RU")
		}
	}
	return codes
}
