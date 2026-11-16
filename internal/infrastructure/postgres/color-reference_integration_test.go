//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	appreference "github.com/proshop-app/proshop-marketplace/internal/application/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

const (
	countColorChanges   = `SELECT count(*) FROM reference.changes WHERE dictionary_code = 'color'`
	selectColorMetadata = `
        SELECT metadata
        FROM reference.entries
        WHERE dictionary_code = 'color' AND id = $1`
)

func TestColorMetadataPersistsWithJournal(t *testing.T) {
	service, pool, _ := referenceService(t)
	ctx := context.Background()
	err := postgres.NewUnitOfWork(pool).Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		_, err := tx.Reference().CreateDictionary(ctx, reference.DictionaryState{
			Code:       "color",
			Kind:       reference.KindClosed,
			AliasKinds: reference.AliasKinds,
			Limits:     reference.DefaultLimits(),
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Create(ctx, appreference.CreateCommand{
		Dictionary: "color",
		Name:       "Хаки",
		Status:     reference.StatusCandidate,
		Meta:       referenceMeta,
	})
	if err != nil {
		t.Fatal(err)
	}
	confirmed := reference.StatusConfirmed
	command := appreference.UpdateCommand{
		Dictionary: "color",
		ID:         result.Entry.ID,
		IfMatch:    result.Entry.Revision,
		Status:     &confirmed,
		Meta:       referenceMeta,
	}
	if _, err := service.Update(ctx, command); err == nil {
		t.Fatal("подтверждение без привязки прошло")
	}
	command.Metadata = json.RawMessage(`{"baseColors":["Зелёный"]}`)
	if _, err := service.Update(ctx, command); err != nil {
		t.Fatal(err)
	}
	var metadata json.RawMessage
	if err := pool.QueryRow(ctx, selectColorMetadata, result.Entry.ID).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	var fields map[string][]string
	if err := json.Unmarshal(metadata, &fields); err != nil || len(fields["baseColors"]) != 1 {
		t.Fatalf("привязка не сохранена: %s %v", metadata, err)
	}
	var changes int
	if err := pool.QueryRow(ctx, countColorChanges).Scan(&changes); err != nil || changes != 2 {
		t.Fatalf("изменения журнала не атомарны: %d %v", changes, err)
	}
}
