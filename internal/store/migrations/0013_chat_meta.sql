-- Structured fields for web chat incident cards (severity, title, state,
-- host name, summary lines). Additive; '{}' means no structured data.
ALTER TABLE chat_messages ADD COLUMN meta jsonb NOT NULL DEFAULT '{}'::jsonb;
