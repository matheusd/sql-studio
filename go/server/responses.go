package server

import "time"

// These response types mirror the Rust `responses` module exactly, including
// JSON field names, so the existing frontend (ui/src/api.ts) works unchanged.
//
// Nullable fields use pointers WITHOUT omitempty so a nil value marshals to JSON
// `null` (the frontend's zod schemas use `.nullable()` and expect the key present).

type Count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Overview struct {
	FileName      string     `json:"file_name"`
	DBSize        string     `json:"db_size"`
	SQLiteVersion *string    `json:"sqlite_version"`
	Created       *time.Time `json:"created"`
	Modified      *time.Time `json:"modified"`
	Tables        int        `json:"tables"`
	Indexes       int        `json:"indexes"`
	Triggers      int        `json:"triggers"`
	Views         int        `json:"views"`
	RowCounts     []Count    `json:"row_counts"`
	ColumnCounts  []Count    `json:"column_counts"`
	IndexCounts   []Count    `json:"index_counts"`
}

type Tables struct {
	Tables []Count `json:"tables"`
}

type Table struct {
	Name        string  `json:"name"`
	SQL         *string `json:"sql"`
	RowCount    int     `json:"row_count"`
	IndexCount  int     `json:"index_count"`
	ColumnCount int     `json:"column_count"`
	TableSize   string  `json:"table_size"`
}

type TableData struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

type TableWithColumns struct {
	TableName string   `json:"table_name"`
	Columns   []string `json:"columns"`
}

type TablesWithColumns struct {
	Tables []TableWithColumns `json:"tables"`
}

type Query struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

type Metadata struct {
	Version     string `json:"version"`
	CanShutdown bool   `json:"can_shutdown"`
}

type ErdColumn struct {
	Name         string `json:"name"`
	DataType     string `json:"data_type"`
	Nullable     bool   `json:"nullable"`
	IsPrimaryKey bool   `json:"is_primary_key"`
}

type ErdTable struct {
	Name    string      `json:"name"`
	Columns []ErdColumn `json:"columns"`
}

type ErdRelationship struct {
	FromTable  string `json:"from_table"`
	FromColumn string `json:"from_column"`
	ToTable    string `json:"to_table"`
	ToColumn   string `json:"to_column"`
}

type Erd struct {
	Tables        []ErdTable        `json:"tables"`
	Relationships []ErdRelationship `json:"relationships"`
}
