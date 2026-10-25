package migration

import (
	"fmt"
)

type ColumnDefinition struct {
	TableName  string `json:"table_name"`
	ColumnName string `json:"column_name"`
	DataType   string `json:"data_type"`
	IsNullable string `json:"is_nullable"`
}

// untuk mengambil seluruh daftar tabel dan kolom saat ini dari PostgreSQL
func (ms *MigrationService) InspectCurrentSchema() (map[string]map[string]ColumnDefinition, error) {
	query := `
		SELECT table_name, column_name, data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema = 'public' 
		  AND table_name != 'schema_migrations';
	`
	rows, err := ms.TargetDB.Query(query)
	if err != nil {
		return nil, fmt.Errorf("gagal membaca metadata schema saat ini: %w", err)
	}
	defer rows.Close()

	// Structure: schemaMap[tableName][columnName] = ColumnDefinition
	schemaMap := make(map[string]map[string]ColumnDefinition)

	for rows.Next() {
		var col ColumnDefinition
		if err := rows.Scan(&col.TableName, &col.ColumnName, &col.DataType, &col.IsNullable); err != nil {
			return nil, err
		}

		if _, exists := schemaMap[col.TableName]; !exists {
			schemaMap[col.TableName] = make(map[string]ColumnDefinition)
		}
		schemaMap[col.TableName][col.ColumnName] = col
	}

	return schemaMap, nil
}