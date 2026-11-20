package catalog

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

// NormalizeQueryText обрезает края строки и проверяет пределы покупательского поиска.
func NormalizeQueryText(text string) (string, error) {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > MaxQueryLength {
		return "", ErrInvalidQuery.WithMessage(fmt.Sprintf("строка поиска длиннее %d символов", MaxQueryLength))
	}
	if queryWordCount(text) > MaxQueryWords {
		return "", ErrInvalidQuery.WithMessage(fmt.Sprintf("строка поиска содержит больше %d слов", MaxQueryWords))
	}
	return text, nil
}

func queryWordCount(text string) int {
	count, inWord := 0, false
	for _, r := range text {
		if reference.IsSpace(r) {
			inWord = false
			continue
		}
		if !inWord {
			count++
			inWord = true
		}
	}
	return count
}
