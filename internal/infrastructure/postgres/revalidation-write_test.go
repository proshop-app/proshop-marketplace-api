package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPermanentRevalidationSQLClasses(t *testing.T) {
	for _, test := range []struct {
		code      string
		permanent bool
	}{
		{code: "22P02", permanent: true},
		{code: "22003", permanent: true},
		{code: "23502", permanent: true},
		{code: "23503", permanent: true},
		{code: "23514", permanent: true},
		{code: "23505"},
		{code: "40001"},
		{code: "40P01"},
		{code: "57014"},
		{code: "08006"},
	} {
		t.Run(test.code, func(t *testing.T) {
			if actual := permanentRevalidationSQL(&pgconn.PgError{Code: test.code}); actual != test.permanent {
				t.Fatal("неверная классификация", actual)
			}
		})
	}
}
