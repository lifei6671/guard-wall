package store

import (
	"context"
	"database/sql"
	"fmt"

	"gorm.io/gorm"
)

// storeORMTransaction binds GORM to one Store-owned SQL transaction. The
// Store remains the only component that can finalize the transaction.
type storeORMTransaction struct {
	tx  *sql.Tx
	orm *gorm.DB
}

func (s *Store) beginStoreORMTransaction(
	ctx context.Context,
	options *sql.TxOptions,
) (*storeORMTransaction, error) {
	if s == nil || s.db == nil || s.orm == nil {
		return nil, fmt.Errorf("begin store ORM transaction: store is closed")
	}
	if ctx == nil {
		return nil, fmt.Errorf("begin store ORM transaction: context is required")
	}
	tx, err := s.db.BeginTx(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("begin store ORM transaction: %w", err)
	}
	return &storeORMTransaction{tx: tx, orm: newGORMTransactionSession(s.orm, tx)}, nil
}
