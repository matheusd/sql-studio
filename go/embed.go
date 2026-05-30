package main

import _ "embed"

// sampleDB is the bundled sample SQLite database used by `sqlite preview`. It is
// application demo data, so it is embedded here in the main package rather than
// in the importable server library. The copy at ./sample.sqlite3 is committed
// (the whole go/ module tree, including built assets, is committed).
//
//go:embed sample.sqlite3
var sampleDB []byte
