-- 0007_captcha_passed_default.sql
-- captcha_passed теперь реально проверяется (image-капча). По умолчанию FALSE —
-- участник считается прошедшим только после успешного ввода.
ALTER TABLE participants ALTER COLUMN captcha_passed SET DEFAULT FALSE;
