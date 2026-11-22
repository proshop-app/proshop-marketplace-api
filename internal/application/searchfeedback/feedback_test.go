package searchfeedback

import (
	"strings"
	"testing"
)

func TestBuyerKeyIsScopedHMAC(t *testing.T) {
	const id = "b0000000-0000-4000-8000-000000000001"
	const expected = "caaab9e3ad646c430f54f582fd1c44763b2ddb9d3fe9bede1b8946723d18d8f6"
	keys := NewBuyerKeys(strings.Repeat("k", 32))
	if key := keys.Key(id); key != expected {
		t.Fatal("псевдоним изменился", key)
	}
	if keys.Key("") != "" || NewBuyerKeys(strings.Repeat("j", 32)).Key(id) == expected {
		t.Fatal("гостю не нужен псевдоним, смена ключа меняет псевдоним")
	}
}
