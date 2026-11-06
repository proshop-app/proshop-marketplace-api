package taxonomy

// CategoryIcons — фиксированный набор имён Lucide, поддерживаемых витриной.
var CategoryIcons = map[string]struct{}{
	"smartphone":      {},
	"laptop":          {},
	"washing-machine": {},
	"cooking-pot":     {},
	"armchair":        {},
	"bed-double":      {},
	"shirt":           {},
	"footprints":      {},
	"handbag":         {},
	"gem":             {},
	"sparkles":        {},
	"heart-pulse":     {},
	"baby":            {},
	"blocks":          {},
	"dumbbell":        {},
	"tent":            {},
	"car":             {},
	"wrench":          {},
	"paint-roller":    {},
	"flower-2":        {},
	"paw-print":       {},
	"apple":           {},
	"pencil":          {},
	"book-open":       {},
	"palette":         {},
	"music":           {},
	"house":           {},
	"spray-can":       {},
	"factory":         {},
}

// ValidIcon проверяет имя без привязки к категории.
func ValidIcon(name string) bool {
	_, ok := CategoryIcons[name]
	return ok
}
