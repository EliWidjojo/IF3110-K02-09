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

-- Penghapusan user atau group membersihkan keanggotaannya tanpa menghapus entitas di sisi lain.
CREATE TABLE IF NOT EXISTS platform.group_memberships (
	user_id INT NOT NULL REFERENCES platform.users(id) ON DELETE CASCADE,
	group_id INT NOT NULL REFERENCES platform.groups(id) ON DELETE CASCADE,
	PRIMARY KEY (user_id, group_id)
);

CREATE TABLE IF NOT EXISTS platform.database_connections (
	id SERIAL PRIMARY KEY,
	name TEXT NOT NULL UNIQUE CHECK (btrim(name) <> ''),
	host TEXT NOT NULL CHECK (btrim(host) <> ''),
	port INT NOT NULL DEFAULT 5432 CHECK (port BETWEEN 1 AND 65535),
	database_name TEXT NOT NULL CHECK (btrim(database_name) <> ''),
	ssl_mode TEXT NOT NULL DEFAULT 'require'
		CHECK (ssl_mode IN ('disable', 'allow', 'prefer', 'require', 'verify-ca', 'verify-full')),
	-- Blob kredensial harus sudah dienkripsi oleh aplikasi sebelum disimpan ke database internal.
	encrypted_credentials BYTEA NOT NULL CHECK (octet_length(encrypted_credentials) > 0)
);

-- Penghapusan group atau konfigurasi database mencabut akses terkait tanpa menghapus entitas di sisi lain.
CREATE TABLE IF NOT EXISTS platform.group_database_access (
	group_id INT NOT NULL REFERENCES platform.groups(id) ON DELETE CASCADE,
	database_id INT NOT NULL REFERENCES platform.database_connections(id) ON DELETE CASCADE,
	access_level TEXT NOT NULL CHECK (access_level IN ('View Only', 'View + Edit')),
	PRIMARY KEY (group_id, database_id)
);
`
