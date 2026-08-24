package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"github.com/matheusd/sql-studio/go/server"
	"github.com/matheusd/sql-studio/go/ui"
	_ "github.com/mattn/go-sqlite3"
	"github.com/spf13/cobra"
)

// version reported by /api/metadata (mirrors the Rust CARGO_PKG_VERSION).
const version = "0.1.51"

// shared flags, populated by cobra.
var (
	flagAddress    string
	flagTimeout    string
	flagBasePath   string
	flagNoBrowser  bool
	flagNoShutdown bool
)

func realMain() error {
	root := &cobra.Command{
		Use:           "sql-studio",
		Short:         "A single binary SQL database explorer",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	pf := root.PersistentFlags()
	pf.StringVarP(&flagAddress, "address", "a", envOr("ADDRESS", "127.0.0.1:3030"), "The address to bind to.")
	pf.StringVarP(&flagTimeout, "timeout", "t", envOr("TIMEOUT", "5s"), "Timeout duration for queries sent from the query page (Go duration, e.g. 5s).")
	pf.StringVarP(&flagBasePath, "base-path", "b", os.Getenv("BASE_PATH"), "Base path to be provided to the UI. [e.g /sql-studio]")
	pf.BoolVar(&flagNoBrowser, "no-browser", envBool("NO_BROWSER"), "Don't open URL in the system browser.")
	pf.BoolVar(&flagNoShutdown, "no-shutdown", envBool("NO_SHUTDOWN"), "Don't show the shutdown button in the UI.")

	sqliteCmd := &cobra.Command{
		Use:   "sqlite <database>",
		Short: "A local SQLite database.",
		Long: "A local SQLite database.\n\n" +
			`Use the path "preview" if you don't have an sqlite db at hand; a sample db will be created for you.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			database := args[0]
			if database == "" {
				database = os.Getenv("DATABASE")
			}
			return serve(func(timeout time.Duration) (server.Database, error) {
				return server.OpenSQLite(database, timeout, sampleDB)
			})
		},
	}

	root.AddCommand(sqliteCmd)
	return root.Execute()
}

// serve opens the database via open and runs the HTTP server until interrupted.
func serve(open func(time.Duration) (server.Database, error)) error {
	timeout, err := time.ParseDuration(flagTimeout)
	if err != nil {
		return fmt.Errorf("invalid --timeout %q: %w", flagTimeout, err)
	}

	if flagBasePath != "" && !strings.HasPrefix(flagBasePath, "/") {
		return fmt.Errorf("base path should have a forward slash (/) prefix")
	}

	db, err := open(timeout)
	if err != nil {
		return err
	}

	srv, err := server.New(db, ui.FS(), server.Options{
		Address:    flagAddress,
		BasePath:   flagBasePath,
		NoShutdown: flagNoShutdown,
		Version:    version,
	})
	if err != nil {
		return err
	}

	// Open the browser only when not behind a base path (likely a proxy) and not
	// disabled. The app is served under the server's prefix, so open that.
	if flagBasePath == "" && !flagNoBrowser {
		openBrowser("http://" + flagAddress + srv.Prefix())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	return srv.Run(ctx)
}

// openBrowser best-effort opens url in the system browser.
func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		cmd = "xdg-open"
		args = []string{url}
	}
	_ = exec.Command(cmd, args...).Start()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string) bool {
	v := strings.ToLower(os.Getenv(key))
	return v == "1" || v == "true"
}

func main() {
	if err := realMain(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err.Error())
		os.Exit(1)
	}
}
