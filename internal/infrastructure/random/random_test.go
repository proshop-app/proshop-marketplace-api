package random_test

import (
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/random"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestIDs(t *testing.T) {
	g := random.IDs{}
	seen := map[ids.UUID]bool{}

	for range 1000 {
		id := g.NewUUID()
		if _, err := ids.ParseUUID(string(id)); err != nil || id[14] != '4' || seen[id] {
			t.Fatalf("UUID v4 без повторов: %s %v", id, err)
		}

		seen[id] = true
	}

	if token := g.NewToken(); len(token) != 43 || token == g.NewToken() {
		t.Fatalf("токен 256 бит: %q", token)
	}
}
