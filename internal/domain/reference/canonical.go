package reference

import "unicode"

const russianAuthorDictionary = "book_author"

// CanonicalLanguageAllowed ограничивает только канонические имена авторов.
// Иноязычные варианты остаются доступны для поиска через aliases.
func CanonicalLanguageAllowed(code, text string) bool {
	if code != russianAuthorDictionary {
		return true
	}

	hasRussianLetter := false

	for _, letter := range text {
		if !unicode.IsLetter(letter) {
			continue
		}

		letter = unicode.ToLower(letter)

		if letter != 'ё' && (letter < 'а' || letter > 'я') {
			return false
		}

		hasRussianLetter = true
	}

	return hasRussianLetter
}

// CanonicalSpelling сохраняет правила написания и добавляет политику имени.
// Общий Spelling не ограничивает язык: им проверяются также aliases.
func (d *Dictionary) CanonicalSpelling(text string) (clean, key string, err error) {
	clean, key, err = d.Spelling(text)

	if err != nil {
		return "", "", err
	}

	if !CanonicalLanguageAllowed(d.Code(), clean) {
		return "", "", ErrInvalid.WithMessage("каноническое имя автора должно быть записано по-русски")
	}

	return clean, key, nil
}
