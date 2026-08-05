-- ===========================================================================
-- 0004_scheduler_claim.sql
--
-- ШАГ 1 из 2 атомарного захвата задач планировщиком.
--
-- ВНИМАНИЕ: ALTER TYPE ... ADD VALUE нельзя использовать В ТОЙ ЖЕ ТРАНЗАКЦИИ,
-- где новое значение фигурирует (напр. в CREATE INDEX ... WHERE status='x').
-- PostgreSQL требует, чтобы новое enum-значение было закоммичено отдельно
-- (SQLSTATE 55P04 "unsafe use of new value of enum type"). Поэтому миграция
-- разбита: здесь только добавление enum-значений, колонки/индексы — в 0005.
--
-- Промежуточные статусы 'publishing'/'finishing' нужны для atomic claim
-- (см. 0005 и store.ClaimForPublish/ClaimForFinish) — устраняют дубли
-- публикации/итогов при параллельных тиках и внешнем cron.
-- ===========================================================================

ALTER TYPE giveaway_status ADD VALUE IF NOT EXISTS 'publishing';
ALTER TYPE giveaway_status ADD VALUE IF NOT EXISTS 'finishing';
