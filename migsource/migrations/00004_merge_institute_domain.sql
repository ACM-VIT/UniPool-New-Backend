-- +goose NO TRANSACTION
-- +goose Up

-- Merge institute_domains into institutes. The previous schema used a
-- separate institute_domains table for a one-to-one relationship; this
-- migration collapses it into a nullable domain column on institutes.
--
-- Safe on both existing databases (migrate data + drop old table) and
-- fresh databases (institute_domains was created by 00001 but is
-- immediately collapsed here).

-- +goose StatementBegin
ALTER TABLE institutes ADD COLUMN IF NOT EXISTS domain VARCHAR(120) NULL;
-- +goose StatementEnd

-- Copy existing domains from institute_domains into institutes.
-- +goose StatementBegin
UPDATE institutes i
   SET domain = (SELECT LOWER(d.domain) FROM institute_domains d WHERE d.institute_id = i.id LIMIT 1)
 WHERE EXISTS (SELECT 1 FROM institute_domains d WHERE d.institute_id = i.id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX IF NOT EXISTS idx_institutes_domain ON institutes (domain ASC);
-- +goose StatementEnd

-- Drop FK constraint first, then index, then the table.
-- +goose StatementBegin
ALTER TABLE institute_domains DROP CONSTRAINT IF EXISTS fk_institute_domains_institute;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS institute_domains@idx_institute_domains_domain CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS institute_domains CASCADE;
-- +goose StatementEnd

-- +goose Down

-- Recreate institute_domains from the domain column.
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS institute_domains (
	id UUID NOT NULL DEFAULT gen_random_uuid(),
	created_at TIMESTAMPTZ NULL,
	updated_at TIMESTAMPTZ NULL,
	deleted_at TIMESTAMPTZ NULL,
	domain VARCHAR(120) NOT NULL,
	institute_id UUID NOT NULL,
	CONSTRAINT institute_domains_pkey PRIMARY KEY (id ASC),
	INDEX idx_institute_domains_institute_id (institute_id ASC),
	UNIQUE INDEX idx_institute_domains_domain (domain ASC),
	INDEX idx_institute_domains_deleted_at (deleted_at ASC)
);
-- +goose StatementEnd

-- +goose StatementBegin
INSERT INTO institute_domains (domain, institute_id)
SELECT LOWER(domain), id FROM institutes WHERE domain IS NOT NULL;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE institute_domains ADD CONSTRAINT fk_institute_domains_institute FOREIGN KEY (institute_id) REFERENCES institutes(id);
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX IF EXISTS institutes@idx_institutes_domain CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE institutes DROP COLUMN IF EXISTS domain;
-- +goose StatementEnd
