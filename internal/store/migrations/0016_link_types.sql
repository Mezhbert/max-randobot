-- 0016_link_types — типы трекинг-ссылок (по ревизии флоу от 13.11):
--   unique  — «уникальная ссылка»: ведёт на канал (последний пост);
--   super   — «суперссылка»: ведёт на конкретный пост (только публичный канал);
--   contest — «конкурсная ссылка»: генерируются пачкой под конкурс
--             (по одной на каждый выбранный канал).
ALTER TABLE utm_links
    ADD COLUMN IF NOT EXISTS link_type TEXT NOT NULL DEFAULT 'unique';
ALTER TABLE utm_links
    ADD CONSTRAINT utm_links_type_chk CHECK (link_type IN ('unique','super','contest'))
    NOT VALID;
