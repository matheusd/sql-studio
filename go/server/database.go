package server

import "context"

// ROWS_PER_PAGE is the number of rows returned per page of table data.
// Mirrors the Rust constant ROWS_PER_PAGE.
const ROWS_PER_PAGE = 50

// Database is the interface every backend implements to serve the UI's API.
// It mirrors the Rust `Database` trait. Additional backends (Postgres, MySQL,
// etc.) can be added later by implementing this interface.
type Database interface {
	Overview(ctx context.Context) (*Overview, error)
	Tables(ctx context.Context) (*Tables, error)
	Table(ctx context.Context, name string) (*Table, error)
	TableData(ctx context.Context, name string, page int) (*TableData, error)
	TablesWithColumns(ctx context.Context) (*TablesWithColumns, error)
	Query(ctx context.Context, query string) (*Query, error)
	Erd(ctx context.Context) (*Erd, error)
}
