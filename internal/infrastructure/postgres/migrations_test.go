package postgres

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

// migrationNumbering — нарушения нумерации: файл, в имени которого goose не
// найдёт номер, и номер, занятый двумя файлами.
func migrationNumbering(names []string) []string {
	var (
		owners   = make(map[int64]string, len(names))
		problems []string
	)

	for _, name := range names {
		// без строгого режима goose пропускает такой файл молча: миграция не применится
		version, err := goose.NumericComponent(name)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: goose не видит номер: %v", name, err))

			continue
		}

		if owner, ok := owners[version]; ok {
			problems = append(problems, fmt.Sprintf("номер %d занят дважды: %s и %s", version, owner, name))

			continue
		}

		owners[version] = name
	}

	return problems
}

// TestMigrationNumbering — встроенный набор goose загрузит целиком. Повтор
// номера он отвергает только на старте migrate, поэтому сторож — в make test,
// без базы и тега integration.
func TestMigrationNumbering(t *testing.T) {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) < 10 {
		t.Fatalf("найдено %d миграций: сторож ничего бы не проверил", len(names))
	}

	for _, problem := range migrationNumbering(names) {
		t.Error(problem)
	}
}

func TestMigrationNumberingGuardCatchesViolations(t *testing.T) {
	names := []string{
		"00001_first.sql",
		"00002_second.sql",
		"00002_twin.sql",
		"third.sql",
	}

	got := migrationNumbering(names)
	if len(got) != 2 {
		t.Fatalf("повтор номера и файл без номера: %v", got)
	}
	if !strings.Contains(got[0], "00002_second.sql и 00002_twin.sql") || !strings.Contains(got[1], "third.sql") {
		t.Fatalf("нарушения без имён файлов: %v", got)
	}
}
