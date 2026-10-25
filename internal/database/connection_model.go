package database

// DatabaseConnection merupakan representasi publik konfigurasi PostgreSQL tanpa password atau ciphertext.
type DatabaseConnection struct {
	Driver        Dialect `json:"driver"`
	AuditUsername string  `json:"audit_username"`
	ID            int     `json:"id"`
	Name          string  `json:"name"`
	Host          string  `json:"host"`
	Port          int     `json:"port"`
	DatabaseName  string  `json:"database_name"`
	Username      string  `json:"username"`
	SSLMode       string  `json:"ssl_mode"`
	SSLRootCert   string  `json:"ssl_root_cert"`
	// PasswordConfigured hanya menunjukkan keberadaan password terenkripsi, termasuk password kosong yang dikonfigurasi eksplisit.
	PasswordConfigured bool `json:"password_configured"`
}
