package moderation_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	appcatalog "github.com/proshop-app/proshop-marketplace/internal/application/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	appmoderation "github.com/proshop-app/proshop-marketplace/internal/application/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/application/moderationstatus"
	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	apptaxonomy "github.com/proshop-app/proshop-marketplace/internal/application/taxonomy"
	"github.com/proshop-app/proshop-marketplace/internal/domain/catalog"
	"github.com/proshop-app/proshop-marketplace/internal/domain/moderation"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestReviewedProjectionReplayPreservesHistoricalApproval(t *testing.T) {
	e := approvedProjectionEnvironment(t, true, "image-1")
	before := e.view(t, "p1")
	closedBefore, err := e.service.GetCase(context.Background(), before.History[0].CaseID)
	if err != nil {
		t.Fatal(err)
	}
	memberBefore, _ := e.store.Participant("t1")
	// Таксономия обновляет только производные поисковые выражения, не исходный товар.
	definition := reviewedProjectionTaxonomy(2)
	definition.Attributes[0].SearchTermsByValue["true"] = []string{"новое выражение"}
	taxonomies := apptaxonomy.NewService(e.store, e.clock, e.service)
	if _, err := taxonomies.LoadPart(context.Background(), "attributes", definition, 1); err != nil {
		t.Fatal(err)
	}
	err = e.store.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		product, err := tx.Products().Get(ctx, "t1", "p1")
		if err != nil {
			return err
		}
		product.SetModelKey("derived-key", "derived-source")
		return tx.Products().Save(ctx, product)
	})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Hour)
	next := reviewedProjectionSnapshot(e, 2)
	applyReviewedProjection(t, e, next)
	after := e.view(t, "p1")
	memberAfter, _ := e.store.Participant("t1")
	if after.Verdict.Status != moderation.Allowed || after.Verdict.DecidedBy != moderation.Operator ||
		after.Verdict.Operator != before.Verdict.Operator ||
		!reflect.DeepEqual(after.Verdict.Decision, before.Verdict.Decision) ||
		after.Verdict.Revision != 2 || after.Verdict.TaxonomyVersion != 2 ||
		!after.Verdict.UpdatedAt.Equal(e.clock.Now()) || after.Verdict.Version != before.Verdict.Version+1 ||
		after.Case != nil || !e.document(t).Visible {
		t.Fatalf("одинаковая карточка потеряла исторический допуск: %+v", after)
	}
	if !reflect.DeepEqual(after.History, before.History) || memberAfter.ApprovedOffers != memberBefore.ApprovedOffers ||
		memberAfter.Version != memberBefore.Version || len(after.Verdicts) != len(before.Verdicts)+1 {
		t.Fatal("повторная доставка создала решение или повторно засчитала одобрение")
	}
	closedAfter, err := e.service.GetCase(context.Background(), before.History[0].CaseID)
	if err != nil || !reflect.DeepEqual(closedBefore, closedAfter) {
		t.Fatal("закрытое дело исходного решения изменилось", err)
	}
	product, _ := e.store.Product("t1", "p1")
	if product.Card.Publication.AttributeSearchTerms["a1"][0] != "новое выражение" {
		t.Fatal("новая поисковая проекция не сохранена")
	}
	err = e.projector.ApplyProduct(context.Background(), appcatalog.ProductEvent{
		EventID:  "duplicate-transport-delivery",
		Snapshot: next,
	})
	if err != nil || !reflect.DeepEqual(e.view(t, "p1"), after) {
		t.Fatal("дубликат ревизии изменил результат переноса", err)
	}
	e.clock.Advance(time.Hour)
	applyReviewedProjection(t, e, reviewedProjectionSnapshot(e, 3))
	repeated := e.view(t, "p1")
	memberRepeated, _ := e.store.Participant("t1")
	if repeated.Verdict.Status != moderation.Allowed || repeated.Verdict.Revision != 3 || repeated.Case != nil ||
		repeated.Verdict.Operator != before.Verdict.Operator ||
		!reflect.DeepEqual(repeated.Verdict.Decision, before.Verdict.Decision) ||
		!reflect.DeepEqual(repeated.History, before.History) || repeated.History[0].Revision != 1 ||
		repeated.Verdict.Version != after.Verdict.Version+1 || !repeated.Verdict.UpdatedAt.Equal(e.clock.Now()) ||
		memberRepeated.ApprovedOffers != memberBefore.ApprovedOffers ||
		len(repeated.Verdicts) != len(after.Verdicts)+1 {
		t.Fatal("вторая техническая ревизия потеряла исходное решение или засчитала его повторно", repeated)
	}
}

func TestReviewedProjectionStatusKeepsHistoricalApproval(t *testing.T) {
	e := approvedProjectionEnvironment(t, true, "image-1")
	before := e.view(t, "p1")
	e.clock.Advance(time.Hour)
	applyReviewedProjection(t, e, reviewedProjectionSnapshot(e, 2))
	publisher := &fake.Publisher{}
	statuses := moderationstatus.NewService(e.store, e.clock, publisher, "offer", "participant")
	if err := statuses.HandleTask(context.Background(), statusTask(t, e, moderationstatus.TaskOffer)); err != nil {
		t.Fatal(err)
	}
	var event moderationstatus.OfferEvent
	if err := json.Unmarshal(publisher.Messages()[0].Value, &event); err != nil || event.Revision != 2 ||
		event.Status != "published" || !event.Visible || event.Decision == nil ||
		event.Decision.At != before.Verdict.Decision.At.Format(time.RFC3339) {
		t.Fatal("магазину отправлено неверное основание допуска", event, err)
	}
}

func TestReviewedProjectionBusinessChangeRequiresEvaluation(t *testing.T) {
	tests := []struct {
		name string
		edit func(*catalog.Snapshot)
	}{
		{
			name: "stock",
			edit: func(snapshot *catalog.Snapshot) { snapshot.Available++ },
		},
		{
			name: "price",
			edit: func(snapshot *catalog.Snapshot) { snapshot.Price = money.FromKopecks(101) },
		},
		{
			name: "promo price",
			edit: func(snapshot *catalog.Snapshot) { snapshot.PromoPrice = nil },
		},
		{
			name: "price profile",
			edit: func(snapshot *catalog.Snapshot) {
				snapshot.PriceProfiles[0].EffectivePrice = money.FromKopecks(89)
			},
		},
		{
			name: "sales count",
			edit: func(snapshot *catalog.Snapshot) { snapshot.SalesCount++ },
		},
		{
			name: "rating",
			edit: func(snapshot *catalog.Snapshot) { snapshot.AverageRating = 4 },
		},
		{
			name: "ratings count",
			edit: func(snapshot *catalog.Snapshot) { snapshot.RatingsCount++ },
		},
		{
			name: "name",
			edit: func(snapshot *catalog.Snapshot) { snapshot.Card.Name = "Другой товар" },
		},
		{
			name: "description",
			edit: func(snapshot *catalog.Snapshot) { snapshot.Card.Description = "Другое описание" },
		},
		{
			name: "image",
			edit: func(snapshot *catalog.Snapshot) { snapshot.Card.Image = "image-2" },
		},
		{
			name: "assets",
			edit: func(snapshot *catalog.Snapshot) { snapshot.Card.Assets[0] = "asset-2" },
		},
		{
			name: "url",
			edit: func(snapshot *catalog.Snapshot) { snapshot.Card.URL = "/p2" },
		},
		{
			name: "sku",
			edit: func(snapshot *catalog.Snapshot) { snapshot.Card.SKU = "sku-2" },
		},
		{
			name: "store category",
			edit: func(snapshot *catalog.Snapshot) { snapshot.Card.Categories[0].Name = "Другая категория" },
		},
		{
			name: "visible",
			edit: func(snapshot *catalog.Snapshot) { snapshot.Card.Visible = false },
		},
		{
			name: "only variant",
			edit: func(snapshot *catalog.Snapshot) { snapshot.Card.OnlyVariant = true },
		},
		{
			name: "variants group",
			edit: func(snapshot *catalog.Snapshot) { snapshot.Card.VariantsID = "group-2" },
		},
		{
			name: "variant axis value",
			edit: func(snapshot *catalog.Snapshot) { snapshot.Card.Variants.Value = "v2" },
		},
		{
			name: "marketplace category",
			edit: func(snapshot *catalog.Snapshot) { snapshot.Card.Marketplace.CategoryID = "c2" },
		},
		{
			name: "raw attribute value",
			edit: func(snapshot *catalog.Snapshot) {
				snapshot.Card.Marketplace.Attributes["a1"] = json.RawMessage(`false`)
			},
		},
		{
			name: "raw marketplace price",
			edit: func(snapshot *catalog.Snapshot) { snapshot.Card.Marketplace.Price = nil },
		},
		{
			name: "raw publication flag",
			edit: func(snapshot *catalog.Snapshot) { snapshot.Card.Marketplace.Published = false },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e := approvedProjectionEnvironment(t, true, "image-1")
			before := e.view(t, "p1")
			e.clock.Advance(time.Hour)
			next := reviewedProjectionSnapshot(e, 2)
			test.edit(&next)
			applyReviewedProjection(t, e, next)
			after := e.view(t, "p1")
			member, _ := e.store.Participant("t1")
			if after.Verdict.Status != moderation.Pending || after.Verdict.DecidedBy != moderation.Rule ||
				after.Verdict.Decision != nil || after.Case == nil || after.Case.Revision != 2 ||
				e.document(t).Visible ||
				!reflect.DeepEqual(before.History, after.History) || member.ApprovedOffers != 1 {
				t.Fatalf("изменённая карточка унаследовала одобрение: %+v", after)
			}
		})
	}
}

func TestReviewedProjectionChangedRulesRequireEvaluation(t *testing.T) {
	for _, phrase := range []string{"Товар", "Другая фраза"} {
		t.Run(phrase, func(t *testing.T) {
			e := approvedProjectionEnvironment(t, true, "image-1")
			if err := e.service.AddStopPhrase(context.Background(), phrase, moderation.Actor{ID: "o2"}); err != nil {
				t.Fatal(err)
			}
			e.clock.Advance(time.Hour)
			applyReviewedProjection(t, e, reviewedProjectionSnapshot(e, 2))
			after := e.view(t, "p1")
			if after.Verdict.Status != moderation.Pending || after.Verdict.DecidedBy != moderation.Rule ||
				after.Verdict.Decision != nil || after.Case == nil || after.Verdict.RulesRevision != 2 {
				t.Fatal("новая ревизия правил обошла повторную оценку", after)
			}
		})
	}
}

func TestReviewedProjectionChangedCategoryPolicyRequiresEvaluation(t *testing.T) {
	for _, policy := range []string{"manual", "forbidden"} {
		t.Run(policy, func(t *testing.T) {
			e := approvedProjectionEnvironment(t, true, "image-1")
			definition := reviewedProjectionTaxonomy(2)
			definition.Categories[0].Moderation = policy
			taxonomies := apptaxonomy.NewService(e.store, e.clock, e.service)
			if _, err := taxonomies.LoadPart(context.Background(), "categories", definition, 1); err != nil {
				t.Fatal(err)
			}
			e.clock.Advance(time.Hour)
			applyReviewedProjection(t, e, reviewedProjectionSnapshot(e, 2))
			after := e.view(t, "p1")
			status := moderation.Pending
			if policy == "forbidden" {
				status = moderation.Rejected
			}
			if after.Verdict.Status != status || after.Verdict.DecidedBy != moderation.Rule ||
				after.Verdict.Decision != nil || e.document(t).Visible {
				t.Fatal("изменившаяся политика категории не применена", after)
			}
		})
	}
}

func TestReviewedProjectionRequireImageTighteningRequiresEvaluation(t *testing.T) {
	e := approvedProjectionEnvironment(t, false, "")
	e.service = appmoderation.NewService(e.store, e.clock, &fake.IDs{}, appmoderation.Config{
		ProbationOffers: 50,
		RequireImage:    true,
	})
	e.projector = appcatalog.NewProjector(e.store, e.clock, e.service)
	e.clock.Advance(time.Hour)
	next := reviewedProjectionSnapshot(e, 2)
	next.Card.Image = ""
	applyReviewedProjection(t, e, next)
	after := e.view(t, "p1")
	if after.Verdict.Status != moderation.Rejected || after.Verdict.DecidedBy != moderation.Rule ||
		after.Verdict.Decision != nil || after.Verdict.Reasons[0].Key != "moderation.image_missing" {
		t.Fatal("ужесточение обязательного изображения не применено", after)
	}
}

func TestReviewedProjectionLaterOperatorActionIsNotResurrected(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual review", true: "reject"}[reject], func(t *testing.T) {
			e := approvedProjectionEnvironment(t, true, "image-1")
			e.clock.Advance(time.Hour)
			c, err := e.service.OpenCase(
				context.Background(),
				ids.OfferID{Tenant: "t1", Product: "p1"},
				"Повторная проверка",
				moderation.Actor{ID: "o2", Name: "Оператор 2"},
			)
			if err != nil {
				t.Fatal(err)
			}
			if reject {
				_, err = e.service.Reject(context.Background(), appmoderation.DecisionCommand{
					CaseID:          c.ID,
					ExpectedVersion: c.Version,
					Operator:        moderation.Actor{ID: "o2", Name: "Оператор 2"},
					Note:            "Отклонено",
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			before := e.view(t, "p1")
			for revision := int64(2); revision <= 3; revision++ {
				e.clock.Advance(time.Hour)
				applyReviewedProjection(t, e, reviewedProjectionSnapshot(e, revision))
				after := e.view(t, "p1")
				member, _ := e.store.Participant("t1")
				if after.Verdict.Status != moderation.Pending || after.Verdict.DecidedBy != moderation.Rule ||
					after.Verdict.Decision != nil || after.Case == nil || e.document(t).Visible ||
					!reflect.DeepEqual(after.History, before.History) || member.ApprovedOffers != 1 {
					t.Fatal("транспортная доставка воскресила прежнее одобрение", after)
				}
			}
		})
	}
}

func TestReviewedProjectionCarryRollsBackAtomically(t *testing.T) {
	e := approvedProjectionEnvironment(t, true, "image-1")
	before := e.view(t, "p1")
	productBefore, _ := e.store.Product("t1", "p1")
	e.store.FailCommit = errors.New("сбой фиксации")
	e.clock.Advance(time.Hour)
	err := e.projector.ApplyProduct(context.Background(), appcatalog.ProductEvent{
		EventID:  "rollback-projection",
		Snapshot: reviewedProjectionSnapshot(e, 2),
	})
	e.store.FailCommit = nil
	productAfter, _ := e.store.Product("t1", "p1")
	if err == nil || !reflect.DeepEqual(e.view(t, "p1"), before) || !reflect.DeepEqual(productBefore, productAfter) {
		t.Fatal("неуспешный commit сохранил часть переноса", err)
	}
}

func approvedProjectionEnvironment(t *testing.T, requireImage bool, image string) environment {
	t.Helper()
	e := setup()
	e.store.SeedTaxonomy(reviewedProjectionTaxonomy(1))
	if !requireImage {
		e.service = appmoderation.NewService(e.store, e.clock, &fake.IDs{}, appmoderation.Config{
			ProbationOffers: 50,
			RequireImage:    false,
		})
		e.projector = appcatalog.NewProjector(e.store, e.clock, e.service)
	}
	snapshot := reviewedProjectionSnapshot(e, 1)
	snapshot.Card.Image = image
	applyReviewedProjection(t, e, snapshot)
	c := e.view(t, "p1").Case
	if c == nil {
		t.Fatal("исходная карточка не поставлена на проверку")
	}
	_, err := e.service.Allow(context.Background(), appmoderation.DecisionCommand{
		CaseID:          c.ID,
		ExpectedVersion: c.Version,
		Operator:        moderation.Actor{ID: "o1", Name: "Оператор"},
		Note:            "Проверено",
	})
	if err != nil || !e.document(t).Visible {
		t.Fatal("исходное одобрение не сохранено", err)
	}
	return e
}

func applyReviewedProjection(t *testing.T, e environment, snapshot catalog.Snapshot) {
	t.Helper()
	err := e.projector.ApplyProduct(context.Background(), appcatalog.ProductEvent{
		EventID:  fmt.Sprintf("reviewed-source:%d", snapshot.Revision),
		Snapshot: snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func reviewedProjectionTaxonomy(version int64) taxonomy.Definition {
	return taxonomy.Definition{
		Version: version,
		Attributes: []taxonomy.Attribute{{
			Code:               "a1",
			Name:               "Атрибут",
			Type:               "boolean",
			SearchTermsByValue: map[string][]string{"true": {"выражение"}},
		}},
		Schemas: map[string]taxonomy.Schema{"s1": {Attributes: []string{"a1"}}},
		Categories: []taxonomy.Category{
			{ID: "c1", Name: "Категория 1", Slug: "c1", Schema: "s1"},
			{ID: "c2", Name: "Категория 2", Slug: "c2", Schema: "s1"},
		},
	}
}

func reviewedProjectionSnapshot(e environment, revision int64) catalog.Snapshot {
	promo := money.FromKopecks(90)
	marketplacePrice := json.Number("1.00")
	return catalog.Snapshot{
		PriceProfiles: []catalog.PriceProfile{{
			Key:            catalog.DefaultPriceProfile,
			Price:          money.FromKopecks(100),
			EffectivePrice: money.FromKopecks(90),
		}},
		TenantID:      "t1",
		ProductID:     "p1",
		Revision:      revision,
		SalesCount:    3,
		AverageRating: 3.5,
		RatingsCount:  2,
		Price:         money.FromKopecks(100),
		PromoPrice:    &promo,
		Available:     4,
		At:            e.clock.Now(),
		Card: catalog.Card{
			Marketplace: &taxonomy.Card{
				Price:      &marketplacePrice,
				CategoryID: "c1",
				Attributes: map[string]json.RawMessage{"a1": json.RawMessage(`true`)},
				Published:  true,
			},
			Variants: &catalog.Variants{
				GroupID: "group-1",
				Axis:    catalog.VariantAxis{Key: "platform.a1", Code: "a1", Name: "Атрибут"},
				Value:   "v1",
			},
			Name:        "Товар",
			Description: "Описание",
			Image:       "image-1",
			Assets:      []string{"asset-1"},
			URL:         "/p1",
			SKU:         "sku-1",
			Categories:  []catalog.Category{{ID: "store-c1", Name: "Категория", URL: "/c1"}},
			VariantsID:  "group-1",
			Visible:     true,
		},
	}
}
