// Package taxonomy — строгие схемы карточек площадки, которыми управляет оператор.
package taxonomy

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// MaxAttributes ограничивает число динамических полей индекса.
	MaxAttributes = 256
	// MaxCategories ограничивает размер операторского дерева.
	MaxCategories = 4096
	// MaxDepth ограничивает глубину дерева.
	MaxDepth = 16
)

var (
	codePattern       = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	slugPattern       = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	categoryIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)
)

// ValueMeta задаёт визуальное представление значения перечисления.
type ValueMeta struct {
	Hex    string `json:"hex,omitempty" yaml:"hex,omitempty"`
	Swatch string `json:"swatch,omitempty" yaml:"swatch,omitempty"`
}

// Attribute — скалярное поле; код имеет один тип и единицу во всей площадке.
type Attribute struct {
	ValuesMeta map[string]ValueMeta `json:"valuesMeta,omitempty" yaml:"valuesMeta,omitempty"`
	Code       string               `json:"code" yaml:"code"`
	Name       string               `json:"name" yaml:"name"`
	Type       string               `json:"type" yaml:"type"`
	Dictionary string               `json:"dictionary,omitempty" yaml:"dictionary,omitempty"`
	Values     []string             `json:"values,omitempty" yaml:"-"`
	Unit       string               `json:"unit,omitempty" yaml:"unit,omitempty"`
	Facet      bool                 `json:"facet" yaml:"facet"`
}

// Category ссылается на схему только в листе, чтобы дерево не дублировало атрибуты.
type Category struct {
	ID     string `json:"id" yaml:"id"`
	Name   string `json:"name" yaml:"name"`
	Title  string `json:"title,omitempty" yaml:"title,omitempty"`
	Slug   string `json:"slug" yaml:"slug"`
	Schema string `json:"schema,omitempty" yaml:"schema,omitempty"`
	// Moderation — политика допуска узла: auto, manual или forbidden; пусто — наследуется.
	Moderation string     `json:"moderation,omitempty" yaml:"moderation,omitempty"`
	Children   []Category `json:"children,omitempty" yaml:"children,omitempty"`
}

// Schema хранит порядок полей и исключения из обязательности по умолчанию.
type Schema struct {
	Attributes []string `json:"attributes" yaml:"attributes"`
	Optional   []string `json:"optional,omitempty" yaml:"optional,omitempty"`
}

// SchemaAttribute содержит обязательность только в контексте схемы.
type SchemaAttribute struct {
	Attribute
	Required bool `json:"required"`
}

// CategorySchema собирается из одного снимка, без обращения к реестру.
type CategorySchema struct {
	Category
	Attributes []SchemaAttribute `json:"attributes"`
}

// Definition — неизменяемый снимок версии таксономии.
type Definition struct {
	Version    int64             `json:"version" yaml:"version"`
	Attributes []Attribute       `json:"attributes" yaml:"attributes"`
	Schemas    map[string]Schema `json:"schemas" yaml:"schemas"`
	Categories []Category        `json:"categories" yaml:"categories"`
}

// Card — отдельный блок площадки в проекции магазина.
type Card struct {
	Price      *json.Number               `json:"price,omitempty"`
	CategoryID string                     `json:"categoryId"`
	Attributes map[string]json.RawMessage `json:"attributes"`
	Published  bool                       `json:"published"`
}

// Issue — стабильный ключ и поле ошибки, доступные оператору.
type Issue struct {
	Code      string `json:"code"`
	Attribute string `json:"attribute,omitempty"`
}

// Publication — результат проверки; только Attributes попадают в поиск.
type Publication struct {
	CategoryName string                     `json:"categoryName"`
	Version      int64                      `json:"version"`
	CategoryID   string                     `json:"categoryId"`
	CategoryPath []string                   `json:"categoryPath"`
	Attributes   map[string]json.RawMessage `json:"attributes"`
	Issues       []Issue                    `json:"issues"`
	Published    bool                       `json:"published"`
}

// FullName сохраняет самостоятельное имя для публикации и поискового фасета.
func (c Category) FullName() string {
	if c.Title != "" {
		return c.Title
	}
	return c.Name
}

// ValidCode исключает пути и служебные символы в именах динамических полей.
func ValidCode(code string) bool { return codePattern.MatchString(code) }

// Validate проверяет дерево и согласованность глобальных кодов.
func (d Definition) Validate() error {
	if d.Version < 1 || d.Version > MaxTaxonomyVersion || len(d.Categories) == 0 {
		return ErrInvalid.WithMessage("нужны положительная версия и категории")
	}
	ids := map[string]bool{}
	slugs := map[string]bool{}
	fullNames := map[string]bool{}
	codes, err := d.validateSchemas()
	if err != nil {
		return err
	}
	var walk func([]Category, int) error
	walk = func(categories []Category, depth int) error {
		if depth > MaxDepth && len(categories) > 0 {
			return ErrInvalid.WithMessage("дерево слишком глубокое")
		}
		names := map[string]bool{}
		for _, c := range categories {
			if c.Moderation != "" && c.Moderation != "auto" && c.Moderation != "manual" && c.Moderation != "forbidden" {
				return ErrInvalid.WithMessage("неизвестная политика модерации")
			}
			invalidName := strings.TrimSpace(c.Name) == "" || utf8.RuneCountInString(c.Name) > 256
			if !categoryIDPattern.MatchString(c.ID) || ids[c.ID] || invalidName {
				return ErrInvalid.WithMessage("неверный или повторный id категории, пустое или длинное имя")
			}
			if !slugPattern.MatchString(c.Slug) || len(c.Slug) > 128 || slugs[c.Slug] {
				return ErrInvalid.WithMessage("неверный или повторный slug категории: " + c.ID)
			}
			if names[c.Name] {
				return ErrInvalid.WithMessage("повтор имени соседних категорий: " + c.ID)
			}
			if c.Title != "" && (strings.TrimSpace(c.Title) == "" ||
				utf8.RuneCountInString(c.Title) > 256 || c.Title == c.Name) {
				return ErrInvalid.WithMessage("неверное полное имя категории: " + c.ID)
			}
			if fullNames[c.FullName()] {
				return ErrInvalid.WithMessage("повтор полного имени категории: " + c.ID)
			}
			names[c.Name] = true
			slugs[c.Slug] = true
			fullNames[c.FullName()] = true
			ids[c.ID] = true
			if len(c.Children) > 0 {
				if c.Schema != "" {
					return ErrInvalid.WithMessage("у раздела не может быть схемы")
				}
			} else if _, exists := d.Schemas[c.Schema]; !exists {
				return ErrInvalid.WithMessage("неизвестная схема листа: " + c.ID)
			}
			if err := walk(c.Children, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(d.Categories, 1); err != nil {
		return err
	}
	if len(ids) > MaxCategories || len(codes) > MaxAttributes {
		return ErrInvalid.WithMessage("слишком много категорий или кодов атрибутов")
	}
	return nil
}

func (a Attribute) validate() error {
	if !ValidCode(a.Code) || strings.TrimSpace(a.Name) == "" || utf8.RuneCountInString(a.Name) > 256 {
		return ErrInvalid.WithMessage("неверный код или имя атрибута")
	}
	if a.Dictionary != "" && (!ValidCode(a.Dictionary) || (a.Type != "enum" && a.Type != "string")) {
		return ErrInvalid.WithMessage("словарь допустим только у enum и string")
	}
	if err := a.validateValuesMeta(); err != nil {
		return err
	}
	switch a.Type {
	case "enum":
		if a.Dictionary == "" || len(a.Values) > 256 || a.Unit != "" {
			return ErrInvalid
		}
		seen := map[string]bool{}
		for _, v := range a.Values {
			if strings.TrimSpace(v) == "" || utf8.RuneCountInString(v) > 256 || seen[v] {
				return ErrInvalid
			}
			seen[v] = true
		}
	case "number", "string", "boolean":
		if len(a.Values) != 0 || (a.Type != "number" && a.Unit != "") || utf8.RuneCountInString(a.Unit) > 64 {
			return ErrInvalid
		}
	default:
		return ErrInvalid.WithMessage("неизвестный тип атрибута")
	}
	return nil
}

func (d Definition) validateSchemas() (map[string]Attribute, error) {
	fields := map[string]Attribute{}
	for _, a := range d.Attributes {
		if err := a.validate(); err != nil {
			return nil, err
		}
		if _, exists := fields[a.Code]; exists {
			return nil, ErrInvalid.WithMessage("повтор кода атрибута")
		}
		fields[a.Code] = a
	}
	for key, schema := range d.Schemas {
		if !ValidCode(key) || len(schema.Attributes) == 0 {
			return nil, ErrInvalid.WithMessage("пустая или неверная схема")
		}
		seen := map[string]bool{}
		for _, code := range schema.Attributes {
			if _, exists := fields[code]; !exists || seen[code] {
				return nil, ErrInvalid.WithMessage("неверная ссылка схемы")
			}
			seen[code] = true
		}
		optional := map[string]bool{}
		for _, code := range schema.Optional {
			if !seen[code] || optional[code] {
				return nil, ErrInvalid.WithMessage("неверный optional")
			}
			optional[code] = true
		}
	}
	return fields, nil
}

// Fields возвращает глобальные определения для поиска по нескольким категориям.
func (d Definition) Fields() map[string]Attribute {
	out := map[string]Attribute{}
	for _, a := range d.Attributes {
		out[a.Code] = a
	}
	return out
}

// CheckEvolution сохраняет адреса ссылок и типы существующих динамических полей.
// Глобальные коды сохраняются даже вне схем, чтобы их нельзя было переиспользовать.
func (d Definition) CheckEvolution(old Definition) error {
	previousSlugs := old.categorySlugs()
	nextSlugs := d.categorySlugs()
	owners := map[string]string{}
	for id, slug := range previousSlugs {
		if slug != "" {
			owners[slug] = id
		}
	}
	for id, slug := range nextSlugs {
		if previous := previousSlugs[id]; previous != "" && previous != slug {
			return ErrConflict.WithMessage("нельзя изменить slug категории: " + id)
		}
		if owner, exists := owners[slug]; exists && owner != id {
			return ErrConflict.WithMessage("нельзя передать slug другой категории: " + id)
		}
	}
	fields := d.Fields()
	for code, previous := range old.Fields() {
		next, ok := fields[code]
		if !ok || next.Type != previous.Type || next.Unit != previous.Unit ||
			next.Dictionary != previous.Dictionary {
			return ErrConflict.WithMessage("нельзя удалить код или изменить тип, единицу, словарь: " + code)
		}
	}
	return nil
}

func (d Definition) categorySlugs() map[string]string {
	out := map[string]string{}
	var walk func([]Category)
	walk = func(categories []Category) {
		for _, category := range categories {
			out[category.ID] = category.Slug
			walk(category.Children)
		}
	}
	walk(d.Categories)
	return out
}

// Category возвращает полную схему и путь от корня до узла.
func (d Definition) Category(id string) (CategorySchema, []string, bool) {
	var find func([]Category, []string) (Category, []string, bool)
	find = func(categories []Category, path []string) (Category, []string, bool) {
		for _, c := range categories {
			next := append(append([]string{}, path...), c.ID)
			if c.ID == id {
				return c, next, true
			}
			if found, p, ok := find(c.Children, next); ok {
				return found, p, true
			}
		}
		return Category{}, nil, false
	}
	category, path, ok := find(d.Categories, nil)
	result := CategorySchema{Category: category, Attributes: []SchemaAttribute{}}
	fields := d.Fields()
	schema := d.Schemas[category.Schema]
	for _, code := range schema.Attributes {
		result.Attributes = append(result.Attributes, SchemaAttribute{
			Attribute: fields[code],
			Required:  !slices.Contains(schema.Optional, code),
		})
	}
	return result, path, ok
}

// Check проверяет все поля, собирая ошибки в детерминированном порядке.
func (d Definition) Check(card *Card) Publication {
	p := Publication{
		Version:    d.Version,
		Attributes: map[string]json.RawMessage{},
		Issues:     []Issue{},
	}
	if card == nil {
		p.Issues = append(p.Issues, Issue{Code: "taxonomy.card_missing"})
		return p
	}
	p.CategoryID = card.CategoryID
	category, path, ok := d.Category(card.CategoryID)
	if !ok {
		p.Issues = append(p.Issues, Issue{Code: "taxonomy.category_unknown"})
		return p
	}
	p.CategoryPath = path
	p.CategoryName = category.FullName()
	fields := map[string]SchemaAttribute{}
	for _, a := range category.Attributes {
		fields[a.Code] = a
		raw, exists := card.Attributes[a.Code]
		if !exists {
			if a.Required {
				p.Issues = append(p.Issues, Issue{Code: "taxonomy.required", Attribute: a.Code})
			}
			continue
		}
		issue := a.check(raw)
		if issue != "" {
			p.Issues = append(p.Issues, Issue{Code: issue, Attribute: a.Code})
		} else {
			p.Attributes[a.Code] = append(json.RawMessage(nil), raw...)
		}
	}
	for code := range card.Attributes {
		if _, exists := fields[code]; !exists {
			p.Issues = append(p.Issues, Issue{Code: "taxonomy.attribute_unknown", Attribute: code})
		}
	}
	sort.Slice(p.Issues, func(i, j int) bool { return p.Issues[i].Attribute < p.Issues[j].Attribute })
	if !card.Published {
		p.Issues = append(p.Issues, Issue{Code: "taxonomy.unpublished"})
	}
	p.Published = card.Published && len(p.Issues) == 0
	if !p.Published {
		p.Attributes = map[string]json.RawMessage{}
	}
	return p
}

func (a Attribute) check(raw json.RawMessage) string {
	if strings.TrimSpace(string(raw)) == "null" || !json.Valid(raw) {
		return "taxonomy.type"
	}
	switch a.Type {
	case "string", "enum":
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return "taxonomy.type"
		}
		if strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > 1024 {
			return "taxonomy.type"
		}
		if a.Type == "enum" {
			for _, allowed := range a.Values {
				if value == allowed {
					return ""
				}
			}
			return "taxonomy.enum_value"
		}
	case "boolean":
		var value bool
		if json.Unmarshal(raw, &value) != nil {
			return "taxonomy.type"
		}
	case "number":
		var value any
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil {
			return "taxonomy.type"
		}
		number, ok := value.(json.Number)
		if !ok {
			return "taxonomy.type"
		}
		f, err := number.Float64()
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return "taxonomy.type"
		}
	}
	return ""
}
