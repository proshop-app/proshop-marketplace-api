# Ценовые контексты показа

Проверено по коду площадки и `proshop-server origin/master`
`f9e06921dd8716c0f5b4e02201756414554ac5b9` (2026-11-12), без живых вызовов.

## Источник и сопоставление покупателей

Площадка передаёт локальный UUID проверенного покупателя из сессии, а не идентификатор из браузера.
`POST /api/v1/internal/marketplace/pricing/contexts` принимает `{buyerId, tenantIds}`,
до 100 магазинов на пачку. Сервер выполняет `MarketplacePricingContextsService.read` в контексте
каждого tenant: проверяет участие/настройки и вызывает `PricingService.select` с
`channel: marketplace, subject: {kind: marketplace, id: buyerId}`.

Сопоставления по телефону/email с клиентом магазина здесь **нет**. Pricing хранит membership
по ключу `marketplace:<buyerId>`; обычный клиент — отдельный субъект `customer:<id>`.
Выбор зависит от активной группы, её типа цены для канала marketplace, активных типов цены
и конфигурации канала/basePriceTypeId. Отсутствие membership означает публичный профиль.
`MarketplaceService.check` регистрирует marketplace-покупателя по UUID в pricing-реестре сервера;
в `pricing/contexts` регистрация не требуется. Данные получателя заказа не определяют группу.

Ответ: `{items:[{tenantId, profileKey, revision}]}`. Профиль — `marketplace:default` либо
`marketplace:<24 hex priceTypeId>`, revision — pricing writeFence. Go проверяет полноту,
уникальность, принадлежность tenant и формат профиля. Прайса и контактных данных в ответе нет.
Стоимость профиля уже есть в `PriceProfiles` проекции и поискового документа: индекс выбирает её
**до** фильтров цены, сортировки, пагинации и фасетов. Для строгого пересчёта этот же endpoint
принимает `items`/`promoCode`, а адаптер `PriceGroup` получает рассчитанные позиции и скидки.

## Маршруты

Раньше `catalog.PriceContexts` собирал видимых участников и синхронно вызывал сервер.
Guard добавил 300ms, LRU 30s и stale 5m, но холодная страница всё равно ждала сеть.
Теперь `pricecontexts.Service.ForDisplay` заменяет только порт контекстов показа:

| Путь `/api/v1` | Использование |
|---|---|
| `/catalog/offers`, фильтры, группы, фасеты | `PrepareQuery`, один снимок до поиска; `pricing: public/personal` только для покупателя |
| `/catalog/offers/{id}`, варианты | Выбор профиля для карточки и вариантов |
| `/recommendations?context=home/offer` | Цены кандидатов и ценовой диапазон исходного предложения |
| `/catalog/sponsored` | Те же контексты для рекламы и органики |
| Панель поиска | Отдельного backend-маршрута нет: recommendations/home, shops, taxonomy; выдача — catalog/offers |
| `/buyer/favorites`, `/buyer/price-watches`, `/buyer/profile` | Персонализация сохранённых предложений через тот же кэш |
| `/cart` GET | Сохранённый снимок, без нового расчёта |
| Изменение/проверка корзины, checkout/confirm, доставка/создание заказа | Прежний строгий API, синхронный PriceGroup/quote/create, ошибки не скрываются |

Отзывы используют прежний ограниченный кэш guard и отдельный бюджет чтения.

## Кэш и фон

Одна временная запись на покупателя содержит карту tenant → profile и время начала загрузки;
таблиц покупателей/продавцов нет. Redis площадки — основной, память с LRU и TTL — резерв
при отсутствии/отказе Redis. В Redis hash на покупателя и ограниченный sorted set активных ключей;
удаление по TTL и вытеснение проверяются атомарными Lua-операциями. Память ограничивает также
pending-загрузки и очищает истёкшие записи при обращениях. Карты копируются на границе.

Страница не вызывает сервер: промах, ошибка или запись за пределом TTL + stale дают публичные
профили всех запрошенных магазинов и запускают фон. Неполный снимок также даёт публичный ответ.
Доступ к Redis имеет бюджет 20ms, включая dial/read/write/pool, без повторов; при отказе читается
память. Запуск публичного API не требует успешного стартового ping Redis; вход/refresh по-прежнему
требуют собственных зависимостей identity и Redis. Неизменённые гостевые JSON и кэш не получают поля pricing.
`personal` означает, что выбран хотя бы один непубличный профиль; подтверждённые default-профили дают `public`.

Фон собирает полный снимок видимых участников, партиями до 100, с общим дедлайном. Одновременно
работает не больше настроенного числа загрузок, один покупатель — одна загрузка на процесс;
очереди ожидающих и неограниченных горутин нет. Разные процессы могут загрузить одного покупателя
параллельно; токен гарантирует, что сохраняется только актуальная загрузка. Фоновая ошибка не
продлевает жизнь записи. Используется пул чтений [platformguard](platform-dependency.md) и общий размыкатель отказов соединения/шлюза.
Ошибка пачки не указывает виновный магазин и не начисляется остальным; старый кэш контекстов удалён: stale никогда не переиздаётся как свежий ответ сервера.
Отмена запроса страницы не отменяет фон; остановка сервиса отменяет его. Вход, refresh и bootstrap
после успешного получения сессии запускают предзагрузку без ожидания сервера.

## События сервера

В `src/common/enums/broker-topics.ts` уже есть `catalog.product.projected` (все priceProfiles),
`catalog.product.price-updated` (прайс товара), `stock.item.updated` (остаток и цена),
`promotions.promotion.updated/deleted`, `catalog.product.promotions-updated`,
`catalog.promotion.projected`, события `platform.tenant.*`. Префикс добавляется на границе
`withTopicPrefix`. Эти события не содержат назначения группы покупателю. `PricingService.assign`
меняет membership транзакционно, но не публикует соответствующий Kafka event.
Нужен новый [price-context-changed.v1](api/price-context-changed.v1.md); потребитель площадки готов.

## Настройки и наблюдаемость

| Переменная | Default | Ограничение |
|---|---|---|
| `PRICE_CONTEXT_TTL` | `60s` | > 0, не больше минуты |
| `PRICE_CONTEXT_STALE_TTL` | `0s` | >= 0; сумма с TTL <= минуты |
| `PRICE_CONTEXT_CACHE_SIZE` | `10000` | 1–100000 покупателей, отдельно общий Redis и процесс |
| `PRICE_CONTEXT_MAX_CONCURRENT` | `8` | 1–1000 фоновых загрузок на процесс |
| `PRICE_CONTEXT_LOAD_TIMEOUT` | `1s` | > 0, <= 30s на весь снимок |

Настройки проверяет `internal/config`. Ограничение суммарной минуты обеспечивает безопасное
старение без событий; при истечении персональная цена немедленно заменяется публичной.
`PLATFORM_READ_TIMEOUT=300ms` ограничивает отдельную партию; прежние настройки breaker общие
с обязательными операциями. `PLATFORM_CACHE_*` теперь относятся только к отзывам.

`marketplace_price_contexts_total{operation,outcome}`: cache hit/miss/stale/error,
response public/personal, load ok/error/timeout/cache_error/invalidated/skipped,
`marketplace_price_context_invalidations_total{tenant,outcome}` — invalidated/unknown_buyer/duplicate/error.
`marketplace_price_context_load_duration_seconds{outcome}` — полный фон,
`marketplace_price_context_cache_size{backend=redis|memory}` — размер при последнем обслуживании.
Общие метрики platformguard сохраняются. Покупателей в метках нет.

Витрине: после входа/refresh тихо повторить каталог через 1–2 секунды при `pricing=public`,
с ограниченным числом попыток; public также нормален для покупателя без персональных условий.
Серверу: создать тему и публикацию outbox по отдельному контракту; площадка тему не создаёт.
