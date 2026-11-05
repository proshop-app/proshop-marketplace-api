// Package referencecatalog связывает каталог с реестром через порт бренда.
package referencecatalog

import (
	"context"

	"github.com/proshop-app/proshop-marketplace/internal/application/reference"
)

// Resolver адаптирует реестр брендов для вычисления ключа модели.
type Resolver struct {
	service *reference.Service
}

// New создаёт адаптер реестра брендов.
func New(service *reference.Service) *Resolver {
	return &Resolver{
		service: service,
	}
}

// Resolve возвращает канонический идентификатор бренда.
func (r *Resolver) Resolve(ctx context.Context, text string) (string, bool, error) {
	result, err := r.service.Resolve(ctx, "brand", text)
	if err != nil || result.Match == nil {
		return "", false, err
	}

	return string(result.Match.EntryID), true, nil
}
