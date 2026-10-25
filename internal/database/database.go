package database

import (
	"fmt"
	"strings"
)

type Dialect string

const (
	Postgres Dialect = "postgres"
	MySQL    Dialect = "mysql"
	MariaDB  Dialect = "mariadb"
)

func (d Dialect) IsMySQL() bool { return d == MySQL || d == MariaDB }
func (d Dialect) Valid() bool   { return d == Postgres || d.IsMySQL() }
func (d Dialect) Bind(n int) string {
	if d.IsMySQL() {
		return "?"
	}
	return fmt.Sprintf("$%d", n)
}
func (d Dialect) Quote(name string) string {
	if d.IsMySQL() {
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

type Config struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Driver Dialect `json:"driver"`
	DSN    string  `json:"dsn"`
}

func (Config) String() string   { return "[konfigurasi legacy disembunyikan]" }
func (Config) GoString() string { return "[konfigurasi legacy disembunyikan]" }
