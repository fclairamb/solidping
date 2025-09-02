package dbctx_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/dbctx"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func TestDBContext(t *testing.T) {
	t.Parallel()

	// Create test database
	tempDir, err := os.MkdirTemp("", "dbctx-test-*")
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})

	svc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = svc.Close()
	})

	database := svc.DB()

	t.Run("GetDB without transaction", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		// Should return the main database connection
		idb := dbctx.GetDB(ctx, database)
		assert.Equal(t, database, idb)

		// TxFromContext should return false
		_, hasTx := dbctx.TxFromContext(ctx)
		assert.False(t, hasTx)
	})

	t.Run("GetDB with transaction", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		err := dbctx.RunInTxDefault(ctx, database, func(txCtx context.Context, transaction bun.Tx) error {
			// Should return the transaction
			idb := dbctx.GetDB(txCtx, database)
			assert.Equal(t, transaction, idb)

			// TxFromContext should return true and the transaction
			ctxTx, hasTx := dbctx.TxFromContext(txCtx)
			assert.True(t, hasTx)
			assert.Equal(t, transaction, ctxTx)

			return nil
		})

		require.NoError(t, err)
	})
}
