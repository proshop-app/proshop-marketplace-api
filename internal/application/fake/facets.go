package fake

import (
	"encoding/json"
	"sort"
	"strconv"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func attributeMatches(raw json.RawMessage, filter ports.AttributeFilter) bool {
	if len(raw) == 0 {
		return false
	}
	value := string(raw)
	var text string
	isString := json.Unmarshal(raw, &text) == nil
	if isString {
		value = text
	}
	if filter.Min != "" || filter.Max != "" {
		number, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return false
		}
		if filter.Min != "" {
			min, _ := strconv.ParseFloat(filter.Min, 64)
			if number < min {
				return false
			}
		}
		if filter.Max != "" {
			max, _ := strconv.ParseFloat(filter.Max, 64)
			if number > max {
				return false
			}
		}
	}
	if len(filter.Values) == 0 {
		return true
	}
	for _, wanted := range filter.Values {
		if value == wanted {
			return true
		}
		number, err := strconv.ParseFloat(value, 64)
		other, otherErr := strconv.ParseFloat(wanted, 64)
		if !isString && err == nil && otherErr == nil && number == other {
			return true
		}
	}
	return false
}

func fakeFacets(hits []ports.OfferDocument, fields []taxonomy.Attribute) []ports.Facet {
	definitions := []taxonomy.Attribute{
		{Code: "category", Name: "Категория площадки", Type: "enum"},
		{Code: "tenant", Name: "Магазин", Type: "enum"},
		{Code: "price", Name: "Цена", Type: "number"},
		{Code: "inStock", Name: "Наличие", Type: "boolean"},
	}
	for _, a := range fields {
		a.Code = "attr." + a.Code
		definitions = append(definitions, a)
	}
	out := []ports.Facet{}
	for _, a := range definitions {
		f := ports.Facet{Code: a.Code, Name: a.Name, Type: a.Type, Unit: a.Unit, Values: []ports.FacetBucket{}}
		counts := map[string]int{}
		for _, doc := range hits {
			var value string
			switch a.Code {
			case "category":
				value = doc.CategoryID
			case "tenant":
				value = doc.TenantID
			case "price":
				value = strconv.FormatInt(doc.Price, 10)
			case "inStock":
				value = strconv.FormatBool(doc.InStock)
			default:
				raw := doc.Attributes[a.Code[len("attr."):]]
				value = string(raw)
				var text string
				if json.Unmarshal(raw, &text) == nil {
					value = text
				}
			}
			if value == "" {
				continue
			}
			f.Count++
			switch {
			case a.Code == "price":
				n := json.Number(value)
				if f.Min == nil {
					f.Min = &n
					f.Max = &n
				} else {
					min, _ := strconv.ParseInt(f.Min.String(), 10, 64)
					max, _ := strconv.ParseInt(f.Max.String(), 10, 64)
					if doc.Price < min {
						f.Min = &n
					}
					if doc.Price > max {
						f.Max = &n
					}
				}
			case a.Type == "number":
				n := json.Number(value)
				number, _ := n.Float64()
				if f.Min == nil {
					f.Min = &n
					f.Max = &n
				} else {
					min, _ := f.Min.Float64()
					max, _ := f.Max.Float64()
					if number < min {
						f.Min = &n
					}
					if number > max {
						f.Max = &n
					}
				}
			default:
				counts[value]++
			}
		}
		for value, count := range counts {
			f.Values = append(f.Values, ports.FacetBucket{Value: value, Count: count})
		}
		sort.Slice(f.Values, func(i, j int) bool { return f.Values[i].Value < f.Values[j].Value })
		out = append(out, f)
	}
	return out
}
