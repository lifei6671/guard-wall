package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

var errGORMMigrationDisabled = errors.New("gorm migration is disabled; use checksummed SQL migrations")

// nonClosingConnPool delegates database work without exposing Store's owned
// *sql.DB to GORM. In particular, it intentionally has no Close or GetDBConn
// method, so GORM cannot close or unwrap the pool.
type nonClosingConnPool struct {
	db *sql.DB
}

var (
	_ gorm.ConnPool   = (*nonClosingConnPool)(nil)
	_ gorm.TxBeginner = (*nonClosingConnPool)(nil)
)

func (p *nonClosingConnPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return p.db.PrepareContext(ctx, query)
}

func (p *nonClosingConnPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return p.db.ExecContext(ctx, query, args...)
}

func (p *nonClosingConnPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return p.db.QueryContext(ctx, query, args...)
}

func (p *nonClosingConnPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return p.db.QueryRowContext(ctx, query, args...)
}

func (p *nonClosingConnPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return p.db.BeginTx(ctx, opts)
}

// nonFinalizingTxConnPool lets GORM execute statements on an existing
// UnitOfWork transaction without exposing any transaction lifecycle methods.
// The UnitOfWork remains the only owner allowed to commit or roll back tx.
type nonFinalizingTxConnPool struct {
	tx *sql.Tx
}

var _ gorm.ConnPool = (*nonFinalizingTxConnPool)(nil)

func (p *nonFinalizingTxConnPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return p.tx.PrepareContext(ctx, query)
}

func (p *nonFinalizingTxConnPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return p.tx.ExecContext(ctx, query, args...)
}

func (p *nonFinalizingTxConnPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return p.tx.QueryContext(ctx, query, args...)
}

func (p *nonFinalizingTxConnPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return p.tx.QueryRowContext(ctx, query, args...)
}

func newGORMTransactionSession(root *gorm.DB, tx *sql.Tx) *gorm.DB {
	session := root.Session(&gorm.Session{
		NewDB:                    true,
		SkipDefaultTransaction:   true,
		SkipHooks:                true,
		DisableNestedTransaction: true,
	})
	pool := &nonFinalizingTxConnPool{tx: tx}
	session.Config.ConnPool = pool
	session.Statement.ConnPool = pool
	return session
}

// moderncDialector supplies the SQLite SQL grammar GORM needs while leaving
// driver registration, pool creation, migrations, and PRAGMAs to Store.
type moderncDialector struct{}

var _ gorm.Dialector = moderncDialector{}

func (moderncDialector) Name() string {
	return "sqlite"
}

func (moderncDialector) Initialize(db *gorm.DB) error {
	callbacks.RegisterDefaultCallbacks(db, &callbacks.Config{
		CreateClauses:        []string{"INSERT", "VALUES", "ON CONFLICT", "RETURNING"},
		UpdateClauses:        []string{"UPDATE", "SET", "FROM", "WHERE", "RETURNING"},
		DeleteClauses:        []string{"DELETE", "FROM", "WHERE", "RETURNING"},
		LastInsertIDReversed: true,
	})

	db.ClauseBuilders["LIMIT"] = buildSQLiteLimit
	db.ClauseBuilders["FOR"] = ignoreSQLiteLocking
	return nil
}

func (moderncDialector) Migrator(*gorm.DB) gorm.Migrator {
	return disabledMigrator{}
}

func (moderncDialector) DataTypeOf(field *schema.Field) string {
	switch field.DataType {
	case schema.Bool:
		return "numeric"
	case schema.Int, schema.Uint:
		if field.AutoIncrement {
			return "integer PRIMARY KEY AUTOINCREMENT"
		}
		return "integer"
	case schema.Float:
		return "real"
	case schema.String:
		return "text"
	case schema.Time:
		if dataType, ok := field.TagSettings["TYPE"]; ok {
			return dataType
		}
		return "datetime"
	case schema.Bytes:
		return "blob"
	default:
		return string(field.DataType)
	}
}

func (moderncDialector) DefaultValueOf(field *schema.Field) clause.Expression {
	if field.AutoIncrement {
		return clause.Expr{SQL: "NULL"}
	}
	return clause.Expr{SQL: "DEFAULT"}
}

func (moderncDialector) BindVarTo(writer clause.Writer, _ *gorm.Statement, _ any) {
	_ = writer.WriteByte('?')
}

func (moderncDialector) QuoteTo(writer clause.Writer, identifier string) {
	parts := splitSQLiteIdentifier(identifier)
	for index, part := range parts {
		if index > 0 {
			_ = writer.WriteByte('.')
		}
		if part == "*" {
			_, _ = writer.WriteString(part)
			continue
		}
		_, _ = writer.WriteString("`")
		_, _ = writer.WriteString(strings.ReplaceAll(part, "`", "``"))
		_, _ = writer.WriteString("`")
	}
}

func splitSQLiteIdentifier(identifier string) []string {
	parts := make([]string, 0, 2)
	var part strings.Builder
	quoted := false
	for index := 0; index < len(identifier); index++ {
		character := identifier[index]
		if quoted {
			if character == '`' {
				if index+1 < len(identifier) && identifier[index+1] == '`' {
					part.WriteByte('`')
					index++
					continue
				}
				quoted = false
				continue
			}
			part.WriteByte(character)
			continue
		}

		switch {
		case character == '`' && part.Len() == 0:
			quoted = true
		case character == '.':
			parts = append(parts, part.String())
			part.Reset()
		default:
			part.WriteByte(character)
		}
	}
	parts = append(parts, part.String())
	return parts
}

// Explain deliberately keeps placeholders intact so SQL rendering cannot
// interpolate credentials, paths, or business values into logs.
func (moderncDialector) Explain(query string, _ ...any) string {
	return query
}

func buildSQLiteLimit(c clause.Clause, builder clause.Builder) {
	limit, ok := c.Expression.(clause.Limit)
	if !ok {
		c.Build(builder)
		return
	}

	value := -1
	if limit.Limit != nil && *limit.Limit >= 0 {
		value = *limit.Limit
	}
	if value >= 0 || limit.Offset > 0 {
		builder.WriteString("LIMIT ")
		builder.WriteString(strconv.Itoa(value))
	}
	if limit.Offset > 0 {
		builder.WriteString(" OFFSET ")
		builder.WriteString(strconv.Itoa(limit.Offset))
	}
}

func ignoreSQLiteLocking(c clause.Clause, builder clause.Builder) {
	if _, ok := c.Expression.(clause.Locking); ok {
		return
	}
	c.Build(builder)
}

func newGORMAdapter(ctx context.Context, db *sql.DB) (*gorm.DB, error) {
	return newGORMAdapterWithDialector(ctx, db, moderncDialector{})
}

func newGORMAdapterWithDialector(ctx context.Context, db *sql.DB, dialector gorm.Dialector) (*gorm.DB, error) {
	if ctx == nil {
		return nil, fmt.Errorf("initialize gorm: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("initialize gorm: %w", err)
	}
	if db == nil {
		return nil, fmt.Errorf("initialize gorm: database is required")
	}
	if dialector == nil {
		return nil, fmt.Errorf("initialize gorm: dialector is required")
	}

	pool := &nonClosingConnPool{db: db}
	orm, err := gorm.Open(dialector, &gorm.Config{
		ConnPool:                                 pool,
		SkipDefaultTransaction:                   true,
		DisableAutomaticPing:                     true,
		Logger:                                   logger.Discard,
		PrepareStmt:                              false,
		DisableForeignKeyConstraintWhenMigrating: true,
		IgnoreRelationshipsWhenMigrating:         true,
		AllowGlobalUpdate:                        false,
		TranslateError:                           false,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize gorm: %w", err)
	}
	return orm, nil
}

// disabledMigrator makes checksummed SQL migrations the only schema owner.
// Methods without an error return panic because the GORM interface provides no
// other fail-fast signal; Store never exports the ORM handle to callers.
type disabledMigrator struct{}

var _ gorm.Migrator = disabledMigrator{}

func (disabledMigrator) AutoMigrate(...any) error { return errGORMMigrationDisabled }
func (disabledMigrator) CurrentDatabase() string  { panic(errGORMMigrationDisabled) }
func (disabledMigrator) FullDataTypeOf(*schema.Field) clause.Expr {
	panic(errGORMMigrationDisabled)
}
func (disabledMigrator) GetTypeAliases(string) []string { panic(errGORMMigrationDisabled) }
func (disabledMigrator) CreateTable(...any) error       { return errGORMMigrationDisabled }
func (disabledMigrator) DropTable(...any) error         { return errGORMMigrationDisabled }
func (disabledMigrator) HasTable(any) bool              { panic(errGORMMigrationDisabled) }
func (disabledMigrator) RenameTable(any, any) error     { return errGORMMigrationDisabled }
func (disabledMigrator) GetTables() ([]string, error)   { return nil, errGORMMigrationDisabled }
func (disabledMigrator) TableType(any) (gorm.TableType, error) {
	return nil, errGORMMigrationDisabled
}
func (disabledMigrator) AddColumn(any, string) error   { return errGORMMigrationDisabled }
func (disabledMigrator) DropColumn(any, string) error  { return errGORMMigrationDisabled }
func (disabledMigrator) AlterColumn(any, string) error { return errGORMMigrationDisabled }
func (disabledMigrator) MigrateColumn(any, *schema.Field, gorm.ColumnType) error {
	return errGORMMigrationDisabled
}
func (disabledMigrator) MigrateColumnUnique(any, *schema.Field, gorm.ColumnType) error {
	return errGORMMigrationDisabled
}
func (disabledMigrator) HasColumn(any, string) bool { panic(errGORMMigrationDisabled) }
func (disabledMigrator) RenameColumn(any, string, string) error {
	return errGORMMigrationDisabled
}
func (disabledMigrator) ColumnTypes(any) ([]gorm.ColumnType, error) {
	return nil, errGORMMigrationDisabled
}
func (disabledMigrator) CreateView(string, gorm.ViewOption) error {
	return errGORMMigrationDisabled
}
func (disabledMigrator) DropView(string) error                 { return errGORMMigrationDisabled }
func (disabledMigrator) CreateConstraint(any, string) error    { return errGORMMigrationDisabled }
func (disabledMigrator) DropConstraint(any, string) error      { return errGORMMigrationDisabled }
func (disabledMigrator) HasConstraint(any, string) bool        { panic(errGORMMigrationDisabled) }
func (disabledMigrator) CreateIndex(any, string) error         { return errGORMMigrationDisabled }
func (disabledMigrator) DropIndex(any, string) error           { return errGORMMigrationDisabled }
func (disabledMigrator) HasIndex(any, string) bool             { panic(errGORMMigrationDisabled) }
func (disabledMigrator) RenameIndex(any, string, string) error { return errGORMMigrationDisabled }
func (disabledMigrator) GetIndexes(any) ([]gorm.Index, error)  { return nil, errGORMMigrationDisabled }
