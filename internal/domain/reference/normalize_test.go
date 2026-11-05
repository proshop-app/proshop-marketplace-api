package reference

import "testing"

func TestKeyFoldsCaseYoLatinAndSpaces(t *testing.T) {
	cases := map[string]string{
		"  Бренд  ":            "бренд",
		"ЁЛКА":                 "елка",
		"Ёлка\u00a0\u3000 Два": "елка два",
		"Škoda":                "skoda",
		"Crème Brûlée":         "creme brulee",
		"Straße":               "strasse",
		"Æon":                  "aeon",
		"Ｂｒａｎｄ":                "brand",
		"Бренд\ufeff":          "бренд",
		"Бренд\u0085Два":       "бренд два",
		"Йогурт":               "йогурт",
		"b1 — b2":              "b1 - b2",
	}
	for text, want := range cases {
		if got := Key(text); got != want {
			t.Errorf("Key(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestSpacesMatchPlatformClass(t *testing.T) {
	// JS \s в Node 24 плюс U+0085; диапазон U+2000–U+200A — циклом
	platform := []rune("\t\n\v\f\r \u0085\u00a0\u1680\u2028\u2029\u202f\u205f\u3000\ufeff")
	for r := rune(0x2000); r <= 0x200a; r++ {
		platform = append(platform, r)
	}
	want := map[rune]bool{}
	for _, r := range platform {
		want[r] = true
	}
	for r := rune(0); r <= 0x10ffff; r++ {
		if IsSpace(r) != want[r] {
			t.Fatalf("IsSpace(%U) = %v", r, IsSpace(r))
		}
	}
}

func TestSpellingLimits(t *testing.T) {
	dict := testDictionary()
	if _, _, err := dict.Spelling("один два три четыре пять шесть семь восемь девять"); err == nil {
		t.Fatal("девять слов прошли лимит в восемь")
	}
	long := make([]rune, DefaultMaxAliasLength+1)
	for i := range long {
		long[i] = 'б'
	}
	if _, _, err := dict.Spelling(string(long)); err == nil {
		t.Fatal("написание длиннее лимита прошло")
	}
	if _, _, err := dict.Spelling("Бренд\u0007"); err == nil {
		t.Fatal("управляющий символ прошёл")
	}
	clean, key, err := dict.Spelling("  Бренд   Два ")
	if err != nil || clean != "Бренд Два" || key != "бренд два" {
		t.Fatalf("Spelling = %q, %q, %v", clean, key, err)
	}
}

func testDictionary() *Dictionary {
	return RestoreDictionary(DictionaryState{
		Code:       "b1",
		Kind:       KindOpen,
		Identity:   true,
		AliasKinds: AliasKinds,
		Limits:     DefaultLimits(),
	})
}
