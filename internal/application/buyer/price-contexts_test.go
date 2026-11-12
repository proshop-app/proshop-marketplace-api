package buyer_test

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/fake"
	"github.com/proshop-app/proshop-marketplace/internal/domain/buyer"
)

func TestLoginAndRefreshPreloadContexts(t *testing.T) {
	e := setup()
	preloader := &fake.PriceContextPreloader{}
	e.auth.WithPriceContexts(preloader)
	ctx := context.Background()
	view, err := e.auth.StartLogin(ctx, "+79991234567", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(preloader.Buyers) != 0 {
		t.Fatal("предзагрузка до подтверждения входа")
	}
	e.calls.Set("ref-1", buyer.CallConfirmed)
	login, err := e.auth.CheckLogin(ctx, view.ID)
	if err != nil || login.Session == nil {
		t.Fatal(login, err)
	}
	id := login.Session.Buyer.ID
	if len(preloader.Buyers) != 1 || preloader.Buyers[0] != id {
		t.Fatal(preloader.Buyers)
	}
	if _, err := e.auth.Refresh(ctx, login.Session.Key); err != nil {
		t.Fatal(err)
	}
	if len(preloader.Buyers) != 2 || preloader.Buyers[1] != id {
		t.Fatal(preloader.Buyers)
	}
	if _, err := e.auth.Authenticate(ctx, login.Session.Token); err != nil {
		t.Fatal(err)
	}
	if len(preloader.Buyers) != 2 {
		t.Fatal("обычная проверка JWT не должна обновлять контексты")
	}
}
