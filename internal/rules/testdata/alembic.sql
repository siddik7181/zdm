-- Running upgrade abc -> def

BEGIN;

ALTER TABLE tools ADD COLUMN owner VARCHAR(64) NULL;

UPDATE alembic_version SET version_num='def' WHERE alembic_version.version_num = 'abc';

THIS IS NOT SQL;

COMMIT;
