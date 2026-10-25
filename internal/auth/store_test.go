package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"IF3110-K02-09/internal/access"
)

func TestPasswordHash(t *testing.T) {
	password := "strong-password-123"
	a, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := HashPassword(password)
	if a == b || strings.Contains(a, password) || !VerifyPassword(a, password) || VerifyPassword(a, "wrong") || VerifyPassword("bad", password) {
		t.Fatal("password hashing failure")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
	if VerifyPassword("pbkdf2-sha256$999999999$x$x", password) {
		t.Fatal("unbounded cost accepted")
	}
}
func TestPersistentGroupPolicyAndRevocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateUser("alice", "strong-password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	g1, _ := s.CreateGroup("readers")
	g2, _ := s.CreateGroup("editors")
	for _, g := range []int{g1, g2} {
		if err := s.SetMembership(g, id, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetGrant(g1, "one", access.View); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGrant(g2, "one", access.Edit); err != nil {
		t.Fatal(err)
	}
	before, p, _ := s.Snapshot(id)
	if p.Databases["one"] != access.Edit || p.Databases["two"] != access.None {
		t.Fatal("invalid effective grant")
	}
	if err := s.SetMembership(g2, id, false); err != nil {
		t.Fatal(err)
	}
	after, p, _ := s.Snapshot(id)
	if after.SecurityVersion <= before.SecurityVersion || p.Databases["one"] != access.View {
		t.Fatal("permission reduction did not revoke")
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	u, ok := reopened.FindUsername("alice")
	if !ok || !VerifyPassword(u.PasswordHash, "strong-password-123") {
		t.Fatal("identity not persisted")
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "strong-password-123") {
		t.Fatal("plaintext password persisted")
	}
	if err := s.SetGrant(g1, "one", access.Level("admin")); err == nil {
		t.Fatal("unknown level accepted")
	}
}
func TestLastAdminAndFailedPersistence(t *testing.T) {
	s, _ := OpenStore("")
	id, _ := s.CreateUser("admin", "strong-password-123", true)
	if err := s.UpdateUser(id, false, false, ""); err == nil {
		t.Fatal("last admin removed")
	}
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(file, "identity.json")
	if _, err := s.CreateGroup("unsaved"); err == nil {
		t.Fatal("expected persistence error")
	}
	if len(s.data.Groups) != 0 {
		t.Fatal("failed persistence changed live state")
	}
}
