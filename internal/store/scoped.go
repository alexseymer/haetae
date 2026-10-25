package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// dbPool is the part of *pgxpool.Pool the store uses.
type dbPool interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
	Begin(ctx context.Context) (pgx.Tx, error)
}

// AppRole is the PostgreSQL role user-facing queries run as. It owns nothing
// and is subject to Row-Level Security, unlike the connection's own role
// (a superuser or table owner bypasses RLS).
const AppRole = "haetae_app"

// ForOrg returns a view of the store for one organisation. Every statement
// runs in a transaction that first switches to AppRole and sets the current
// organisation, so the database itself refuses to show or change another
// organisation's rows, even for a query that forgets its WHERE clause.
//
// Use it for everything done on behalf of a signed-in user. The agent gateway
// and background jobs use the unscoped store and pass org ids explicitly.
func (s *Store) ForOrg(orgID uuid.UUID) *Store {
	return &Store{pool: &scopedPool{raw: s.raw, org: orgID}, raw: s.raw, scoped: true}
}

type scopedPool struct {
	raw *pgxpool.Pool
	org uuid.UUID
}

// begin starts a transaction restricted to the organisation.
func (p *scopedPool) begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.raw.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+AppRole); err != nil {
		_ = tx.Rollback(ctx)
		return nil, fmt.Errorf("store: switch to %s: %w", AppRole, err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", p.org.String()); err != nil {
		_ = tx.Rollback(ctx)
		return nil, fmt.Errorf("store: set organisation: %w", err)
	}
	return tx, nil
}

func (p *scopedPool) Begin(ctx context.Context) (pgx.Tx, error) { return p.begin(ctx) }

func (p *scopedPool) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx, err := p.begin(ctx)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	tag, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		_ = tx.Rollback(ctx)
		return tag, err
	}
	return tag, tx.Commit(ctx)
}

func (p *scopedPool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return &txRows{Rows: rows, tx: tx, ctx: ctx}, nil
}

func (p *scopedPool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	rows, err := p.Query(ctx, sql, args...)
	if err != nil {
		return errRow{err}
	}
	return &txRow{rows: rows}
}

func (p *scopedPool) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	tx, err := p.begin(ctx)
	if err != nil {
		return errBatch{err}
	}
	return &txBatch{BatchResults: tx.SendBatch(ctx, b), tx: tx, ctx: ctx}
}

// txRows commits the transaction when the rows are closed.
type txRows struct {
	pgx.Rows
	tx   pgx.Tx
	ctx  context.Context
	done bool
}

func (r *txRows) Close() {
	if r.done {
		return
	}
	r.done = true
	r.Rows.Close()
	if r.Err() != nil {
		rollback(r.tx)
		return
	}
	_ = r.tx.Commit(r.ctx)
}

// rollback ends a failed transaction with its own short deadline. The
// request's context may be cancelled already (client gone, timeout), and a
// rollback sent with a cancelled context would be skipped.
func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

// txRow adapts rows to pgx.Row.
type txRow struct{ rows pgx.Rows }

func (r *txRow) Scan(dest ...any) error {
	defer r.rows.Close()
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return err
		}
		return pgx.ErrNoRows
	}
	if err := r.rows.Scan(dest...); err != nil {
		return err
	}
	return r.rows.Err()
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// txBatch commits the transaction when the batch results are closed.
type txBatch struct {
	pgx.BatchResults
	tx   pgx.Tx
	ctx  context.Context
	done bool
}

func (b *txBatch) Close() error {
	if b.done {
		return nil
	}
	b.done = true
	if err := b.BatchResults.Close(); err != nil {
		rollback(b.tx)
		return err
	}
	return b.tx.Commit(b.ctx)
}

type errBatch struct{ err error }

func (b errBatch) Exec() (pgconn.CommandTag, error) { return pgconn.CommandTag{}, b.err }
func (b errBatch) Query() (pgx.Rows, error)         { return nil, b.err }
func (b errBatch) QueryRow() pgx.Row                { return errRow(b) }
func (b errBatch) Close() error                     { return b.err }

// QueryRowForTest and ExecForTest run raw SQL through the store's pool (so, for
// a ForOrg view, as the restricted role). They exist for tests that prove the
// database enforces isolation; production code uses the typed methods.
func (s *Store) QueryRowForTest(ctx context.Context, sql string, args ...any) pgx.Row {
	return s.pool.QueryRow(ctx, sql, args...)
}

// ExecForTest is the Exec counterpart of QueryRowForTest.
func (s *Store) ExecForTest(ctx context.Context, sql string, args ...any) error {
	_, err := s.pool.Exec(ctx, sql, args...)
	return err
}
