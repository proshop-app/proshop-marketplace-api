package delivery_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/domain/delivery"
	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

var t0 = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

func TestParseCarrier(t *testing.T) {
	for _, valid := range []string{"cdek", "yandex", "ozon-fresh"} {
		if _, err := delivery.ParseCarrier(valid); err != nil {
			t.Fatalf("%s: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "C", "CDEK", "1cdek", "cdek_x", "a"} {
		if _, err := delivery.ParseCarrier(invalid); !errors.Is(err, delivery.ErrInvalidCarrier) {
			t.Fatalf("%q должен быть отвергнут", invalid)
		}
	}
}

func TestNewPickupPoint(t *testing.T) {
	point, err := delivery.NewPickupPoint(
		"cdek",
		" MSK1 ",
		"",
		"ул. Примерная, 1",
		"Город",
		delivery.Location{Lat: 55.7, Lon: 37.6},
		"Пн-Пт 10-20",
	)
	if err != nil || point.Code != "MSK1" || point.Name != "ул. Примерная, 1" {
		t.Fatalf("пункт: %+v, %v", point, err)
	}

	cases := map[string]func() error{
		"без кода": func() error {
			_, err := delivery.NewPickupPoint(
				"cdek",
				" ",
				"n",
				"a",
				"c",
				delivery.Location{Lat: 1, Lon: 1},
				"",
			)
			return err
		},
		"без адреса": func() error {
			_, err := delivery.NewPickupPoint("cdek", "x", "n", "", "c", delivery.Location{Lat: 1, Lon: 1}, "")
			return err
		},
		"нулевые координаты": func() error {
			_, err := delivery.NewPickupPoint("cdek", "x", "n", "a", "c", delivery.Location{}, "")
			return err
		},
		"широта вне пределов": func() error {
			_, err := delivery.NewPickupPoint(
				"cdek",
				"x",
				"n",
				"a",
				"c",
				delivery.Location{Lat: 91, Lon: 1},
				"",
			)
			return err
		},
		"не число": func() error {
			_, err := delivery.NewPickupPoint(
				"cdek",
				"x",
				"n",
				"a",
				"c",
				delivery.Location{Lat: math.NaN(), Lon: 1},
				"",
			)
			return err
		},
	}

	for name, run := range cases {
		if err := run(); !errors.Is(err, delivery.ErrInvalidPickupPoint) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestNewQuote(t *testing.T) {
	quote, err := delivery.NewQuote(
		delivery.Offered{Available: true, Cost: money.FromKopecks(30001), MinDays: 2, MaxDays: 4},

		t0,
		30*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if quote.Status != delivery.QuoteAvailable || quote.Cost.Kopecks() != 30001 ||
		!quote.ExpiresAt.Equal(t0.Add(30*time.Minute)) {
		t.Fatalf("расчёт: %+v", quote)
	}

	short, _ := delivery.NewQuote(
		delivery.Offered{Available: true, Cost: money.FromKopecks(100), ExpiresAt: t0.Add(5 * time.Minute)},

		t0,
		time.Hour,
	)
	if !short.ExpiresAt.Equal(t0.Add(5*time.Minute)) || short.Expired(t0.Add(4*time.Minute)) ||
		!short.Expired(t0.Add(5*time.Minute)) {
		t.Fatalf("срок — не позже обещанного магазином: %+v", short)
	}

	reasons := []string{
		delivery.ReasonCarrierNotConnected,
		delivery.ReasonPickupPointUnavailable,
		delivery.ReasonNoRate,
	}

	for _, reason := range reasons {
		unavailable, err := delivery.NewQuote(delivery.Offered{Reason: reason}, t0, time.Hour)
		if err != nil || unavailable.Status != delivery.QuoteUnavailable || unavailable.Reason != reason {
			t.Fatalf("недоступно, %s: %+v %v", reason, unavailable, err)
		}
	}

	for name, offered := range map[string]delivery.Offered{
		"отрицательная цена":    {Available: true, Cost: money.FromKopecks(-1)},
		"сроки наоборот":        {Available: true, MinDays: 5, MaxDays: 2},
		"истёкший":              {Available: true, ExpiresAt: t0.Add(-time.Second)},
		"причина не из словаря": {Reason: "too_far"},
		"без причины":           {},
		"причина маркетплейса":  {Reason: delivery.ReasonSellerUnreachable},
	} {
		if _, err := delivery.NewQuote(offered, t0, time.Hour); !errors.Is(err, delivery.ErrInvalidQuote) {
			t.Fatalf("%s: %v", name, err)
		}
	}

	if unreachable := delivery.Unreachable(t0); unreachable.Reason != delivery.ReasonSellerUnreachable ||
		!unreachable.Expired(t0) {
		t.Fatalf("магазин не ответил: %+v", unreachable)
	}
}
