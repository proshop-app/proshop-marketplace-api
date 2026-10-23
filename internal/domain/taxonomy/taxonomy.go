// Package taxonomy — строгие схемы карточек площадки, которыми управляет оператор.
package taxonomy

import (
	"encoding/json"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// MaxAttributes ограничивает число динамических полей индекса.
	MaxAttributes = 128
	// MaxCategories ограничивает размер операторского дерева.
	MaxCategories = 1000
	// MaxDepth ограничивает глубину дерева.
	MaxDepth = 16
)

var codePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Attribute — скалярное поле; код имеет один тип и единицу во всей площадке.
type Attribute struct {
	Code     string   `json:"code" yaml:"code"`
	Name     string   `json:"name" yaml:"name"`
	Type     string   `json:"type" yaml:"type"`
	Required bool     `json:"required" yaml:"required"`
	Values   []string `json:"values,omitempty" yaml:"values,omitempty"`
	Unit     string   `json:"unit,omitempty" yaml:"unit,omitempty"`
	Facet    bool     `json:"facet" yaml:"facet"`
}

// Category — узел дерева; схема полная, наследования атрибутов нет.
type Category struct {
	ID         string      `json:"id" yaml:"id"`
	Name       string      `json:"name" yaml:"name"`
	Attributes []Attribute `json:"attributes" yaml:"attributes"`
	Children   []Category  `json:"children" yaml:"children"`
}

// Definition — неизменяемый снимок версии таксономии.
type Definition struct {
	Version    int64      `json:"version" yaml:"version"`
	Categories []Category `json:"categories" yaml:"categories"`
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

// ValidCode исключает пути и служебные символы в именах динамических полей.
func ValidCode(code string) bool { return codePattern.MatchString(code) }

// Validate проверяет дерево и согласованность глобальных кодов.
func (d Definition) Validate() error {
	if d.Version < 1 || len(d.Categories) == 0 {
		return ErrInvalid.WithMessage("нужны положительная версия и категории")
	}
	ids := map[string]bool{}
	codes := map[string]Attribute{}
	var walk func([]Category, int) error
	walk = func(categories []Category, depth int) error {
		if depth > MaxDepth && len(categories) > 0 {
			return ErrInvalid.WithMessage("дерево слишком глубокое")
		}
		for _, c := range categories {
			invalidName := strings.TrimSpace(c.Name) == "" || utf8.RuneCountInString(c.Name) > 256
			if !ValidCode(c.ID) || ids[c.ID] || invalidName {
				return ErrInvalid.WithMessage("неверный или повторный id категории, пустое или длинное имя")
			}
			ids[c.ID] = true
			local := map[string]bool{}
			for _, a := range c.Attributes {
				if err := a.validate(); err != nil {
					return err
				}
				if local[a.Code] {
					return ErrInvalid.WithMessage("повтор кода атрибута в категории")
				}
				local[a.Code] = true
				if old, ok := codes[a.Code]; ok && !sameField(old, a) {
					return ErrInvalid.WithMessage("код атрибута имеет разные определения")
				}
				codes[a.Code] = a
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
	switch a.Type {
	case "enum":
		if len(a.Values) == 0 || len(a.Values) > 256 || a.Unit != "" {
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

func sameField(a, b Attribute) bool {
	return a.Type == b.Type && a.Unit == b.Unit && a.Name == b.Name && a.Facet == b.Facet &&
		reflect.DeepEqual(a.Values, b.Values)
}

// Fields — глобальные определения кодов, обязательность зависит от категории.
func (d Definition) Fields() map[string]Attribute {
	out := map[string]Attribute{}
	var walk func([]Category)
	walk = func(categories []Category) {
		for _, c := range categories {
			for _, a := range c.Attributes {
				out[a.Code] = a
			}
			walk(c.Children)
		}
	}
	walk(d.Categories)
	return out
}

// CheckEvolution защищает существующие динамические поля от смены типа и единицы.
// Коды сохраняются хотя бы в одной схеме, чтобы удалённый код нельзя было переиспользовать.
func (d Definition) CheckEvolution(old Definition) error {
	fields := d.Fields()
	for code, previous := range old.Fields() {
		next, ok := fields[code]
		if !ok || next.Type != previous.Type || next.Unit != previous.Unit {
			return ErrConflict.WithMessage("нельзя удалить глобальный код, изменить его тип или единицу: " + code)
		}
	}
	return nil
}

// Category возвращает полную схему и путь от корня до узла.
func (d Definition) Category(id string) (Category, []string, bool) {
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
	return find(d.Categories, nil)
}

// Check проверяет все поля, собирая ошибки в детерминированном порядке.
func (d Definition) Check(card *Card) Publication {
	p := Publication{Version: d.Version, Attributes: map[string]json.RawMessage{}, Issues: []Issue{}}
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
	p.CategoryName = category.Name
	fields := map[string]Attribute{}
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
