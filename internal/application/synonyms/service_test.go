package synonyms_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	app "github.com/proshop-app/proshop-marketplace/internal/application/synonyms"
	"github.com/proshop-app/proshop-marketplace/internal/domain/synonyms"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

var testNow = time.Date(2026, 11, 21, 18, 0, 0, 0, time.UTC)

type environment struct {
	store     *fake.Store
	clock     *fake.Clock
	service   *app.Service
	publisher *fake.Publisher
	inputs    []ports.SynonymInput
	calls     int
	checked   int
	validate  func(ports.SynonymValidationResponse) (ports.SynonymValidationResponse, error)
}

type model struct {
	calls int
	fail  bool
}

func TestDailyCheckAndPublicationBudgets(t *testing.T) {
	env := setup(120)
	for i := 0; i < 6; i++ {
		run(t, env)
	}
	view := snapshot(t, env)
	if env.checked != 100 || env.calls != 5 || view.Revision != 1 || len(view.Rules) != 20 {
		t.Fatalf(
			"сутки: %d проверок, %d вызовов, ревизия %d, правил %d",
			env.checked,
			env.calls,
			view.Revision,
			len(view.Rules),
		)
	}

	env.clock.Advance(24 * time.Hour)
	result := run(t, env)
	view = snapshot(t, env)
	if result.DailyChecked != 20 || view.Revision != 2 || len(view.Rules) != 40 {
		t.Fatalf("следующие сутки: %+v, ревизия %d, правил %d", result, view.Revision, len(view.Rules))
	}
	if len(env.store.OutboxRows()) != 2 {
		t.Fatalf("событий: %d", len(env.store.OutboxRows()))
	}
}

func TestSynonymChecksFailClosedAndRetry(t *testing.T) {
	env := setup(1)
	env.validate = func(result ports.SynonymValidationResponse) (ports.SynonymValidationResponse, error) {
		result.Results[0].Complete = false
		result.Results[0].Decision = synonyms.Retry
		return result, nil
	}
	if result := run(t, env); result.Retry != 1 || result.Activated != 0 {
		t.Fatalf("неполная проверка: %+v", result)
	}
	run(t, env)
	if env.calls != 1 {
		t.Fatalf("повтор до срока: %d вызовов", env.calls)
	}
	env.clock.Advance(synonyms.RetryInterval)
	env.validate = func(result ports.SynonymValidationResponse) (ports.SynonymValidationResponse, error) {
		result.Results[0].Evidence.TaxonomyRevision = "16"
		return result, nil
	}
	if result := run(t, env); result.Retry != 1 || result.Activated != 0 {
		t.Fatalf("проверка старого каталога: %+v", result)
	}
	env.clock.Advance(synonyms.RetryInterval)
	env.validate = nil
	if result := run(t, env); result.Activated != 1 {
		t.Fatalf("после повтора: %+v", result)
	}
}

func TestMalformedBatchAndOutageKeepSnapshot(t *testing.T) {
	env := setup(2)
	ctx := context.Background()
	env.validate = func(result ports.SynonymValidationResponse) (ports.SynonymValidationResponse, error) {
		result.Results[1] = result.Results[0]
		return result, nil
	}
	if _, err := env.service.Run(ctx); err == nil || len(snapshot(t, env).Rules) != 0 {
		t.Fatalf("дубликат в ответе: %v", err)
	}
	env.clock.Advance(synonyms.RetryInterval)
	env.validate = nil
	run(t, env)
	before := snapshot(t, env)
	env.clock.Advance(synonyms.RecheckInterval)
	env.validate = func(result ports.SynonymValidationResponse) (ports.SynonymValidationResponse, error) {
		return result, errors.New("поиск недоступен")
	}
	if _, err := env.service.Run(ctx); err == nil {
		t.Fatal("сбой поиска должен быть виден воркеру")
	}
	if after := snapshot(t, env); before.Revision != after.Revision || len(after.Rules) != 2 {
		t.Fatalf("сбой изменил снимок: %+v", after)
	}
	env.clock.Advance(synonyms.RetryInterval)
	env.validate = func(result ports.SynonymValidationResponse) (ports.SynonymValidationResponse, error) {
		result.Results[0].RescuedScopes = 2
		return result, nil
	}
	if got := run(t, env); got.Reject != 1 || got.Active != 1 {
		t.Fatalf("проверка активной пары: %+v", got)
	}
}

func TestBrandConflictStopListAndPermanentDisable(t *testing.T) {
	env := setup(4)
	env.inputs[3].Category = env.inputs[2].Category
	env.inputs[3].CategoryName = env.inputs[2].CategoryName
	env.inputs[3].From = env.inputs[2].From
	env.store.SeedSynonymSources(env.inputs, "вариант-0")
	run(t, env)
	view := snapshot(t, env)
	if len(view.Rules) != 1 || view.Rules[0].From != "вариант-1" {
		t.Fatalf("бренд и неоднозначные пары: %+v", view)
	}

	ctx := context.Background()
	cmd := app.OperatorChange{ExpectedRevision: view.Revision, Actor: "operator", Reason: "омоним"}
	list, err := env.service.ReplaceStopList(ctx, []string{"Вариант", "вариант"}, cmd)
	view = snapshot(t, env)
	if err != nil || list.Revision != 2 || len(list.BlockedTerms) != 1 || len(view.Rules) != 0 {
		t.Fatalf("стоп-лист: %+v, %+v, %v", list, view, err)
	}
	if _, err := env.service.ReplaceStopList(ctx, []string{}, cmd); !errors.Is(err, synonyms.ErrConflict) {
		t.Fatalf("старая ревизия: %v", err)
	}
	cmd.ExpectedRevision = list.Revision
	if _, err := env.service.ReplaceStopList(ctx, []string{}, cmd); err != nil {
		t.Fatal(err)
	}
	env.clock.Advance(24 * time.Hour)
	run(t, env)
	if got := snapshot(t, env); len(got.Rules) != 0 || got.Revision != 3 {
		t.Fatalf("после снятия стоп-листа: %+v", got)
	}
}

func TestOperatorDisableWhileValidationIsRunning(t *testing.T) {
	env := setup(1)
	env.validate = func(result ports.SynonymValidationResponse) (ports.SynonymValidationResponse, error) {
		cmd := app.OperatorChange{Actor: "operator", Reason: "проверка", ExpectedRevision: 0}
		_, err := env.service.Disable(context.Background(), result.Results[0].ID, cmd)
		return result, err
	}
	run(t, env)
	page, err := env.service.List(context.Background(), "", 20)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Phase != synonyms.Disabled ||
		len(snapshot(t, env).Rules) != 0 {
		t.Fatalf("устаревшая проверка включила правило: %+v, %v", page, err)
	}
}

func TestSourceRevocationAndExpiry(t *testing.T) {
	env := setup(2)
	env.validate = func(result ports.SynonymValidationResponse) (ports.SynonymValidationResponse, error) {
		result.Results[0].Decision = synonyms.Reject
		return result, nil
	}
	run(t, env)
	env.clock.Advance(synonyms.CandidateTTL)
	env.store.SeedSynonymSources(env.inputs, "вариант")
	if result := run(t, env); result.Active != 0 || result.Queued != 0 {
		t.Fatalf("удаление поддержки или истечение TTL: %+v", result)
	}
	if view := snapshot(t, env); view.Revision != 2 || len(view.Rules) != 0 {
		t.Fatalf("устаревший снимок: %+v", view)
	}
}

func TestModelOnlyQuarantinesAndCachesInput(t *testing.T) {
	env := setup(1)
	provider := &model{}
	env.service.WithModel(provider)
	run(t, env)
	run(t, env)
	page, err := env.service.List(context.Background(), "", 20)
	if err != nil || provider.calls != 1 || env.checked != 1 || len(page.Entries) != 2 {
		t.Fatalf("модель: %d вызов, %d проверок, %+v, %v", provider.calls, env.checked, page, err)
	}
	for _, item := range page.Entries {
		if item.Source.Kind == synonyms.Model && (item.Phase != synonyms.Quarantined || item.LastCheck != nil) {
			t.Fatalf("модельное предложение допущено: %+v", item)
		}
	}
}

func TestModelRetriesRespectDailyBudget(t *testing.T) {
	env := setup(1)
	provider := &model{fail: true}
	env.service.WithModel(provider)
	for i := 0; i < 6; i++ {
		_, _ = env.service.Run(context.Background())
	}
	if provider.calls != 4 || len(snapshot(t, env).Rules) != 1 {
		t.Fatalf("отказ модели: %d попытки", provider.calls)
	}
	env.clock.Advance(24 * time.Hour)
	_, _ = env.service.Run(context.Background())
	if provider.calls != 5 {
		t.Fatalf("новый бюджет не открылся: %d попыток", provider.calls)
	}
}

func (m *model) Identity() string { return "fixture" }

func (m *model) Propose(
	_ context.Context,
	_ string,
	concepts []ports.SynonymModelConcept,
) ([]ports.SynonymModelProposal, error) {
	m.calls++
	if m.fail {
		return nil, errors.New("модель недоступна")
	}
	return []ports.SynonymModelProposal{{
		Category: concepts[0].ID,
		From:     "Эквивалент модели",
		To:       concepts[0].Name,
		Relation: "equivalent",
	}}, nil
}

func setup(count int) *environment {
	env := &environment{store: fake.NewStore(), clock: fake.NewClock(testNow), publisher: &fake.Publisher{}}
	var definition taxonomy.Definition
	definition.Version = 17
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("section-%03d", i)
		name := fmt.Sprintf("Раздел %d", i)
		definition.Categories = append(definition.Categories, taxonomy.Category{
			ID:         id,
			Name:       name,
			Definition: "Товар одного вида.",
		})
		env.inputs = append(env.inputs, ports.SynonymInput{
			Category:     id,
			CategoryName: name,
			From:         fmt.Sprintf("Вариант-%d", i),
			To:           name,
			Source:       synonyms.Source{Kind: synonyms.TaxonomyName, ID: id},
		})
	}
	env.store.SeedTaxonomy(definition)
	env.store.SeedSynonymSources(env.inputs)
	adapter := fake.SynonymValidator{ValidateFunc: env.validation}
	env.service = app.New(env.store, env.clock, adapter, env.publisher, app.EventTopic)
	return env
}

func (env *environment) validation(
	_ context.Context,
	request ports.SynonymValidationRequest,
) (ports.SynonymValidationResponse, error) {
	env.calls++
	env.checked += len(request.Rules)
	response := ports.SynonymValidationResponse{BatchID: request.BatchID, ValidatorVersion: 1}
	for _, direction := range request.Rules {
		response.Results = append(response.Results, synonyms.Validation{
			ID:                  direction.ID,
			Complete:            true,
			ScopesChecked:       5,
			SourceLiteralScopes: 1,
			TargetLiteralScopes: 5,
			RescuedScopes:       3,
			Decision:            synonyms.Pass,
			Evidence: synonyms.Evidence{
				Fingerprint:      strings.Repeat("a", 64),
				TaxonomyRevision: "17",
				CheckedAt:        env.clock.Now(),
			},
		})
	}
	if env.validate != nil {
		return env.validate(response)
	}
	return response, nil
}

func run(t *testing.T, env *environment) app.RunResult {
	t.Helper()
	result, err := env.service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func snapshot(t *testing.T, env *environment) synonyms.Snapshot {
	t.Helper()
	_, body, err := env.service.Snapshot(context.Background(), -1)
	if err != nil {
		t.Fatal(err)
	}
	var view synonyms.Snapshot
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestRepeatedCatalogRejectWaitsForSourceChange(t *testing.T) {
	env := setup(1)
	env.validate = func(result ports.SynonymValidationResponse) (ports.SynonymValidationResponse, error) {
		result.Results[0].Decision = synonyms.Reject
		return result, nil
	}
	run(t, env)
	env.clock.Advance(synonyms.RetryInterval)
	run(t, env)
	env.clock.Advance(synonyms.RetryInterval)
	run(t, env)
	if env.calls != 2 {
		t.Fatalf("повторно отклонённая пара снова проверена: %d вызовов", env.calls)
	}

	env.inputs[0].Source.Revision = "2"
	env.store.SeedSynonymSources(env.inputs)
	env.validate = nil
	if got := run(t, env); got.Activated != 1 {
		t.Fatalf("новая версия источника: %+v", got)
	}
}
