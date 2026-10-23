package money_test

import (
	"errors"
	"math"
	"testing"

	"github.com/proshop-app/proshop-marketplace/pkg/money"
)

func TestParseRubles(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"199", 19900},
		{"199.9", 19990},
		{"199.99", 19999},
		{"84.9915", 8499},
		{"84.995", 8500},
		{"84.99150000000001", 8499},
		{"0.005", 1},
		{"0.004", 0},
		{"-12.345", -1235},
		{"+7.10", 710},
		{" 15.5 ", 1550},
		{"0.995", 100},
		{"92233720368547758.07", math.MaxInt64},
	}

	for _, tc := range cases {
		got, err := money.ParseRubles(tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got.Kopecks() != tc.want {
			t.Fatalf("%q: %d копеек, ожидалось %d", tc.in, got.Kopecks(), tc.want)
		}
	}
}

func TestParseRublesRejects(t *testing.T) {
	for _, in := range []string{"", "-", "abc", "1e3", "1.", ".5", "1.2.3", "1,5", "92233720368547758.08"} {
		if _, err := money.ParseRubles(in); err == nil {
			t.Fatalf("%q должна быть отвергнута", in)
		}
	}

	_, err := money.ParseRubles("92233720368547759")
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("ожидалось переполнение, получено %v", err)
	}
}

func TestAddSub(t *testing.T) {
	a := money.FromKopecks(150)
	b := money.FromKopecks(275)

	sum, err := a.Add(b)
	if err != nil || sum.Kopecks() != 425 {
		t.Fatalf("сумма %v, %v", sum, err)
	}

	diff, err := a.Sub(b)
	if err != nil || diff.Kopecks() != -125 || !diff.IsNegative() {
		t.Fatalf("разность %v, %v", diff, err)
	}
}

func TestOverflow(t *testing.T) {
	maxValue := money.FromKopecks(math.MaxInt64)
	minValue := money.FromKopecks(math.MinInt64)

	if _, err := maxValue.Add(money.FromKopecks(1)); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("сложение: ожидалось переполнение, получено %v", err)
	}
	if _, err := minValue.Add(money.FromKopecks(-1)); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("сложение отрицательных: ожидалось переполнение, получено %v", err)
	}
	if _, err := minValue.Sub(money.FromKopecks(1)); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("вычитание: ожидалось переполнение, получено %v", err)
	}
	if _, err := maxValue.Sub(money.FromKopecks(-1)); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("вычитание отрицательного: ожидалось переполнение, получено %v", err)
	}
}

func TestCmpAndString(t *testing.T) {
	if money.FromKopecks(1).Cmp(money.FromKopecks(2)) != -1 ||
		money.FromKopecks(2).Cmp(money.FromKopecks(1)) != 1 ||
		money.FromKopecks(2).Cmp(money.FromKopecks(2)) != 0 {
		t.Fatal("сравнение работает неверно")
	}
	if !money.FromKopecks(0).IsZero() {
		t.Fatal("ноль должен быть нулём")
	}

	cases := map[int64]string{0: "0.00", 5: "0.05", 123450: "1234.50", -5: "-0.05", -123456: "-1234.56"}
	for kopecks, want := range cases {
		if got := money.FromKopecks(kopecks).String(); got != want {
			t.Fatalf("%d: %q, ожидалось %q", kopecks, got, want)
		}
	}
}
