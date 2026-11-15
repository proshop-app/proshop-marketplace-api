package catalog

import "testing"

func TestIdentityNumber(t *testing.T) {
	for input, want := range map[string]string{
		"128.0":            "128",
		"1.28e2":           "128",
		"42.50":            "42.5",
		"-0.00":            "0",
		"-0.00500":         "-0.005",
		"9007199254740993": "9007199254740993",
		"1.230e-2000":      "123e-2002",
		"true":             "",
		`"128"`:            "",
		"null":             "",
		"01":               "",
	} {
		if got := IdentityNumber(input); got != want {
			t.Fatalf("%s: %s, ожидается %s", input, got, want)
		}
	}
}
