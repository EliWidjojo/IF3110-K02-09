package platform

// User menjadi identitas bersama untuk modul authentication dan administrasi platform.
type User struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	IsActive bool   `json:"is_active"`
	// IsPlatformAdmin menandai wewenang pengelolaan platform yang terpisah dari tingkat akses database.
	IsPlatformAdmin bool `json:"is_platform_admin"`
}

// Group merepresentasikan kelompok pengguna yang menjadi dasar pemberian akses database.
type Group struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// GroupMembership memisahkan keanggotaan dari user agar satu user dapat tergabung dalam beberapa group.
type GroupMembership struct {
	UserID  int `json:"user_id"`
	GroupID int `json:"group_id"`
}

// DatabaseConnection merupakan representasi publik konfigurasi PostgreSQL tanpa password atau ciphertext.
type DatabaseConnection struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	DatabaseName string `json:"database_name"`
	Username     string `json:"username"`
	SSLMode      string `json:"ssl_mode"`
	SSLRootCert  string `json:"ssl_root_cert"`
	// PasswordConfigured hanya menunjukkan keberadaan password terenkripsi, termasuk password kosong yang dikonfigurasi eksplisit.
	PasswordConfigured bool `json:"password_configured"`
}

// AccessLevel menyatakan izin pada database tertentu dan terpisah dari peran Platform Admin.
type AccessLevel string

const (
	AccessViewOnly AccessLevel = "View Only"
	AccessViewEdit AccessLevel = "View + Edit"
)

// GroupDatabaseAccess mengikat izin ke pasangan group dan database, bukan langsung ke user.
type GroupDatabaseAccess struct {
	GroupID     int         `json:"group_id"`
	DatabaseID  int         `json:"database_id"`
	AccessLevel AccessLevel `json:"access_level"`
}
