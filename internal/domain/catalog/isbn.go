package catalog

import "strings"

// ParseISBN проверяет ISBN-10/13 и возвращает ISBN-13; разделители не значимы.
func ParseISBN(value string) (string, bool) {
	value = strings.NewReplacer("-", "", " ", "").Replace(value)
	if len(value) == 13 {
		if !strings.HasPrefix(value, "978") && !strings.HasPrefix(value, "979") {
			return "", false
		}
		return ParseGTIN(value)
	}
	if len(value) != 10 {
		return "", false
	}
	sum := 0
	for index, digit := range value {
		number := int(digit - '0')
		if index == 9 && (digit == 'X' || digit == 'x') {
			number = 10
		} else if digit < '0' || digit > '9' {
			return "", false
		}
		sum += (10 - index) * number
	}
	if sum%11 != 0 {
		return "", false
	}
	prefix := "978" + value[:9]
	sum = 0
	for index, digit := range prefix {
		weight := 1 + 2*(index%2)
		sum += int(digit-'0') * weight
	}
	return prefix + string(rune('0'+(10-sum%10)%10)), true
}
