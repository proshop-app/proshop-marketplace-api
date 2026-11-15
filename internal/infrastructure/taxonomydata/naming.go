package taxonomydata

// У парфюмерии отдельная покупательская аудитория содержит один физический вид.
var singleChildAudienceExceptions = map[string]string{
	"beauty.perfume-women":    "Аудитория парфюмерии; концентрация — атрибут единственного вида.",
	"beauty.perfume-men":      "Аудитория парфюмерии; концентрация — атрибут единственного вида.",
	"beauty.perfume-children": "Аудитория парфюмерии; концентрация — атрибут единственного вида.",
}

// Исключения короткого имени не меняют уникальность полного title.
var duplicateLeafNameExceptions = map[string]map[string]string{
	"трубы": {
		"music.trumpets":   "Духовой музыкальный инструмент; родитель — музыкальные инструменты.",
		"renovation.pipes": "Сантехническая труба; родитель — сантехника и отопление.",
	},
}
