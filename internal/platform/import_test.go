package platform_test

import (
	"IF3110-K02-09/internal/auth"
	"IF3110-K02-09/internal/database"
	"IF3110-K02-09/internal/platform"
	"IF3110-K02-09/internal/testutil"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyImportIsAtomicIdempotentAndPreservesPassword(t *testing.T) {
	p := testutil.NewPostgres(t)
	ctx := context.Background()
	if err := platform.EnsureSchema(ctx, p.DB); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	rand.Read(key)
	t.Setenv(database.DatabaseCredentialsKeyEnv, base64.StdEncoding.EncodeToString(key))
	cipher, err := database.NewDatabasePasswordCipherFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("original-password-preserved")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "identity.json")
	content, _ := json.Marshal(map[string]any{"Users": map[int]auth.User{42: {ID: 42, Username: "legacy", PasswordHash: hash, Admin: true}}, "Groups": map[int]any{9: map[string]any{"ID": 9, "Name": "legacy-group", "Members": map[int]bool{42: true}, "Grants": map[string]string{}}}})
	if os.WriteFile(path, content, 0600) != nil {
		t.Fatal("fixture tidak dapat ditulis")
	}
	for i := 0; i < 2; i++ {
		if err = platform.ImportLegacy(ctx, p.DB, cipher, path, ""); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var stored string
	p.DB.QueryRow(`SELECT count(*) FROM platform.users`).Scan(&count)
	if count != 1 {
		t.Fatal("impor menduplikasi user")
	}
	p.DB.QueryRow(`SELECT password_hash FROM platform.users WHERE username='legacy'`).Scan(&stored)
	if stored != hash || !auth.VerifyPassword(stored, "original-password-preserved") {
		t.Fatal("password lama berubah")
	}
	if auth.NewPostgresStore(p.DB).Bootstrap(ctx, "replacement", "replacement-password-long") != nil {
		t.Fatal("bootstrap ulang gagal")
	}
	p.DB.QueryRow(`SELECT count(*) FROM platform.users`).Scan(&count)
	if count != 1 {
		t.Fatal("bootstrap mengubah instalasi existing")
	}
	changed := append(content, byte(' '))
	os.WriteFile(path, changed, 0600)
	if platform.ImportLegacy(ctx, p.DB, cipher, path, "") == nil {
		t.Fatal("sumber berubah tidak ditolak")
	}
	actual, _ := os.ReadFile(path)
	if string(actual) != string(changed) {
		t.Fatal("sumber ditimpa")
	}
	conflict := filepath.Join(t.TempDir(), "conflict.json")
	bad, _ := json.Marshal(map[string]any{"Users": map[int]auth.User{99: {ID: 99, Username: "legacy", PasswordHash: hash, Disabled: true}}, "Groups": map[int]any{}})
	os.WriteFile(conflict, bad, 0600)
	if platform.ImportLegacy(ctx, p.DB, cipher, conflict, "") == nil {
		t.Fatal("konflik identitas tidak ditolak")
	}
	p.DB.QueryRow(`SELECT count(*) FROM platform.users`).Scan(&count)
	if count != 1 {
		t.Fatal("impor konflik menambah data parsial")
	}
}
