package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Учётный драйвер исполняет настоящий SQL репозиториев без PostgreSQL.
// Он фиксирует запросы и объём значений (без протокола/TOAST), но не моделирует блокировки PostgreSQL.
type snapshotDatabase struct {
	mu      sync.Mutex
	state   snapshotSQLState
	queries map[string]int
	bytes   int
	fail    string
}

type snapshotSQLState struct {
	version  int64
	raw      []byte
	revision int64
	phrases  [][]driver.Value
}

type snapshotConnector struct {
	database *snapshotDatabase
}

type snapshotDriver struct{}

type snapshotConnection struct {
	database *snapshotDatabase
	state    snapshotSQLState
	dirty    bool
}

type snapshotRows struct {
	columns []string
	values  [][]driver.Value
}

func snapshotORM(t testing.TB, raw []byte) (*gorm.DB, *snapshotDatabase) {
	t.Helper()

	database := &snapshotDatabase{
		state: snapshotSQLState{
			version:  1,
			raw:      raw,
			revision: 1,
		},
		queries: map[string]int{},
	}
	pool := sql.OpenDB(snapshotConnector{database: database})
	t.Cleanup(func() { _ = pool.Close() })
	orm, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: pool}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}

	return orm, database
}

func (c snapshotConnector) Connect(context.Context) (driver.Conn, error) {
	return &snapshotConnection{database: c.database}, nil
}

func (snapshotConnector) Driver() driver.Driver { return snapshotDriver{} }

func (snapshotDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("нужен connector")
}

func (*snapshotConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("нужен QueryContext")
}

func (*snapshotConnection) Close() error { return nil }

func (c *snapshotConnection) Begin() (driver.Tx, error) {
	c.database.mu.Lock()
	defer c.database.mu.Unlock()

	c.state, c.dirty = c.database.state, false
	return c, nil
}

func (c *snapshotConnection) Commit() error {
	c.database.mu.Lock()
	defer c.database.mu.Unlock()

	if c.database.fail == "commit" {
		return errors.New("ошибка COMMIT")
	}
	if c.dirty {
		c.database.state = c.state
	}
	return nil
}

func (*snapshotConnection) Rollback() error { return nil }

func (c *snapshotConnection) QueryContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Rows, error) {
	c.database.mu.Lock()
	defer c.database.mu.Unlock()

	c.database.queries[query]++
	if c.database.fail == query {
		return nil, errors.New("ошибка чтения")
	}
	rows := &snapshotRows{columns: []string{"value"}}
	switch query {
	case readTaxonomyVersion, lockTaxonomy:
		rows.values = [][]driver.Value{{c.state.version}}
		c.database.bytes += 8
	case selectTaxonomy:
		rows.values = [][]driver.Value{{c.state.raw}}
		c.database.bytes += len(c.state.raw)
	case readModerationRules, lockModerationRules:
		rows.values = [][]driver.Value{{c.state.revision}}
		c.database.bytes += 8
	case listModerationPhrases:
		rows.columns = []string{"key", "text", "operator_id", "operator_name", "created_at"}
		rows.values = c.state.phrases
		for _, phrase := range c.state.phrases {
			for _, value := range phrase {
				if text, ok := value.(string); ok {
					c.database.bytes += len(text)
				}
			}
			c.database.bytes += 8
		}
	default:
		return nil, fmt.Errorf("неожиданный SQL: %s", query)
	}
	return rows, nil
}

func (c *snapshotConnection) ExecContext(
	_ context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Result, error) {
	c.database.mu.Lock()
	defer c.database.mu.Unlock()

	c.database.queries[query]++
	c.dirty = true
	switch query {
	case updateTaxonomy:
		if c.state.version != args[2].Value.(int64) {
			return driver.RowsAffected(0), nil
		}
		c.state.version = args[0].Value.(int64)
		c.state.raw = []byte(args[1].Value.(string))
	case insertTaxonomy:
	case bumpModerationRules:
		c.state.revision++
	case insertModerationPhrase:
		for _, phrase := range c.state.phrases {
			if phrase[0] == args[0].Value {
				return driver.RowsAffected(0), nil
			}
		}
		phrase := make([]driver.Value, len(args))
		for i, arg := range args {
			phrase[i] = arg.Value
		}
		c.state.phrases = append(append([][]driver.Value(nil), c.state.phrases...), phrase)
	case deleteModerationPhrase:
		phrases := [][]driver.Value{}
		for _, phrase := range c.state.phrases {
			if phrase[0] != args[0].Value {
				phrases = append(phrases, phrase)
			}
		}
		if len(phrases) == len(c.state.phrases) {
			return driver.RowsAffected(0), nil
		}
		c.state.phrases = phrases
	default:
		return nil, fmt.Errorf("неожиданный SQL: %s", query)
	}
	return driver.RowsAffected(1), nil
}

func (r *snapshotRows) Columns() []string { return r.columns }

func (*snapshotRows) Close() error { return nil }

func (r *snapshotRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}

func (d *snapshotDatabase) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.queries, d.bytes = map[string]int{}, 0
}

func (d *snapshotDatabase) count(query string) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.queries[query]
}

func (d *snapshotDatabase) readVolume() (int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	reads := 0
	for query, count := range d.queries {
		if strings.HasPrefix(query, "SELECT") {
			reads += count
		}
	}
	return reads, d.bytes
}

func snapshotTransaction(orm *gorm.DB, snapshots *Snapshots, run func(*scope) error) error {
	tx := orm.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	s := newScope(tx, snapshots)
	defer func() { _ = tx.Rollback().Error }()
	if err := run(s); err != nil {
		return err
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}
	for _, observe := range s.afterCommit {
		observe()
	}
	return nil
}
