# Общий реестр: marketplace — потребитель platform

Все определения атрибутов, открытые и закрытые значения, варианты написаний,
статусы, ревизии, журнал и поставки принадлежат MongoDB `platform` сервера
`proshop-server`. Marketplace хранит дерево категорий и производные снимки
схем в PostgreSQL. Его историческая миграция `00027` не переписывается и
не удаляет старые данные: runtime не читает и не меняет `reference.*`.

## Контракт клиента

Адрес — `PLATFORM_API_URL`, внутренний префикс
`/api/v1/internal/reference/{dict}/`. Чтение использует
`Authorization: Bearer <REFERENCE_READ_TOKEN>`, запись — отдельный
`REFERENCE_WRITE_TOKEN`. При записи backend передаёт `X-Reference-Actor`
после своей проверки оператора. Входящий браузерный заголовок не доверяется.
Токены не следуют редиректам; upstream 5xx, недоступность и негодный ответ
выходят как 503 `reference.unavailable`.

Существующие 9 маршрутов marketplace `/internal/reference/{dict}/...`
сохранены как тонкий proxy к владельцу:

| Метод и суффикс | Доступ |
| --- | --- |
| `GET snapshot?since=&scope=global` | читатель или оператор |
| `POST resolve` | читатель или оператор |
| `GET entries?status=&limit=&offset=` | читатель или оператор |
| `GET entries/{id}` | читатель или оператор |
| `POST entries` | оператор |
| `PATCH entries/{id}` | оператор |
| `POST entries/{id}/merge` | оператор |
| `POST entries/{id}/aliases` | оператор |
| `DELETE aliases/{id}` | оператор |

JSON, статусы, `ETag` и `If-Match` сохраняются. Полная схема —
`api/openapi.yaml`. `reference.key_taken` и `reference.entry_changed`
совместимы с `REFERENCE_KEY_TAKEN` и `REFERENCE_ENTRY_CHANGED` прежнего API.
Правила нормализации, статусов, занятости ключей и ревизий проверяет владелец;
marketplace не воспроизводит локальные мутации. Query ограничен 64 КиБ,
тело `resolve` — 4 КиБ, остальных мутаций — 64 КиБ.

Наблюдаемый `ModelKeys` запрашивает общий `brand/resolve` с прежним сроком
300 мс. При отказе ключ не назначается, публикация товара не блокируется.
Стабильные идентификаторы импортированных записей сохраняются владельцем.

## Закрытые значения таксономии

`GET /api/v1/internal/reference/{dict}/values` возвращает:

```json
{
  "schemaVersion": 1,
  "dictionary": "a1",
  "kind": "closed",
  "revision": 12,
  "items": [{"id": "stable-id", "value": "Значение", "order": 0, "meta": {"hex": "#ffffff"}}]
}
```

По коду каждого `enum` выполняется чтение владельца. Порядок и `valuesMeta`
гидратируются в ответе схемы; PostgreSQL-снимок не становится запасным
источником. Это действует для HTTP таксономии, проверки публикации проекций,
фильтров выдачи, вариантов и revalidation при поставках дерева.
Недоступный или пустой общий словарь — явный отказ, а не принятие старого
значения или превращение перечисления в свободный ввод.

Конструкторы клиента и UoW не обращаются к сети. Marketplace запускается и
проходит собственную readiness без вызова server reference API. На сервере
первое чтение marketplace taxonomy выполняется лениво после bootstrap, чтобы
избежать цикла server → marketplace → server до открытия HTTP.

Команды `taxonomy apply|rollback|load` кроме `DATABASE_URL` требуют
`PLATFORM_API_URL` и `REFERENCE_READ_TOKEN`: перепроверка товаров использует
текущие общие значения. `taxonomy check` остаётся офлайн.

## Поставки общего реестра

В marketplace больше нет встроенных списков и применения поставок к PG.
Прежний формат каталога `manifest.json` + `entries.json` сохранён для клиента:

```sh
go run ./cmd/marketplace reference check --dir path/to/delivery
go run ./cmd/marketplace reference apply --dir path/to/delivery
go run ./cmd/marketplace reference rollback --dir path/to/delivery
```

`check` проверяет JSON, контрольную сумму и счётчики без сети и окружения
сервиса. Семантику применения и отката проверяет владелец.
`apply` и `rollback` требуют только `PLATFORM_API_URL` и
`REFERENCE_WRITE_TOKEN`, не открывают PostgreSQL и не публикуют Kafka-события.
Запрос — `POST /api/v1/internal/reference/{dict}/deliveries/{apply|rollback}`:

```json
{"release": 1, "checksum": "sha256 исходного entries.json", "entriesJson": "[точный исходный JSON]"}
```

`entriesJson` содержит исходный UTF-8 текст со всеми пробелами и переводами
строк, поэтому владелец проверяет тот же SHA-256. Имена файлов и локальные
пути владельцу не передаются. Поставки и `catalog.reference.changed` публикует
только server; повтор, история, защита правок оператора и откат принадлежат ему.

## Старые незавершённые задачи PG outbox

Прежде переключения остановить старый marketplace emitter и импортировать
реестр в platform с сохранением идентификаторов. Новый consumer исключает
старый `kind=reference.changed` из обычного разбора: он не выдаёт старое
событие за событие нового владельца и не подтверждает его ложным `complete`.
Явная команда сохраняет payload и парковку:

```sh
DATABASE_URL=... go run ./cmd/marketplace reference archive-pending --out /path/new-export.json
```

Команда создаёт новый файл с режимом `0600`, пишет и синхронизирует экспорт,
затем помечает только экспортированные незавершённые задачи:
`last_error=ownership-transferred`, `next_at=infinity`. `done_at` и payload
остаются неизменными. Уже помеченное повторно не экспортируется. При сбое
файла парковки нет; при сбое парковки экспорт сохраняется, повтор выполняется
с новым именем файла. Другие виды outbox не затрагиваются. Автоматического
удаления PG-реестра или данных outbox нет.
