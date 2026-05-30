package server

import (
	"fmt"
	"time"
)

// formatSize converts a byte count into a human-readable string, mirroring the
// Rust `helpers::format_size` (units B, KB, MB, GB, TB; divide by 1024; 2 dp).
func formatSize(size float64) string {
	units := [...]string{"B", "KB", "MB", "GB", "TB"}
	unit := 0
	for size >= 1024.0 && unit < len(units)-1 {
		size /= 1024.0
		unit++
	}
	return fmt.Sprintf("%.2f %s", size, units[unit])
}

// sqliteValueToJSON converts a value scanned from SQLite into a JSON-marshalable
// Go value, mirroring the Rust `helpers::rusqlite_value_to_json`:
//
//	NULL    -> nil        (JSON null)
//	INTEGER -> int64      (JSON number)
//	REAL    -> float64    (JSON number)
//	TEXT    -> string     (JSON string)
//	BLOB    -> []int      (JSON array of byte values)
//
// go-sqlite3 scans TEXT as string and BLOB as []byte, preserving the
// text-vs-blob distinction that the Rust code relies on.
func sqliteValueToJSON(v any) any {
	switch val := v.(type) {
	case nil:
		return nil
	case int64:
		return val
	case float64:
		return val
	case string:
		return val
	case bool:
		// SQLite has no native bool; go-sqlite3 may surface one for some
		// expressions. Match serde's numeric-ish handling by passing through.
		return val
	case []byte:
		// BLOB: serialize as a JSON array of byte values, like serde_json::json!(blob).
		out := make([]int, len(val))
		for i, b := range val {
			out[i] = int(b)
		}
		return out
	case time.Time:
		// go-sqlite3 parses datetime-typed columns into time.Time. Rust returns
		// the raw text; emit an RFC3339 string so the cell still renders.
		return val.UTC().Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", val)
	}
}
