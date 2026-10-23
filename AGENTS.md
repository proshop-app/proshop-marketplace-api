# AGENTS.md

Инструкции для агентов, работающих в репозитории. Полные правила — в
[CLAUDE.md](CLAUDE.md); здесь — то, без чего нельзя начинать.

1. **Слои.** `domain ← application ← interfaces, infrastructure`, сборка в
   `cmd/`. Домен и `pkg/` импортируют только стандартную библиотеку и `pkg/`
   (домен — ещё свои пакеты). Приложение не импортирует `infrastructure`,
   `interfaces`, `config`. Нарушение ловят `internal/architecture` и
   `depguard` — CI красный.
2. **Деньги** — `pkg/money`, целые копейки, `Add`/`Sub`; рубли из событий —
   `money.ParseRubles` из `json.Number`. Никакого `float64`.
3. **Агрегаты** меняются своими методами; переходы — условным `UPDATE … WHERE
   status = исходный AND version = исходная`, иначе типизированный конфликт.
4. **Наружу — только через порты** `internal/application/ports`. Тесты
   приложения — на `internal/application/fake`; настоящие сервисы — только в
   `internal/infrastructure/**/*_test.go` с тегом `integration`.
5. **Ошибки** — `pkg/errs` с кодом и ключом; HTTP переводит их одной
   таблицей `internal/interfaces/http/errors.go`.
6. **Универсальность.** Ни одной константы под конкретный магазин, город или
   категорию.
7. **Kafka.** Топики — сервера: не заводить их у брокера. Магазин — из ключа,
   тела или `x-tenant`; без магазина — в `parked_events` с метрикой. Коммит —
   после записи, дедуп — `processed_events`.
8. **Раскладка кода.** `const`/`var`/`type` в начале файла, функции после;
   `cmd/*/main.go` — только `main`; строки ≤ 120; многострочный литерал или
   вызов — по элементу на строку; SQL — константами в начале файла. Держат
   `internal/architecture` и `lll`.
9. **Сервер.** Внутренний API сервера — только по `docs/server-contract.md`;
   адаптеры внешних API тестируются на `testdata`, живые вызовы — под флагом.

Проверки перед пушем:

```bash
go build ./... && go vet ./... && go vet -tags integration ./... && make lint && make test && make test-integration
```

`make dev` поднимает всё одной командой. Коммиты и PR — по-русски, в master
не пушить.
