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
| `make taxonomy-check` / `make taxonomy-manifest` | проверить поставку и манифест YAML без БД |
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
| `SEARCH_URL` | да | — | адрес общего `proshop-search` без `/api` |
| `SEARCH_MARKETPLACE_TOKEN` | да | — | отдельный служебный токен поиска, ≥ 32 символов |
| `SEARCH_TIMEOUT`, `SEARCH_MAINTENANCE_INTERVAL` | | `35s`, `1m` | срок HTTP-запроса и период проверки пересборки |
| `OPENSEARCH_URLS` | да | — | адреса кластера геоиндекса пунктов выдачи через запятую |
| `OPENSEARCH_USERNAME`, `OPENSEARCH_PASSWORD` | | — | задаются парой |
| `OPENSEARCH_INSECURE_SKIP_VERIFY` | | `false` | самоподписанный сертификат локально |
| `OPENSEARCH_SHARDS`, `OPENSEARCH_REPLICAS` | | `1`, `1` | геоиндекс пунктов выдачи |
| `KAFKA_BROKERS` | да | — | брокеры установки через запятую |
| `KAFKA_TOPIC_PREFIX` | | пусто | `KAFKA_TOPIC_PREFIX` сервера: топики `<prefix>.<имя>` |
| `KAFKA_GROUP_ID` | | `marketplace` | своя группа консьюмера |
| `KAFKA_MAX_ATTEMPTS` | | `5` | неудач до парковки сообщения |
| `KAFKA_TOPIC_REFERENCE_CHANGED` | | `catalog.reference.changed` | топик событий реестра без префикса, см. `docs/reference.md` |
| `REFERENCE_READ_TOKEN` | | пусто | токен чтения реестра для бэкендов: снимок, `resolve`, записи; без прав оператора |
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
| `POST /api/v1/operator/events/{eventId}/retry` | оператор | повтор через outbox, обязательные expectedVersion и note |
| `POST /api/v1/operator/events/{eventId}/resolve` | оператор | разбор с expectedVersion, note и аудитом |
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
субсидией доставки), `catalog_products`, `offers` (остатки и
цены), `outbox`, `processed_events`, `parked_events`, `buyers`,
`phone_challenges`, `carts`, `cart_lines`, `pickup_points`, `job_leases`
(аренда фоновых задач), `checkouts`, `checkout_groups`. Индекс предложений —
отдельный каталог общего `proshop-search`; восстанавливается из
проекции через служебный HTTP API (`make reindex`, а при первом запуске или
смене схемы — автоматически). Самостоятельного поискового алгоритма в backend
маркетплейса больше нет. Пункты выдачи —
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
- Общий `proshop-search`: индекс предложений, поиск и фильтры, карточка, перестроение
  из проекции. Прямой OpenSearch остаётся у геоиндекса пунктов выдачи.
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
Данные поставки 4 — три нормализованных YAML-файла и манифест:
`attributes.yaml`, `schemas.yaml`, `categories.yaml`, `manifest.yaml`.
Формат и пример частей — [taxonomy-data.md](docs/taxonomy-data.md),
выпуск и откат — [taxonomy-releases.md](docs/taxonomy-releases.md).

Глобальные атрибуты задаются один раз, схема хранит порядок кодов и optional,
лист ссылается на схему. Все поля схемы обязательны, кроме optional.
Enum ссылается на закрытый словарь; string может ссылаться на открытый.
При публикации confirmed-значения закрытых словарей сохраняются в снимке;
проверка карточек, фасеты и варианты используют этот снимок.
Код нельзя удалить; type, unit и dictionary неизменны.
Лимиты: 4096 категорий, 256 атрибутов, глубина 16.

```bash
make migrate
make reference-apply
make taxonomy-check
go run ./cmd/taxonomy manifest
make taxonomy-apply
```

Оператор правит части через API с If-Match. Команда `taxonomy load`,
цель `taxonomy-load` и прежний развёрнутый пример удалены.
Откат к старому формату снимка запрещён; миграции JSONB не нужны.

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
| `GET /api/v1/taxonomy/categories` | открыт | дерево со ссылками schema и версией |
| `GET /api/v1/taxonomy/categories/{id}/schema` | открыт | схема узла |
| `GET /api/v1/taxonomy/version` | открыт | текущая версия, до загрузки — 0 |
| `GET /api/v1/operator/taxonomy` | оператор | текущий снимок |
| `GET /api/v1/taxonomy/attributes` | открыт | глобальные атрибуты с dictionary, values и valuesMeta |
| `GET /api/v1/taxonomy/schemas` | открыт | словарь схем с attributes и optional |
| `GET /api/v1/operator/taxonomy/{part}` | оператор | скачать YAML части без вычисляемых values |
| `PUT /api/v1/operator/taxonomy/{part}` | оператор | YAML ≤ 2 МиБ; If-Match обязателен, версия +1 |
| `GET /api/v1/operator/catalog/offers/{offerId}/publication` | оператор | версия проверки, причины отказа, канонические атрибуты |

`part` — categories, attributes или schemas. Устаревший If-Match — 409,
неверный YAML или отсутствующий If-Match — 400. Старые потребители дерева
с attributes и старого PUT должны перейти на новые ручки отдельными задачами.

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

## Общий поиск предложений

Переход, порядок обновления и откат описаны в [docs/shared-search.md](docs/shared-search.md).
`make dev` требует запущенный `proshop-search` с тем же `SEARCH_MARKETPLACE_TOKEN`;
одного compose этого репозитория для поиска предложений недостаточно.
API таксономии, её PostgreSQL-таблицы и поставка данных используют реализацию master.

Реестр справочных сущностей принадлежит площадке: 19 поставок в
`deploy/reference`, применение после миграций — `make reference-apply`.
Контракт потребителей и пополнение описаны в [docs/reference.md](docs/reference.md).
Сбор кандидатов требует отдельного `SEARCH_CANDIDATES_TOKEN` для поиска;
`REFERENCE_CANDIDATES_INTERVAL=1h` задаёт период, `0` выключает сбор.
При пустом токене сборщик не запускается.
