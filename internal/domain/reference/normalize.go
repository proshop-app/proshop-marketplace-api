package reference

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// NormalizerVersion — версия правила ключа. Хранится в словаре и отдаётся в
// снимке: потребитель сверяет её со своей копией правила. Смена правила —
// новая версия и миграция пересчёта ключей с новой ревизией записей.
const NormalizerVersion = "v1"

// foldGroup — символы, которые сворачиваются в одну ASCII-строку.
type foldGroup struct {
	to   string
	from string
}

var (
	// spaces — пробельный класс платформы: JS \s плюс U+0085. Тот же класс у
	// сервера и поиска (internal/common/whitespace): strings.Fields не знает
	// U+FEFF, и ключ написания с U+FEFF в конце разошёлся бы с ключом поиска.
	spaces = &unicode.RangeTable{
		R16: []unicode.Range16{
			{Lo: 0x0009, Hi: 0x000d, Stride: 1},
			{Lo: 0x0020, Hi: 0x0020, Stride: 1},
			{Lo: 0x0085, Hi: 0x0085, Stride: 1},
			{Lo: 0x00a0, Hi: 0x00a0, Stride: 1},
			{Lo: 0x1680, Hi: 0x1680, Stride: 1},
			{Lo: 0x2000, Hi: 0x200a, Stride: 1},
			{Lo: 0x2028, Hi: 0x2029, Stride: 1},
			{Lo: 0x202f, Hi: 0x202f, Stride: 1},
			{Lo: 0x205f, Hi: 0x205f, Stride: 1},
			{Lo: 0x3000, Hi: 0x3000, Stride: 1},
			{Lo: 0xfeff, Hi: 0xfeff, Stride: 1},
		},
		LatinOffset: 4,
	}
	folding = foldTable()
)

// IsSpace — символ пробельного класса платформы.
func IsSpace(r rune) bool {
	return unicode.Is(spaces, r)
}

// Words режет текст по пробельным последовательностям класса платформы.
func Words(text string) []string {
	return strings.FieldsFunc(text, IsSpace)
}

// Collapse убирает пробелы по краям и сводит каждую их последовательность в
// один пробел, регистр не трогает: так хранится написание варианта.
func Collapse(text string) string {
	return strings.Join(Words(text), " ")
}

// Key — ключ написания, одна функция на всю платформу: ё→е, нижний регистр,
// свёртка латиницы с диакритикой в ASCII, пробелы одним классом. Повторяет
// value_normalizer индекса: «Ёлка », «ЁЛКА» и «елка» дают один ключ, а
// «Škoda» и «skoda» — другой общий. Кириллица, кроме ё, не сворачивается.
func Key(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, word := range Words(text) {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		for _, r := range word {
			r = unicode.ToLower(r)
			if r == 'ё' {
				r = 'е'
			}
			if folded, ok := folding[r]; ok {
				b.WriteString(folded)
				continue
			}
			b.WriteRune(r)
		}
	}

	return b.String()
}

// Length — длина написания в символах, как её видит человек и лимит словаря.
func Length(text string) int {
	return utf8.RuneCountInString(text)
}

func foldTable() map[rune]string {
	table := make(map[rune]string)
	for _, group := range foldGroups {
		for _, r := range group.from {
			table[r] = group.to
		}
	}

	return table
}
