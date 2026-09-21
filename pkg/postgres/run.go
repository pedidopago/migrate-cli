package postgres

import (
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/lib/pq"
	"github.com/pedidopago/migrate-cli/pkg/migrate"
)

// Run applies migrations to PostgreSQL. Flags, env vars and commands are the
// same as the MariaDB build -- see the README -- so the k8s manifests and the
// operator's muscle memory carry over unchanged.
func Run() {
	migrate.Run(migrate.Dialect{
		DriverName:  "postgres",
		MigrateName: "postgres",
		WithInstance: func(db *sql.DB) (database.Driver, error) {
			return postgres.WithInstance(db, &postgres.Config{})
		},
		PrepareDSN:       prepareDSN,
		IsUndefinedTable: isUndefinedTable,
		LogSessionInfo:   logSessionInfo,
	})
}

// prepareDSN carries lock_timeout on the DSN, which is Postgres's equivalent of
// MariaDB's lock_wait_timeout and matters for the same reason: an ALTER waiting
// on an ACCESS EXCLUSIVE lock does not wait quietly, it queues every other query
// on that table behind itself. Postgres defaults lock_timeout to 0 -- wait
// forever -- so this is the difference between a failed run and a stalled table.
//
// It goes on as a plain parameter, in milliseconds. lib/pq forwards any
// parameter it does not recognise to the server as a startup setting, which
// works in both DSN shapes (URL and keyword/value) and, unlike libpq's `options`
// field, does not have to be merged with whatever the operator already put
// there. A lock_timeout the operator set themselves wins.
func prepareDSN(dsn string, lockWaitTimeout int) string {
	t := lockWaitTimeout
	if t == -1 {
		t = migrate.DefaultLockWaitTimeout
	}
	if t <= 0 {
		return dsn
	}

	// ponytail: substring, not a parsed parameter -- same as the MariaDB side.
	// A DSN whose password happens to contain "lock_timeout" would suppress
	// this; the warning is here so that suppression is never silent, which is
	// the part that actually matters in an unattended init container.
	if strings.Contains(dsn, "lock_timeout") {
		slog.Warn("leaving lock_timeout as the DSN already sets it; this run is not protected by the default")
		return dsn
	}

	slog.Info("lock timeout set for this run", slog.Int("seconds", t))
	if isURL(dsn) {
		return migrate.AppendDSNParam(strings.TrimSpace(dsn), "lock_timeout="+strconv.Itoa(t*1000))
	}
	return strings.TrimSpace(dsn) + " lock_timeout=" + strconv.Itoa(t*1000)
}

// isURL trims first: a DSN read from a k8s secret routinely arrives with a
// trailing newline, and a URL misread as keyword/value gets a bare
// `lock_timeout=` field appended to it, which then fails to connect at all.
func isURL(dsn string) bool {
	l := strings.ToLower(strings.TrimSpace(dsn))
	return strings.HasPrefix(l, "postgres://") || strings.HasPrefix(l, "postgresql://")
}

// isUndefinedTable reports SQLSTATE 42P01, "relation does not exist".
func isUndefinedTable(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "42P01"
}

func logSessionInfo(db *sql.DB) {
	var lt string
	if err := db.QueryRow("SHOW lock_timeout").Scan(&lt); err == nil {
		slog.Info("lock_timeout in force for this session", slog.String("value", lt))
	}
}
