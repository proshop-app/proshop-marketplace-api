package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func snapshotDefinition(version int64) taxonomy.Definition {
	return taxonomy.Definition{
		Version: version,
		Attributes: []taxonomy.Attribute{
			{
				Code:               "a1",
				Name:               "Атрибут",
				Type:               "enum",
				Dictionary:         "d1",
				Values:             []string{"v1"},
				Facet:              true,
				FacetOptions:       []string{"f1"},
				FacetValuesByValue: map[string][]string{"v1": {"f1"}},
				SearchTermsByValue: map[string][]string{"v1": {"term"}},
				ValuesMeta:         map[string]taxonomy.ValueMeta{"v1": {Hex: "#112233"}},
			},
		},
		Schemas: map[string]taxonomy.Schema{
			"s1": {
				Attributes: []string{"a1"},
				Identity: &taxonomy.SchemaIdentity{
					Model: []string{"a1"},
				},
			},
		},
		Categories: []taxonomy.Category{
			{
				ID:   "root",
				Name: "Раздел",
				Slug: "root",
				Children: []taxonomy.Category{
					{
						ID:     "c1",
						Name:   "Категория",
						Slug:   "c1",
						Schema: "s1",
					},
				},
			},
		},
	}
}

func snapshotJSON(t testing.TB, definition taxonomy.Definition) []byte {
	t.Helper()
	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func readSnapshots(s *scope) error {
	ctx := context.Background()
	if _, err := s.Taxonomy().Current(ctx); err != nil {
		return err
	}
	_, err := s.ModerationRules().Current(ctx)
	return err
}

func TestSnapshotsShareCommittedValuesAndAlwaysLock(t *testing.T) {
	orm, database := snapshotORM(t, snapshotJSON(t, snapshotDefinition(1)))
	snapshots := &snapshots{}
	var first taxonomy.Definition
	ctx := context.Background()
	err := snapshotTransaction(orm, snapshots, func(s *scope) error {
		var err error
		first, err = s.Taxonomy().Current(ctx)
		if err != nil {
			return err
		}
		for i := 0; i < 3; i++ {
			if err := readSnapshots(s); err != nil {
				return err
			}
		}
		if _, ok := snapshots.taxonomy.get(1); ok {
			t.Fatal("снимок опубликован до COMMIT")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		err := snapshotTransaction(orm, snapshots, func(s *scope) error {
			current, err := s.Taxonomy().Current(ctx)
			if err != nil {
				return err
			}
			if &first.Attributes[0] != &current.Attributes[0] {
				t.Fatal("тёплое чтение копирует снимок")
			}
			_, err = s.ModerationRules().Current(ctx)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if database.count(readTaxonomyVersion) != 7 || database.count(readModerationRules) != 6 {
		t.Fatal("чтение пропустило запрос версии с FOR SHARE", database.queries)
	}
	if database.count(selectTaxonomy) != 1 || database.count(listModerationPhrases) != 1 {
		t.Fatal("повторная загрузка неизменного снимка", database.queries)
	}
}

func TestSnapshotsRollbackDoesNotPoisonReusedVersion(t *testing.T) {
	for _, failure := range []string{"rollback", "panic", "commit"} {
		t.Run(failure, func(t *testing.T) {
			orm, database := snapshotORM(t, snapshotJSON(t, snapshotDefinition(1)))
			snapshots := &snapshots{}
			if err := snapshotTransaction(orm, snapshots, readSnapshots); err != nil {
				t.Fatal(err)
			}
			if failure == "commit" {
				database.fail = failure
			}
			failSnapshotWrite(t, orm, snapshots, failure)
			database.fail = ""
			if value, ok := snapshots.taxonomy.get(1); !ok || value.Version != 1 {
				t.Fatal("откат заменил зафиксированную таксономию")
			}
			if value, ok := snapshots.moderation.get(1); !ok || len(value.Phrases) != 0 {
				t.Fatal("откат заменил зафиксированные правила")
			}
			// Та же версия после отката теперь содержит другие данные.
			err := snapshotTransaction(orm, snapshots, func(s *scope) error {
				next := snapshotDefinition(2)
				next.Categories[0].Name = "Зафиксировано"
				return s.Taxonomy().Save(context.Background(), next, 1)
			})
			if err != nil {
				t.Fatal(err)
			}
			err = snapshotTransaction(orm, snapshots, func(s *scope) error {
				d, err := s.Taxonomy().Current(context.Background())
				if err != nil {
					return err
				}
				if d.Version != 2 || d.Categories[0].Name != "Зафиксировано" {
					t.Fatal("прочитана отменённая версия", d)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func failSnapshotWrite(t *testing.T, orm *gorm.DB, snapshots *snapshots, failure string) {
	t.Helper()
	abort := errors.New("откат")
	defer func() {
		value := recover()
		recovered, isError := value.(error)
		if failure == "panic" && (!isError || !errors.Is(recovered, abort)) {
			t.Fatalf("ожидалась паника отката, получено %v", value)
		}
		if value != nil && failure != "panic" {
			panic(value)
		}
	}()
	err := snapshotTransaction(orm, snapshots, func(s *scope) error {
		ctx := context.Background()
		next := snapshotDefinition(2)
		next.Categories[0].Name = "Отменено"
		if err := s.Taxonomy().Save(ctx, next, 1); err != nil {
			return err
		}
		if err := s.ModerationRules().AddPhrase(ctx, snapshotPhrase()); err != nil {
			return err
		}
		if err := readSnapshots(s); err != nil {
			return err
		}
		if failure == "panic" {
			panic(abort)
		}
		if failure == "rollback" {
			return abort
		}
		return nil
	})
	if err == nil {
		t.Fatal("ожидался отказ транзакции")
	}
}

func snapshotPhrase() moderation.StopPhrase {
	return moderation.StopPhrase{
		Key:       "phrase",
		Text:      "phrase",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestModerationSnapshotsTrackEveryPhraseChange(t *testing.T) {
	orm, database := snapshotORM(t, snapshotJSON(t, snapshotDefinition(1)))
	snapshots := &snapshots{}
	ctx := context.Background()
	if err := snapshotTransaction(orm, snapshots, readSnapshots); err != nil {
		t.Fatal(err)
	}
	operations := []func(*scope) error{
		func(s *scope) error { return s.ModerationRules().AddPhrase(ctx, snapshotPhrase()) },
		func(s *scope) error {
			err := s.ModerationRules().AddPhrase(ctx, snapshotPhrase())
			if !errors.Is(err, moderation.ErrPhraseExists) {
				return fmt.Errorf("повтор фразы: %w", err)
			}
			return nil
		},
		func(s *scope) error {
			_, err := s.ModerationRules().RemovePhrase(ctx, "missing")
			return err
		},
		func(s *scope) error {
			_, err := s.ModerationRules().RemovePhrase(ctx, "phrase")
			return err
		},
	}
	for index, operation := range operations {
		if err := snapshotTransaction(orm, snapshots, operation); err != nil {
			t.Fatal(err)
		}
		err := snapshotTransaction(orm, snapshots, func(s *scope) error {
			rules, err := s.ModerationRules().Current(ctx)
			if err != nil {
				return err
			}
			wantRevision, wantPhrases := int64(2), 1
			if index == 3 {
				wantRevision, wantPhrases = 3, 0
			}
			if rules.Revision != wantRevision || len(rules.Phrases) != wantPhrases {
				t.Fatalf("операция %d: %+v", index, rules)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if database.count(bumpModerationRules) != 2 || database.count(listModerationPhrases) != 3 {
		t.Fatal("фактические изменения и загрузки не совпали", database.queries)
	}
}

func TestSnapshotsDoNotCacheErrors(t *testing.T) {
	for _, query := range []string{readTaxonomyVersion, selectTaxonomy, readModerationRules, listModerationPhrases} {
		t.Run(query, func(t *testing.T) {
			orm, database := snapshotORM(t, snapshotJSON(t, snapshotDefinition(1)))
			snapshots := &snapshots{}
			database.fail = query
			if err := snapshotTransaction(orm, snapshots, readSnapshots); err == nil {
				t.Fatal("ошибка базы потеряна")
			}
			database.fail = ""
			if err := snapshotTransaction(orm, snapshots, readSnapshots); err != nil {
				t.Fatal(err)
			}
			// Ошибка запроса версии не должна отдавать даже прогретый снимок.
			if query == readTaxonomyVersion || query == readModerationRules {
				database.fail = query
				if err := snapshotTransaction(orm, snapshots, readSnapshots); err == nil {
					t.Fatal("кэш обошёл ошибку блокирующего запроса")
				}
			}
		})
	}
}

func TestSnapshotsConcurrentReadersAndVersionChanges(t *testing.T) {
	orm, _ := snapshotORM(t, snapshotJSON(t, snapshotDefinition(1)))
	snapshots := &snapshots{}
	ctx := context.Background()
	start := make(chan struct{})
	errors := make(chan error, 9)
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for iteration := 0; iteration < 30; iteration++ {
				err := snapshotTransaction(orm, snapshots, func(s *scope) error {
					d, err := s.Taxonomy().Current(ctx)
					if err != nil {
						return err
					}
					rules, err := s.ModerationRules().Current(ctx)
					if err != nil {
						return err
					}
					return exerciseSnapshot(d, rules)
				})
				if err != nil {
					errors <- err
					return
				}
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		for version := int64(2); version <= 15; version++ {
			err := snapshotTransaction(orm, snapshots, func(s *scope) error {
				if err := s.Taxonomy().Lock(ctx); err != nil {
					return err
				}
				if err := s.Taxonomy().Save(ctx, snapshotDefinition(version), version-1); err != nil {
					return err
				}
				if version%2 == 0 {
					return s.ModerationRules().AddPhrase(ctx, snapshotPhrase())
				}
				_, err := s.ModerationRules().RemovePhrase(ctx, "phrase")
				return err
			})
			if err != nil {
				errors <- err
				return
			}
		}
	}()
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if err := snapshotTransaction(orm, snapshots, readSnapshots); err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshots.taxonomy.get(15); !ok {
		t.Fatal("кэш не догнал последнюю версию")
	}
}

func exerciseSnapshot(d taxonomy.Definition, rules moderation.Rules) error {
	before, _ := json.Marshal(d)
	category, _, _ := d.Category("c1")
	card := &taxonomy.Card{
		CategoryID: "c1",
		Published:  true,
		Attributes: map[string]json.RawMessage{"a1": json.RawMessage(`"v1"`)},
	}
	publication := d.Check(card)
	publication.AttributeSearchTerms["a1"][0] = "local"
	publication.AttributeFacetValues["a1"][0] = "local"
	product, err := catalog.NewProduct(snapshotProductEvent(1).Snapshot, time.Time{})
	if err != nil {
		return err
	}
	product.Revalidate(d, time.Time{})
	appcatalog.NewModelKeys(nil, nil, true).Apply(context.Background(), product, d)
	d.Fields()
	d.DictionarySources()
	d.CandidateScopes("d1", reference.KindOpen)
	d.ModerationPolicy(category.ID)
	rules.RequireImage = true
	rules.Evaluate(moderation.Input{Name: "Товар"})
	if _, err := yaml.Marshal(d); err != nil {
		return err
	}
	after, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if string(before) != string(after) {
		return errors.New("читатель изменил общий снимок")
	}
	return nil
}
