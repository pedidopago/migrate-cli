package mariadb

import (
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	sqldriver "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/pedidopago/migrate-cli/pkg/migrate"
)

// Run applies migrations to MariaDB/MySQL. Behaviour, flags and env vars are
// documented in the README and implemented in pkg/migrate.
func Run() {
	migrate.Run(migrate.Dialect{
		DriverName:  "mysql",
		MigrateName: "mysql",
		WithInstance: func(db *sql.DB) (database.Driver, error) {
			return mysql.WithInstance(db, &mysql.Config{})
		},
		PrepareDSN:       prepareDSN,
		IsUndefinedTable: isUndefinedTable,
		LogSessionInfo:   logSessionInfo,
	})
}

// prepareDSN carries lock_wait_timeout and multiStatements on the DSN. An
// explicit lock_wait_timeout already in the URL wins.
//
// Production servers routinely carry lock_wait_timeout=86400 -- a full day --
// and that turns one blocked ALTER into an outage: even an ALGORITHM=INSTANT
// change needs a brief exclusive metadata lock, MDL requests are FIFO, and
// every query on that table queues behind the one that is waiting.
func prepareDSN(dsn string, lockWaitTimeout int) string {
	if !strings.Contains(dsn, "lock_wait_timeout") {
		t := lockWaitTimeout
		if t == -1 {
			t = migrate.DefaultLockWaitTimeout
		}
		if t > 0 {
			dsn = migrate.AppendDSNParam(dsn, "lock_wait_timeout="+strconv.Itoa(t))
			slog.Info("lock wait timeout set for this run", slog.Int("seconds", t))
		}
	}
	if !strings.Contains(dsn, "multiStatements") {
		dsn = migrate.AppendDSNParam(dsn, "multiStatements=true")
	}
	return dsn
}

// isUndefinedTable reports MySQL error 1146, "table doesn't exist".
func isUndefinedTable(err error) bool {
	var myErr *sqldriver.MySQLError
	return errors.As(err, &myErr) && myErr.Number == 1146
}

func logSessionInfo(db *sql.DB) {
	var lwt uint64
	if err := db.QueryRow("SELECT @@session.lock_wait_timeout").Scan(&lwt); err == nil {
		slog.Info("lock_wait_timeout in force for this session", slog.Uint64("seconds", lwt))
	}
}
