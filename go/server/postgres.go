package server

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var postgresIdentifier = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// PostgresDB is the PostgreSQL implementation of Database. It uses a
// caller-owned pool and limits all catalog access to one validated schema.
type PostgresDB struct {
	db           *sql.DB
	schema       string
	queryTimeout time.Duration
}

// NewPostgres wraps an externally-owned PostgreSQL pool. SQL Studio never
// closes or retunes the pool. schema must be the lowercase schema pinned for
// all catalog and relation operations.
func NewPostgres(db *sql.DB, schema string, queryTimeout time.Duration) (*PostgresDB, error) {
	if db == nil {
		return nil, fmt.Errorf("postgres database is nil")
	}
	if !postgresIdentifier.MatchString(schema) {
		return nil, fmt.Errorf("invalid PostgreSQL schema %q", schema)
	}
	if queryTimeout <= 0 {
		return nil, fmt.Errorf("PostgreSQL query timeout must be positive")
	}
	return &PostgresDB{db: db, schema: schema, queryTimeout: queryTimeout}, nil
}

func quotePostgresIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
func (d *PostgresDB) relation(name string) string {
	return quotePostgresIdentifier(d.schema) + "." + quotePostgresIdentifier(name)
}

func (d *PostgresDB) relationNames(ctx context.Context) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema=$1 AND table_type IN ('BASE TABLE','VIEW') ORDER BY table_name`, d.schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func (d *PostgresDB) relationExists(ctx context.Context, name string) (string, error) {
	var found string
	err := d.db.QueryRowContext(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema=$1 AND table_name=$2 AND table_type IN ('BASE TABLE','VIEW')`, d.schema, name).Scan(&found)
	return found, err
}

func (d *PostgresDB) columns(ctx context.Context, name string) ([]colInfo, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT c.column_name,COALESCE(NULLIF(c.domain_name,''),c.data_type),CASE WHEN c.is_nullable='NO' THEN 1 ELSE 0 END,COALESCE(pk.ordinal_position,0)
		FROM information_schema.columns c LEFT JOIN (
		 SELECT kcu.column_name,kcu.ordinal_position FROM information_schema.table_constraints tc
		 JOIN information_schema.key_column_usage kcu ON kcu.constraint_name=tc.constraint_name AND kcu.constraint_schema=tc.constraint_schema AND kcu.table_schema=tc.table_schema AND kcu.table_name=tc.table_name
		 WHERE tc.table_schema=$1 AND tc.table_name=$2 AND tc.constraint_type='PRIMARY KEY'
		) pk ON pk.column_name=c.column_name WHERE c.table_schema=$1 AND c.table_name=$2 ORDER BY c.ordinal_position`, d.schema, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []colInfo
	for rows.Next() {
		var c colInfo
		if err := rows.Scan(&c.name, &c.ctype, &c.notnull, &c.pk); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func (d *PostgresDB) count(ctx context.Context, name string) int {
	var count int
	if err := d.db.QueryRowContext(ctx, `SELECT count(*) FROM `+d.relation(name)).Scan(&count); err != nil {
		return 0
	}
	return count
}

// Overview reports PostgreSQL catalog and relation-size metrics. There is no
// local database file, so FileName is the current database and Created/Modified
// remain unavailable.
func (d *PostgresDB) Overview(ctx context.Context) (*Overview, error) {
	ctx, cancel := context.WithTimeout(ctx, d.queryTimeout)
	defer cancel()
	var databaseName, version string
	var bytes int64
	if err := d.db.QueryRowContext(ctx, `SELECT current_database(),current_setting('server_version'),pg_database_size(current_database())`).Scan(&databaseName, &version, &bytes); err != nil {
		return nil, err
	}
	var tables, indexes, triggers, views int
	if err := d.db.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE c.relkind IN ('r','p')),count(*) FILTER (WHERE c.relkind='i'),count(*) FILTER (WHERE c.relkind='v'),count(*) FILTER (WHERE c.relkind IN ('r','p','v')) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1`, d.schema).Scan(&tables, &indexes, &views, new(int)); err != nil {
		return nil, err
	}
	if err := d.db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.triggers WHERE trigger_schema=$1`, d.schema).Scan(&triggers); err != nil {
		return nil, err
	}
	names, err := d.relationNames(ctx)
	if err != nil {
		return nil, err
	}
	rows, columns, indexCounts := make([]Count, 0, len(names)), make([]Count, 0, len(names)), make([]Count, 0, len(names))
	for _, name := range names {
		rows = append(rows, Count{Name: name, Count: d.count(ctx, name)})
		info, err := d.columns(ctx, name)
		if err != nil {
			return nil, err
		}
		columns = append(columns, Count{Name: name, Count: len(info)})
		var indexesForRelation int
		_ = d.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname=$1 AND tablename=$2`, d.schema, name).Scan(&indexesForRelation)
		indexCounts = append(indexCounts, Count{Name: name, Count: indexesForRelation})
	}
	sortCountDesc(rows)
	sortCountDesc(columns)
	sortCountDesc(indexCounts)
	return &Overview{FileName: databaseName, DBSize: formatSize(float64(bytes)), SQLiteVersion: &version, Tables: tables, Indexes: indexes, Triggers: triggers, Views: views, RowCounts: rows, ColumnCounts: columns, IndexCounts: indexCounts}, nil
}

func (d *PostgresDB) Tables(ctx context.Context) (*Tables, error) {
	names, err := d.relationNames(ctx)
	if err != nil {
		return nil, err
	}
	tables := make([]Count, 0, len(names))
	for _, name := range names {
		tables = append(tables, Count{Name: name, Count: d.count(ctx, name)})
	}
	sort.SliceStable(tables, func(i, j int) bool {
		if tables[i].Count != tables[j].Count {
			return tables[i].Count < tables[j].Count
		}
		return tables[i].Name < tables[j].Name
	})
	return &Tables{Tables: tables}, nil
}

func (d *PostgresDB) Table(ctx context.Context, name string) (*Table, error) {
	name, err := d.relationExists(ctx, name)
	if err != nil {
		return nil, err
	}
	info, err := d.columns(ctx, name)
	if err != nil {
		return nil, err
	}
	var indexes int
	if err := d.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname=$1 AND tablename=$2`, d.schema, name).Scan(&indexes); err != nil {
		return nil, err
	}
	var size int64
	if err := d.db.QueryRowContext(ctx, `SELECT pg_total_relation_size(c.oid) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`, d.schema, name).Scan(&size); err != nil {
		return nil, err
	}
	return &Table{Name: name, RowCount: d.count(ctx, name), IndexCount: indexes, ColumnCount: len(info), TableSize: formatSize(float64(size))}, nil
}

func (d *PostgresDB) TableData(ctx context.Context, name string, page int) (*TableData, error) {
	empty := &TableData{Columns: []string{}, Rows: [][]any{}}
	name, err := d.relationExists(ctx, name)
	if err != nil {
		return empty, nil
	}
	info, err := d.columns(ctx, name)
	if err != nil || len(info) == 0 {
		return empty, nil
	}
	if page < 1 {
		page = 1
	}
	order := info[0].name
	for _, c := range info {
		if c.pk > 0 {
			order = c.name
			break
		}
	}
	rows, err := d.db.QueryContext(ctx, `SELECT * FROM `+d.relation(name)+` ORDER BY `+quotePostgresIdentifier(order)+` LIMIT $1 OFFSET $2`, ROWS_PER_PAGE, (page-1)*ROWS_PER_PAGE)
	if err != nil {
		return empty, nil
	}
	defer rows.Close()
	cols, data, err := scanPostgresRows(rows)
	if err != nil {
		return empty, nil
	}
	return &TableData{Columns: cols, Rows: data}, nil
}

func (d *PostgresDB) TablesWithColumns(ctx context.Context) (*TablesWithColumns, error) {
	names, err := d.relationNames(ctx)
	if err != nil {
		return nil, err
	}
	tables := make([]TableWithColumns, 0, len(names))
	for _, name := range names {
		info, err := d.columns(ctx, name)
		if err != nil {
			return nil, err
		}
		cols := make([]string, 0, len(info))
		for _, c := range info {
			cols = append(cols, c.name)
		}
		tables = append(tables, TableWithColumns{TableName: name, Columns: cols})
	}
	sort.SliceStable(tables, func(i, j int) bool { return len(tables[i].TableName) < len(tables[j].TableName) })
	return &TablesWithColumns{Tables: tables}, nil
}

func (d *PostgresDB) Query(ctx context.Context, query string) (*Query, error) {
	ctx, cancel := context.WithTimeout(ctx, d.queryTimeout)
	defer cancel()
	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, data, err := scanPostgresRows(rows)
	if err != nil {
		return nil, err
	}
	return &Query{Columns: cols, Rows: data}, nil
}

func (d *PostgresDB) Erd(ctx context.Context) (*Erd, error) {
	names, err := d.relationNames(ctx)
	if err != nil {
		return nil, err
	}
	erd := &Erd{}
	for _, name := range names {
		info, err := d.columns(ctx, name)
		if err != nil {
			return nil, err
		}
		table := ErdTable{Name: name, Columns: make([]ErdColumn, 0, len(info))}
		for _, c := range info {
			table.Columns = append(table.Columns, ErdColumn{Name: c.name, DataType: c.ctype, Nullable: c.notnull == 0, IsPrimaryKey: c.pk > 0})
		}
		erd.Tables = append(erd.Tables, table)
	}
	rows, err := d.db.QueryContext(ctx, `SELECT tc.table_name,kcu.column_name,ccu.table_name,ccu.column_name FROM information_schema.table_constraints tc JOIN information_schema.key_column_usage kcu ON kcu.constraint_name=tc.constraint_name AND kcu.constraint_schema=tc.constraint_schema JOIN information_schema.constraint_column_usage ccu ON ccu.constraint_name=tc.constraint_name AND ccu.constraint_schema=tc.constraint_schema WHERE tc.table_schema=$1 AND tc.constraint_type='FOREIGN KEY'`, d.schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r ErdRelationship
		if err := rows.Scan(&r.FromTable, &r.FromColumn, &r.ToTable, &r.ToColumn); err != nil {
			return nil, err
		}
		erd.Relationships = append(erd.Relationships, r)
	}
	return erd, rows.Err()
}

func scanPostgresRows(rows *sql.Rows) ([]string, [][]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	out := [][]any{}
	for rows.Next() {
		values := make([]any, len(cols))
		holders := make([]any, len(cols))
		for i := range values {
			holders[i] = &values[i]
		}
		if err := rows.Scan(holders...); err != nil {
			return nil, nil, err
		}
		row := make([]any, len(values))
		for i, v := range values {
			row[i] = postgresValueToJSON(v)
		}
		out = append(out, row)
	}
	if cols == nil {
		cols = []string{}
	}
	return cols, out, rows.Err()
}
func postgresValueToJSON(v any) any {
	switch value := v.(type) {
	case nil:
		return nil
	case time.Time:
		return value.UTC().Format(time.RFC3339Nano)
	case []byte:
		return string(value)
	default:
		return value
	}
}

var _ Database = (*PostgresDB)(nil)
