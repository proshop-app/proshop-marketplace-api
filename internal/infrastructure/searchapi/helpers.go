package searchapi

import "strings"

func validGeneration(value string) bool {
	if !strings.HasPrefix(value, "marketplace-offers-") || len(value) > 128 {
		return false
	}

	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}

	return true
}
