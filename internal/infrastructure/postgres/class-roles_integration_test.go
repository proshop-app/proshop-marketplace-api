//go:build integration

package postgres_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	classRolesScript = "../../../deploy/postgres/class-roles.sql"
	// classRoleNames — роли скрипта; общие на кластер PostgreSQL, не на базу теста.
	classRoleNames = `SELECT rolname FROM pg_roles WHERE rolname = ANY($1)`
	// missingSchemaUsage — схемы приложения, куда группа ролей классов не может войти.
	missingSchemaUsage = `SELECT nspname FROM pg_namespace
        WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema'
          AND NOT has_schema_privilege('marketplace_app', oid, 'USAGE')`
	// missingTableRights — таблицы приложения без полного набора прав на данные.
	missingTableRights = `SELECT n.nspname || '.' || c.relname FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE c.relkind IN ('r', 'p') AND n.nspname NOT LIKE 'pg\_%' AND n.nspname <> 'information_schema'
          AND NOT (has_table_privilege('marketplace_app', c.oid, 'SELECT')
               AND has_table_privilege('marketplace_app', c.oid, 'INSERT')
               AND has_table_privilege('marketplace_app', c.oid, 'UPDATE')
               AND has_table_privilege('marketplace_app', c.oid, 'DELETE'))`
	// missingSequenceRights — последовательности приложения, недоступные вставке.
	missingSequenceRights = `SELECT n.nspname || '.' || c.relname FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE c.relkind = 'S' AND n.nspname NOT LIKE 'pg\_%' AND n.nspname <> 'information_schema'
          AND NOT CASE WHEN c.relkind = 'S' THEN has_sequence_privilege('marketplace_app', c.oid, 'USAGE') END`
	// laterSchema — то, что создаст следующая миграция владельца: схема, таблица, последовательность.
	laterSchema = `CREATE SCHEMA later_schema;
        CREATE TABLE later_schema.items (id bigserial PRIMARY KEY);
        CREATE TABLE public.later_items (id bigserial PRIMARY KEY)`
)

var classRoleList = []string{
	"marketplace_app",
	"marketplace_read",
	"marketplace_checkout",
	"marketplace_ingestion",
	"marketplace_background",
	"marketplace_orders",
}

// Роли классов за пулером получают права на данные во ВСЕХ схемах приложения (public, reference)
// и на то, что миграции создадут позже. Скрипт развёртывания выдавал права только на public:
// API за пулером получал «permission denied for schema reference» на каждом чтении реестра.
func TestClassRolesReachEverySchema(t *testing.T) {
	pool := testPool(t)
	script, err := os.ReadFile(classRolesScript)
	if err != nil {
		t.Fatal(err)
	}
	dropClassRolesAfter(t, pool)
	// Скрипт повторяемый: второй запуск — то же состояние, без ошибок.
	for range 2 {
		runScript(t, pool, string(script))
	}
	runScript(t, pool, laterSchema)
	for name, query := range map[string]string{
		"вход в схему":       missingSchemaUsage,
		"права на таблицы":   missingTableRights,
		"последовательности": missingSequenceRights,
	} {
		if missing := textRows(t, pool, query); len(missing) > 0 {
			t.Errorf("%s: без прав %v", name, missing)
		}
	}
}

// runScript выполняет несколько операторов простым протоколом, как psql.
func runScript(t *testing.T, pool *pgxpool.Pool, script string) {
	t.Helper()
	conn, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Conn().PgConn().Exec(context.Background(), script).ReadAll(); err != nil {
		t.Fatal(err)
	}
}

func textRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return values
}

// dropClassRolesAfter убирает после теста роли, которых до него не было: роли общие на кластер,
// поэтому заведённые развёртыванием (compose с пулером) не трогаются.
func dropClassRolesAfter(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	existed := textRows(t, pool, classRoleNames, classRoleList)
	if len(existed) > 0 {
		return
	}
	t.Cleanup(func() {
		cleanup := "DROP OWNED BY marketplace_app"
		for _, role := range classRoleList[1:] {
			cleanup += "; DROP ROLE " + role
		}
		runScript(t, pool, cleanup+"; DROP ROLE marketplace_app")
	})
}
