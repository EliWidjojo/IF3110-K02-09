package database

import (
	"errors"
	"strings"
	"unicode"
)

// This parser is not a SQL sandbox; credentials must be confined to their target database.
func Statements(input string, dialect Dialect, nonAtomicDDL bool) ([]string, error) {
	if !dialect.Valid() {
		return nil, errors.New("unsupported dialect")
	}
	var cleaned strings.Builder
	var result []string
	quote := byte(0)
	dollar := ""
	blockDepth := 0
	line := false
	for i := 0; i < len(input); i++ {
		c := input[i]
		if line {
			if c == '\n' {
				line = false
				cleaned.WriteByte(' ')
			}
			continue
		}
		if blockDepth > 0 {
			if i+1 < len(input) && input[i:i+2] == "/*" {
				blockDepth++
				i++
			} else if i+1 < len(input) && input[i:i+2] == "*/" {
				blockDepth--
				i++
			}
			continue
		}
		if dollar != "" {
			if strings.HasPrefix(input[i:], dollar) {
				cleaned.WriteString(dollar)
				i += len(dollar) - 1
				dollar = ""
			} else {
				cleaned.WriteByte(c)
			}
			continue
		}
		if quote != 0 {
			cleaned.WriteByte(c)
			// SQL-mode-dependent escapes could hide transaction commands from this parser.
			if c == '\\' {
				return nil, errors.New("backslash escapes are unsupported; use doubled quotes")
			}
			if c == quote {
				if i+1 < len(input) && input[i+1] == quote {
					i++
					cleaned.WriteByte(quote)
				} else {
					quote = 0
				}
			}
			continue
		}
		if i+1 < len(input) && input[i:i+2] == "--" && (!dialect.IsMySQL() || i+2 == len(input) || unicode.IsSpace(rune(input[i+2]))) {
			line = true
			i++
			cleaned.WriteByte(' ')
			continue
		}
		if c == '#' && dialect.IsMySQL() {
			line = true
			cleaned.WriteByte(' ')
			continue
		}
		if i+1 < len(input) && input[i:i+2] == "/*" {
			if i+2 < len(input) && (input[i+2] == '!' || input[i+2] == '+') {
				return nil, errors.New("executable comments and hints are unsupported")
			}
			blockDepth = 1
			i++
			cleaned.WriteByte(' ')
			continue
		}
		if c == '\'' || c == '"' || (c == '`' && dialect.IsMySQL()) {
			quote = c
			cleaned.WriteByte(c)
			continue
		}
		if c == '$' && dialect == Postgres {
			j := i + 1
			for j < len(input) && (input[j] == '_' || input[j] >= 'a' && input[j] <= 'z' || input[j] >= 'A' && input[j] <= 'Z' || j > i+1 && input[j] >= '0' && input[j] <= '9') {
				j++
			}
			if j < len(input) && input[j] == '$' {
				dollar = input[i : j+1]
				cleaned.WriteString(dollar)
				i = j
				continue
			}
		}
		if c == ';' {
			if stmt := strings.TrimSpace(cleaned.String()); stmt != "" {
				result = append(result, stmt)
			}
			cleaned.Reset()
		} else {
			cleaned.WriteByte(c)
		}
	}
	if quote != 0 || dollar != "" || blockDepth > 0 {
		return nil, errors.New("unterminated SQL literal or comment")
	}
	if stmt := strings.TrimSpace(cleaned.String()); stmt != "" {
		result = append(result, stmt)
	}
	if len(result) == 0 {
		return nil, errors.New("empty migration")
	}
	for _, stmt := range result {
		fields := strings.FieldsFunc(strings.ToUpper(stmt), func(r rune) bool { return !unicode.IsLetter(r) && r != '_' })
		if len(fields) == 0 {
			return nil, errors.New("invalid migration statement")
		}
		verb := fields[0]
		ddl := verb == "CREATE" || verb == "ALTER" || verb == "DROP" || verb == "TRUNCATE" || verb == "COMMENT" || verb == "RENAME"
		dml := verb == "INSERT" || verb == "UPDATE" || verb == "DELETE"
		if !ddl && !dml {
			return nil, errors.New("only schema DDL and INSERT/UPDATE/DELETE migrations are supported")
		}
		if dialect.IsMySQL() && ddl && (!nonAtomicDDL || len(result) != 1) {
			return nil, errors.New("MySQL/MariaDB DDL requires one statement and explicit non_atomic_ddl=true; DDL cannot be rolled back")
		}
		if nonAtomicDDL && (!dialect.IsMySQL() || !ddl || len(result) != 1) {
			return nil, errors.New("non_atomic_ddl is only for one MySQL/MariaDB DDL statement")
		}
		if ddl && verb != "TRUNCATE" {
			object := ""
			if len(fields) > 1 {
				object = fields[1]
			}
			if object == "OR" && len(fields) > 3 && fields[2] == "REPLACE" {
				object = fields[3]
			}
			if (object == "UNIQUE" || object == "FULLTEXT" || object == "SPATIAL") && len(fields) > 2 {
				object = fields[2]
			}
			if object == "ON" && len(fields) > 2 {
				object = fields[2]
			}
			switch object {
			case "TABLE", "INDEX", "VIEW", "SCHEMA", "SEQUENCE", "TYPE":
			default:
				return nil, errors.New("jenis objek DDL tidak didukung")
			}
		}

	}
	return result, nil
}
