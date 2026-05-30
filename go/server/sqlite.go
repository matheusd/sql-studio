package server

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// SQLiteDB is the SQLite implementation of Database. It ports the Rust `sqlite`
// module, reusing the same SQL queries and result ordering.
type SQLiteDB struct {
	path         string
	db           *sql.DB
	queryTimeout time.Duration
}

// OpenSQLite opens a SQLite database. If path == "preview", the bundled sample
// database is written to "sample.db" and opened read-only; otherwise the file at
// path is opened read-write. sample is the embedded sample DB bytes (see
// server.SampleDB()).
func OpenSQLite(path string, queryTimeout time.Duration, sample []byte) (*SQLiteDB, error) {
	var dsn string
	if path == "preview" {
		if err := os.WriteFile("sample.db", sample, 0o644); err != nil {
			return nil, err
		}
		path = "sample.db"
		dsn = path + "?mode=ro"
	} else {
		dsn = path + "?mode=rw"
	}

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	// A single connection keeps access to the file serialized and deterministic.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		return nil, err
	}

	// Confirm the file is actually a database (mirrors the Rust open check).
	var tables int
	err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type="table"`).Scan(&tables)
	if err != nil {
		return nil, err
	}
	slog.Info("opened sqlite database", "tables", tables, "path", path)

	return &SQLiteDB{path: path, db: db, queryTimeout: queryTimeout}, nil
}

// colInfo holds a row of PRAGMA table_info.
type colInfo struct {
	name    string
	ctype   string
	notnull int
	pk      int
}

func (d *SQLiteDB) tableInfo(ctx context.Context, name string) ([]colInfo, error) {
	rows, err := d.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info('%s')", name))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []colInfo
	for rows.Next() {
		var cid int
		var nm, ct string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &nm, &ct, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		out = append(out, colInfo{name: nm, ctype: ct, notnull: notnull, pk: pk})
	}
	return out, rows.Err()
}

func (d *SQLiteDB) tableNames(ctx context.Context) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type="table"`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// scalarInt runs a query expected to return a single integer.
func (d *SQLiteDB) scalarInt(ctx context.Context, query string, args ...any) (int, error) {
	var v int
	err := d.db.QueryRowContext(ctx, query, args...).Scan(&v)
	return v, err
}

// scalarIntOr returns the scalar int, or def on any error (Rust's unwrap_or).
func (d *SQLiteDB) scalarIntOr(ctx context.Context, def int, query string, args ...any) int {
	v, err := d.scalarInt(ctx, query, args...)
	if err != nil {
		return def
	}
	return v
}

// hasFirstColumnPK mirrors the Rust check: the pk flag of the first PRAGMA
// table_info row equals 1.
func (d *SQLiteDB) hasFirstColumnPK(ctx context.Context, name string) bool {
	info, err := d.tableInfo(ctx, name)
	if err != nil || len(info) == 0 {
		return false
	}
	return info[0].pk == 1
}

func (d *SQLiteDB) Overview(ctx context.Context) (*Overview, error) {
	fileName := filepath.Base(d.path)

	stat, err := os.Stat(d.path)
	if err != nil {
		return nil, err
	}
	dbSize := formatSize(float64(stat.Size()))
	modified := stat.ModTime().UTC()
	// created: not portably available on Linux; Rust yields None here too.

	var sqliteVersion string
	if err := d.db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&sqliteVersion); err != nil {
		return nil, err
	}

	tables, err := d.scalarInt(ctx, `SELECT count(*) FROM sqlite_master WHERE type="table"`)
	if err != nil {
		return nil, err
	}
	indexes, err := d.scalarInt(ctx, `SELECT count(*) FROM sqlite_master WHERE type="index"`)
	if err != nil {
		return nil, err
	}
	triggers, err := d.scalarInt(ctx, `SELECT count(*) FROM sqlite_master WHERE type="trigger"`)
	if err != nil {
		return nil, err
	}
	views, err := d.scalarInt(ctx, `SELECT count(*) FROM sqlite_master WHERE type="view"`)
	if err != nil {
		return nil, err
	}

	names, err := d.tableNames(ctx)
	if err != nil {
		return nil, err
	}

	rowCounts := make([]Count, 0, len(names))
	for _, name := range names {
		count := d.scalarIntOr(ctx, 0, fmt.Sprintf("SELECT count(*) FROM '%s'", name))
		rowCounts = append(rowCounts, Count{Name: name, Count: count})
	}
	sortCountDesc(rowCounts)

	columnCounts := make([]Count, 0, len(names))
	for _, name := range names {
		info, err := d.tableInfo(ctx, name)
		count := 0
		if err == nil {
			count = len(info)
		}
		columnCounts = append(columnCounts, Count{Name: name, Count: count})
	}
	sortCountDesc(columnCounts)

	indexCounts := make([]Count, 0, len(names))
	for _, name := range names {
		count := d.scalarIntOr(ctx, 0,
			"SELECT count(*) FROM sqlite_master WHERE type='index' AND tbl_name=?1", name)
		if d.hasFirstColumnPK(ctx, name) {
			count++
		}
		indexCounts = append(indexCounts, Count{Name: name, Count: count})
	}
	sortCountDesc(indexCounts)

	return &Overview{
		FileName:      fileName,
		DBSize:        dbSize,
		SQLiteVersion: &sqliteVersion,
		Created:       nil,
		Modified:      &modified,
		Tables:        tables,
		Indexes:       indexes,
		Triggers:      triggers,
		Views:         views,
		RowCounts:     rowCounts,
		ColumnCounts:  columnCounts,
		IndexCounts:   indexCounts,
	}, nil
}

func (d *SQLiteDB) Tables(ctx context.Context) (*Tables, error) {
	names, err := d.tableNames(ctx)
	if err != nil {
		return nil, err
	}

	tables := make([]Count, 0, len(names))
	for _, name := range names {
		count := d.scalarIntOr(ctx, 0, fmt.Sprintf("SELECT count(*) FROM '%s'", name))
		tables = append(tables, Count{Name: name, Count: count})
	}
	// Ascending by count, then by name (Rust: a.count.cmp(b.count).then(a.name.cmp(b.name))).
	sort.SliceStable(tables, func(i, j int) bool {
		if tables[i].Count != tables[j].Count {
			return tables[i].Count < tables[j].Count
		}
		return tables[i].Name < tables[j].Name
	})

	return &Tables{Tables: tables}, nil
}

func (d *SQLiteDB) Table(ctx context.Context, name string) (*Table, error) {
	stat, err := os.Stat(d.path)
	if err != nil {
		return nil, err
	}
	moreThanFive := stat.Size() > 5_000_000_000

	var sqlText string
	err = d.db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type="table" AND name = ?1`, name).Scan(&sqlText)
	if err != nil {
		return nil, err
	}

	rowCount := d.scalarIntOr(ctx, 0, fmt.Sprintf("SELECT count(*) FROM '%s'", name))

	tableSize := "N/A"
	if moreThanFive {
		tableSize = "> 5GB"
	} else {
		var size int64
		if err := d.db.QueryRowContext(ctx,
			"SELECT SUM(pgsize) FROM dbstat WHERE name = ?1", name).Scan(&size); err == nil {
			tableSize = formatSize(float64(size))
		}
	}

	indexCount := d.scalarIntOr(ctx, 0,
		"SELECT count(*) FROM sqlite_master WHERE type='index' AND tbl_name=?1", name)
	if d.hasFirstColumnPK(ctx, name) {
		indexCount++
	}

	columnCount := 0
	if info, err := d.tableInfo(ctx, name); err == nil {
		columnCount = len(info)
	}

	return &Table{
		Name:        name,
		SQL:         &sqlText,
		RowCount:    rowCount,
		IndexCount:  indexCount,
		ColumnCount: columnCount,
		TableSize:   tableSize,
	}, nil
}

func (d *SQLiteDB) TableData(ctx context.Context, name string, page int) (*TableData, error) {
	empty := &TableData{Columns: []string{}, Rows: [][]any{}}

	info, err := d.tableInfo(ctx, name)
	if err != nil || len(info) == 0 {
		return empty, nil
	}
	firstColumn := info[0].name
	offset := (page - 1) * ROWS_PER_PAGE

	query := fmt.Sprintf(
		"SELECT * FROM '%s' ORDER BY %s LIMIT %d OFFSET %d",
		name, firstColumn, ROWS_PER_PAGE, offset,
	)
	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return empty, nil
	}
	defer rows.Close()

	columns, rowData, err := scanRows(rows)
	if err != nil {
		return empty, nil
	}
	return &TableData{Columns: columns, Rows: rowData}, nil
}

func (d *SQLiteDB) TablesWithColumns(ctx context.Context) (*TablesWithColumns, error) {
	names, err := d.tableNames(ctx)
	if err != nil {
		return nil, err
	}

	tables := make([]TableWithColumns, 0, len(names))
	for _, name := range names {
		columns := []string{}
		if info, err := d.tableInfo(ctx, name); err == nil {
			for _, c := range info {
				columns = append(columns, c.name)
			}
		}
		tables = append(tables, TableWithColumns{TableName: name, Columns: columns})
	}
	// Sort by table name length (Rust: sort_by_key(|t| t.table_name.len())).
	sort.SliceStable(tables, func(i, j int) bool {
		return len(tables[i].TableName) < len(tables[j].TableName)
	})

	return &TablesWithColumns{Tables: tables}, nil
}

func (d *SQLiteDB) Query(ctx context.Context, query string) (*Query, error) {
	ctx, cancel := context.WithTimeout(ctx, d.queryTimeout)
	defer cancel()

	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, rowData, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	return &Query{Columns: columns, Rows: rowData}, nil
}

func (d *SQLiteDB) Erd(ctx context.Context) (*Erd, error) {
	names, err := d.tableNames(ctx)
	if err != nil {
		return nil, err
	}

	tables := make([]ErdTable, 0, len(names))
	relationships := make([]ErdRelationship, 0)

	for _, tableName := range names {
		info, err := d.tableInfo(ctx, tableName)
		if err != nil {
			return nil, err
		}
		columns := make([]ErdColumn, 0, len(info))
		for _, c := range info {
			columns = append(columns, ErdColumn{
				Name:         c.name,
				DataType:     c.ctype,
				Nullable:     c.notnull == 0,
				IsPrimaryKey: c.pk > 0,
			})
		}

		fks, err := d.foreignKeys(ctx, tableName)
		if err != nil {
			return nil, err
		}
		relationships = append(relationships, fks...)

		tables = append(tables, ErdTable{Name: tableName, Columns: columns})
	}

	return &Erd{Tables: tables, Relationships: relationships}, nil
}

// foreignKeys reads PRAGMA foreign_key_list: id, seq, table, from, to, ...
func (d *SQLiteDB) foreignKeys(ctx context.Context, tableName string) ([]ErdRelationship, error) {
	rows, err := d.db.QueryContext(ctx, fmt.Sprintf("PRAGMA foreign_key_list('%s')", tableName))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ErdRelationship
	for rows.Next() {
		var id, seq int
		var toTable string
		var fromCol string
		var toCol sql.NullString
		var onUpdate, onDelete, match sql.NullString
		if err := rows.Scan(&id, &seq, &toTable, &fromCol, &toCol, &onUpdate, &onDelete, &match); err != nil {
			// Rust filter_maps errors away; skip the row.
			continue
		}
		// Rust drops rows whose `to` column can't be read as a String.
		if !toCol.Valid {
			continue
		}
		out = append(out, ErdRelationship{
			FromTable:  tableName,
			FromColumn: fromCol,
			ToTable:    toTable,
			ToColumn:   toCol.String,
		})
	}
	return out, rows.Err()
}

// scanRows reads all columns of all rows into JSON-marshalable values.
func scanRows(rows *sql.Rows) ([]string, [][]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	out := make([][]any, 0)
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		row := make([]any, len(cols))
		for i, v := range vals {
			row[i] = sqliteValueToJSON(v)
		}
		out = append(out, row)
	}
	if cols == nil {
		cols = []string{}
	}
	return cols, out, rows.Err()
}

// sortCountDesc sorts counts by Count descending (stable), matching Rust's
// sort_by(|a, b| b.count.cmp(&a.count)).
func sortCountDesc(counts []Count) {
	sort.SliceStable(counts, func(i, j int) bool {
		return counts[i].Count > counts[j].Count
	})
}
