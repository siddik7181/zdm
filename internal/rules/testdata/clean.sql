-- Safe: nullable column, INSTANT pinned.
ALTER TABLE users ADD COLUMN last_seen DATETIME NULL, ALGORITHM=INSTANT;
-- Bounded data change is not flagged.
UPDATE users SET last_seen = NOW() WHERE id < 100;
