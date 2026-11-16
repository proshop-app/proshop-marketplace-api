package colorpalette

import "github.com/proshop-app/proshop-marketplace/pkg/errs"

// ErrInvalid — нарушение правил палитры или привязки оттенка.
var ErrInvalid = errs.New(errs.Invalid, "reference.color_invalid", "неверная привязка цвета")
