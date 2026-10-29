//go:build integration

package opensearch_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/opensearch"
)

func clusterURL() string {
	if urls := os.Getenv("TEST_OPENSEARCH_URLS"); urls != "" {
		return strings.Split(urls, ",")[0]
	}

	return "http://localhost:29200"
}

func testIndex(t *testing.T) *opensearch.Client {
	t.Helper()

	client, err := opensearch.New(opensearch.Config{
		URLs:     []string{clusterURL()},
		Shards:   1,
		Replicas: 0,
	})

	if err != nil {
		t.Fatal(err)
	}

	if err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}

	return client
}
