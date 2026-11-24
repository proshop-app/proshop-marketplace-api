package postgres

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
)

func TestProjectionValidationReadsMetadataWithoutSnapshots(t *testing.T) {
	orm, database := snapshotORM(t, snapshotJSON(t, snapshotDefinition(2)))
	verdict := moderation.VerdictState{
		TenantID:        "t1",
		ProductID:       "p1",
		Revision:        4,
		RulesRevision:   3,
		TaxonomyVersion: 2,
		DecidedBy:       moderation.Rule,
		Status:          moderation.Allowed,
		Version:         5,
	}
	raw, err := json.Marshal(verdict)
	if err != nil {
		t.Fatal(err)
	}
	database.state.validation = []driver.Value{"t1", "p1", int64(2), int64(3), true, false, raw}
	err = snapshotTransaction(orm, &snapshots{}, func(s *scope) error {
		sources, err := s.ProjectionValidation().Current(context.Background(), "t1", "p1")
		if err != nil {
			return err
		}
		if sources.TaxonomyVersion != 2 || sources.RulesRevision != 3 || !sources.Participant ||
			sources.Probation || sources.Verdict == nil || sources.Verdict.State().Revision != 4 ||
			sources.Verdict.Origin().Version != 5 {
			t.Fatal("потеряны версии или CAS прежнего вердикта", sources)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(database.queries) != 1 || database.count(readValidationSources) != 1 {
		t.Fatal("проверка метаданных загрузила лишние данные", database.queries)
	}
}

func TestProjectionValidationReturnsMissingVerdictAndErrors(t *testing.T) {
	orm, database := snapshotORM(t, snapshotJSON(t, snapshotDefinition(1)))
	database.state.validation = []driver.Value{"t1", "p1", int64(1), int64(1), false, false, nil}
	if err := snapshotTransaction(orm, &snapshots{}, func(s *scope) error {
		sources, err := s.ProjectionValidation().Current(context.Background(), "t1", "p1")
		if err != nil {
			return err
		}
		if sources.Verdict != nil || sources.Participant {
			t.Fatal("отсутствующие данные стали подтверждённой проверкой")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	database.fail = readValidationSources
	err := snapshotTransaction(orm, &snapshots{}, func(s *scope) error {
		_, err := s.ProjectionValidation().Current(context.Background(), "t1", "p1")
		return err
	})
	if err == nil {
		t.Fatal("ошибка проверки текущих источников потеряна")
	}
}
