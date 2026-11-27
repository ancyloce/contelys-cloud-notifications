// Package postgres holds Notifications' stores over its PostgreSQL database (ADR 0036): the pool,
// the migrations that the binary embeds, and the queries that sqlc generates into gen/.
package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/ancyloce/contelys-cloud-notifications/internal/infrastructure/postgres/gen"
)

//go:embed migrations/*.sql
var migrations embed.FS

// poolSize is the most connections that one instance opens (ADR 0036).
const poolSize = 10

// Open returns a pool of the database at url, which must hold no password. Every new connection
// reads the password from passwordFile (ADR 0020), so the instance starts without its database or
// its password, and reports not ready until it can connect. The pool connects lazily.
func Open(ctx context.Context, url, passwordFile string) (*pgxpool.Pool, error) {
	if err := CheckURL(url); err != nil {
		return nil, err
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("postgres: database.url is not a PostgreSQL URL")
	}
	config.MaxConns = poolSize
	config.BeforeConnect = func(_ context.Context, conn *pgx.ConnConfig) error {
		password, err := os.ReadFile(passwordFile)
		if err != nil {
			return fmt.Errorf("postgres: the database password: %w", err)
		}
		conn.Password = strings.TrimSpace(string(password))
		return nil
	}
	return pgxpool.NewWithConfig(ctx, config)
}

// CheckURL refuses a database URL that is not one, or that holds a password, which reaches the
// instance as a file instead. Its error never repeats the URL.
func CheckURL(url string) error {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return errors.New("database.url is not a PostgreSQL URL")
	}
	if config.ConnConfig.Password != "" {
		return errors.New("database.url holds a password, which belongs in database.password_file")
	}
	return nil
}

// Ledger is Notifications' database: its migrations and its queries.
type Ledger struct {
	pool    *pgxpool.Pool
	queries *gen.Queries
	// versions answers the readiness check on the migrations.
	versions *goose.Provider
}

// NewLedger returns the ledger in pool. Its migrations run under goose's own version table and
// advisory lock, apart from the tables that the shared service library migrates.
func NewLedger(pool *pgxpool.Pool) (*Ledger, error) {
	l := &Ledger{pool: pool, queries: gen.New(pool)}
	var err error
	l.versions, err = l.provider()
	return l, err
}

// provider is a goose provider of the embedded migrations. A provider tries to create its version
// table only once, so each attempt to migrate needs a new one.
func (l *Ledger) provider() (*goose.Provider, error) {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, stdlib.OpenDBFromPool(l.pool), files, goose.WithSessionLocker(locker))
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	return provider, nil
}

// Migrate applies the migrations that the database lacks, and tries again after each wait until
// they apply or ctx ends. Instances that start together migrate one at a time, under goose's
// advisory lock. It hands each failure to report.
func (l *Ledger) Migrate(ctx context.Context, wait time.Duration, report func(error)) {
	for {
		provider, err := l.provider()
		if err == nil {
			_, err = provider.Up(ctx)
			_ = provider.Close()
		}
		if err == nil || ctx.Err() != nil {
			return
		}
		report(fmt.Errorf("postgres: migrations: %w", err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Readable reports whether the ledger can be read.
func (l *Ledger) Readable(ctx context.Context) error {
	return l.queries.ReadLedger(ctx)
}

// Migrated reports whether the database holds every migration that this binary embeds. A newer
// instance's migrations, which only expand the schema, leave an older instance ready.
func (l *Ledger) Migrated(ctx context.Context) error {
	current, target, err := l.versions.GetVersions(ctx)
	if err != nil {
		return fmt.Errorf("postgres: migrations: %w", err)
	}
	if current < target {
		return fmt.Errorf("postgres: the database is at migration %d of %d", current, target)
	}
	return nil
}
