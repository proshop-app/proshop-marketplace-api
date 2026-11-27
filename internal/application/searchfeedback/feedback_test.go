package searchfeedback

import (
	"strings"
	"testing"
)

// Эталон посчитан независимо: HMAC-SHA256(HMAC-SHA256(секрет, buyerKeysPurpose),
// "marketplace\x00"+buyerId). Сам секрет в псевдоним не входит.
func TestBuyerKeyIsScopedHMAC(t *testing.T) {
	const id = "b0000000-0000-4000-8000-000000000001"
	const expected = "e26282f68b5723c956527a4aa362cd995dbbcdca567986eede6a4bb3c5632ab4"
	keys := NewBuyerKeys(strings.Repeat("k", 32))
	if key := keys.Key(id); key != expected {
		t.Fatal("псевдоним изменился", key)
	}
	if keys.Key("") != "" || NewBuyerKeys(strings.Repeat("j", 32)).Key(id) == expected {
		t.Fatal("гостю не нужен псевдоним, смена ключа меняет псевдоним")
	}
}
