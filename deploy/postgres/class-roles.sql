-- Роли классов нагрузки площадки за пулером. Выполняет администратор базы (суперпользователь
-- или роль с CREATEROLE), не сервис: docs/postgres-connections.md, раздел «Роли».
-- Скрипт повторяемый. Пароли ролей задаются отдельно (CloudNativePG managed roles или
-- ALTER ROLE … PASSWORD), здесь их нет.
--
-- Сроки ролей обязаны совпадать с DATABASE_<КЛАСС>_STATEMENT_TIMEOUT и
-- DATABASE_<КЛАСС>_TRANSACTION_TIMEOUT сервиса: старт сверяет их и не идёт при расхождении.
-- После смены сроков — RECONNECT пулера: роль применяется при открытии серверного соединения.

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'marketplace_app') THEN
        CREATE ROLE marketplace_app NOLOGIN;
    END IF;
END
$$;

DO $$
DECLARE
    class text;
BEGIN
    FOREACH class IN ARRAY ARRAY['read', 'checkout', 'ingestion', 'background', 'orders'] LOOP
        IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'marketplace_' || class) THEN
            EXECUTE format('CREATE ROLE %I LOGIN', 'marketplace_' || class);
        END IF;
        EXECUTE format('GRANT marketplace_app TO %I', 'marketplace_' || class);
    END LOOP;
END
$$;

ALTER ROLE marketplace_read SET statement_timeout = '750ms';
ALTER ROLE marketplace_read SET idle_in_transaction_session_timeout = '2s';
ALTER ROLE marketplace_checkout SET statement_timeout = '2s';
ALTER ROLE marketplace_checkout SET idle_in_transaction_session_timeout = '5s';
ALTER ROLE marketplace_ingestion SET statement_timeout = '2s';
ALTER ROLE marketplace_ingestion SET idle_in_transaction_session_timeout = '5s';
ALTER ROLE marketplace_background SET statement_timeout = '10s';
ALTER ROLE marketplace_background SET idle_in_transaction_session_timeout = '30s';
ALTER ROLE marketplace_orders SET statement_timeout = '2s';
ALTER ROLE marketplace_orders SET idle_in_transaction_session_timeout = '5s';

-- Только данные: схему меняют миграции под владельцем marketplace по прямому адресу.
GRANT CONNECT ON DATABASE marketplace TO marketplace_app;

-- Права на всё, что уже создано, — по каждой схеме приложения (public, reference и любая следующая):
-- список схем здесь не перечисляется, чтобы новая схема миграции не осталась без прав.
DO $$
DECLARE
    app_schema text;
BEGIN
    FOR app_schema IN
        SELECT nspname FROM pg_namespace
        WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema'
    LOOP
        EXECUTE format('GRANT USAGE ON SCHEMA %I TO marketplace_app', app_schema);
        EXECUTE format(
            'GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO marketplace_app',
            app_schema
        );
        EXECUTE format('GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA %I TO marketplace_app', app_schema);
    END LOOP;
END
$$;

-- Права на то, что миграции создадут позже, — без привязки к схеме: новая таблица, последовательность
-- или схема владельца marketplace доступна ролям классов сразу, без повторного запуска скрипта.
ALTER DEFAULT PRIVILEGES FOR ROLE marketplace
    GRANT USAGE ON SCHEMAS TO marketplace_app;
ALTER DEFAULT PRIVILEGES FOR ROLE marketplace
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO marketplace_app;
ALTER DEFAULT PRIVILEGES FOR ROLE marketplace
    GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO marketplace_app;
