package platform

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
)

const DatabaseCredentialsKeyEnv = "DATABASE_CREDENTIALS_KEY"

var (
	ErrDatabaseEncryptionKey = errors.New("kunci enkripsi kredensial tidak tersedia atau tidak valid")
	ErrDatabaseEncryption    = errors.New("password koneksi tidak dapat dienkripsi")
	ErrDatabaseDecryption    = errors.New("kredensial koneksi tidak dapat dibuka dengan aman")
)

type DatabasePasswordCipher struct {
	aead cipher.AEAD
}

// Kunci wajib berasal dari environment berupa Base64 dari 32 byte dan tidak memiliki nilai bawaan.
func NewDatabasePasswordCipherFromEnv() (*DatabasePasswordCipher, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(os.Getenv(DatabaseCredentialsKeyEnv))
	defer clear(key)
	if err != nil || len(key) != 32 {
		return nil, ErrDatabaseEncryptionKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrDatabaseEncryptionKey
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrDatabaseEncryptionKey
	}
	return &DatabasePasswordCipher{aead: aead}, nil
}

func (c *DatabasePasswordCipher) requireKey() error {
	if c == nil || c.aead == nil {
		return ErrDatabaseEncryptionKey
	}
	return nil
}

func (c *DatabasePasswordCipher) encrypt(databaseID int, password string) ([]byte, error) {
	if err := c.requireKey(); err != nil {
		return nil, err
	}
	if databaseID <= 0 {
		return nil, ErrDatabaseEncryption
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrDatabaseEncryption
	}
	plaintext := []byte(password)
	defer clear(plaintext)
	// Envelope memuat versi, nonce acak, dan ciphertext bertag autentikasi agar nonce tidak pernah digunakan ulang secara sengaja.
	envelope := append([]byte{1}, nonce...)
	return c.aead.Seal(envelope, nonce, plaintext, databasePasswordAAD(databaseID)), nil
}

func (c *DatabasePasswordCipher) decrypt(databaseID int, envelope []byte) (string, error) {
	if err := c.requireKey(); err != nil {
		return "", err
	}
	if databaseID <= 0 || len(envelope) < 1+c.aead.NonceSize()+c.aead.Overhead() || envelope[0] != 1 {
		return "", ErrDatabaseDecryption
	}
	nonceEnd := 1 + c.aead.NonceSize()
	plaintext, err := c.aead.Open(nil, envelope[1:nonceEnd], envelope[nonceEnd:], databasePasswordAAD(databaseID))
	if err != nil {
		return "", ErrDatabaseDecryption
	}
	defer clear(plaintext)
	return string(plaintext), nil
}

// ID koneksi yang tidak berubah diikat sebagai authenticated data untuk menolak pemindahan ciphertext antarbaris.
func databasePasswordAAD(databaseID int) []byte {
	return binary.BigEndian.AppendUint64([]byte("platform.database_connections.password.v1:"), uint64(databaseID))
}

func (*DatabasePasswordCipher) String() string   { return "[kunci kredensial disembunyikan]" }
func (*DatabasePasswordCipher) GoString() string { return "[kunci kredensial disembunyikan]" }
