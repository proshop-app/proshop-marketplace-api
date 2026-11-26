package platformguard

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/pkg/ids"
)

func TestCreationLooksForRecentOnlyWithinTimeoutWindowOrOnProbe(t *testing.T) {
	cfg := testConfig()
	clock := fake.NewClock(time.Unix(1000, 0))
	c := New(fake.NewPlatform(), cfg, clock, nil, nil)
	if recent, excluded := c.CreationCandidates(false); recent || len(excluded) != 0 {
		t.Fatal("без таймаутов исполнитель должен брать задачи с начала очереди", recent, excluded)
	}

	p := admit(t, c, "t1", checkoutClass)
	p.strict = true
	c.release(p, context.DeadlineExceeded)
	if recent, excluded := c.CreationCandidates(false); !recent || !slices.Equal(excluded, []ids.TenantID{"t1"}) {
		t.Fatal("после таймаута нужен поиск с конца очереди с пропуском молчащего магазина", recent, excluded)
	}
	clock.Advance(cfg.OpenInterval)
	if recent, excluded := c.CreationCandidates(false); recent || len(excluded) != 0 {
		t.Fatal("после окна таймаута исполнитель должен вернуться к началу очереди", recent, excluded)
	}
	if recent, excluded := c.CreationCandidates(true); !recent || !slices.Equal(excluded, []ids.TenantID{"t1"}) {
		t.Fatal("проба должна искать с конца очереди, даже когда окно таймаута истекло", recent, excluded)
	}
}

func TestSuccessfulShopProbeClearsSilenceBeforeItExpires(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	clock := fake.NewClock(time.Unix(1000, 0))
	c := New(fake.NewPlatform(), cfg, clock, nil, nil)
	p := admit(t, c, "t1", checkoutClass)
	p.strict = true
	c.release(p, context.DeadlineExceeded)

	clock.Advance(cfg.OpenInterval)
	if _, excluded := c.CreationCandidates(true); !slices.Equal(excluded, []ids.TenantID{"t1"}) {
		t.Fatal("молчание должно учитываться после открытия допуска к пробе", excluded)
	}
	probe := admit(t, c, "t1", checkoutClass)
	c.release(probe, nil)
	if _, excluded := c.CreationCandidates(true); len(excluded) != 0 {
		t.Fatal("ответивший магазин должен сразу вернуться к созданию", excluded)
	}
}

func TestCreationExclusionsExpireAndKeepActiveProbe(t *testing.T) {
	cfg := testConfig()
	cfg.FailureThreshold = 1
	clock := fake.NewClock(time.Unix(1000, 0))
	c := New(fake.NewPlatform(), cfg, clock, nil, nil)
	c.release(admit(t, c, "t1", checkoutClass), ErrUnavailable)
	p := admit(t, c, "t2", checkoutClass)
	p.strict = true
	c.release(p, context.DeadlineExceeded)

	clock.Advance(cfg.OpenInterval)
	if _, excluded := c.CreationCandidates(true); !slices.Equal(excluded, []ids.TenantID{"t2"}) {
		t.Fatal("истёкший размыкатель должен допустить пробу", excluded)
	}
	probe := admit(t, c, "t1", checkoutClass)
	clock.Advance(cfg.FailureWindow - cfg.OpenInterval)
	if _, excluded := c.CreationCandidates(true); !slices.Equal(excluded, []ids.TenantID{"t1"}) {
		t.Fatal("истёкший срок молчания нужно убрать, активную пробу сохранить", excluded)
	}
	c.release(probe, context.Canceled)
	if _, excluded := c.CreationCandidates(true); len(excluded) != 0 {
		t.Fatal("после отмены пробы магазин снова допускается", excluded)
	}
}

func TestEvictedShopDoesNotRemainExcludedFromCreation(t *testing.T) {
	cfg := testConfig()
	cfg.TenantStateSize = 1
	clock := fake.NewClock(time.Unix(1000, 0))
	c := New(fake.NewPlatform(), cfg, clock, nil, nil)
	p := admit(t, c, "t1", checkoutClass)
	p.strict = true
	c.release(p, context.DeadlineExceeded)
	c.release(admit(t, c, "t2", checkoutClass), nil)
	if _, excluded := c.CreationCandidates(true); len(excluded) != 0 {
		t.Fatal("вытесненный магазин остался в списке исключений", excluded)
	}
}

func BenchmarkCreationCandidates(b *testing.B) {
	const silent = 50
	for _, shops := range []int{100, 10_000, 100_000} {
		b.Run(fmt.Sprint(shops), func(b *testing.B) {
			cfg := testConfig()
			cfg.TenantStateSize = shops
			cfg.FailureMinCalls = silent + 1
			clock := fake.NewClock(time.Unix(1000, 0))
			c := New(nil, cfg, clock, nil, nil)
			for i := range shops {
				id := ids.TenantID(fmt.Sprintf("t%d", i))
				if i < silent {
					p := admit(b, c, id, checkoutClass)
					p.strict = true
					c.release(p, context.DeadlineExceeded)
				} else {
					c.tenantState(id, clock.Now())
				}
			}

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				recent, excluded := c.CreationCandidates(false)
				if !recent || len(excluded) != silent {
					b.Fatal("ожидались 50 молчащих магазинов", recent, len(excluded))
				}
			}
		})
	}
}
