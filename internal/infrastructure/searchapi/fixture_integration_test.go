//go:build integration

package searchapi_test

import (
	"os"
	"testing"
	"time"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/searchapi"
)

func testSearchIndex(t *testing.T) *searchapi.Index {
	t.Helper()

	address := os.Getenv("TEST_SEARCH_API_URL")

	if address == "" {
		t.Skip("запустите proshop-search/tools/marketplace-fixture; задайте TEST_SEARCH_API_URL=http://127.0.0.1:19207")
	}

	if address != "http://127.0.0.1:19207" {
		t.Fatal("тест контракта требует изолированный loopback-контур")
	}
	client, err := searchapi.New(searchapi.Config{
		URL:     address,
		Token:   "uno-marketplace-integration-token-0001",
		Timeout: 35 * time.Second,
	})

	if err != nil {
		t.Fatal(err)
	}

	return client
}
