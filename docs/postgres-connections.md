# Соединения с PostgreSQL

Как сервис площадки держит соединения с базой, почему через пулер и как его развернуть.
Версии, по которым сверено поведение: pgx v5.7.6 (`go.mod`), CloudNativePG 1.28.1
(пулер по умолчанию — `ghcr.io/cloudnative-pg/pgbouncer:1.25.1`, `pkg/versions/versions.go`
тега v1.28.1), PgBouncer 1.25.1 (`doc/config.md` тега `pgbouncer_1_25_1`).

## Схема

```
реплики API ──(клиентские соединения pgxpool, по классам и ролям)──► Pooler marketplace-pg-pooler-rw
                                                                     (PgBouncer, transaction, 2 реплики)
                                                                            │ серверные соединения:
                                                                            │ пул на роль класса
                                                                            ▼
реплики API ──(прямые сеансы: только advisory lock)────────────────► marketplace-pg-rw (PostgreSQL)
Job миграций, служебные команды ──(прямой адрес)───────────────────►
```

- `DATABASE_URL` — адрес пулов процесса. Без `DATABASE_DIRECT_URL` это сама база, и всё
  идёт напрямую, как до пулера: `make dev`, интеграционные тесты, локальный запуск.
- `DATABASE_DIRECT_URL` — прямой адрес базы. Задан — значит `DATABASE_URL` ведёт к пулеру
  в режиме транзакций. По прямому адресу идут только сеансовые advisory lock (пересборка
  индекса, расписания, тик продвижения), миграции goose и служебные команды (`cmd/migrate`,
  `cmd/taxonomy`, команды `marketplace`).
- У каждого класса нагрузки своя роль: `DATABASE_<КЛАСС>_USER` и `DATABASE_<КЛАСС>_PASSWORD`
  (`READ`, `CHECKOUT`, `INGESTION`, `BACKGROUND`, `ORDERS`). В режиме пулера они обязательны и
  различны, адрес пулера — без учётных данных; без пулера роли не задаются. Это проверяет старт.

## Почему пулер

Без пулера каждая реплика открывает до суммы своих пулов прямых соединений: по умолчанию
65 (чтение 8, HTTP-запись 6, приём 12, фон 10, исполнитель оформления 29), плюс тик
продвижения на каждой реплике, плюс 7 соединений расписаний и пересборки. Число соединений
растёт линейно с репликами и упирается в `max_connections`. Клиентское соединение к PgBouncer
стоит пулеру несколько килобайт; серверное соединение PostgreSQL — процесс. Через пулер
в режиме транзакций серверное соединение занято только на время транзакции, и число
серверных соединений задаёт пулер, а не число реплик.

## Что несовместимо с режимом транзакций и как решено

Таблица возможностей PgBouncer (`features.html`) для режима транзакций: `SET/RESET`,
`LISTEN`, курсоры `WITH HOLD`, SQL `PREPARE/DEALLOCATE`, временные таблицы без
`ON COMMIT DROP`, `LOAD` и сеансовые advisory lock — не поддерживаются; `NOTIFY`, курсоры
без `WITH HOLD`, временные таблицы `ON COMMIT DROP` и подготовленные запросы протокола
(при `max_prepared_statements` > 0) — поддерживаются.

### Сроки SQL

До пулера сроки класса передавались параметрами запуска соединения
(`ConnConfig.RuntimeParams`: `statement_timeout`, `idle_in_transaction_session_timeout`,
`internal/infrastructure/postgres/deadlines.go`, `applyBudget`). PgBouncer 1.25.1 пропускает
в пакете запуска только `client_encoding`, `datestyle`, `timezone`,
`standard_conforming_strings` и `application_name`; остальные — ошибка соединения, если их
нет в `ignore_startup_parameters`, а оттуда они молча отбрасываются (CloudNativePG сам
добавляет туда `extra_float_digits,options`). `track_extra_parameters` годится только для
параметров, о которых сервер сообщает клиенту (`ParameterStatus`); сроков среди них нет.

Выбраны **роли на класс со сроками роли** (`ALTER ROLE … SET`):

- серверное соединение пулера открывается под ролью класса и получает её сроки при запуске
  процесса PostgreSQL — ноль обращений на транзакцию и на одиночный запрос вне транзакции;
- PgBouncer держит пул на пару «роль/база» (`default_pool_size` — «per user/database pair»),
  так что роль на класс заодно даёт раздельные пулы серверных соединений;
- `SET LOCAL` в начале транзакции стоил бы обращение на каждую транзакцию и не покрывал бы
  одиночные запросы вне транзакции (`withConnection`); параметры пулера
  (`query_timeout`, `idle_transaction_timeout`) общие на весь пулер, а сроки классов разные.

Старт сверяет сроки роли с `DATABASE_<КЛАСС>_STATEMENT_TIMEOUT` и
`DATABASE_<КЛАСС>_TRANSACTION_TIMEOUT` одним запросом на класс (`verifyRoleBudget`) и не идёт
при расхождении. Клиентский срок операции (`context.WithTimeout`, включая ожидание
соединения) по-прежнему берётся из настройки класса: он хранится у пула класса
(`postgres.ClassPool`), а не восстанавливается из параметров соединения.

Сеанс, изменённый `SET` без `LOCAL`, за пулером отдал бы свои сроки чужой транзакции.
Такие операторы запрещены сторожем `internal/architecture/sessionstate_test.go`.

### Подготовленные запросы

pgx по умолчанию работает в `QueryExecModeCacheStatement`: именованные подготовленные запросы
протокола и кэш на 512 запросов на соединение; закрытие — сообщением протокола `Close`, не SQL
`DEALLOCATE` (`conn.go`, `pgconn.Deallocate`, `Pipeline.SendDeallocate`). PgBouncer с 1.21
отслеживает подготовленные запросы протокола в режиме транзакций и сам готовит их на том
серверном соединении, которое досталось клиенту; с 1.24 поддержка включена по умолчанию
(`max_prepared_statements` = 200). SQL `PREPARE/EXECUTE/DEALLOCATE` он не отслеживает — их
в коде нет, сторож это держит. GORM работает поверх `database/sql` и того же pgx
(`stdlib.OpenDBFromPool`), `PrepareStmt` не включён. Менять режим pgx не нужно;
`max_prepared_statements` = 512 — не меньше кэша pgx, чтобы пулер не вытеснял и не готовил
заново частые запросы (в пакете `postgres` около 265 SQL-констант и запросы GORM).

### Блокировки и сеансы

| Место | Блокировка | Решение |
|---|---|---|
| `IndexRebuildLock` — пересборка индекса (`Indexer.Rebuild`, `Maintain`, команда `reindex`) | `pg_try_advisory_lock` сеанса | прямой сеанс `DirectSessions`: работа идёт многими транзакциями минутами, транзакционная блокировка потребовала бы одной долгой транзакции |
| `ScheduledWork` — шесть расписаний | сеансовая на время прохода | прямой сеанс; интервал берётся запросом пула, сеанс открывает только реплика с интервалом |
| Тик продвижения | был отдельный `IndexRebuildLock` на каждой реплике раз в интервал | переведён на `ScheduledWork` с тем же именем блокировки: тик идёт одной репликой раз в интервал, прямой сеанс — только у неё |
| goose (`lock.NewPostgresSessionLocker`, `repairingMigrationLocker`), `CREATE/DROP INDEX CONCURRENTLY` | сеансовая | миграции только по прямому адресу |
| `pg_advisory_xact_lock` (`event_idempotency.go`, `moderation-idempotency.go`, `store-cards.go`, `buyer-features.go`) | транзакционная | годится за пулером, без изменений |

Сеанс блокировки — отдельное соединение `pgx.Conn` по прямому адресу, вне пулов классов,
со сроками фонового класса; закрытие снимает блокировку, проверка владельца — `Ping` раз
в секунду.

### Прочее состояние сеанса

`LISTEN`, временных таблиц, `set_config(…, false)`, `search_path` и `SET` в коде нет.
Курсор сводки магазина (`store-card-offers.go`, `DECLARE … NO SCROLL CURSOR`) — без
`WITH HOLD`, внутри транзакции `UnitOfWork`: за пулером работает. Сторож
`TestNoSessionStateBehindPooler` запрещает в SQL процесса `SET`/`RESET` без `LOCAL`,
`DISCARD`, `set_config(…, false)`, `LISTEN`, `WITH HOLD`, временные таблицы, SQL `PREPARE` и
сеансовые advisory lock вне `index-rebuild-lock.go`.

### Долгие транзакции

Серверное соединение пулера занято на время транзакции. Самые долгие — фоновые: курсор
сводки магазина и пачки пересборки индекса ограничены сроком фонового класса (10 с SQL,
30 с транзакция). HTTP-вызовы исполнителя оформления к магазину идут вне транзакции
(`order_calls.go`, `createGroup`), поэтому 29 клиентских мест исполнителя не держат
29 серверных соединений. Пул фонового класса за пулером (12 серверных соединений) вмещает
несколько одновременных долгих транзакций, не задевая другие классы.

## Пулер

Манифест — [`deploy/postgres/pooler.yaml`](../deploy/postgres/pooler.yaml). CloudNativePG
задаёт только базу `*` и не даёт настраивать разделы `[databases]` и `[users]`, поэтому
`pool_size` на роль задать нельзя: размер пула одинаков для всех ролей —
`default_pool_size`. Изоляция классов держится тем, что пул у каждой роли свой.

| Параметр | Значение | Почему |
|---|---|---|
| `instances` | 2 | выкат и потеря узла не останавливают площадку; пул серверных соединений у каждой реплики пулера свой |
| `type` | `rw` | вся работа — на основном узле |
| `poolMode` | `transaction` | серверное соединение занято только на время транзакции |
| `max_client_conn` | 1000 | до 65 клиентских соединений на реплику API, все могут прийти в одну реплику пулера; запас до 15 реплик |
| `default_pool_size` | 6 | серверных соединений на класс на реплику пулера; 12 на класс всего |
| `max_db_connections` | 30 | 5 классов × 6: не ниже суммы, иначе простаивающие соединения одного класса отнимают место у другого |
| `max_prepared_statements` | 512 | не меньше кэша pgx |
| `reserve_pool_size` | 0 (умолчание) | резерв ломал бы жёсткую квоту класса |
| `query_wait_timeout` | 120 с (умолчание) | клиентские сроки классов (≤ 30 с) обрывают ожидание раньше |
| `ignore_startup_parameters` | не задаётся | сервис за пулером не передаёт параметров сроков; добавить их сюда — молча потерять сроки |

## Роли

Скрипт — [`deploy/postgres/class-roles.sql`](../deploy/postgres/class-roles.sql), выполняет
администратор базы, не сервис. Группа `marketplace_app` (без входа) получает только права
на данные: `SELECT, INSERT, UPDATE, DELETE` таблиц, `USAGE, SELECT, UPDATE` последовательностей
и те же права по умолчанию на объекты, которые создадут миграции владельца `marketplace`.
Роли классов `marketplace_read`, `marketplace_checkout`, `marketplace_ingestion`,
`marketplace_background`, `marketplace_orders` — с входом, члены группы, со сроками класса.
Пароли — CloudNativePG managed roles (`spec.managed.roles`, `passwordSecret` типа
`kubernetes.io/basic-auth`) или `ALTER ROLE … PASSWORD`. Схему меняет только владелец
`marketplace` миграциями по прямому адресу.

## Переменные окружения сервиса

```
DATABASE_URL=postgres://marketplace-pg-pooler-rw.data.svc:5432/marketplace?sslmode=require
DATABASE_DIRECT_URL=postgres://marketplace:…@marketplace-pg-rw.data.svc:5432/marketplace?sslmode=require
DATABASE_READ_USER=marketplace_read              DATABASE_READ_PASSWORD=…
DATABASE_CHECKOUT_USER=marketplace_checkout      DATABASE_CHECKOUT_PASSWORD=…
DATABASE_INGESTION_USER=marketplace_ingestion    DATABASE_INGESTION_PASSWORD=…
DATABASE_BACKGROUND_USER=marketplace_background  DATABASE_BACKGROUND_PASSWORD=…
DATABASE_ORDERS_USER=marketplace_orders          DATABASE_ORDERS_PASSWORD=…
```

`DATABASE_<КЛАСС>_MAX_CONNS` теперь — клиентские соединения реплики к пулеру: дешёвые,
умолчания прежние. Ограничивает базу `default_pool_size` пулера. Job миграций получает в
`DATABASE_URL` прямой адрес и не задаёт ни `DATABASE_DIRECT_URL`, ни ролей.

## Ёмкость

До пулера (все соединения прямые):

```
R × (65 + 1 тик продвижения) + 7 (расписания и пересборка) + 3 × служебных команд
```

| Реплик API | Соединений к PostgreSQL |
|---|---|
| 1 | 66 + 7 + 3 = 76 |
| 2 | 132 + 7 + 3 = 142 — больше `max_connections` = 100 |
| 10 | 660 + 7 + 3 = 670 |

Через пулер (I — реплик пулера, P — `default_pool_size`, 5 классов, 1 — соединение
`auth_query` пулера на реплику):

```
I × (5 × P + 1) + 8 прямых сеансов (пересборка + 7 расписаний, только у владельцев)
  + 3 × служебных команд
```

| Реплик API | Соединений к PostgreSQL (I = 2, P = 6) | Клиентских соединений к пулеру |
|---|---|---|
| 1 | 62 + 8 + 3 = 73 | 65 |
| 2 | 73 | 130 |
| 10 | 73 | 650 |

Пока нужна пересборка индекса, остальные реплики раз в `SEARCH_MAINTENANCE_INTERVAL` на
мгновение открывают прямой сеанс, чтобы узнать, что блокировка занята: до R − 1 коротких
сеансов сверху.

`max_connections` базы ≥ 73 + `superuser_reserved_connections` (3) + служебные соединения
CloudNativePG и мониторинга (~5) + ручной вход (~5) ≈ 86: умолчания 100 хватает при любом
числе реплик API. Пропускная способность класса растёт не репликами API, а P: размер
серверного пула выбирается по ядрам базы (порядка 2–4 активных соединений на ядро всего),
P = 6 при I = 2 — 60 одновременных транзакций на 5 классов.

## Выкат

1. Роли: managed roles с паролями в кластере, затем `class-roles.sql` суперпользователем
   (`kubectl cnpg psql marketplace-pg -n data -- -d marketplace -f class-roles.sql`).
2. `Pooler` из `deploy/postgres/pooler.yaml`; сетевые политики: API → пулер (5432),
   пулер → кластер (5432).
3. Секрет сервиса: `DATABASE_URL` — адрес пулера без учётных данных, `DATABASE_DIRECT_URL` —
   прежний прямой адрес, роли классов. Job миграций — `DATABASE_URL` из прямого адреса.
4. Выкат API. Старт сверяет сроки ролей; расхождение — отказ старта с текстом, какая роль
   и какой срок.

Блокировка продвижения сохранила имя `uno:marketplace:promotion`: старые и новые реплики
во время выката не пройдут тик одновременно.

## Откат

Удалить из секрета `DATABASE_DIRECT_URL` и роли классов, вернуть в `DATABASE_URL` прямой
адрес с учётными данными владельца — сервис снова идёт напрямую. `Pooler` и роли можно
оставить: без клиентов они не держат серверных соединений сверх `min_pool_size` (0).

## Что наблюдать

- Пулер (порт 9127, `cnpg_pgbouncer_*`): `cnpg_pgbouncer_pools_cl_waiting` и
  `cnpg_pgbouncer_pools_maxwait` по роли — класс ждёт серверного соединения, пул класса мал;
  `cnpg_pgbouncer_pools_sv_active`/`sv_idle` — занятость серверного пула;
  `cnpg_pgbouncer_stats_avg_query_time`; `cnpg_pgbouncer_collection_errors_total`.
- База: `SELECT usename, application_name, state, count(*) FROM pg_stat_activity
  WHERE datname = 'marketplace' GROUP BY 1, 2, 3` — по ролям классов не больше I × P,
  у владельца `marketplace` — только прямые сеансы блокировок и команды; соединения
  `idle in transaction` дольше срока класса быть не должно.
- Сервис: `marketplace_goroutine_panics_total` и отказы старта по `verifyRoleBudget`.
