package migrate

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

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// Dialect is everything that differs between one database and the next. The
// rest of this package -- flag and env parsing, the command switch, and above
// all the guards that refuse a backwards migration -- is identical for every
// engine, and duplicating it per engine is how the guards drift apart.
type Dialect struct {
	// DriverName is the database/sql driver to open with.
	DriverName string
	// MigrateName is the name golang-migrate knows this database by.
	MigrateName string
	// WithInstance wraps an open *sql.DB in the golang-migrate driver.
	WithInstance func(*sql.DB) (database.Driver, error)
	// PrepareDSN applies engine-specific connection settings. lockWaitTimeout
	// is in seconds, -1 meaning "not set by the operator".
	PrepareDSN func(dsn string, lockWaitTimeout int) string
	// IsUndefinedTable reports whether err means "schema_migrations does not
	// exist yet", which is the ordinary state of a fresh database.
	IsUndefinedTable func(error) bool
	// LogSessionInfo reports engine-specific session settings worth seeing
	// before a migration runs. Optional.
	LogSessionInfo func(*sql.DB)
}

var (
	databaseURL = flag.String("database-url", "", "Database URL")
	command     = flag.String("command", "_default_", "Migration command")
	migrations  = flag.String("migrations", "", "Migrations directory (ex: file://database/migrations)")
	// lockWaitTimeout is seconds, and -1 means "not set here". Production
	// servers routinely carry lock_wait_timeout=86400 -- a full day -- and that
	// turns one blocked ALTER into an outage: even an ALGORITHM=INSTANT change
	// needs a brief exclusive metadata lock, MDL requests are FIFO, and every
	// query on that table queues behind the one that is waiting. Failing in
	// seconds and retrying costs a run; waiting a day costs the table.
	lockWaitTimeout = flag.Int("lock-wait-timeout", -1, "Seconds to wait for a metadata lock before giving up (0 = leave the server default)")
	allowDown       = flag.Bool("allow-down", false, "Permit migrations that move the schema BACKWARDS")
)

// defaultLockWaitTimeout applies when neither the flag, the env nor the DSN says
// otherwise. Long enough to ride out ordinary contention, short enough that a
// blocked ALTER gives up instead of holding the queue.
const defaultLockWaitTimeout = 10

func Run(d Dialect) {
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

	if v := os.Getenv("MIGRATION_LOCK_WAIT_TIMEOUT"); v != "" {
		if *lockWaitTimeout != -1 {
			slog.Warn("detected env MIGRATION_LOCK_WAIT_TIMEOUT, but the lock-wait-timeout parameter is already set; will ignore env")
		} else if n, err := strconv.Atoi(v); err != nil {
			slog.Error("MIGRATION_LOCK_WAIT_TIMEOUT is not a number", slog.String("value", v))
			os.Exit(1)
		} else {
			*lockWaitTimeout = n
		}
	}

	if os.Getenv("MIGRATION_ALLOW_DOWN") == "true" {
		*allowDown = true
	}

	// Carried on the DSN rather than a SET after connecting: the migrate driver
	// runs on a pool, and a session variable set on one connection does not
	// reach the others.
	*databaseURL = d.PrepareDSN(*databaseURL, *lockWaitTimeout)

	slog.Debug("will open a connection to the database")

	db, err := sql.Open(d.DriverName, *databaseURL)

	if err != nil && (*command != "new" && *command != "check" && *command != "c") {
		slog.Error("could not open a connection to the database", slog.String("error", err.Error()))
		os.Exit(2)
	} else if err == nil {
		defer db.Close()
	}

	fmt.Println("connected to the database")

	logMigrationDataHeader(d, db, strings.TrimPrefix(*migrations, "file://"))

	if os.Getenv("SKIP_ALL") == "true" {
		fmt.Println("SKIP_ALL is true, will skip migration")
		os.Exit(0)
	}

	var driver database.Driver
	var m *migrate.Migrate
	if db != nil {
		driver, err = d.WithInstance(db)
		if err != nil {
			slog.Error("could not create the migration driver", slog.String("error", err.Error()))
			os.Exit(3)
		}
		m, err = migrate.NewWithDatabaseInstance(*migrations, d.MigrateName, driver)
		if err != nil {
			slog.Error("could not create the migration driver (2)", slog.String("error", err.Error()))
			os.Exit(4)
		}
	}

	// Every path that can move the schema backwards is refused unless it was
	// asked for. See refuseDown for why the guard is not just on "down".
	switch *command {
	case "up", "u":
		err = m.Up()
	case "down", "d":
		refuseDown(db, "the down command rolls back EVERY migration")
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
		if steps < 0 {
			refuseDown(db, fmt.Sprintf("a step count of %d rolls back %d migration(s)", steps, -steps))
		}
		err = m.Steps(steps)
	case "sync":
		mainDir := strings.TrimPrefix(*migrations, "file://")
		maxv := maxMigrationVersion(mainDir)
		refuseDownTo(db, maxv, "sync")
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
		refuseDownTo(db, uint(version), *command)
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
func logMigrationDataHeader(d Dialect, db *sql.DB, migrationsPath string) {
	if db != nil {
		var version uint64
		var dirty bool
		err := db.QueryRow("SELECT version, dirty FROM schema_migrations LIMIT 1").Scan(&version, &dirty)
		switch {
		// This runs before WithInstance creates schema_migrations, so a fresh
		// database surfaces either as no rows or as "table does not exist".
		case errors.Is(err, sql.ErrNoRows) || d.IsUndefinedTable(err):
			slog.Info("schema_migrations: no version recorded (fresh database)")
		case err != nil:
			slog.Warn("schema_migrations: could not read version", slog.String("error", err.Error()))
		default:
			slog.Info("schema_migrations", slog.Uint64("version", version), slog.Bool("dirty", dirty))
		}
	}

	if db != nil && d.LogSessionInfo != nil {
		d.LogSessionInfo(db)
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

// refuseDown stops a backwards migration unless it was explicitly asked for.
//
// The default assumes production, because that is where this runs unattended as
// an init container. A rollback there is not a mistake to be logged -- it drops
// columns and tables from a live database, and golang-migrate runs each file
// untransacted, so a down that fails half way leaves the schema in a shape
// nobody designed.
func refuseDown(db *sql.DB, what string) {
	if *allowDown {
		slog.Warn("proceeding with a BACKWARDS migration because it was explicitly allowed", slog.String("reason", what))
		return
	}
	slog.Error("refusing to migrate backwards",
		slog.String("reason", what),
		slog.String("hint", "pass --allow-down (or MIGRATION_ALLOW_DOWN=true) if this is really what you want"))
	if db != nil {
		_ = db.Close()
	}
	os.Exit(11)
}

// refuseDownTo is the same guard for the commands that name a target version
// instead of a direction. `sync` and a bare version number both call Migrate,
// which moves the schema DOWN when the database is ahead of the target -- which
// is exactly what happens when an older image is deployed over a newer schema.
// Guarding only the `down` command would leave that door open, and it is the
// door that opens by accident.
func refuseDownTo(db *sql.DB, target uint, label string) {
	current, ok := currentVersion(db)
	if !ok || current <= uint64(target) {
		return
	}
	refuseDown(db, fmt.Sprintf("%s targets version %d but the database is at %d", label, target, current))
}

// currentVersion reads schema_migrations. ok is false when there is nothing to
// compare against -- a fresh database, or a read that failed -- and the caller
// then lets the migration proceed: refusing on a failed read would block every
// deploy the moment the query breaks.
func currentVersion(db *sql.DB) (version uint64, ok bool) {
	if db == nil {
		return 0, false
	}
	var dirty bool
	if err := db.QueryRow("SELECT version, dirty FROM schema_migrations LIMIT 1").Scan(&version, &dirty); err != nil {
		return 0, false
	}
	return version, true
}

// DefaultLockWaitTimeout is the timeout applied when neither the flag, the env
// nor the DSN says otherwise.
const DefaultLockWaitTimeout = defaultLockWaitTimeout

// AppendDSNParam adds a query parameter to a DSN, with or without an existing
// query string.
func AppendDSNParam(dsn, param string) string {
	if strings.Contains(dsn, "?") {
		return dsn + "&" + param
	}
	return dsn + "?" + param
}
