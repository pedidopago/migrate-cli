package mariadb

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	sqldriver "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

var (
	databaseURL = flag.String("database-url", "", "Database URL")
	command     = flag.String("command", "_default_", "Migration command")
	migrations  = flag.String("migrations", "", "Migrations directory (ex: file://database/migrations)")
)

func Run() {
	slog.Info("starting migrations app")
	defer slog.Info("stopping migrations app")

	flag.Parse()
	args := append([]string{""}, flag.Args()...)

	if v := os.Getenv("DATABASE_URL"); v != "" {
		if *databaseURL != "" {
			slog.Warn("detected env DATABASE_URL, but the database-url parameter is already set; will ignore env")
		} else {
			*databaseURL = v
		}
	}

	if v := os.Getenv("MIGRATION_COMMAND"); v != "" {
		if *command != "_default_" {
			slog.Warn("detected env MIGRATION_COMMAND, but the command parameter is already set; will ignore env")
		} else {
			*command = v
		}
	}

	if *command == "_default_" {
		*command = "sync"
	}

	if v := os.Getenv("MIGRATION_URL"); v != "" {
		if *migrations != "" {
			slog.Warn("detected env MIGRATION_URL, but the migrations parameter is already set; will ignore env")
		} else {
			*migrations = v
		}
	}

	if *migrations == "" {
		slog.Error("--migrations (and|or env MIGRATION_URL) is required")
		os.Exit(1)
	}

	if !strings.Contains(*databaseURL, "multiStatements") {
		if !strings.Contains(*databaseURL, "?") {
			*databaseURL += "?multiStatements=true"
		} else {
			*databaseURL += "&multiStatements=true"
		}
	}

	slog.Debug("will open a connection to the database")

	db, err := sql.Open("mysql", *databaseURL)

	if err != nil && (*command != "new" && *command != "check" && *command != "c") {
		slog.Error("could not open a connection to the database", slog.String("error", err.Error()))
		os.Exit(2)
	} else if err == nil {
		defer db.Close()
	}

	fmt.Println("connected to the database")

	logMigrationDataHeader(db, strings.TrimPrefix(*migrations, "file://"))

	if os.Getenv("SKIP_ALL") == "true" {
		fmt.Println("SKIP_ALL is true, will skip migration")
		os.Exit(0)
	}

	var driver database.Driver
	var m *migrate.Migrate
	if db != nil {
		driver, err = mysql.WithInstance(db, &mysql.Config{})
		if err != nil {
			slog.Error("could not create the migration driver", slog.String("error", err.Error()))
			os.Exit(3)
		}
		m, err = migrate.NewWithDatabaseInstance(*migrations, "mysql", driver)
		if err != nil {
			slog.Error("could not create the migration driver (2)", slog.String("error", err.Error()))
			os.Exit(4)
		}
	}

	switch *command {
	case "up", "u":
		err = m.Up()
	case "down", "d":
		err = m.Down()
	case "force", "f":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Please specify a version to force the migration to")
			os.Exit(2)
		}
		var version int
		version, err = strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(3)
		}
		err = m.Force(version)
	case "step", "steps", "s":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Please specify a step count")
			os.Exit(4)
		}
		var steps int
		steps, err = strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(5)
		}
		err = m.Steps(steps)
	case "sync":
		mainDir := strings.TrimPrefix(*migrations, "file://")
		maxv := maxMigrationVersion(mainDir)
		err = m.Migrate(uint(maxv))
	case "new":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Please specify a migration name")
			os.Exit(6)
		}
		mainDir := strings.TrimPrefix(*migrations, "file://")
		maxn := maxMigrationVersion(mainDir)
		updata := "-- write your UP migration here\n"
		downdata := "-- write your DOWN migration here\n"
		fnup := filepath.Join(mainDir, fmt.Sprintf("%05d_%s.up.sql", maxn+1, args[1]))
		fndown := filepath.Join(mainDir, fmt.Sprintf("%05d_%s.down.sql", maxn+1, args[1]))
		if err := os.WriteFile(fnup, []byte(updata), 0644); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(7)
		}
		if err := os.WriteFile(fndown, []byte(downdata), 0644); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(8)
		}
		fmt.Printf("Created migration files:\n%s\n%s\n", fnup, fndown)
		os.Exit(0)
	case "check", "c":
		upfiles := make(map[string]bool)
		downfiles := make(map[string]bool)
		mainDir := strings.TrimPrefix(*migrations, "file://")
		filepath.Walk(mainDir, func(path string, info fs.FileInfo, err error) error {
			if strings.HasSuffix(path, ".up.sql") {
				_, fname := filepath.Split(path)
				num := strings.SplitN(fname, "_", 2)[0]
				if upfiles[num] {
					fmt.Fprintf(os.Stderr, "Duplicate migration version %s (%s)\n", num, path)
					os.Exit(9)
				}
				upfiles[num] = true
			}
			if strings.HasSuffix(path, ".down.sql") {
				_, fname := filepath.Split(path)
				num := strings.SplitN(fname, "_", 2)[0]
				if downfiles[num] {
					fmt.Fprintf(os.Stderr, "Duplicate migration version %s (%s)\n", num, path)
					os.Exit(9)
				}
				downfiles[num] = true
			}
			return nil
		})
		fmt.Fprintln(os.Stdout, "All migration files are unique")
		os.Exit(0)
	default:
		var version uint64
		version, err = strconv.ParseUint(*command, 10, 64)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(10)
		}
		err = m.Migrate(uint(version))
	}
	if err != nil {
		if err.Error() == "no change" {
			slog.Info(err.Error())
			os.Exit(0)
		}

		slog.Error(err.Error())

		os.Exit(10)
	}

	slog.Info("success!")
}

func maxMigrationVersion(migrationsPath string) uint {
	var maxn uint = 0
	_ = filepath.Walk(migrationsPath, func(path string, info fs.FileInfo, err error) error {
		if v, ok := migrationFileVersion(path); ok && v > maxn {
			maxn = v
		}
		return nil
	})
	return maxn
}

// logMigrationDataHeader reports where the database is and where the files
// could take it, before anything runs.
//
// The dirty flag is the reason this exists: golang-migrate runs each file
// untransacted, so a statement that fails mid-file leaves the version recorded
// and dirty, and every later run refuses with "Dirty database version N". Seeing
// that up front -- next to the versions actually available on disk -- is the
// difference between "force N and re-run" and reading a driver error.
//
// Never fatal: this is diagnostics. A database that cannot answer still gets its
// migration attempted, and the failure is reported by the command itself.
func logMigrationDataHeader(db *sql.DB, migrationsPath string) {
	if db != nil {
		var version uint64
		var dirty bool
		err := db.QueryRow("SELECT version, dirty FROM schema_migrations LIMIT 1").Scan(&version, &dirty)
		var myErr *sqldriver.MySQLError
		switch {
		// This runs before WithInstance creates schema_migrations, so a fresh
		// database surfaces either as no rows or as "table doesn't exist" (1146).
		case errors.Is(err, sql.ErrNoRows) || (errors.As(err, &myErr) && myErr.Number == 1146):
			slog.Info("schema_migrations: no version recorded (fresh database)")
		case err != nil:
			slog.Warn("schema_migrations: could not read version", slog.String("error", err.Error()))
		default:
			slog.Info("schema_migrations", slog.Uint64("version", version), slog.Bool("dirty", dirty))
		}
	}

	up, down := lastMigrationVersions(migrationsPath)
	slog.Info("migrations available", slog.Uint64("last_up", uint64(up)), slog.Uint64("last_down", uint64(down)))
}

// migrationFileVersion extracts the leading numeric version from a migration
// filename (e.g. "00042_add_foo.up.sql" -> 42). ok is false for non-.sql files
// or names without a numeric prefix.
func migrationFileVersion(path string) (version uint, ok bool) {
	if !strings.HasSuffix(path, ".sql") {
		return 0, false
	}
	_, fname := filepath.Split(path)
	num, _, _ := strings.Cut(fname, "_")
	v, err := strconv.ParseUint(num, 10, 64)
	if err != nil {
		return 0, false
	}
	return uint(v), true
}

// lastMigrationVersions reports the highest up and down versions on disk
// separately, because an up without its down is a real and common mistake and
// the two numbers differing is the cheapest way to see it.
func lastMigrationVersions(migrationsPath string) (up, down uint) {
	_ = filepath.Walk(migrationsPath, func(path string, info fs.FileInfo, err error) error {
		v, ok := migrationFileVersion(path)
		if !ok {
			return nil
		}
		if strings.HasSuffix(path, ".up.sql") && v > up {
			up = v
		}
		if strings.HasSuffix(path, ".down.sql") && v > down {
			down = v
		}
		return nil
	})
	return up, down
}
