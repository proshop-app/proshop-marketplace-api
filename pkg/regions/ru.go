// Package regions — коды административных регионов ISO 3166-2.
package regions

// ValidRU проверяет существование кода, а не только его форму.
func ValidRU(code string) bool {
	_, ok := ruNames[code]
	return ok
}
