package http

import (
	"strings"
	"testing"
)

func TestM5HTTP(t *testing.T) {
	e := newModerationEnv(t)
	for _, path := range []string{
		"/cases?order=unknown",
		"/verdicts?status=rejected&decidedBy=unknown",
	} {
		e.request(t, "GET", moderationURL+path, "", "", 400)
	}
	response := e.request(t, "GET", moderationURL+"/cases?order=oldest&reason=moderation.probation", "", "", 200)
	for _, field := range []string{`"offer":`, `"seller":`, `"name":"Товар"`, `"price":19999`} {
		if !strings.Contains(response.Body.String(), field) {
			t.Fatal(response.Body.String())
		}
	}
	response = e.request(t, "GET", moderationURL+"/cases?reason=moderation.image_missing", "", "", 200)
	if !strings.Contains(response.Body.String(), `"total":0`) {
		t.Fatal(response.Body.String())
	}
	response = e.request(t, "GET", moderationURL+"/rules", "", "", 200)
	for _, field := range []string{`"revision":1`, `"taxonomyVersion":1`, `"probationOffers":50`} {
		if !strings.Contains(response.Body.String(), field) {
			t.Fatal(response.Body.String())
		}
	}
	response = e.request(t, "GET", moderationURL+"/offers/t1~p1", "", "", 200)
	for _, field := range []string{`"offer":`, `"participant":`, `"verdicts":`, `"rulesRevision":1`} {
		if !strings.Contains(response.Body.String(), field) {
			t.Fatal(response.Body.String())
		}
	}
	prefix := "/api/v1/operator/participants/t1"
	for _, body := range []string{`{}`, `{"note":"  "}`} {
		response = e.request(t, "POST", prefix+"/disable", body, "", 400)
		if !strings.Contains(response.Body.String(), "participant.invalid_note") {
			t.Fatal(response.Body.String())
		}
	}
	e.request(t, "PUT", prefix+"/trust", `{"trust":"trusted"}`, "", 400)
	e.request(t, "POST", prefix+"/enable", `{}`, "", 200)
	e.request(t, "POST", prefix+"/disable", `{"note":"Заметка"}`, "", 200)
}

func TestM5OperatorVerdictHTTP(t *testing.T) {
	e := newModerationEnv(t)
	e.request(t, "POST", moderationURL+"/reasons", `{"code":"r1","message":"Текст"}`, "", 201)
	body := `{"expectedVersion":1,"reason":"r1","note":"Заметка"}`
	e.request(t, "POST", moderationURL+"/cases/"+e.caseID+"/reject", body, "", 200)
	path := moderationURL + "/verdicts?status=rejected&decidedBy=operator"
	response := e.request(t, "GET", path, "", "", 200)
	for _, field := range []string{
		`"total":1`,
		`"operator":`,
		`"action":"reject"`,
		`"reason":{"code":"r1","message":"Текст"}`,
		`"key":"moderation.rejected_by_operator","message":"Текст"`,
	} {
		if !strings.Contains(response.Body.String(), field) {
			t.Fatal(response.Body.String())
		}
	}
	response = e.request(t, "GET", moderationURL+"/verdicts?status=rejected&decidedBy=rule", "", "", 200)
	if !strings.Contains(response.Body.String(), `"total":0`) {
		t.Fatal(response.Body.String())
	}
}
