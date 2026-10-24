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

-- User lama dapat belum memiliki hash; modul authentication harus menolak login sampai kredensial tersedia.
ALTER TABLE platform.users
	ADD COLUMN IF NOT EXISTS password_hash TEXT CHECK (btrim(password_hash) <> '');

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
	username TEXT NOT NULL CHECK (btrim(username) <> ''),
	ssl_mode TEXT NOT NULL DEFAULT 'require'
		CHECK (ssl_mode IN ('disable', 'allow', 'prefer', 'require', 'verify-ca', 'verify-full')),
	ssl_root_cert TEXT NOT NULL DEFAULT '',
	-- Kolom ini menyimpan envelope password terenkripsi, sedangkan username disimpan pada kolom terpisah.
	encrypted_credentials BYTEA NOT NULL CHECK (octet_length(encrypted_credentials) > 0)
);

-- Username konfigurasi lama dibiarkan NULL sampai dilengkapi tanpa menebak atau menimpa kredensial yang sudah ada.
ALTER TABLE platform.database_connections
	ADD COLUMN IF NOT EXISTS username TEXT CHECK (btrim(username) <> ''),
	ADD COLUMN IF NOT EXISTS ssl_root_cert TEXT NOT NULL DEFAULT '';

-- Penghapusan group atau konfigurasi database mencabut akses terkait tanpa menghapus entitas di sisi lain.
CREATE TABLE IF NOT EXISTS platform.group_database_access (
	group_id INT NOT NULL REFERENCES platform.groups(id) ON DELETE CASCADE,
	database_id INT NOT NULL REFERENCES platform.database_connections(id) ON DELETE CASCADE,
	access_level TEXT NOT NULL CHECK (access_level IN ('View Only', 'View + Edit')),
	PRIMARY KEY (group_id, database_id)
);
`
