# Словари для поставки 10

Здесь нет новых словарных записей. Имена пулов — ссылки; значения и их
нормализацию поставляет uno-reference-data. Без регистрации всех ссылок
`apply` отклоняет публикацию одной транзакцией. `taxonomy check` проверяет
структуру без подключения к реестру.

Из 182 строковых атрибутов 21 без словаря:
8 активных идентификаторов/свободных текстов и 13 исторических неиспользуемых строк.
Все активные фильтруемые строки имеют dictionary; значения не зашиты в YAML.

Всего 196 различных ссылок на пулы, из них 111 новых относительно поставки 9.
18 контекстных правил compatible_model заменяют общий источник по направлениям.
В books отдельное правило book_genre использует тот же пул жанров без сравнения с именем листа.
Ссылки соответствуют только сохранившимся направлениям и используемым в них полям.

## Уже есть внешний файл пула, требуется проверить поставку и kind

Наличие файла в pools не подтверждает его импорт в стенд или пригодность
всех записей новому объекту. Эти источники читаются, не изменяются.

| Код словаря | Требуемый kind |
|---|---|
| `auto_model` | open |
| `book_language` | open |
| `book_series` | open |
| `connection` | open |
| `fastener_standard` | open |
| `food_diet` | open |
| `food_flavor` | open |
| `industrial_standard` | open |
| `keyboard_layout` | open |
| `medical_size` | open |
| `model` | open |
| `pet_breed` | open |
| `plant_species` | open |
| `plant_variety` | open |
| `surface_type` | open |
| `tool_mount` | open |

## Требуются новые пулы / отдельная подготовка

| Код словаря | Требуемый kind |
|---|---|
| `active_ingredient` | open |
| `animal_breed` | open |
| `animal_sex` | open |
| `animal_species` | open |
| `audience_age_group` | open |
| `bag_construction` | open |
| `bag_fastener` | open |
| `bag_lock` | open |
| `bike_wheel_mount` | open |
| `board_game_kind` | open |
| `body_area` | open |
| `book_edition_form` | open |
| `book_edition_status` | open |
| `camera_mount` | open |
| `certificate_program` | open |
| `child_seat_standard` | open |
| `clothing_use` | open |
| `collectible_condition` | open |
| `compat_accessories` | open |
| `compat_appliances` | open |
| `compat_auto` | open |
| `compat_baby` | open |
| `compat_computers` | open |
| `compat_electronics` | open |
| `compat_furniture` | open |
| `compat_garden` | open |
| `compat_health` | open |
| `compat_industry` | open |
| `compat_jewelry` | open |
| `compat_kitchen` | open |
| `compat_music` | open |
| `compat_outdoors` | open |
| `compat_pets` | open |
| `compat_renovation` | open |
| `compat_tools` | open |
| `compat_toys` | open |
| `compatible_model` | open |
| `component_package` | open |
| `compression_class` | open |
| `computer_form_factor` | open |
| `control_method` | open |
| `diaper_size_label` | open |
| `electrical_safety` | open |
| `fastener_drive` | open |
| `filling_material` | open |
| `film_format` | open |
| `film_genre` | open |
| `flower_preservation` | open |
| `food_form` | open |
| `food_processing` | open |
| `fuel_type` | open |
| `fungus_species` | open |
| `furniture_placement` | open |
| `furniture_use` | open |
| `garment_neckline` | open |
| `greeting_occasion` | open |
| `hair_attachment` | open |
| `hair_effect` | open |
| `handedness` | open |
| `handwear_size_label` | open |
| `headwear_size_label` | open |
| `interface_standard` | open |
| `ip_rating` | open |
| `lens_replacement` | open |
| `license_platform` | open |
| `microphone_pattern` | open |
| `mount_standard` | open |
| `music_genre` | open |
| `paint_finish` | open |
| `perfume_concentration` | open |
| `pet_life_stage` | open |
| `pharmaceutical_form` | open |
| `plant_growth_stage` | open |
| `plant_light` | open |
| `poster_theme` | open |
| `ppe_standard` | open |
| `recording_language` | open |
| `recording_medium` | open |
| `ring_size_label` | open |
| `shoe_back` | open |
| `shoe_heel` | open |
| `shoe_insole` | open |
| `shoe_toe` | open |
| `shoe_use` | open |
| `shoe_width` | open |
| `size_clothing_label` | open |
| `size_footwear_label` | open |
| `size_standard` | open |
| `target_gender` | closed |
| `textile_weave` | open |
| `vehicle_drive` | open |
| `vehicle_transmission` | open |
| `vessel_coating` | open |
| `video_game_genre` | open |
| `watch_movement` | open |

`target_gender` — новый closed-пул пола без возрастных категорий. Примеры и
поисковые выражения используют уже подтверждённые обозначения прежнего gender;
они не создают записи нового пула. Старый gender и его словарь остаются для истории.

`compatible_model` означает только модель; стандарты посадки — `mount_standard`.
Контекстные `compat_*` не должны содержать голые стандарты вместо обозначений моделей.
`model` нормализует обозначения изготовителя, не является универсальным ID изделия.
Комбинация замка с сервисным досмотром разбирается на механизм bag_lock и tsa_lock.

## Активные строки без словаря

| Код | Причина |
|---|---|
| `gtin` | Точный идентификатор торговой единицы; не фасет и не кандидат нормализации. |
| `part_number` | Точный артикул производителя; регистр и символы сохраняются, словарь не меняет идентификатор. |
| `equipment` | Дословное описание заводского комплекта; произвольный текст, не фасет. |
| `ingredients` | Дословный состав и порядок ингредиентов с маркировки; произвольный текст, не фасет. |
| `fertilizer_composition` | Дословная формула и массовые доли питательных веществ; свободный текст маркировки, не фасет. |
| `book_isbn` | Точный идентификатор книжного издания; проверяется как маркировка, а не словарное название. |
| `coin_inscription` | Дословная надпись как часть изображения монеты; свободный текст, не фасет. |
| `issn` | Идентификатор периодического издания; хранится точно, не нормализуется словарём. |

Их facet=false. В исторических кодах не собираются новые кандидаты.
Нормализация каталожных строк по будущим пулам и фактическое обучение подсказок
потребуют отдельного прогона после установки поставки реестра.

## Закрытые словари перед HTTP-публикацией (доработка 7)

`resolveDictionaries` требует существования всех 196 ссылок с правильным kind: 17 closed и 179 open. В `deploy/reference` есть файлы 19 из 196 словарей (16 closed и 3 open); для 177 ссылок файлов здесь нет. Файл на диске не доказывает импорт на стенд.

Для каждого closed нужен минимум один confirmed. Дополнительно все ключи `valuesMeta` и непустых `searchTermsByValue` должны быть точными confirmed-значениями. Для текущих файлов это: все 18 цветов из color/entries.json; «Женский», «Мужской» в gender и target_gender; «Демисезон», «Зима», «Лето» в season. Остальные closed должны содержать как минимум одно подтверждённое значение; для полноценного ассортимента импортируется весь проверенный пул. Новые значения здесь не сочиняются.

| Closed | Confirmed в deploy/reference |
|---|---:|
| `color` | 18 |
| `gender` | 5 |
| `season` | 4 |
| `body_material` | 7 |
| `cookware_material` | 10 |
| `textile_material` | 11 |
| `jewelry_metal` | 8 |
| `book_binding` | 5 |
| `storage_type` | 3 |
| `screen_matrix` | 9 |
| `battery_chemistry` | 6 |
| `firmness` | 4 |
| `size_clothing` | 8 |
| `size_shoes_eu` | 27 |
| `size_kids_height` | 21 |
| `size_ring` | 21 |
| `target_gender` | нет файла |

**Блокер данных:** `target_gender` отсутствует в deploy/reference и требует отдельного лицензионно корректного пула uno-reference-data; в частности подтверждённых «Женский» и «Мужской» для заданных поисковых выражений. Для унисекс-ассортимента также требуется «Унисекс». Значения уже встречаются в прежнем gender, но заголовок другого словаря и его записи автоматически не возникают.

Пустые открытые словари допустимы для таксономии, однако штатный `/reference/import` требует непустые entries каждого поставляемого словаря. Значит, 177 отсутствующих источников нужно подготовить отдельной поставкой с настоящими записями, не заглушками. Список новых пулов выше остаётся актуальным.
