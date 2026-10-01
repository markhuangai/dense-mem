//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"

	"gorm.io/gorm"
)

type communityOwnershipCounters struct {
	statements   atomic.Int64
	transactions atomic.Int64
	completions  atomic.Int64
}

func (c *communityOwnershipCounters) reset() {
	c.statements.Store(0)
	c.transactions.Store(0)
	c.completions.Store(0)
}

type communityOwnershipConnPool struct {
	gorm.ConnPool
	counters *communityOwnershipCounters
}

func (p *communityOwnershipConnPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return p.ConnPool.PrepareContext(ctx, query)
}

func (p *communityOwnershipConnPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	p.counters.statements.Add(1)
	return p.ConnPool.ExecContext(ctx, query, args...)
}

func (p *communityOwnershipConnPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	p.counters.statements.Add(1)
	return p.ConnPool.QueryContext(ctx, query, args...)
}

func (p *communityOwnershipConnPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	p.counters.statements.Add(1)
	return p.ConnPool.QueryRowContext(ctx, query, args...)
}

func (p *communityOwnershipConnPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	var tx gorm.ConnPool
	var err error
	if beginner, ok := p.ConnPool.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else if beginner, ok := p.ConnPool.(gorm.ConnPoolBeginner); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else {
		return nil, fmt.Errorf("community ownership benchmark: connection pool cannot begin a transaction")
	}
	if err != nil {
		return nil, err
	}
	p.counters.transactions.Add(1)
	return &communityOwnershipTx{ConnPool: tx, counters: p.counters}, nil
}

type communityOwnershipTx struct {
	gorm.ConnPool
	counters *communityOwnershipCounters
}

func (tx *communityOwnershipTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx.counters.statements.Add(1)
	return tx.ConnPool.ExecContext(ctx, query, args...)
}

func (tx *communityOwnershipTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	tx.counters.statements.Add(1)
	return tx.ConnPool.QueryContext(ctx, query, args...)
}

func (tx *communityOwnershipTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	tx.counters.statements.Add(1)
	return tx.ConnPool.QueryRowContext(ctx, query, args...)
}

func (tx *communityOwnershipTx) Commit() error {
	err := tx.ConnPool.(gorm.TxCommitter).Commit()
	if err == nil {
		tx.counters.completions.Add(1)
	}
	return err
}

func (tx *communityOwnershipTx) Rollback() error {
	err := tx.ConnPool.(gorm.TxCommitter).Rollback()
	if err == nil {
		tx.counters.completions.Add(1)
	}
	return err
}

func communityOwnershipCountedDB(db *gorm.DB, counters *communityOwnershipCounters) *gorm.DB {
	counted := db.Session(&gorm.Session{})
	pool := &communityOwnershipConnPool{ConnPool: db.ConnPool, counters: counters}
	counted.ConnPool = pool
	counted.Statement.ConnPool = pool
	return counted
}
