-- 0013: стоимость размещения трекинг-ссылки (сколько заплачено за выход:
-- реклама/пост). Нужна для CPA = cost / подписки в аналитике.
ALTER TABLE utm_links
    ADD COLUMN IF NOT EXISTS cost NUMERIC(12,2) NOT NULL DEFAULT 0;
