package database

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// txKey is the context key carrying the active *gorm.DB transaction handle.
type txKey struct{}

// ErrNoTx is returned by Manager.WithinTx when called outside a
// transaction-capable context.
var ErrNoTx = errors.New("database: no transaction in context")

// Manager runs units of work inside database transactions and resolves the
// active transaction for repositories, so nested service calls transparently
// join the outermost transaction.
type Manager struct {
	db *gorm.DB
}

// NewManager creates a transaction Manager bound to db.
func NewManager(db *gorm.DB) *Manager {
	return &Manager{db: db}
}

// DB returns the *gorm.DB to use for the given context: the active
// transaction when present, the raw connection otherwise.
func (m *Manager) DB(ctx context.Context) *gorm.DB {
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok && tx != nil {
		return tx
	}
	return m.db
}

// WithinTx executes fn inside a transaction. When the context already
// carries a transaction (a nested call), fn runs inside it without opening
// another one. The transaction commits when fn returns nil and rolls back
// on any error or panic.
func (m *Manager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(*gorm.DB); ok {
		// Already inside a transaction: join it.
		return fn(ctx)
	}

	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	})
}

// BaseRepository is a generic CRUD repository for model type T. All reads
// and writes flow through the transaction Manager so they automatically
// join an open transaction when present on the context.
type BaseRepository[T any] struct {
	tx *Manager
}

// NewBaseRepository constructs a BaseRepository bound to a transaction Manager.
func NewBaseRepository[T any](tx *Manager) *BaseRepository[T] {
	return &BaseRepository[T]{tx: tx}
}

// DB exposes the transaction-scoped *gorm.DB for custom queries in subtypes.
func (r *BaseRepository[T]) DB(ctx context.Context) *gorm.DB {
	return r.tx.DB(ctx)
}

// Create inserts a new record.
func (r *BaseRepository[T]) Create(ctx context.Context, entity *T) error {
	return r.tx.DB(ctx).Create(entity).Error
}

// CreateMany inserts a batch of records in a single statement.
func (r *BaseRepository[T]) CreateMany(ctx context.Context, entities []*T) error {
	if len(entities) == 0 {
		return nil
	}
	return r.tx.DB(ctx).Create(entities).Error
}

// GetByID fetches a record by primary key. Returns gorm.ErrRecordNotFound
// when absent.
func (r *BaseRepository[T]) GetByID(ctx context.Context, id any) (*T, error) {
	var entity T
	if err := r.tx.DB(ctx).First(&entity, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &entity, nil
}

// List returns all records matching the conditions, ordered by the given
// clauses.
func (r *BaseRepository[T]) List(ctx context.Context, conds []any, orderBy ...string) ([]*T, error) {
	query := r.tx.DB(ctx)
	if len(conds) > 0 {
		query = query.Where(conds[0], conds[1:]...)
	}
	for _, o := range orderBy {
		query = query.Order(o)
	}
	var entities []*T
	if err := query.Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// Count returns the number of records matching the conditions.
func (r *BaseRepository[T]) Count(ctx context.Context, conds ...any) (int64, error) {
	var count int64
	query := r.tx.DB(ctx).Model(new(T))
	if len(conds) > 0 {
		query = query.Where(conds[0], conds[1:]...)
	}
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// Update persists all fields of entity (full save).
func (r *BaseRepository[T]) Update(ctx context.Context, entity *T) error {
	return r.tx.DB(ctx).Save(entity).Error
}

// UpdateFields applies a partial update by column name.
func (r *BaseRepository[T]) UpdateFields(ctx context.Context, id any, fields map[string]any) error {
	return r.tx.DB(ctx).Model(new(T)).Where("id = ?", id).Updates(fields).Error
}

// Delete removes the record with the given primary key.
func (r *BaseRepository[T]) Delete(ctx context.Context, id any) error {
	return r.tx.DB(ctx).Delete(new(T), "id = ?", id).Error
}

// Upsert inserts the record or, on conflict with the primary key, updates
// the given columns.
func (r *BaseRepository[T]) Upsert(ctx context.Context, entity *T, conflictColumns []string, updateColumns []string) error {
	return r.tx.DB(ctx).Clauses(clause.OnConflict{
		Columns:   onConflictColumns(conflictColumns),
		DoUpdates: clause.AssignmentColumns(updateColumns),
	}).Create(entity).Error
}

func onConflictColumns(names []string) []clause.Column {
	cols := make([]clause.Column, 0, len(names))
	for _, n := range names {
		cols = append(cols, clause.Column{Name: n})
	}
	return cols
}

// Paginate is a helper for list endpoints: it returns one page of records
// and the total count matching the conditions.
func (r *BaseRepository[T]) Paginate(ctx context.Context, offset, limit int, conds []any, orderBy ...string) ([]*T, int64, error) {
	if limit <= 0 {
		return nil, 0, fmt.Errorf("database: invalid page limit %d", limit)
	}
	if offset < 0 {
		return nil, 0, fmt.Errorf("database: negative page offset %d", offset)
	}
	total, err := r.Count(ctx, conds...)
	if err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, nil
	}

	query := r.tx.DB(ctx).Offset(offset).Limit(limit)
	if len(conds) > 0 {
		query = query.Where(conds[0], conds[1:]...)
	}
	for _, o := range orderBy {
		query = query.Order(o)
	}
	var entities []*T
	if err := query.Find(&entities).Error; err != nil {
		return nil, 0, err
	}
	return entities, total, nil
}
