package opensearch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	opensearchgo "github.com/opensearch-project/opensearch-go/v4"
)

type mappingTransport func(*http.Request) (*http.Response, error)

func (f mappingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMappingUpgradeRequiresRebuild(t *testing.T) {
	for _, version := range []int{0, offerMappingVersion} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			client, err := opensearchgo.NewClient(opensearchgo.Config{
				Addresses: []string{"http://index.invalid"},
				Transport: mappingTransport(func(r *http.Request) (*http.Response, error) {
					body := ""
					if r.Method == http.MethodGet {
						body = fmt.Sprintf(`{"generation":{"mappings":{"_meta":{"taxonomySchema":%d}}}}`, version)
					}
					return &http.Response{
						StatusCode: 200,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(body)),
					}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			index := &Index{client: client, alias: "offers"}
			exists, err := index.Exists(context.Background())
			if err != nil || exists != (version == offerMappingVersion) {
				t.Fatalf("%v %v", exists, err)
			}
		})
	}
}
