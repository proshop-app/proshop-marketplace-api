# proshop-marketplace

Backend федеративного маркетплейса платформы Proshop: единый каталог
предложений всех участвующих магазинов, затем корзина, checkout с оплатой
одним платежом и заказы у магазинов. Go 1.23, слоистая архитектура по DDD,
PostgreSQL, OpenSearch, Kafka. Проект — `proshop-server/docs/marketplace-mvp.md`,
правила кода — [CLAUDE.md](CLAUDE.md).

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
| `make reindex` | перестроить индекс предложений из проекции |
| `make build` | `bin/marketplace` и `bin/migrate` |
| `make image` | образ: сборка на `golang:1.23.2-alpine`, запуск на `scratch` |
| `make infra-up`, `make infra-down` | только инфраструктура |

golangci-lint и air запускаются через `go run` с версиями из `Makefile`,
goose встроен в `cmd/migrate` и закреплён в `go.mod`. `GOTOOLCHAIN=local`:
более новый toolchain не скачивается.

## Окружение

Процесс проверяет окружение на старте и не поднимается, пока не заданы
обязательные переменные; все ошибки выводятся разом. `make dev` задаёт
значения для compose сам, полный список с умолчаниями — `.env.example`.

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

## HTTP

Все суммы — целые копейки. Ошибки — `{"error":{"code":"<ключ>","message":"…"}}`,
код ответа выбирается по классу ошибки одной таблицей.

| Маршрут | Доступ | Что делает |
|---|---|---|
| `GET /health` | открыт | PostgreSQL, OpenSearch, Kafka: 200 или 503 |
| `GET /metrics` | открыт | метрики Prometheus |
| `GET /api/v1/catalog/offers` | открыт | выдача: `q`, `tenant`, `category`, `tag`, `priceMin`, `priceMax`, `inStock`, `sort` (`relevance`, `price_asc`, `price_desc`, `newest`), `limit` ≤ 100, `offset` |
| `GET /api/v1/catalog/offers/{offerId}` | открыт | карточка; `offerId` = `<tenantId>~<productId>` |
| `GET /api/v1/operator/participants` | оператор | участники: `limit` ≤ 200, `offset` |
| `POST /api/v1/operator/participants` | оператор | завести участника `{"tenantId","name"}`; магазин должен быть в зеркале реестра |
| `GET /api/v1/operator/participants/{tenantId}` | оператор | участник |
| `PATCH /api/v1/operator/participants/{tenantId}` | оператор | переименовать `{"name"}` |
| `PUT /api/v1/operator/participants/{tenantId}/payment-account` | оператор | магазин продавца в ЮKassa `{"paymentAccountId"}`: `onboarding → connected` |
| `POST /api/v1/operator/participants/{tenantId}/enable` | оператор | показать в каталоге |
| `POST /api/v1/operator/participants/{tenantId}/disable` | оператор | скрыть из каталога |

**Доступ оператора — временный.** До недели 3 операторские ручки закрыты
служебным токеном из `OPERATOR_TOKEN` (`Authorization: Bearer <токен>`).
Вход оператора через панель на Vue 3 со своей ролью придёт на неделе 3;
токен тогда перестанет быть входом для людей.

Предложение видно в каталоге, если участник включён оператором, магазин
`active` в реестре платформы и товар включён в магазине. Без
`paymentAccountId` предложение видно с `checkoutAvailable: false`.

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
`tenants` (зеркало реестра), `participants`, `catalog_products`,
`catalog_tags`, `offers` (остатки и цены), `outbox`, `processed_events`,
`parked_events`. Индекс предложений — OpenSearch, алиас `OPENSEARCH_INDEX`
над поколениями; восстанавливается из проекции (`make reindex`, а на старте —
сам, если алиаса нет).

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

## Дальше по неделям

| Неделя | Что придёт |
|---|---|
| 2 | `buyer` (вход по звонку через sms.ru, сессии, лимиты в Redis), `cart`, расчёт доставки через сервер (Д1), `checkout` до `creating_orders`; клиент внутреннего API сервера (`SERVICE_TOKEN_MARKETPLACE`) |
| 3 | `payment` (холд, capture, возврат, чек), `settlement`, `order` из событий `orders.*` и `delivery.*`, сверка; вход оператора через панель на Vue 3 вместо служебного токена |
| 4 | сквозной прогон в kind, враждебные проверки денежного пути, правки |

## Стыки с сервером, которых пока нет

- Сервер ещё не ставит заголовок `x-tenant` (PR-04a): пока его нет, события
  `stock.item.updated` и `catalog.product.price-updated` уходят в отравленные
  с метрикой; цена и остаток берутся из снимков `catalog.product.projected`.
- Топиков `platform.tenant.*` у сервера ещё нет (PR-08d): без них зеркало
  реестра пусто и завести участника нельзя.
