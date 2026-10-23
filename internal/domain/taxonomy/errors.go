package taxonomy

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

// Ошибки схем и конфликтов версий; ключи стабильны для клиентов.
var (
	ErrInvalid  = errs.New(errs.Invalid, "taxonomy.invalid", "некорректная таксономия")
	ErrConflict = errs.New(errs.Conflict, "taxonomy.version_conflict", "конфликт версии таксономии")
	ErrCategory = errs.New(errs.NotFound, "taxonomy.category_unknown", "неизвестная категория площадки")
)
