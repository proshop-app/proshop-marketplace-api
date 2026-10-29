package catalog

import "testing"

func TestGTINChecksumAndPadding(t *testing.T) {
	valid := map[string]string{
		"96385074":       "0000096385074",
		"036000291452":   "0036000291452",
		"4006381333931":  "4006381333931",
		"10012345000017": "10012345000017",
	}
	for input, want := range valid {
		got, ok := ParseGTIN(input)
		if !ok || got != want {
			t.Fatalf("%s: %s, %v", input, got, ok)
		}
		changed := input[:len(input)-1] + string('0'+(input[len(input)-1]-'0'+1)%10)
		if _, ok := ParseGTIN(changed); ok {
			t.Fatalf("принята неверная контрольная цифра: %s", changed)
		}
	}
	for _, input := range []string{"", "400638133393", "400638133393x", " 4006381333931", "400-6381333931"} {
		if _, ok := ParseGTIN(input); ok {
			t.Fatalf("принят неверный формат: %q", input)
		}
	}
}

func TestModelPartKey(t *testing.T) {
	if got := ModelPartKey("  AB-12 3  "); got != "ab123" {
		t.Fatalf("нормализация: %q", got)
	}
}
