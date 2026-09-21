package postgres

import (
	"errors"
	"testing"

	"github.com/lib/pq"
)

func TestPrepareDSN(t *testing.T) {
	cases := []struct {
		name    string
		dsn     string
		timeout int
		want    string
	}{{
		name:    "url gets a query parameter",
		dsn:     "postgres://u:p@host:5432/db?sslmode=disable",
		timeout: 10,
		want:    "postgres://u:p@host:5432/db?sslmode=disable&lock_timeout=10000",
	}, {
		name:    "url without a query string still works",
		dsn:     "postgres://u:p@host:5432/db",
		timeout: 5,
		want:    "postgres://u:p@host:5432/db?lock_timeout=5000",
	}, {
		// The whole reason this is a plain parameter and not an `options` entry:
		// libpq allows one options field, so merging into it meant reproducing
		// libpq's own first-wins/last-wins rules. A parameter just coexists.
		name:    "url leaves an unrelated options value alone",
		dsn:     "postgres://host/db?options=-c+search_path%3Dtenant_a",
		timeout: 10,
		want:    "postgres://host/db?options=-c+search_path%3Dtenant_a&lock_timeout=10000",
	}, {
		name:    "keyword dsn gets a field, not a query parameter",
		dsn:     "host=db user=erp dbname=erp",
		timeout: 10,
		want:    "host=db user=erp dbname=erp lock_timeout=10000",
	}, {
		name:    "keyword dsn leaves an unrelated options value alone",
		dsn:     "host=db options='-c search_path=tenant_a' user=erp",
		timeout: 10,
		want:    "host=db options='-c search_path=tenant_a' user=erp lock_timeout=10000",
	}, {
		name:    "keyword dsn separated by tabs needs no parsing at all",
		dsn:     "host=db\toptions='-c search_path=a'",
		timeout: 10,
		want:    "host=db\toptions='-c search_path=a' lock_timeout=10000",
	}, {
		name:    "-1 means the operator said nothing, so the default applies",
		dsn:     "postgres://host/db",
		timeout: -1,
		want:    "postgres://host/db?lock_timeout=10000",
	}, {
		name:    "0 leaves the server default alone",
		dsn:     "postgres://host/db",
		timeout: 0,
		want:    "postgres://host/db",
	}, {
		name:    "an explicit lock_timeout parameter wins",
		dsn:     "postgres://host/db?lock_timeout=1000",
		timeout: 10,
		want:    "postgres://host/db?lock_timeout=1000",
	}, {
		name:    "an explicit lock_timeout inside options wins",
		dsn:     "host=db options='-c lock_timeout=1000'",
		timeout: 10,
		want:    "host=db options='-c lock_timeout=1000'",
	}, {
		// A DSN from a k8s secret often carries a trailing newline; treating it
		// as keyword/value would append a field to a URL and break the connect.
		name:    "a url with surrounding whitespace is still a url",
		dsn:     "  postgres://host/db\n",
		timeout: 10,
		want:    "postgres://host/db?lock_timeout=10000",
	}, {
		name:    "an uppercase url scheme is still a url",
		dsn:     "Postgres://host/db",
		timeout: 10,
		want:    "Postgres://host/db?lock_timeout=10000",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := prepareDSN(tc.dsn, tc.timeout); got != tc.want {
				t.Errorf("prepareDSN(%q, %d)\n got %q\nwant %q", tc.dsn, tc.timeout, got, tc.want)
			}
		})
	}
}

func TestIsUndefinedTable(t *testing.T) {
	// A fresh database must read as "no version recorded", not as a warning.
	if !isUndefinedTable(&pq.Error{Code: "42P01"}) {
		t.Error("42P01 should report as an undefined table")
	}
	if isUndefinedTable(&pq.Error{Code: "42703"}) {
		t.Error("42703 (undefined column) should not")
	}
	if isUndefinedTable(errors.New("connection refused")) {
		t.Error("a non-pq error should not")
	}
	if isUndefinedTable(nil) {
		t.Error("nil should not")
	}
}
