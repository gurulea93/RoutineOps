-- Stage 6: telegram link tokens for IT admin notifications
ALTER TABLE users ADD COLUMN IF NOT EXISTS telegram_link_token TEXT UNIQUE;
