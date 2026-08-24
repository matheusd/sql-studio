# Build the Go port of sql-studio.
#
# All Go code lives in the go/ directory, which is a single module
# (github.com/matheusd/sql-studio) with three packages:
#   .        main      — the command; embeds the sample database
#   ui       ui        — the built frontend assets (embeds ./dist)
#   server   server    — the importable HTTP/server library (no embeds)
#
# Rooting the module at go/ keeps ui/node_modules out of the module. The built
# assets (go/ui/dist and go/sample.sqlite3) are committed, so `go build` works
# without a prior npm build; run `make assets` to refresh them after changing
# the frontend. Requires CGO (mattn/go-sqlite3).

GO          ?= go
BIN         ?= sql-studio
UI_SRC      := ui
UI_DST      := go/ui/dist
SAMPLE_DST  := go/sample.sqlite3

# Match Rust's bundled SQLite, which enables the dbstat virtual table used to
# report per-table sizes.
CGO_CFLAGS  ?= -DSQLITE_ENABLE_DBSTAT_VTAB
export CGO_CFLAGS

.PHONY: all ui assets build run vet lib-build tidy clean

all: build

# Build the React/TypeScript UI into ui/dist.
ui:
	cd $(UI_SRC) && npm install && npm run build

# Refresh the committed built assets inside the module from their sources.
assets:
	rm -rf $(UI_DST)
	cp -r $(UI_SRC)/dist $(UI_DST)
	cp sample.sqlite3 $(SAMPLE_DST)

# Build the single binary at the repo root.
build:
	CGO_ENABLED=1 CGO_CFLAGS="$(CGO_CFLAGS)" $(GO) -C go build -o "$(CURDIR)/$(BIN)" .

run: build
	./$(BIN) sqlite preview

vet:
	CGO_ENABLED=1 CGO_CFLAGS="$(CGO_CFLAGS)" $(GO) -C go vet ./...

# Prove importable server/UI packages do not pull the cgo SQLite driver.
lib-build:
	CGO_ENABLED=0 $(GO) -C go build ./server ./ui

tidy:
	$(GO) -C go mod tidy

clean:
	rm -f $(BIN)
