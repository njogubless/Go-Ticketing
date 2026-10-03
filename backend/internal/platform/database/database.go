// Package database owns the connection pool, the transaction manager and the
// migration runner.
package database

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blessnduta/ticketing-system/migrations"
)

// Pool wraps pgxpool with the transaction-propagation logic the app layer's
// TxManager port expects.
type Pool struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// Querier is the subset of pgx both a pool and a transaction satisfy.
// Repositories accept this, so the same repository code runs inside or outside
// a transaction with no branching.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type Config struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
}

func Connect(ctx context.Context, cfg Config, logger *slog.Logger) (*Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	poolConfig.MaxConns = cfg.MaxConns
	poolConfig.MinConns = cfg.MinConns
	poolConfig.MaxConnLifetime = cfg.MaxConnLifetime
	// Recycling idle connections defends against a load balancer or firewall
	// silently dropping a long-idle TCP session, which otherwise surfaces as a
	// mysterious "unexpected EOF" on the first request after a quiet period.
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.HealthCheckPeriod = time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	logger.Info("database connected", slog.Int("max_conns", int(cfg.MaxConns)))
	return &Pool{pool: pool, logger: logger}, nil
}

func (p *Pool) Close() { p.pool.Close() }

func (p *Pool) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

// txKey carries an active transaction on the context.
type txKey struct{}

// Querier returns the transaction bound to this context, or the pool. This is
// the mechanism that lets a repository join an ambient transaction without its
// method signatures mentioning transactions at all.
func (p *Pool) Querier(ctx context.Context) Querier {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return p.pool
}

// WithinTx implements app.TxManager.
//
// Nested calls join the outer transaction rather than opening a second one:
// a service that calls another service must not commit half of the caller's
// work. Postgres savepoints would allow true nesting, but "join the outer"
// is the behaviour the use cases actually want, and it is simpler to reason
// about than partial rollback.
func (p *Pool) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, alreadyInTx := ctx.Value(txKey{}).(pgx.Tx); alreadyInTx {
		return fn(ctx)
	}

	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	// Rollback on panic as well as on error: a panic mid-transaction that left
	// the connection with an open transaction would poison it for every
	// subsequent user of that pooled connection.
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(recovered)
		}
	}()

	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		// Roll back with a context detached from the request's, so a client
		// disconnect cannot abort the rollback and leak the transaction.
		if rollbackErr := tx.Rollback(context.WithoutCancel(ctx)); rollbackErr != nil &&
			!strings.Contains(rollbackErr.Error(), "tx is closed") {
			p.logger.ErrorContext(ctx, "rollback failed", slog.Any("error", rollbackErr))
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Migrations
// ---------------------------------------------------------------------------

// Migrate applies pending migrations in filename order.
//
// Hand-rolled rather than pulled from a library, for three reasons: the
// migrations are embedded in the binary so a deployment cannot drift from its
// schema; each file runs inside a transaction so a failure leaves nothing
// half-applied; and an advisory lock means several instances starting at once
// cannot race, which is exactly what happens on a rolling deploy.
func (p *Pool) Migrate(ctx context.Context) error {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	const migrationLockID = 947_213_558
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockID); err != nil {
			p.logger.Error("failed to release migration lock", slog.Any("error", err))
		}
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := map[string]bool{}
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return err
		}
		applied[version] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	entries, err := migrations.FS.ReadDir(migrations.Dir)
	if err != nil {
		return fmt.Errorf("read migrations directory: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		version := strings.TrimSuffix(name, ".sql")
		if applied[version] {
			continue
		}

		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		p.logger.Info("applying migration", slog.String("version", version))

		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", version, err)
		}
	}

	p.logger.Info("migrations up to date", slog.Int("total", len(names)))
	return nil
}
