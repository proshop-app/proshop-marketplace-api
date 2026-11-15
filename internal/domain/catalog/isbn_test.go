package catalog

import "testing"

func TestParseISBN(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "0-306-40615-2", want: "9780306406157"},
		{input: "9780306406157", want: "9780306406157"},
		{input: "0 8044 2957 X", want: "9780804429573"},
		{input: "9780306406158"},
		{input: "0306406153"},
		{input: "0036000291452"},
		{input: "X306406152"},
		{input: ""},
	}
	for _, test := range tests {
		got, ok := ParseISBN(test.input)
		if got != test.want || ok != (test.want != "") {
			t.Fatalf("%q: %q, %v", test.input, got, ok)
		}
	}
}
