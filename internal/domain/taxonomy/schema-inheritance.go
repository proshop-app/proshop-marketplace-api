package taxonomy

import "slices"

// ExpandSchemas разворачивает единственный исходник схем перед сохранением снимка.
// Абстрактные наборы не попадают в API; optional наследуется и может расширяться.
func ExpandSchemas(source map[string]Schema) (map[string]Schema, error) {
	resolved := map[string]Schema{}
	visiting := map[string]bool{}
	var expand func(string) (Schema, error)
	expand = func(name string) (Schema, error) {
		if schema, found := resolved[name]; found {
			return schema, nil
		}
		recipe, found := source[name]
		if !found || !ValidCode(name) || visiting[name] {
			return Schema{}, ErrInvalid.WithMessage("неизвестный родитель или цикл схем: " + name)
		}
		visiting[name] = true
		schema := Schema{}
		if recipe.Extends != "" {
			parent, err := expand(recipe.Extends)
			if err != nil {
				return Schema{}, err
			}
			schema.Attributes = slices.Clone(parent.Attributes)
			schema.Optional = slices.Clone(parent.Optional)
		}
		for _, code := range recipe.Attributes {
			if slices.Contains(schema.Attributes, code) {
				return Schema{}, ErrInvalid.WithMessage("повтор унаследованного поля: " + name + "." + code)
			}
			schema.Attributes = append(schema.Attributes, code)
		}
		seen := map[string]bool{}
		for _, code := range recipe.Optional {
			if !slices.Contains(schema.Attributes, code) || seen[code] {
				return Schema{}, ErrInvalid.WithMessage("неверный optional: " + name + "." + code)
			}
			seen[code] = true
			if !slices.Contains(schema.Optional, code) {
				schema.Optional = append(schema.Optional, code)
			}
		}
		if len(schema.Attributes) == 0 {
			return Schema{}, ErrInvalid.WithMessage("пустая схема: " + name)
		}
		visiting[name] = false
		resolved[name] = schema
		return schema, nil
	}
	for name := range source {
		if _, err := expand(name); err != nil {
			return nil, err
		}
	}
	out := map[string]Schema{}
	for name, schema := range resolved {
		if !source[name].Abstract {
			out[name] = schema
		}
	}
	return out, nil
}

// ExpandSchemaSource проверяет ссылки даже в абстрактных наборах, не вошедших в API.
func (d Definition) ExpandSchemaSource() (map[string]Schema, error) {
	fields := d.Fields()
	for name, schema := range d.Schemas {
		for _, code := range schema.Attributes {
			if _, found := fields[code]; !found {
				return nil, ErrInvalid.WithMessage("неизвестное поле исходника схемы: " + name + "." + code)
			}
		}
	}
	return ExpandSchemas(d.Schemas)
}
