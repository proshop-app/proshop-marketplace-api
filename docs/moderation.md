# Модерация предложений

Модуль `moderation` — политика допуска предложений в каталог площадки.
Решение владельца и границы — `proshop-server/docs/marketplace-mvp.md` §6d
(2026-11-05). Здесь — контракт реализации в этом репозитории.

Принцип: магазин не ждёт человека после каждого изменения товара, площадка
решает, что попадает в её каталог. Правила детерминированы и являются
данными оператора; ИИ-проверки — позже, тем же механизмом.

## Границы

- Контекст `internal/domain/moderation` и сценарии
  `internal/application/moderation`; таблицы с префиксом `moderation_` в
  схеме площадки; общие `UnitOfWork`, `outbox`, индекс.
- Структурная проверка карточки (`taxonomy.Publication`, причины
  `taxonomy.*`) остаётся в каталоге и не дублируется. Модерация отвечает
  на другой вопрос: «площадка согласна это показывать?».
- Допуск магазина как целого остаётся у `participant` (включение
  оператором, статус тенанта). Модуль добавляет к участнику уровень доверия
  и журнал решений.

Видимость предложения:

```
offer.Visible = seller.Visible && card.Visible && publication.Published && verdict.Allowed()
```

Предложение без вердикта (проекция старше модуля) считается скрытым с
причиной `moderation.not_evaluated`; вердикт появляется при следующей
ревизии снимка или по команде переоценки.

## Вердикт

Агрегат `Verdict`, ключ `tenantId + productId`, хранит ревизию снимка, по
которой вычислен, ревизию правил и версию таксономии.

| Статус | В каталоге | Кто ставит |
|---|---|---|
| `allowed` | виден | правила или оператор |
| `pending` | скрыт до решения | правила |
| `rejected` | скрыт, причины известны | правила или оператор |

Причина — `{key, message}`; ключи стабильны:

| Ключ | Значение |
|---|---|
| `moderation.not_evaluated` | вердикта ещё нет |
| `moderation.category_forbidden` | категория площадки запрещена политикой узла |
| `moderation.category_manual` | категория требует ручной проверки |
| `moderation.stop_phrase` | найдена стоп-фраза; `message` содержит фразу |
| `moderation.image_missing` | у карточки нет изображения |
| `moderation.probation` | участник на испытании |
| `moderation.rejected_revision` | предыдущая ревизия отклонена оператором, новая ждёт человека |
| `moderation.manual_review` | оператор открыл дело вручную, «На ручной проверке» |
| `moderation.rejected_by_operator` | отказ оператора; `message` — текст из словаря или заметка |

Правила применяются к снимку карточки по порядку; первый отказ
останавливает оценку, пометки `pending` накапливаются:

1. Политика узла таксономии (`forbidden` — `rejected`; `manual` —
   `pending`).
2. Стоп-фразы: ключ фразы ищется подстрокой в ключе названия и описания;
   каждое попадание — отдельная причина `pending`.
3. `MODERATION_REQUIRE_IMAGE` (по умолчанию `true`): карточка без
   изображения — `rejected`.
4. Испытание: участник `probation` — `pending` с `moderation.probation`.
5. Ручной отказ прилипает: если последнее решение оператора по предложению
   было `reject`, любая новая ревизия — `pending` с
   `moderation.rejected_revision`, даже при чистых правилах. Ручное одобрение действует на одну ревизию:
   следующая проходит правила заново.

Ключ текста: обрезать края, схлопнуть последовательности пробелов (класс
`unicode.IsSpace` плюс U+0085) в один, нижний регистр, `ё → е`. Одна функция
`moderation.Key` на модуль, покрыта тестами.

Удалённый товар (`Deleted`) и снимок без карточки площадки вердикт не
получают: предложение и так скрыто структурно. Повтор той же ревизии и тех
же правил вердикт не меняет (метод возвращает `false`).

Переоценка: вердикт пересчитывается при новой ревизии снимка (в
транзакции проекции), при фоновой перепроверке таксономии только при смене
эффективной политики, `Published` или `Issues`, при изменении стоп-фраз
и при смене уровня доверия участника. Изменение стоп-фраз и смена уровня
доверия ставят в outbox задачу `moderation.tenant.reevaluate`
(дедуп по участнику, постраничный проход по проекциям магазина, условные
обновления). Переоценка меняет только автоматические вердикты: решение
оператора по текущей ревизии не перезаписывается. `taxonomyVersion` вердикта —
версия последней оценки, она может отставать от головы таксономии.

## Уровень доверия участника

Каждый активный магазин реестра — кандидат с `awaitingReview=true`, без согласия
магазина. Валидная доставка и готовая карточка (`card.ready`) допускают кандидата в очередь оператора.
Оператор подключает (`connected`) или возвращает (`returned`) с замечаниями;
магазин исправляет и отправляет снова через сервер (`candidate`). Отключение
подключённого даёт `disconnected`. Эти стадии независимы от доверия.

`participant.State.Trust`: `probation` или `trusted`; `ApprovedOffers` —
число одобренных оператором предложений. Новый участник — `probation`.
После `MODERATION_PROBATION_OFFERS` (по умолчанию 50) одобрений участник
автоматически становится `trusted`, в журнал участника пишется запись с
действием `trust` и причиной `probation_completed`, в outbox ставится
задача переоценки. Оператор меняет уровень вручную с причиной и заметкой.

Журнал `participant_decisions` только на добавление: `tenantId`, оператор
(`id`, `name`), действие (`candidate`, `return`, `enable`, `disable`, `trust`), значение (для
`trust` — новый уровень), причина, заметка, время. Включение и выключение
участника и возврат кандидата пишут туда же; сценарии решений принимают решение
оператора (`Decision{Operator, Reason, Note}`), HTTP передаёт principal из
контекста. Создание кандидата записывается с исполнителем `system`.

## Дело и решения

`Case` — одно открытое дело на предложение (уникальность в хранилище).
Поля: `id`, `tenantId`, `productId`, ревизия, источник (`rule`, `operator`),
статус (`open`, `allowed`, `rejected`, `superseded`), причины на момент
открытия или последней ревизии, заметка, `openedAt`, `decidedAt`, версия.

- Дело открывается, когда вердикт становится `pending`. Если дело уже
  открыто, оно получает новую ревизию и причины.
- Новая ревизия с чистыми правилами при открытом деле: вердикт `allowed`,
  дело закрывается как `superseded`.
- Оператор открывает дело сам (`source: operator`) по `offerId` с заметкой;
  вердикт предложения становится `pending`.
- Решение: `allow` или `reject`; код причины из словаря `moderation_reasons`
  необязателен, заметка обязательна, 1–2000 символов после `TrimSpace`.
  `expectedVersion` обязателен, не совпал — `moderation.case_conflict`
  (класс `conflict`) с текущим делом. Одна транзакция: решение, запись в
  `moderation_decisions` (`caseId`, оператор, действие, код причины,
  заметка, ревизия, время), новый вердикт, счётчик одобрений участника,
  задача `catalog.offer.sync`.
- `allow` даёт `allowed` для ревизии дела. Если снимок ушёл вперёд (ревизия
  проекции больше ревизии дела) — `moderation.case_stale`, оператор
  перечитывает дело.
- `reject` даёт `rejected` с `moderation.rejected_by_operator`.
- Жалоб покупателей нет: обратная связь покупателя — комментарии к товару
  у магазина (решение владельца 2026-11-05).

Стоп-фразы (`moderation_stop_phrases`): фраза 2–120 символов после
схлопывания, ключ уникален, кто и когда завёл. Таблица пуста при установке:
платформа универсальна, слов и тематик в коде нет. Словарь причин
(`moderation_reasons`): код `^[a-z][a-z0-9_]{0,63}$`, текст 1–200 символов,
пуст при установке. Любое изменение стоп-фраз увеличивает ревизию правил
(`moderation_rules`, одна строка) и ставит переоценку по всем участникам.

## Хранение

Миграция `00031_moderation.sql`:

- `moderation_verdicts` (`tenant_id`, `product_id`) — `revision`,
  `rules_revision`, `taxonomy_version`, `status`, `reasons jsonb`,
  `decided_by` (`rule`, `operator`), `manual_reject bool`, `version`,
  `updated_at`; индекс по `(status, tenant_id)`;
- `moderation_cases` — поля выше; частичный уникальный индекс
  `(tenant_id, product_id) WHERE status = 'open'`; индекс
  `(status, opened_at)`;
- `moderation_decisions` — только добавление, триггер запрещает `UPDATE` и
  `DELETE` (как `reference.changes`);
- `moderation_stop_phrases`, `moderation_reasons`, `moderation_rules`
  (`id = 1`, `revision`);
- `participants`: `trust text NOT NULL DEFAULT 'probation'`,
  `approved_offers integer NOT NULL DEFAULT 0`; `participant_decisions`
  только на добавление.

Статусы и действия — `CHECK`. Условные обновления по версии, ноль строк —
типизированный конфликт. SQL — именованными константами в начале файла
репозитория, по агрегату на файл.

## Индекс

`ports.OfferState` получает `Verdict *moderation.Verdict`. `Document()`
прибавляет версию вердикта к сумме версий; `nil` — слагаемое 0 и скрытие.
Скрытое предложение — документ без карточки, как сейчас. Синхронизация —
существующая задача `catalog.offer.sync`, которую ставит каждое изменение
вердикта.

## Сценарии приложения (M1)

`internal/application/moderation`:

- `Evaluate(ctx, tx, product, participant, now)` — вызывается проекцией
  каталога после сохранения снимка и при `Revalidate`; читает политику
  узла, стоп-фразы, ревизию правил, предыдущий вердикт и открытое дело;
  сохраняет вердикт и дело, ставит синхронизацию. Импортных циклов между
  пакетами приложения нет: каталог зависит от интерфейса, реализацию
  подставляет `cmd`.
- `Service`: `OpenCase`, `Allow`, `Reject`, `GetCase`, `ListCases`
  (фильтры: статус, участник, источник; `{items,total,limit,offset}`,
  порядок `opened_at DESC`), `GetOffer` (вердикт, дело, история),
  `ListVerdicts` (статус, участник), стоп-фразы (`List`, `Add`, `Remove`),
  причины (`List`, `Put`), `Reevaluate(tenantID)` для задачи outbox.
- Участник: `SetTrust(ctx, tenantID, trust, decision)`, журнал
  `ListDecisions(ctx, tenantID, page)`.
- Команда `marketplace moderation reevaluate` в `cmd/marketplace/commands.go`:
  ставит задачу переоценки каждому участнику; удерживает
  `IndexRebuildLock`, чтобы не гоняться с пересборкой индекса.

## События магазину (M3)

Решение владельца 2026-11-05: обратная связь магазину — события Kafka
через outbox, тем же путём, что `catalog.reference.changed`
(`docs/reference.md`). Сервер магазина читает их и показывает статус в
админке; контракт для сервера — `docs/server-contract.md`, раздел
«События маркетплейса магазину».

| Событие | Имя без префикса | Ключ | Когда |
|---|---|---|---|
| статус предложения | `KAFKA_TOPIC_OFFER_STATUS`, по умолчанию `marketplace.offer.status` | JSON `["tenantId","productId"]`, как у проекций | изменилась карточка, её публикация или вердикт |
| статус участника | `KAFKA_TOPIC_PARTICIPANT_STATUS`, по умолчанию `marketplace.participant.status` | `tenantId` | возврат, включение, выключение, смена доверия |

Полное имя — `<KAFKA_TOPIC_PREFIX>.<имя>`; топики заводит сервер установки,
издатель их не создаёт. Заголовок `x-tenant` — магазин. Обе переменные не
могут быть пустыми, проверка на старте как у `KAFKA_TOPIC_REFERENCE_CHANGED`.

Тела, `schemaVersion: 1`, `at` — RFC 3339 UTC:

```json
{"schemaVersion": 1, "tenantId": "t1", "productId": "p1", "revision": 12,
 "status": "hidden", "visible": false,
 "reasons": [{"source": "taxonomy", "key": "taxonomy.required", "message": "…"},
             {"source": "moderation", "key": "moderation.stop_phrase", "message": "…"}],
 "decision": {"action": "reject", "reason": {"code": "wrong_category", "message": "…"},
              "note": "…", "at": "2026-11-05T11:59:00Z"},
 "at": "2026-11-05T12:00:00Z"}
{"schemaVersion": 1, "tenantId": "t1", "stage": "connected", "enabled": true, "trust": "trusted",
 "reason": null, "note": "Одобрено 50 предложений", "by": "system",
 "at": "2026-11-05T12:00:00Z"}
```

- `decision` — решение оператора, которым установлен текущий статус:
  `action` (`allow`, `reject`), `reason` из словаря `{code, message}` или
  `null`, `note`, `at`; статус от правил — `decision: null`. Текст причины —
  на момент решения (решение владельца 2026-11-05).
- У участника `reason` — объект из того же словаря или `null`, `by` —
  `operator` или `system` (автоматическое повышение).

- `status`: `published` — публикация по схеме прошла и вердикт `allowed`;
  `pending` — вердикт `pending`; `hidden` — всё остальное (структурные
  причины, `rejected`, вердикта нет). `visible` = `status == published`.
  Видимость самого участника в событие не входит: его состояние — второе
  событие.
- `reasons` — причины обеих групп: `Publication.Issues` с `source:
  taxonomy` и причины вердикта с `source: moderation`. Пустой список при
  `published`. Одно событие на изменение `visible`, `status`, причин или
  решения.
- Удалённый товар события не получает.
- Задачи outbox `moderation.offer.status` (дедуп по предложению) и
  `moderation.participant.status` (дедуп по участнику) ставятся в той же
  транзакции, что и изменение: приём снимка (`ApplyProduct`), перепроверка
  в транзакции фоновой пачки при смене таксономии (публикация версии только
  ставит проход через outbox), `Evaluate` при изменении вердикта, `OpenCase`,
  `Allow`, `Reject`, переоценка; включение, выключение и смена доверия
  участника. Обработчик читает текущее состояние на момент публикации и
  шлёт его: склеенные задачи дают одно событие с последним состоянием,
  повтор после сбоя безопасен. Для участника `reason` и `note` — из
  последней записи журнала решений.
- Публикация — порт `ports.Publisher`, реализация — `infrastructure/kafka`
  `Writer`, регистрация обработчиков рядом с `reference.changed` в
  `cmd/marketplace/server.go`.

## HTTP оператора (M2)

Префикс `/api/v1/operator/moderation`, та же проверка сессии оператора и
`OPERATOR_TOKEN`, что у остальных операторских ручек; роль `operator`.
Ошибки — `{error:{code,message}}`, коды стабильны; классы `pkg/errs`
переводятся общей таблицей `statusByCode`. Списки — `{items,total,limit,offset}`,
`limit` по умолчанию 50, не больше 200, `offset ≥ 0`.

| Ручка | Тело и ответ |
|---|---|
| `GET /cases?status&tenantId&source&limit&offset` | дела, `opened_at DESC`; `status` — `open` (по умолчанию), `allowed`, `rejected`, `superseded`, `all`; `source` — `rule`, `operator` |
| `GET /cases/{caseId}` | `{case, offer, history}` |
| `POST /cases` | `{offerId, note}` → 201 `case`; открытое дело уже есть — 409 `moderation.case_conflict` с `{error, case}` |
| `POST /cases/{caseId}/allow`, `POST /cases/{caseId}/reject` | `{expectedVersion, reason?, note}` → 200 `case`; версия не совпала или дело закрыто — 409 `moderation.case_conflict` с текущим делом; ревизия снимка ушла вперёд — 409 `moderation.case_stale`; пустая или длинная заметка, неизвестный код причины — 400 |
| `GET /offers/{offerId}` | `{verdict, case, history}`; нет проекции — 404 |
| `GET /verdicts?status&tenantId&limit&offset` | вердикты, `updated_at DESC`; `status` обязателен |
| `GET /stop-phrases` | `{items}` по времени добавления |
| `POST /stop-phrases` | `{text}` → 201 `stopPhrase`; занятый ключ — 409 `moderation.stop_phrase_exists` |
| `DELETE /stop-phrases/{key}` | 204; нет — 404 |
| `GET /reasons` | `{items}` по коду |
| `PUT /reasons/{code}` | `{text}` → 200 `reason`, заводит или меняет |
| `DELETE /reasons/{code}` | 204; нет — 404 |

Формы:

```json
{"case": {"id": "…", "offerId": "t1~p1", "tenantId": "t1", "productId": "p1", "revision": 12,
  "source": "rule", "status": "open", "reasons": [{"key": "moderation.stop_phrase", "message": "…"}],
  "note": "", "openedAt": "…", "decidedAt": null, "version": 1}}
{"offer": {"name": "…", "description": "…", "image": "…", "price": 19999, "basePrice": 24999,
  "categoryId": "c1", "categoryPath": ["…"], "attributes": {"a1": "v1"},
  "seller": {"slug": "…", "name": "…"}, "publication": {"published": true, "issues": []}}}
{"history": [{"operator": {"id": "…", "name": "…"}, "action": "allow", "reason": "", "note": "…",
  "revision": 12, "at": "…"}]}
{"verdict": {"offerId": "t1~p1", "tenantId": "t1", "productId": "p1", "revision": 12, "status": "pending",
  "reasons": [], "decidedBy": "rule", "updatedAt": "…"}}
{"stopPhrase": {"key": "…", "text": "…", "operator": {"id": "…", "name": "…"}, "createdAt": "…"}}
{"reason": {"code": "counterfeit", "text": "…"}}
```

`offer` — карточка так, как её увидел бы покупатель: цены в копейках,
категория площадки путём имён, канонические атрибуты из `Publication`.
`Idempotency-Key` для `allow` и `reject` — по образцу решений по
отравленным событиям: ключ ограничен оператором, запрос (дело, действие,
`expectedVersion`, `reason`, `note`) сверяется отпечатком, повтор отдаёт
первоначальный ответ 24 часа, другой запрос с тем же ключом — 409
`IDEMPOTENCY_KEY_REUSED`. Механизм обобщается с `operator_event_idempotency`
или получает свою таблицу в миграции `00032`.

Участники:

| Ручка | Тело и ответ |
|---|---|
| `POST /operator/participants/{tenantId}/enable`, `/disable` | необязательное тело `{reason, note}`; записывается в журнал |
| `PUT /operator/participants/{tenantId}/trust` | `{trust, reason?, note}`, `trust` — `probation` или `trusted` → `View`; журнал и переоценка |
| `GET /operator/participants/{tenantId}/decisions?limit&offset` | `{items:[{operator, action, value, reason, note, at}], total, limit, offset}`, `at DESC` |

`View` участника получает `trust` и `approvedOffers`.
`GET /operator/catalog/offers/{offerId}/publication` дополняется блоком
`moderation: {status, reasons}` — так раздел «Каталог» админки показывает обе
группы причин.

OpenAPI: `api/openapi.yaml`, тег `moderation`; `openapi_test.go` сверяет
маршруты со схемой. Снимок для админки `docs/api/marketplace.openapi.yaml`
обновляется тем же изменением.

## Метрики

Prometheus, регистрация как у outbox:

| Метрика | Вид | Метки |
|---|---|---|
| `marketplace_moderation_verdicts_total` | счётчик изменений вердикта | `tenant`, `status`, `decided_by` |
| `marketplace_moderation_decisions_total` | счётчик решений оператора | `action` |
| `marketplace_moderation_cases_open` | gauge открытых дел | `tenant` |
| `marketplace_moderation_case_oldest_age_seconds` | gauge возраста старейшего открытого дела | — |

Gauge обновляет фоновый цикл раз в `MODERATION_METRICS_INTERVAL`
(по умолчанию 60s) одним запросом по открытым делам.

## Решения владельца с макета UNO (M4)

Согласованы 2026-11-05 при проработке макета (фреймы 38–53), записаны в
§6d. Реализуются поверх M1–M3:

1. **Дело вручную** (`POST /cases`) сразу переводит вердикт в `pending` с
   причиной `moderation.manual_review` и текстом «На ручной проверке»;
   предложение скрыто до решения.
2. **Снимок карточки у дела.** Дело хранит карточку своей ревизии (имя,
   описание, изображение, цена, категория путём, атрибуты, продавец) в
   момент открытия и при смене ревизии; `GET /cases/{caseId}` отдаёт
   карточку из снимка, в том числе у закрытого (`superseded`, `allowed`,
   `rejected`) дела. Миграция `00033`: колонка `card jsonb` у
   `moderation_cases`, старые строки — `NULL`, тогда карточка читается из
   проекции.
3. **Один словарь причин** для решений по делам и по участникам
   (выключение, смена доверия). Причину нельзя удалить, код после создания
   не меняется: `GET /reasons`, `POST /reasons` `{code, message}` (занятый
   код — 409 `moderation.reason_exists`), `PUT /reasons/{code}` `{message}`;
   `DELETE /reasons/{code}` убирается из API и OpenAPI. Поле текста причины
   в API — `message`. Код причины в решениях и у участника проверяется по
   словарю (неизвестный — 400); пустой код допустим.
4. **Доверие.** `PUT /operator/participants/{tenantId}/trust`
   `{trust, reason?, note}`; ручное понижение до `probation` обнуляет
   `approvedOffers`. Автоматическое повышение пишется в
   `participant_decisions` с исполнителем `system` (колонка `actor`:
   `operator` или `system`, миграция `00033`), без причины, заметка —
   служебный текст с числом одобренных.
5. **Участники в API.** `GET /operator/participants?trust=probation|trusted`
   фильтрует список; у `View` участника уже есть `trust` и
   `approvedOffers`.
6. **События магазину** по обновлённому контракту выше: `decision` у
   предложения, `reason` объектом и `by` у участника; `server-contract.md`
   раздел «Статус предложения и участника» — источник истины для сервера.
7. **Операторский API решения** возвращает в `history` и в `decision`
   причину как `{code, message}`.

## Доработки API под макет UNO (M5)

Спецификация фреймов 38–53 выявила, чего не хватает API для вёрстки
раздела. Решения 2026-11-05, реализуются поверх M4; миграция одна —
`00034_moderation_history.sql`.

1. **Сводка предложения в списках.** Элементы `GET /cases` и
   `GET /verdicts` получают `offer: {name, image, price}` и
   `seller: {slug, name}`: у дела — из снимка карточки, у вердикта — из
   проекции. Так колонка «Товар» не делает запрос на строку.
2. **Порядок очереди.** `GET /cases?order=oldest|newest`, по умолчанию
   `oldest`: первая строка — старейшее дело, баннер очереди берёт возраст
   из неё.
3. **Фильтр по причине.** `GET /cases?reason=<ключ>` — дела, у которых в
   причинах есть этот ключ.
4. **Отклонённые.** `GET /verdicts?decidedBy=rule|operator`; у вердикта с
   `decidedBy: operator` — `operator: {id, name}` и
   `decision: {action, reason: {code, message}|null, note, at}` (хранятся в
   строке вердикта: колонки `operator_id`, `operator_name`,
   `decision jsonb`). Причина отказа оператора в `reasons` —
   `moderation.rejected_by_operator` с `message` из словаря, без причины —
   заметка.
5. **Два смысла одного ключа.** Прилипающий отказ получает свой ключ
   `moderation.rejected_revision` («Предыдущая ревизия отклонена
   оператором»); `moderation.manual_review` — только дело, открытое
   вручную. Таблица ключей выше и `server-contract.md` обновлены.
6. **Предложение без дела.** `GET /offers/{offerId}` отдаёт `offer` (как у
   дела, из проекции), `participant: {tenantId, name, enabled, visible}`
   для блока «Почему скрыто» и `verdicts` — историю вердиктов (п. 7).
7. **История вердиктов.** Таблица только на добавление
   `moderation_verdict_log`: `tenant_id`, `product_id`, `revision`,
   `status`, `reasons jsonb`, `decided_by`, `operator_id`, `operator_name`,
   `rules_revision`, `taxonomy_version`, `at`; запись при каждом сохранённом
   изменении вердикта в той же транзакции. `GET /offers/{offerId}` отдаёт
   до 50 последних записей, новые сверху.
8. **Версия правил.** `rulesRevision` и `taxonomyVersion` в ответах
   вердикта и в истории; `GET /operator/moderation/rules` →
   `{revision, taxonomyVersion, requireImage, probationOffers}`.
9. **Порог испытания.** `View` участника получает `probationOffers`
   (порог из настроек), чтобы показать «{approved} из {required}».
10. **Прежний уровень в журнале.** У записи журнала участника —
    `previousValue` (колонка `previous_value`, для старых строк пусто).
11. **Заметка обязательна** при выключении участника и смене доверия
    (1–2000 символов, как у решения по делу); при включении — необязательна.
12. Ключ и текст причин оператора в OpenAPI — `message`; плейсхолдер кода
    во фронте — `reason_code` (фронт).

## Доводка M1 (вместе с M2)

- Действия и статусы дела и вердикта — типизированные константы домена
  вместо строк `"allow"`, `"reject"`, `"open"`, `"superseded"`.
- Оценщик модерации — явный параметр `NewProjector` и
  `taxonomy.NewService`, не вариативный; тесты передают `nil`.
- Ключ `moderation.not_evaluated` и его сообщение — функция или константа
  модуля `moderation`, каталог строку не знает.

## Таксономия

У узла категории необязательное поле `moderation: auto | manual | forbidden`
в YAML и в снимке `GET /taxonomy/categories`; наследуется вниз от
ближайшего предка с явным значением, по умолчанию `auto`. Неизвестное
значение — `taxonomy.invalid` при загрузке. Описание поля — в
`docs/taxonomy-structure.md`.

## Настройки

| Переменная | Значение |
|---|---|
| `MODERATION_PROBATION_OFFERS` | положительное число, по умолчанию 50 |
| `MODERATION_REQUIRE_IMAGE` | `true`/`false`, по умолчанию `true` |
| `MODERATION_METRICS_INTERVAL` | положительный интервал, по умолчанию `60s` |

## Проверка

- Домен: таблица случаев по каждому правилу, порядок правил, прилипание
  ручного отказа, одобрение на одну ревизию, повтор ревизии без изменений,
  `Key`.
- Приложение на `application/fake`: снимок участника на испытании даёт
  `pending`, дело и документ без карточки; одобрение открывает карточку;
  пятидесятое одобрение переводит в `trusted` и ставит переоценку;
  переоценка закрывает дела как `superseded`; добавление стоп-фразы
  прячет совпавшее предложение; `expectedVersion` и устаревшая ревизия;
  проекция без вердикта скрыта.
- Интеграционные (`integration`, `pgtest`): репозитории вердиктов, дел,
  решений, стоп-фраз, журнал участника, уникальность открытого дела,
  запрет правки журнала.
- Сторожа слоёв и раскладки, `make lint`.

### Готовность и постмодерация карточки магазина

Кандидат попадает в очередь только при `awaitingReview`, валидной доставке и
`card.ready=true`. Подключение неготового кандидата отклоняется с 409
`participant.card_not_ready`. Операторский ответ содержит `cardReady`, `cardMissing`.
Утрата готовности карточки не меняет стадию подключённого участника.

`POST /api/v1/operator/participants/{tenantId}/card/hide` принимает
`{key: "logo", reason: "..."}`; причина обязательна, до 2000 символов.
`DELETE /api/v1/operator/participants/{tenantId}/card/hide/{key}` снимает скрытие.
Изменение и решение `card_hide` / `card_show` записываются одной транзакцией
в существующий журнал участника; value — ключ пункта, note — причина скрытия.
Повтор того же решения не увеличивает версию и не дублирует журнал.
Автоматическое снятие скрытия записывается как `card_show` от `system`.
Сверка автоматически снимает скрытие только при изменении соответствующего значения.
Публичные ответы никогда не включают скрытый текст или ключ логотипа.
