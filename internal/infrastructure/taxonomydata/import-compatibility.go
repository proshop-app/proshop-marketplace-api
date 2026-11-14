package taxonomydata

// sequentialImportSchemas сохраняет только ключи схем листьев поставки 9,
// необходимые между загрузкой новых schemas.yaml и заменой categories.yaml.
// Новые листья на эти схемы не ссылаются; убрать можно после миграции всех стендов.
var sequentialImportSchemas = map[string]string{
	"phone_accessory":   "Чехлы и аксессуары телефона прежних категорий",
	"baby_hygiene":      "Гигиена малышей прежних категорий",
	"clothing_kids":     "Детская одежда прежних категорий",
	"gaming":            "Игровая техника прежних категорий",
	"kitchen_appliance": "Кухонная техника прежних категорий",
	"workwear":          "Спецодежда прежних категорий",
	"outerwear_kids":    "Детская верхняя одежда прежних категорий",
}
