package opensearch

// indexBody — настройки и схема поколения индекса. Схема строгая: поле,
// которого нет в схеме, — ошибка записи, а не тихое расширение индекса.
func indexBody(shards, replicas int) map[string]any {
	keyword := map[string]any{"type": "keyword"}
	stored := map[string]any{"type": "keyword", "index": false}
	text := map[string]any{"type": "text", "analyzer": "offer_text"}

	return map[string]any{
		"settings": map[string]any{
			"number_of_shards":   shards,
			"number_of_replicas": replicas,
			"analysis": map[string]any{
				"analyzer": map[string]any{
					// русская морфология с латиницей: каталог универсален, без словарей тематики
					"offer_text": map[string]any{
						"type":      "custom",
						"tokenizer": "standard",
						"filter":    []string{"lowercase", "russian_stem_filter"},
					},
				},
				"filter": map[string]any{
					"russian_stem_filter": map[string]any{"type": "stemmer", "language": "russian"},
				},
			},
		},
		"mappings": map[string]any{
			"dynamic": "strict",
			"properties": map[string]any{
				"offerId":     keyword,
				"tenantId":    keyword,
				"productId":   keyword,
				"sellerSlug":  keyword,
				"sellerName":  map[string]any{"type": "text", "analyzer": "offer_text", "fields": map[string]any{"raw": keyword}},
				"name":        map[string]any{"type": "text", "analyzer": "offer_text", "fields": map[string]any{"raw": keyword}},
				"description": text,
				"image":       stored,
				"url":         stored,
				"sku":         keyword,
				"categories": map[string]any{
					"properties": map[string]any{
						"id":   keyword,
						"name": text,
						"url":  stored,
					},
				},
				"tags":              keyword,
				"variantsId":        keyword,
				"price":             map[string]any{"type": "long"},
				"basePrice":         map[string]any{"type": "long"},
				"available":         map[string]any{"type": "integer"},
				"inStock":           map[string]any{"type": "boolean"},
				"visible":           map[string]any{"type": "boolean"},
				"listed":            map[string]any{"type": "boolean"},
				"checkoutAvailable": map[string]any{"type": "boolean"},
				"updatedAt":         map[string]any{"type": "date"},
			},
		},
	}
}
