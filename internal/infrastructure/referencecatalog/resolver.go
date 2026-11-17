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
	return r.ResolveDictionary(ctx, "brand", text)
}

// ResolveDictionary возвращает канонический id записи указанного словаря.
func (r *Resolver) ResolveDictionary(ctx context.Context, dictionary, text string) (string, bool, error) {
	result, err := r.service.Resolve(ctx, dictionary, text)
	if err != nil || result.Match == nil {
		return "", false, err
	}

	return string(result.Match.EntryID), true, nil
}

// ResolveDictionaryBatch использует три чтения словаря вместо resolve на каждый товар.
func (r *Resolver) ResolveDictionaryBatch(
	ctx context.Context,
	dictionary string,
	texts []string,
) (map[string]string, error) {
	resolved, err := r.service.ResolveBatch(ctx, dictionary, texts)
	result := make(map[string]string, len(resolved))
	for text, id := range resolved {
		result[text] = string(id)
	}
	return result, err
}
