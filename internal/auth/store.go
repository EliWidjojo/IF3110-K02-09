package auth

import "errors"

var ErrNotFound = errors.New("identitas tidak tersedia")
var ErrConflict = errors.New("identitas sudah ada")

// User hanya menjadi rekaman internal dan tidak pernah diserialisasi sebagai respons.
type User struct {
	ID               int
	Username         string
	PasswordHash     string
	Admin            bool
	Disabled         bool
	SecurityVersion  uint64
	PrivilegeVersion uint64
}
