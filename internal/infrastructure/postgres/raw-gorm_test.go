package postgres

import (
	"context"
	"reflect"
	"testing"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	rawSQLNumbered = `SELECT $2::text, $1::text, $2::text, $10::integer,
		$3::integer, $4::integer, $5::integer, $6::integer, $7::integer, $8::integer, $9::integer`
	rawSQLJSONOperators = `SELECT $1::jsonb ? 'enabled', $1::jsonb ?| array['a', 'b'],
		$1::jsonb ?& array['a'], $1::jsonb @> $2::jsonb, $1::jsonb @? '$.a', $1::jsonb @@ '$.a == 1'`
	rawSQLQuoted = `SELECT '$1 ? @value', $$literal $2 ? @value$$, $tag$literal $1 ?$tag$, $1::text
		/* $2 ? @value */ -- $3 ? @value
		`
	rawSQLArrays     = `SELECT $1::text[], $2::text[], $3::text[], $4::text[], $5::bytea, $6::text, $7::jsonb`
	rawSQLBoundValue = `SELECT $1::text, $2::text`
)

type rawSQLCase struct {
	name  string
	query string
	args  []any
}

// Raw GORM интерпретирует ? и @, а срезы обычно разворачивает в список параметров.
// PostgreSQL-запросы репозиториев должны сохранить свой синтаксис и исходные значения.
func TestRawSQLPreservesPostgresParameters(t *testing.T) {
	db := rawSQLDryRun(t)
	label := "t1"
	var absent []string
	var nullText *string
	cases := []rawSQLCase{
		{
			name:  "numbered_and_repeated",
			query: rawSQLNumbered,
			args: []any{
				"first",
				"second",
				3,
				4,
				5,
				6,
				7,
				8,
				9,
				10,
			},
		},
		{
			name:  "json_operators",
			query: rawSQLJSONOperators,
			args: []any{
				`{"a":1,"enabled":true}`,
				`{"a":1}`,
			},
		},
		{
			name:  "quoted_literals_and_comments",
			query: rawSQLQuoted,
			args: []any{
				"bound value",
			},
		},
		{
			name:  "native_arrays_nulls_and_bytes",
			query: rawSQLArrays,
			args: []any{
				[]string{"t1", "a,b", "? @value $1"},
				[]string{},
				absent,
				[]*string{&label, nil},
				[]byte{0, 1, 2},
				nullText,
				nil,
			},
		},
		{
			name:  "sql_like_data_stays_bound",
			query: rawSQLBoundValue,
			args: []any{
				`'); DROP TABLE reference.entries; -- ? @value $2`,
				gorm.Expr("current_user"),
			},
		},
		{
			name:  "operators_without_parameters",
			query: `SELECT '{}'::jsonb ? 'enabled', '@value'`,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			result := rawSQL(ctx, db, test.query, test.args...)
			if result.Error != nil {
				t.Fatal(result.Error)
			}
			if got := result.Statement.SQL.String(); got != test.query {
				t.Fatalf("SQL изменился:\nполучено: %s\nожидалось: %s", got, test.query)
			}
			if got := len(result.Statement.Vars); got != len(test.args) {
				t.Fatalf("число параметров изменилось: получено %d, ожидалось %d", got, len(test.args))
			}
			for index, expected := range test.args {
				if got := result.Statement.Vars[index]; !reflect.DeepEqual(got, expected) {
					t.Fatalf("параметр %d изменился: получено %#v, ожидалось %#v", index+1, got, expected)
				}
			}
			if result.Statement.Context != ctx {
				t.Fatal("запрос потерял контекст вызывающего сценария")
			}
			if db.Statement.SQL.Len() != 0 || len(db.Statement.Vars) != 0 {
				t.Fatal("запрос изменил общий шаблон GORM")
			}
		})
	}
}

func rawSQLDryRun(t *testing.T) *gorm.DB {
	t.Helper()

	dialector := gormpostgres.New(gormpostgres.Config{
		DSN: "host=127.0.0.1 port=1 sslmode=disable",
	})
	db, err := gorm.Open(dialector, &gorm.Config{
		DryRun:                 true,
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	})

	return db
}
