//go:build integration

package searchapi_test

import (
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/searchapi"
)

func testSearchIndex(t *testing.T) *searchapi.Index {
	t.Helper()

	address := os.Getenv("TEST_SEARCH_API_URL")

	if address == "" {
		t.Skip("запустите proshop-search/tools/marketplace-fixture; задайте TEST_SEARCH_API_URL=http://127.0.0.1:19207")
	}

	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" ||
		parsed.User != nil || parsed.Path != "" || parsed.ForceQuery || parsed.RawQuery != "" ||
		parsed.Fragment != "" || strings.Contains(address, "#") {
		t.Fatal("тест контракта требует изолированный loopback-контур")
	}
	client, err := searchapi.New(searchapi.Config{
		URL:   address,
		Token: "uno-marketplace-integration-token-0001",
	})

	if err != nil {
		t.Fatal(err)
	}

	return client
}
