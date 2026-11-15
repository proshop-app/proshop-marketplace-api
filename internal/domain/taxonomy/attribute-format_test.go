package taxonomy

import (
	"errors"
	"strings"
	"testing"
)

func TestReleaseAttributeFormat(t *testing.T) {
	for _, format := range []string{"", "gtin", "isbn", "unknown", "ISBN"} {
		t.Run(format, func(t *testing.T) {
			release := identityRelease()
			release.Attributes[0].Format = format
			err := ValidateRelease(release)
			valid := format == "" || format == "gtin" || format == "isbn"
			if valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), release.Attributes[0].Code) {
				t.Fatal("нет ошибки с кодом атрибута", err)
			}
		})
	}
}
