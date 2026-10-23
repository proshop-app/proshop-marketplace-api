# proshop-marketplace

Backend федеративного маркетплейса платформы Proshop: единый каталог
предложений всех участвующих магазинов, вход покупателя по звонку, корзина,
доставка до пунктов выдачи, checkout с оплатой одним платежом и заказы у
магазинов. Go 1.23, слоистая архитектура по DDD, PostgreSQL, OpenSearch,
Kafka, Redis. Проект — `proshop-server/docs/marketplace-mvp.md`, правила кода —
[CLAUDE.md](CLAUDE.md), HTTP — [api/openapi.yaml](api/openapi.yaml), стык с
сервером — [docs/server-contract.md](docs/server-contract.md).

## Запуск

Нужны Docker с Compose и Go 1.23.2. Глобально ничего ставить не нужно.

```bash
make dev
```

Команда поднимает PostgreSQL 16, OpenSearch 2.19, Kafka 3.9 в режиме KRaft и
Redis, применяет миграции и запускает сервис на `:8080` с перезапуском при
правках. Порты compose на хосте: PostgreSQL `25432`, OpenSearch `29200`,
Kafka `29094`, Redis `26379`; любой меняется переменной
`MARKETPLACE_<СЕРВИС>_PORT`.

| Команда | Что делает |
|---|---|
| `make dev` | инфраструктура, миграции, сервис с перезапуском (air) |
| `make test` | юнит-тесты с `-race` и сторож слоистости |
| `make test-integration` | интеграционные тесты на compose (поднимает его сам) |
| `make lint` | golangci-lint v2.3.1 с `depguard` по слоям |
| `make migrate` | миграции к `DATABASE_URL` (`go run ./cmd/migrate up`; ещё `down`, `status`) |
| `make taxonomy-load FILE=…` | загрузить версию схем площадки из YAML |
| `make reindex` | перестроить индекс предложений из проекции |
| `make refresh-pickup-points` | обновить справочники пунктов выдачи подключённых перевозчиков |
| `make build` | `bin/marketplace`, `bin/migrate`, `bin/taxonomy` |
| `make image` | образ: сборка на `golang:1.23.2-alpine`, запуск на `scratch` |
| `make infra-up`, `make infra-down` | только инфраструктура |

golangci-lint и air запускаются через `go run` с версиями из `Makefile`,
goose встроен в `cmd/migrate` и закреплён в `go.mod`. `GOTOOLCHAIN=local`:
более новый toolchain не скачивается.

## Окружение

Процесс проверяет окружение на старте и не поднимается, пока не заданы
обязательные переменные; все ошибки выводятся разом. `make dev` задаёт
значения для compose сам (вход по звонку — в песочнице, API сервера —
`http://localhost:5001`), полный список с умолчаниями — `.env.example`.

| Переменная | Обязательна | По умолчанию | Назначение |
|---|---|---|---|
| `DATABASE_URL` | да | — | PostgreSQL `postgres://…` |
| `DATABASE_MAX_CONNS` | | `10` | размер пула, 1–1000 |
| `OPENSEARCH_URLS` | да | — | адреса кластера через запятую |
| `OPENSEARCH_USERNAME`, `OPENSEARCH_PASSWORD` | | — | задаются парой |
| `OPENSEARCH_INSECURE_SKIP_VERIFY` | | `false` | самоподписанный сертификат локально |
| `OPENSEARCH_INDEX` | | `marketplace-offers` | алиас рабочего индекса |
| `OPENSEARCH_SHARDS`, `OPENSEARCH_REPLICAS` | | `1`, `1` | поколения индекса |
| `KAFKA_BROKERS` | да | — | брокеры установки через запятую |
| `KAFKA_TOPIC_PREFIX` | | пусто | `KAFKA_TOPIC_PREFIX` сервера: топики `<prefix>.<имя>` |
| `KAFKA_GROUP_ID` | | `marketplace` | своя группа консьюмера |
| `KAFKA_MAX_ATTEMPTS` | | `5` | неудач до парковки сообщения |
| `OPERATOR_TOKEN` | да | — | служебный токен операторских ручек, ≥ 32 символов |
| `HTTP_ADDR` | | `:8080` | адрес HTTP |
| `SHUTDOWN_TIMEOUT` | | `20s` | ожидание запросов при остановке |
| `LOG_LEVEL` | | `info` | `debug`, `info`, `warn`, `error` |
| `OUTBOX_POLL_INTERVAL`, `OUTBOX_BATCH`, `OUTBOX_PER_TENANT`, `OUTBOX_LEASE` | | `1s`, `100`, `20`, `1m` | разбор outbox |
| `RETENTION_PROCESSED_EVENTS`, `RETENTION_OUTBOX_DONE`, `RETENTION_PARKED_EVENTS`, `RETENTION_INTERVAL` | | `336h`, `72h`, `720h`, `1h` | чистка служебных записей |
| `RETENTION_CHALLENGES` | | `24h` | чистка истёкших проверок входа |
| `REDIS_URL` | да | — | Redis `redis://…`: сессии и лимиты входа |
| `SMSRU_MODE` | | `live` | `live` — шлюз sms.ru, `sandbox` — любой номер подтверждается сам, только для разработки |
| `SMSRU_API_ID` | при `live` | — | ключ sms.ru |
| `SMSRU_BASE_URL` | | `https://sms.ru` | адрес шлюза |
| `SMSRU_CALLBACK_URL` | | — | адрес оповещений о звонках; задан — регистрируется у шлюза на старте |
| `SMSRU_SANDBOX_CONFIRM_AFTER` | | `10s` | через сколько песочница считает звонок состоявшимся |
| `LOGIN_CHALLENGE_TTL`, `LOGIN_CHECK_INTERVAL` | | `5m`, `2s` | срок проверки входа, частота опроса шлюза |
| `LOGIN_PHONE_LIMIT`, `LOGIN_IP_LIMIT` | | `3/10m`, `20/1h` | попыток входа на номер и на адрес |
| `SESSION_TTL` | | `720h` | срок сессии покупателя |
| `HTTP_TRUST_PROXY` | | `false` | брать адрес клиента из `X-Forwarded-For` (за балансировщиком) |
| `PLATFORM_API_URL` | да | — | внутренний API сервера установки |
| `SERVICE_TOKEN_MARKETPLACE` | да | — | токен маркетплейса к API сервера, ≥ 32 символов |
| `PLATFORM_API_TIMEOUT` | | `5s` | ожидание ответа сервера |
| `CART_TTL` | | `720h` | срок жизни корзины без изменений |
| `QUOTE_TTL`, `QUOTE_TIMEOUT` | | `30m`, `5s` | наибольший срок расчёта доставки, ожидание магазина |
| `CDEK_CLIENT_ID`, `CDEK_CLIENT_SECRET` | | — | задаются парой; без них СДЭК не подключён |
| `CDEK_API_URL` | | `https://api.cdek.ru` | адрес API СДЭК |
| `YANDEX_DELIVERY_TOKEN` | | — | без него Яндекс Доставка не подключена |
| `YANDEX_DELIVERY_API_URL` | | `https://b2b-authproxy.taxi.yandex.net` | адрес API Яндекс Доставки |
| `PICKUP_REFRESH_INTERVAL` | | `24h` | обновление справочников пунктов выдачи |
| `OPENSEARCH_PICKUP_INDEX` | | `marketplace-pickup-points` | индекс пунктов выдачи |
| `SETTINGS_SYNC_INTERVAL` | | `5m` | сверка настроек участия магазинов с сервером |

## HTTP

Все суммы — целые копейки. Ошибки — `{"error":{"code":"<ключ>","message":"…"}}`,
код ответа выбирается по классу ошибки одной таблицей.

| Маршрут | Доступ | Что делает |
|---|---|---|
| `GET /health` | открыт | PostgreSQL, OpenSearch, Kafka: 200 или 503 |
| `GET /metrics` | открыт | метрики Prometheus |
| `GET /api/v1/catalog/offers` | открыт | выдача: `q`, `tenant`, `category`, `tag`, `priceMin`, `priceMax`, `inStock`, `sort` (`relevance`, `price_asc`, `price_desc`, `newest`), `limit` ≤ 100, `offset` |
| `GET /api/v1/catalog/offers/{offerId}` | открыт | карточка; `offerId` = `<tenantId>~<productId>` |
| `POST/GET/DELETE /api/v1/operator/session` | оператор платформы | вход по паролю, текущая сессия, выход |
| `PUT /api/v1/operator/participants/{tenantId}/delivery` | оператор | регионы ISO и перевозчики, CAS по version |
| `GET /api/v1/operator/events` | оператор | пагинация и фильтры запаркованных сообщений |
| `POST /api/v1/operator/events/{eventId}/retry` | оператор | повтор через outbox, обязательная note |
| `POST /api/v1/operator/events/{eventId}/resolve` | оператор | разбор с обязательной note и аудитом |
| `GET /api/v1/operator/participants` | оператор | участники: `limit` ≤ 200, `offset` |
| `POST /api/v1/operator/participants` | оператор | завести участника `{"tenantId","name"}`; магазин должен быть в зеркале реестра |
| `GET /api/v1/operator/participants/{tenantId}` | оператор | участник |
| `PATCH /api/v1/operator/participants/{tenantId}` | оператор | переименовать `{"name"}` |
| `POST /api/v1/operator/participants/{tenantId}/enable` | оператор | показать в каталоге |
| `POST /api/v1/operator/participants/{tenantId}/disable` | оператор | скрыть из каталога |
| `POST /api/v1/auth/call` | открыт | начать вход `{"phone"}`: номер, на который покупатель звонит |
| `POST /api/v1/auth/call/check` | открыт | состояние входа `{"challengeId"}`; после звонка ставит куку сессии |
| `POST /api/v1/auth/webhooks/smsru` | открыт | оповещение sms.ru о звонке |
| `POST /api/v1/auth/logout` | покупатель | выход |
| `GET /api/v1/me` | покупатель | текущий покупатель |
| `GET /api/v1/cart` | покупатель | корзина по продавцам |
| `POST /api/v1/cart/lines` | покупатель | добавить `{"offerId","quantity"}` |
| `PUT /api/v1/cart/lines/{offerId}` | покупатель | количество `{"quantity"}` |
| `DELETE /api/v1/cart/lines/{offerId}` | покупатель | убрать позицию |
| `POST /api/v1/cart/verify` | покупатель | перепроверить цены и наличие у магазинов |
| `GET /api/v1/delivery/carriers` | открыт | подключённые перевозчики |
| `GET /api/v1/delivery/pickup-points` | открыт | пункты: `carrier` и `bbox=minLon,minLat,maxLon,maxLat` или `city`, `limit` |
| `POST /api/v1/checkout` | покупатель | покупка из корзины; заголовок `Idempotency-Key` |
| `GET /api/v1/checkout/{checkoutId}` | покупатель | покупка |
| `POST /api/v1/checkout/{checkoutId}/delivery/quotes` | покупатель | расчёт доставки у каждого продавца `{"carrier","pickupPointCode"}` |
| `POST /api/v1/checkout/{checkoutId}/confirm` | покупатель | подтвердить: перепроверка цен, повтор расчёта у не ответивших |

Полное описание — [api/openapi.yaml](api/openapi.yaml); тест сверяет его с
таблицей маршрутов.

**Сессия покупателя** — непрозрачный токен в куке `__Host-mp_session`
(`Secure`, `HttpOnly`, `SameSite=Lax`); серверный рендер витрины может
передать его заголовком `Authorization: Bearer`. В Redis лежит только хеш
токена со сроком `SESSION_TTL`, в теле ответов токена нет. Попытки входа
ограничены на номер и на адрес клиента.
**Доступ оператора:** HTTP панели закрыт токеном `OPERATOR_TOKEN`.
Список покупок — `GET /api/v1/operator/checkouts?stuck=true`: группы,
статусы и фильтр ожидания оплаты дольше 15 минут.

Предложение видно, когда участник включён оператором, магазин active,
согласился участвовать, передал обязательные настройки доставки и опубликовал
корректную карточку площадки. Оплата идёт отдельно каждому магазину на его кассу.

После confirmed: `POST /api/v1/checkout/{id}/orders` с `{contact:{phone,name}}`
сначала обновляет настройки и доставку всех продавцов. Изменение платы —
`409 DELIVERY_CHANGED` с новым снимком: нужно подтвердить заново, заказов нет.
После согласования создаются заказы и возвращается покупка с `nextPayment`. `GET /api/v1/checkout/{id}`
показывает статусы и следующую ссылку; `POST /api/v1/checkout/{id}/groups/{tenantId}/pay`
возвращает ссылку только текущей группы до expiresAt; поздняя группа —
`409 PAYMENT_ORDER`. Во всех ответах остальные paymentUrl равны null. `GET /api/v1/orders` — кабинет владельца,
пагинация `after` по последнему ID. Возврат с кассы — `/api/v1/checkout/{id}/return`,
экран проверки; параметры кассы не меняют статуса. Все адреса покупки — только владельцу.

`MARKETPLACE_PUBLIC_URL` — обязательный внешний адрес маркетплейса для returnUrl,
`ORDERS_SYNC_INTERVAL=30s` — возобновление создания на старте и сверка по расписанию;
оплаченные группы опрашиваются раз в 10 минут. После expiresAt ссылка закрыта,
сверка продолжается до ответа магазина. Отменённые группы атомарно возвращаются в корзину.
Доставка включена от freeDeliveryFrom магазина; ниже — cost минус его subsidy,
остаток оплачивается на кассе этого магазина. Субсидия продавца не меньше
`DELIVERY_SUBSIDY_MIN_PERCENT` (по умолчанию 30%, диапазон 0–100), даже если
его maxAmount ниже. Публичный порог для админки: `GET /api/v1/delivery/settings`. Платформа денег не принимает.
Выдача и карточка принимают `deliveryCarrier` вместе с `deliveryRegion`:
`deliveryAvailable=false` оставляет товар видимым, но отмечает доставку недоступной.
Код ISO 3166-2 RU доступен в `regionCode` ПВЗ (пусто при неизвестном соответствии). Обновлённая схема индекса каталога
автоматически создаёт новое поколение; справочники ПВЗ обновляются на старте.
Полный внутренний контракт: [docs/server-contract.md](docs/server-contract.md).

## Kafka

Своя группа `KAFKA_GROUP_ID`, новая группа читает с самого раннего
сообщения, позиция фиксируется после записи итога, дедуп — `processed_events`
по `<topic>:<partition>:<offset>`.

| Топик | Откуда магазин | Что делает |
|---|---|---|
| `catalog.product.projected` | ключ `["tenantId","productId"]` и тело | снимок товара по ревизии |
| `catalog.tag.projected` | ключ `["tenantId","tagId"]` и тело | справочник тегов |
| `stock.item.updated` | заголовок `x-tenant` | свободный остаток и прайс позиции |
| `catalog.product.price-updated` | заголовок `x-tenant` | новый прайс |
| `platform.tenant.created`, `updated`, `suspended`, `resumed`, `deleted` | тело | зеркало реестра магазинов |

Сообщение без магазина, с расходящимся магазином или повреждённое уходит в
`parked_events` с метрикой `marketplace_kafka_parked_total{topic,tenant,reason}`.
Топиками владеет сервер: маркетплейс подписывается только на уже заведённые
топики и подхватывает новые без перезапуска.

## Данные

PostgreSQL, миграции goose в `internal/infrastructure/postgres/migrations`:
`tenants` (зеркало реестра), `participants` (с настройками участия и
субсидией доставки), `catalog_products`, `catalog_tags`, `offers` (остатки и
цены), `outbox`, `processed_events`, `parked_events`, `buyers`,
`phone_challenges`, `carts`, `cart_lines`, `pickup_points`, `job_leases`
(аренда фоновых задач), `checkouts`, `checkout_groups`. Индекс предложений —
OpenSearch, алиас `OPENSEARCH_INDEX` над поколениями; восстанавливается из
проекции (`make reindex`, а на старте — сам, если алиаса нет). Пункты выдачи —
индекс `OPENSEARCH_PICKUP_INDEX` с `geo_point`, наполняется из таблицы при
обновлении справочников. Redis — сессии и лимиты входа.

## Сделано на неделе 1

- Каркас по слоям §3: `pkg/money`, `pkg/ids`, `pkg/errs`, контексты
  `participant` и `catalog`, порты и fake-реализации.
- Конфиг с проверкой на старте.
- PostgreSQL на pgx: миграции, `UnitOfWork` = транзакция, условные
  переходы по статусу и версии, outbox с арендой и квотой на магазин,
  дедуп событий, отравленные сообщения, чистка по сроку.
- Kafka: консьюмеры каталога, остатков, цен и реестра; магазин из ключа,
  тела или `x-tenant`; отравленные с метрикой; коммит после записи.
- OpenSearch: индекс предложений, поиск и фильтры, карточка, перестроение
  из проекции.
- HTTP: `/health`, `/metrics`, выдача и карточка, операторские ручки участников.
- Сторож слоистости (тест и `depguard`), CI: линтер, юнит-тесты,
  интеграционные тесты на compose, сборка образа.

## Сделано на неделе 2

- `buyer`: вход по звонку через sms.ru за портом `CallCheck` (песочница для
  разработки, живой вызов — только тестом под `SMSRU_LIVE_TEST=1`), сессии и
  лимиты в Redis, покупатель в контексте запроса и журналах.
- `cart`: корзина в PostgreSQL со сроком жизни, остаток сверяется с
  проекцией, цена и наличие — с магазином через API сервера.
- `delivery`: справочники пунктов выдачи СДЭК и Яндекс Доставки (таблица и
  индекс с `geo_point`), обновление по расписанию и командой, поиск по
  области или городу; расчёт доставки у каждого продавца с субсидией и сроком
  цены.
- `checkout` до `confirmed`: из корзины, по продавцам, с ключом
  идемпотентности; денег пока нет.
- Настройки участия магазина с сервера: флаг участия — условие видимости,
  субсидия доставки — в расчёте.
- OpenAPI 3.1 всего HTTP, контракт с сервером — `docs/server-contract.md`.

## Дальше по неделям

| Неделя | Что придёт |
|---|---|
| 3 (сделано) | Независимые заказы и кассы магазинов, идемпотентная сага, опрос статусов, кабинет и панель покупок HTTP |
| 4 | сквозной прогон в kind, враждебные проверки денежного пути, правки |

## Стыки с сервером, которых пока нет

- Внутреннего API для маркетплейса (PR-M1, `docs/server-contract.md`) ещё
  нет, он придёт после PR-08d: до него флаг участия магазинов выключен,
  предложения не видны, перепроверка корзины и расчёт доставки отвечают
  «магазин не ответил».

- Сервер ещё не ставит заголовок `x-tenant` (PR-04a): пока его нет, события
  `stock.item.updated` и `catalog.product.price-updated` уходят в отравленные
  с метрикой; цена и остаток берутся из снимков `catalog.product.projected`.
- Топиков `platform.tenant.*` у сервера ещё нет (PR-08d): без них зеркало
  реестра пусто и завести участника нельзя.

## Таксономия и карточка площадки

Основное дерево каталога — категории площадки. Атрибуты магазина не
участвуют в фасетах. Публикация требует корректного блока
`product.marketplace` в `catalog.product.projected` и всех прежних условий
видимости продавца/товара. Контракт и причины отказа —
[«Карточка маркетплейса»](docs/server-contract.md#карточка-маркетплейса).
Данные оператора задаются одним YAML-документом:

```yaml
version: 1
categories:
  - id: c1
    name: Категория
    attributes:
      - code: a1
        name: Атрибут
        type: enum
        required: true
        values: [v1, v2]
        facet: true
    children:
      - id: c2
        name: Подкатегория
        attributes: []
        children: []
```

- `id` категории и `code` атрибута: `[a-z][a-z0-9_]{0,63}`, id уникален
  во всём дереве. Глубина ≤ 16, категорий ≤ 1000, глобальных кодов ≤ 128.
- Каждая категория содержит полную схему, без наследования от родителей.
  Можно выбирать любой узел. `attributes` и `children` можно опустить.
- Типы — `enum`, `number`, `string`, `boolean`. Для `enum` обязателен
  непустой словарь уникальных `values` (≤ 256); `unit` допускается только
  у `number`. `required` и `facet` по умолчанию false. Значения скалярные;
  строки непустые, до 1024 символов, числа конечные и представимые в double.
- Код глобален: имя, тип, единица, словарь и признак фасета должны совпадать
  во всех категориях одной версии; обязательность может различаться.
- Версия — положительное целое int64. Повтор той же версии с теми же
  данными безопасен; другая версия должна быть строго больше текущей.
  Порядок элементов списков — часть данных. Неизвестные поля, дубли YAML
  и дополнительные документы отклоняются.
- Тип и единица существующего кода неизменны. Код нельзя удалить из всех
  схем: это защищает от повторного использования поля с несовместимым типом
  в OpenSearch. Смена типа — новый код. Имя, словарь, обязательность и
  участие в фасетах можно менять новой версией.

Нейтральный пример — [taxonomy/example.yaml](taxonomy/example.yaml):
«Текстиль для дома», «Освещение», «Посуда». Пример не загружается автоматически.

```bash
make migrate
make taxonomy-load FILE=taxonomy/example.yaml
# эквивалентно: DATABASE_URL=… go run ./cmd/taxonomy load taxonomy/example.yaml
```

В PostgreSQL миграция `00020_taxonomy.sql` добавляет `taxonomy_versions`
(история снимков) и `taxonomy_head` (текущая версия). Карточка, исходный
блок площадки и результат проверки хранятся в JSONB `catalog_products.card`.
Загрузка, перепроверка всех карточек и outbox атомарны; чтение схемы при
приёме карточки согласовано с загрузкой блокировкой головы. Это синхронная
операция для объёма MVP, её длительность зависит от числа проекций.
Поиск обновляется асинхронно через outbox. При обновлении сервиса старый
формат индекса определяется по `_meta.taxonomySchema`: до запуска HTTP
создаётся новое поколение из проекции и переключается алиас. Товары без
блока площадки скрываются; магазины должны прислать карточки новой ревизией.

| Маршрут | Доступ | Ответ |
|---|---|---|
| `GET /api/v1/taxonomy/categories` | открыт | дерево с полной схемой каждой категории и версией |
| `GET /api/v1/taxonomy/categories/{id}/schema` | открыт | схема узла |
| `GET /api/v1/taxonomy/version` | открыт | текущая версия, до загрузки — 0 |
| `GET /api/v1/operator/taxonomy` | оператор | текущий снимок |
| `PUT /api/v1/operator/taxonomy` | оператор | загрузить один YAML, Content-Type: application/yaml, ≤ 2 MiB |
| `GET /api/v1/operator/catalog/offers/{offerId}/publication` | оператор | версия проверки, причины отказа, канонические атрибуты |

Чтение таксономии поддерживает `ETag`, `If-None-Match` → 304 и
`X-Taxonomy-Version`. Операторские ручки закрыты `Authorization: Bearer
<OPERATOR_TOKEN>`. Источник диагностики — PostgreSQL, поэтому причины видны
ещё до синхронизации индекса.

`GET /api/v1/catalog/offers`:

- `category` — категория площадки с потомками; `categories` в карточке —
  категория площадки, `categoryId` — её id; `attributes` — канонические значения.
- `attr.<код>=значение` — точное значение, повтор одного параметра означает
  OR; разные коды — AND. `attr.<код>.min` / `attr.<код>.max` — включительные
  границы для number; диапазон и точные значения одного кода не совмещаются.
- Пример: `?category=c1&attr.a1=v1&attr.a1=v2&attr.a2.min=1.5&attr.a2.max=10`.
  `inStock=false` выбирает отсутствующие позиции, `true` — имеющиеся.
  Фильтровать можно и поле без `facet`. Неизвестные коды, неверный тип,
  словарь или диапазон дают 400.
- `facets` рассчитываются по всей отфильтрованной выдаче до пагинации.
  `category`, `tenant`, `price`, `inStock` присутствуют всегда. Атрибутный
  `attr.<код>` — только при `facet=true` и покрытии ≥ 20% предложений:
  каждый пятый результат достаточно полезен для смешанного каталога, а
  единичные редкие признаки не засоряют интерфейс. Порог —
  `catalog.FacetCoveragePercent`. При пустой выдаче атрибутных фасетов нет.
- У чисел — min/max; у enum/string/boolean — значения и count (до 256
  наиболее частых значений, поведение распределённых terms-агрегаций
  OpenSearch). Count фасета — число предложений с полем.
- Динамические поля `attributes.<код>`: enum/string/boolean → keyword,
  number → double, включая первое целое значение. Числовые атрибуты имеют
  точность double; денежные поля остаются long в копейках. Границы цены
  берутся из `_source` крайних документов, без преобразования в double.

Проверки таксономии: домен — все виды отказов и эволюция схем; приложение —
идемпотентность, откат, перепроверка и восстановление публикации; HTTP —
кэширование и токен; compose — миграции, версии, блокировки, динамические
поля и агрегации. `make test` и `make test-integration` включают эти проверки
без исключений для сторожа слоёв.


## Панель оператора

Встроенный [операторский шлюз](docs/operator-gateway.md): отдельная cookie-сессия
платформы, роли, доставка участника и разбор запаркованных событий.
[Контракт для панели](docs/api/marketplace.openapi.yaml) совпадает с `api/openapi.yaml`.
Для входа нужны `OPERATOR_EMAIL`, `OPERATOR_PASSWORD_HASH`, `OPERATOR_ORIGIN` и HTTPS.
`OPERATOR_TOKEN` сохраняется для машинного доступа.
