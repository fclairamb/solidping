// Package dbctx provides context-aware database transaction management for bun.DB.
//
// Usage examples:
//
//	// Simple query without transaction
//	func GetUser(ctx context.Context, db *bun.DB, id int) (*User, error) {
//		idb := dbctx.GetDB(ctx, db)  // Returns db if no transaction in context
//		return idb.NewSelect().Model(&User{}).Where("id = ?", id).Exec(ctx)
//	}
//
//	// Complex operation with transaction
//	func TransferFunds(ctx context.Context, db *bun.DB, from, to int, amount float64) error {
//		return dbctx.RunInTxDefault(ctx, db, func(txCtx context.Context, tx bun.Tx) error {
//			// All operations within this function will use the transaction
//			if err := updateBalance(txCtx, db, from, -amount); err != nil {
//				return err
//			}
//			return updateBalance(txCtx, db, to, amount)
//		})
//	}
//
//	func updateBalance(ctx context.Context, db *bun.DB, userID int, delta float64) error {
//		idb := dbctx.GetDB(ctx, db)  // Returns transaction if available in context
//		_, err := idb.NewUpdate().Model(&User{}).
//			Set("balance = balance + ?", delta).
//			Where("id = ?", userID).Exec(ctx)
//		return err
//	}
package dbctx

import (
	"context"
	"database/sql"

	"github.com/uptrace/bun"
)

type contextKey string

const (
	txKey contextKey = "db_tx"
)

// WithTx stores a transaction in the context.
func WithTx(ctx context.Context, tx bun.Tx) context.Context {
	return context.WithValue(ctx, txKey, tx)
}

// TxFromContext retrieves a transaction from the context.
func TxFromContext(ctx context.Context) (bun.Tx, bool) {
	tx, ok := ctx.Value(txKey).(bun.Tx)
	return tx, ok
}

// GetDB returns either the transaction from context or the main database connection.
func GetDB(ctx context.Context, db *bun.DB) bun.IDB {
	if tx, ok := TxFromContext(ctx); ok {
		return tx
	}

	return db
}

// RunInTx executes a function within a database transaction.
func RunInTx(
	ctx context.Context,
	db *bun.DB,
	opts *sql.TxOptions,
	fn func(ctx context.Context, tx bun.Tx) error,
) error {
	return db.RunInTx(ctx, opts, func(ctx context.Context, tx bun.Tx) error {
		// Add transaction to context for nested operations
		txCtx := WithTx(ctx, tx)
		return fn(txCtx, tx)
	})
}

// RunInTxDefault executes a function within a database transaction with default options.
func RunInTxDefault(ctx context.Context, db *bun.DB, fn func(ctx context.Context, tx bun.Tx) error) error {
	return RunInTx(ctx, db, nil, fn)
}
