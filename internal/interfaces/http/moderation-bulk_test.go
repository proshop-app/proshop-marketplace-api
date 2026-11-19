package http

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModerationBulkHTTP(t *testing.T) {
	e := newModerationEnv(t)
	path := moderationURL + "/cases/bulk-allow"
	body := fmt.Sprintf(`{"cases":[{"id":%q,"expectedVersion":1}],"note":"Проверено"}`, e.caseID)
	e.request(t, "POST", path, body, "", 400)
	first := e.request(t, "POST", path, body, fmt.Sprintf("%d:bulk", e.clock.Now().UnixMilli()), 200)
	replay := e.request(t, "POST", path, body, fmt.Sprintf("%d:bulk", e.clock.Now().UnixMilli()), 200)
	if first.Body.String() != replay.Body.String() || !strings.Contains(first.Body.String(), `"approved":1`) {
		t.Fatal(first.Body.String(), replay.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("X-Marketplace-Money-Unit", "RUB")
	response := httptest.NewRecorder()
	e.handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatal(response.Code)
	}
	e.request(t, "GET", moderationURL+"/cases/bulk-count?tenantId=t1&reason=moderation.probation", "", "", 200)
}

func TestModerationBulkValidation(t *testing.T) {
	e := newModerationEnv(t)
	path := moderationURL + "/cases/bulk-allow"
	cases := strings.Repeat(fmt.Sprintf(`{"id":%q,"expectedVersion":1},`, e.caseID), 200)
	tooMany := `{"cases":[` + cases + fmt.Sprintf(`{"id":%q,"expectedVersion":1}],"note":"Проверено"}`, e.caseID)
	for _, body := range []string{
		`{"note":"Проверено"}`,
		`{"selector":{"source":"bad"},"note":"Проверено"}`,
		`{"selector":{"reason":"bad"},"note":"Проверено"}`,
		`{"selector":{"tenantId":"t1","reasons":["moderation.probation"]},"note":" "}`,
		tooMany,
	} {
		e.request(t, "POST", path, body, "validation", 400)
	}
}
