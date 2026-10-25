package database

import (
	"crypto/sha256"
	"encoding/hex"
)

func HistorySchema(d Dialect, name string) string {
	if !d.IsMySQL() {
		return "platform_migrations"
	}
	sum := sha256.Sum256([]byte(name))
	return "atlas_history_" + hex.EncodeToString(sum[:8])
}
