package platform

// SchemaSQL hanya diterapkan pada database internal platform, terpisah dari migrasi database target.
// Penyimpanan kredensial oleh modul authentication perlu merujuk identitas user yang sama.
const SchemaSQL = `
CREATE SCHEMA IF NOT EXISTS platform;

CREATE TABLE IF NOT EXISTS platform.users (
	id SERIAL PRIMARY KEY,
	username TEXT NOT NULL UNIQUE CHECK (btrim(username) <> ''),
	is_active BOOLEAN NOT NULL DEFAULT TRUE,
	is_platform_admin BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE IF NOT EXISTS platform.groups (
	id SERIAL PRIMARY KEY,
	name TEXT NOT NULL UNIQUE CHECK (btrim(name) <> '')
);
`
