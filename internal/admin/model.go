package admin

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
