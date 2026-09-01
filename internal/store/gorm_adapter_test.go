package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func TestGORMCoreDependencyBoundary(t *testing.T) {
	drivers := sql.Drivers()
	if !containsString(drivers, "sqlite") {
		t.Fatalf("sql.Drivers() = %v, want modernc sqlite", drivers)
	}
	if containsString(drivers, "sqlite3") {
		t.Fatalf("sql.Drivers() = %v, must not register mattn sqlite3", drivers)
	}

	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("debug.ReadBuildInfo() unavailable")
	}
	modules := make(map[string]bool, len(buildInfo.Deps))
	for _, dependency := range buildInfo.Deps {
		modules[dependency.Path] = true
	}
	if !modules["gorm.io/gorm"] {
		t.Fatal("compiled test binary does not contain approved gorm core")
	}
	for _, forbidden := range []string{"gorm.io/driver/sqlite", "github.com/mattn/go-sqlite3"} {
		if modules[forbidden] {
			t.Fatalf("compiled test binary contains forbidden module %q", forbidden)
		}
	}

	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	err = filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse imports in %s: %w", path, err)
		}
		for _, imported := range file.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return fmt.Errorf("decode import in %s: %w", path, err)
			}
			if strings.HasPrefix(importPath, "gorm.io/driver/") || importPath == "github.com/mattn/go-sqlite3" {
				return fmt.Errorf("forbidden SQLite driver import %q in %s", importPath, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNonClosingConnPoolDoesNotExposeOwnedDB(t *testing.T) {
	store := openTestStore(t)
	pool, ok := store.orm.Config.ConnPool.(*nonClosingConnPool)
	if !ok {
		t.Fatalf("ConnPool = %T, want *nonClosingConnPool", store.orm.Config.ConnPool)
	}
	if pool.db != store.db {
		t.Fatal("GORM adapter does not delegate to Store-owned pool")
	}
	if store.orm.Statement.ConnPool != store.orm.Config.ConnPool {
		t.Fatal("GORM statement and config use different pools")
	}
	if _, ok := any(pool).(gorm.GetDBConnector); ok {
		t.Fatal("nonClosingConnPool must not expose GetDBConn")
	}
	if _, ok := any(pool).(interface{ Close() error }); ok {
		t.Fatal("nonClosingConnPool must not expose Close")
	}
	if _, err := store.orm.DB(); !errors.Is(err, gorm.ErrInvalidDB) {
		t.Fatalf("GORM DB() error = %v, want ErrInvalidDB", err)
	}
}

func TestGORMAdapterInitializationPerformsNoIO(t *testing.T) {
	connector := &rejectingConnector{err: errors.New("unexpected connect")}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })

	orm, err := newGORMAdapter(context.Background(), db)
	if err != nil {
		t.Fatalf("newGORMAdapter(): %v", err)
	}
	if orm == nil {
		t.Fatal("newGORMAdapter() returned nil adapter")
	}
	if calls := connector.connectCalls.Load(); calls != 0 {
		t.Fatalf("adapter initialization made %d database connections, want 0", calls)
	}
}

func TestGORMAdapterInitializationHonorsContext(t *testing.T) {
	connector := &rejectingConnector{err: errors.New("unexpected connect")}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newGORMAdapter(ctx, db); !errors.Is(err, context.Canceled) {
		t.Fatalf("newGORMAdapter() error = %v, want context.Canceled", err)
	}
	if calls := connector.connectCalls.Load(); calls != 0 {
		t.Fatalf("canceled initialization made %d database connections, want 0", calls)
	}
}

func TestGORMInitializationFailureDoesNotCloseRawPool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := openDatabase(ctx, filepath.Join(t.TempDir(), "guard.db"))
	if err != nil {
		t.Fatalf("openDatabase(): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	wantErr := errors.New("dialector initialization failed")
	if _, err := newGORMAdapterWithDialector(ctx, db, failingDialector{err: wantErr}); !errors.Is(err, wantErr) {
		t.Fatalf("newGORMAdapterWithDialector() error = %v, want %v", err, wantErr)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("GORM closed Store-owned pool after initialization failure: %v", err)
	}
	var value int
	if err := db.QueryRowContext(ctx, "SELECT ?", 7).Scan(&value); err != nil || value != 7 {
		t.Fatalf("raw pool after GORM failure = value %d error %v", value, err)
	}
}

func TestGORMAdapterConfiguration(t *testing.T) {
	store := openTestStore(t)
	config := store.orm.Config
	if !config.SkipDefaultTransaction || !config.DisableAutomaticPing || config.PrepareStmt ||
		config.FullSaveAssociations || config.DisableNestedTransaction ||
		!config.DisableForeignKeyConstraintWhenMigrating || !config.IgnoreRelationshipsWhenMigrating ||
		config.AllowGlobalUpdate || config.DryRun || config.QueryFields || config.TranslateError {
		t.Fatalf("unexpected GORM config: %+v", config)
	}
	if config.Logger != logger.Discard {
		t.Fatalf("Logger = %T, want logger.Discard", config.Logger)
	}
	if config.Dialector.Name() != "sqlite" {
		t.Fatalf("Dialector.Name() = %q, want sqlite", config.Dialector.Name())
	}
}

func TestGORMAdapterHasNoSchemaOrMigrationSideEffects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := openDatabase(ctx, filepath.Join(t.TempDir(), "guard.db"))
	if err != nil {
		t.Fatalf("openDatabase(): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrations, err := loadMigrations(migrationFileSystem())
	if err != nil {
		t.Fatalf("loadMigrations(): %v", err)
	}
	if err := applyMigrations(ctx, db, migrations); err != nil {
		t.Fatalf("applyMigrations(): %v", err)
	}

	before := gormSchemaFingerprint(t, ctx, db)
	orm, err := newGORMAdapter(ctx, db)
	if err != nil {
		t.Fatalf("newGORMAdapter(): %v", err)
	}
	after := gormSchemaFingerprint(t, ctx, db)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("schema changed during GORM initialization\nbefore: %v\nafter:  %v", before, after)
	}

	if err := orm.AutoMigrate(&gormAdapterProbe{}); !errors.Is(err, errGORMMigrationDisabled) {
		t.Fatalf("AutoMigrate() error = %v, want errGORMMigrationDisabled", err)
	}
	if err := orm.Migrator().CreateTable(&gormAdapterProbe{}); !errors.Is(err, errGORMMigrationDisabled) {
		t.Fatalf("CreateTable() error = %v, want errGORMMigrationDisabled", err)
	}
	if got := gormSchemaFingerprint(t, ctx, db); !reflect.DeepEqual(got, before) {
		t.Fatalf("schema changed after rejected AutoMigrate\nbefore: %v\nafter:  %v", before, got)
	}
}

func TestModerncDialectorCRUDAndBoundParameters(t *testing.T) {
	store := openTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	createGORMProbeTable(t, ctx, store.db)

	payload := "alpha' OR 1=1 --"
	row := gormAdapterProbe{ID: 1, Tenant: "tenant-a", Value: payload, Version: 1, UpdatedAtUS: 100}
	result := store.orm.WithContext(ctx).Create(&row)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("Create() = rows %d error %v", result.RowsAffected, result.Error)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO gorm_adapter_probe(id, tenant, value, version, updated_at_us)
		VALUES (2, 'tenant-b', 'control', 1, 100)`); err != nil {
		t.Fatalf("insert control row: %v", err)
	}
	var rawValue, rawState string
	var rawVersion int64
	if err := store.db.QueryRowContext(ctx, `
		SELECT value, version, state FROM gorm_adapter_probe WHERE id = 1`).
		Scan(&rawValue, &rawVersion, &rawState); err != nil {
		t.Fatalf("raw read after Create(): %v", err)
	}
	if rawValue != payload || rawVersion != 1 || rawState != "active" {
		t.Fatalf("raw row after Create() = value %q version %d state %q", rawValue, rawVersion, rawState)
	}

	var got gormAdapterProbe
	result = store.orm.WithContext(ctx).
		Where("tenant = ? AND value = ?", row.Tenant, payload).
		First(&got)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("First() = rows %d error %v", result.RowsAffected, result.Error)
	}
	if got.ID != row.ID || got.Value != payload || got.State != "active" {
		t.Fatalf("First() = %+v", got)
	}

	result = store.orm.WithContext(ctx).
		Model(&gormAdapterProbe{}).
		Where("id = ? AND version = ?", row.ID, 1).
		Updates(map[string]any{
			"value":         "updated",
			"version":       gorm.Expr("version + ?", 1),
			"updated_at_us": 200,
		})
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("Updates() = rows %d error %v", result.RowsAffected, result.Error)
	}
	result = store.orm.WithContext(ctx).
		Model(&gormAdapterProbe{}).
		Where("id = ? AND version = ?", row.ID, 1).
		Update("value", "stale")
	if result.Error != nil || result.RowsAffected != 0 {
		t.Fatalf("stale Update() = rows %d error %v", result.RowsAffected, result.Error)
	}
	var controlValue string
	if err := store.db.QueryRowContext(ctx, `
		SELECT value, version FROM gorm_adapter_probe WHERE id = 1`).
		Scan(&rawValue, &rawVersion); err != nil {
		t.Fatalf("raw read after Updates(): %v", err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT value FROM gorm_adapter_probe WHERE id = 2").Scan(&controlValue); err != nil {
		t.Fatalf("raw read control row after Updates(): %v", err)
	}
	if rawValue != "updated" || rawVersion != 2 || controlValue != "control" {
		t.Fatalf("raw rows after Updates() = target %q/v%d control %q", rawValue, rawVersion, controlValue)
	}

	dryRun := store.orm.WithContext(ctx).Session(&gorm.Session{DryRun: true}).
		Where("tenant = ? AND value = ?", row.Tenant, payload).
		First(&gormAdapterProbe{})
	if strings.Contains(dryRun.Statement.SQL.String(), payload) {
		t.Fatalf("DryRun SQL interpolated payload: %s", dryRun.Statement.SQL.String())
	}
	if !containsAny(dryRun.Statement.Vars, payload) {
		t.Fatalf("DryRun vars = %v, want payload", dryRun.Statement.Vars)
	}

	result = store.orm.WithContext(ctx).Delete(&gormAdapterProbe{})
	if !errors.Is(result.Error, gorm.ErrMissingWhereClause) {
		t.Fatalf("unscoped Delete() error = %v, want ErrMissingWhereClause", result.Error)
	}
	var rowCount int
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM gorm_adapter_probe").Scan(&rowCount); err != nil {
		t.Fatalf("raw count after rejected Delete(): %v", err)
	}
	if rowCount != 2 {
		t.Fatalf("row count after rejected Delete() = %d, want 2", rowCount)
	}
	result = store.orm.WithContext(ctx).Where("id = ?", row.ID).Delete(&gormAdapterProbe{})
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("scoped Delete() = rows %d error %v", result.RowsAffected, result.Error)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT count(*) FROM gorm_adapter_probe WHERE id = 1").Scan(&rowCount); err != nil {
		t.Fatalf("raw target count after scoped Delete(): %v", err)
	}
	if rowCount != 0 {
		t.Fatalf("target row count after scoped Delete() = %d, want 0", rowCount)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT value FROM gorm_adapter_probe WHERE id = 2").Scan(&controlValue); err != nil {
		t.Fatalf("raw control row after scoped Delete(): %v", err)
	}
	if controlValue != "control" {
		t.Fatalf("control row after scoped Delete() = %q, want control", controlValue)
	}
}

func TestGORMExplicitTransactionUsesOwnedPool(t *testing.T) {
	store := openTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	createGORMProbeTable(t, ctx, store.db)

	insert := func(id int64) error {
		return store.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return tx.Create(&gormAdapterProbe{
				ID: id, Tenant: "tenant", Value: fmt.Sprintf("value-%d", id), Version: 1, UpdatedAtUS: id,
			}).Error
		})
	}
	if err := insert(90); err != nil {
		t.Fatalf("committed Transaction(): %v", err)
	}

	rollbackErr := errors.New("rollback requested")
	err := store.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&gormAdapterProbe{
			ID: 91, Tenant: "tenant", Value: "rollback", Version: 1, UpdatedAtUS: 91,
		}).Error; err != nil {
			return err
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("rolled-back Transaction() error = %v, want %v", err, rollbackErr)
	}

	var committed, rolledBack int
	if err := store.db.QueryRowContext(ctx,
		"SELECT count(*) FROM gorm_adapter_probe WHERE id = 90").Scan(&committed); err != nil {
		t.Fatalf("read committed row: %v", err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT count(*) FROM gorm_adapter_probe WHERE id = 91").Scan(&rolledBack); err != nil {
		t.Fatalf("read rolled-back row: %v", err)
	}
	if committed != 1 || rolledBack != 0 {
		t.Fatalf("transaction rows = committed %d rolled back %d", committed, rolledBack)
	}

	err = store.orm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&gormAdapterProbe{
			ID: 92, Tenant: "tenant", Value: "outer", Version: 1, UpdatedAtUS: 92,
		}).Error; err != nil {
			return err
		}
		return tx.Transaction(func(*gorm.DB) error { return nil })
	})
	if !errors.Is(err, gorm.ErrUnsupportedDriver) {
		t.Fatalf("nested Transaction() error = %v, want ErrUnsupportedDriver", err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT count(*) FROM gorm_adapter_probe WHERE id = 92").Scan(&rolledBack); err != nil {
		t.Fatalf("read outer row after rejected nested transaction: %v", err)
	}
	if rolledBack != 0 {
		t.Fatalf("outer transaction partial row count = %d, want rollback", rolledBack)
	}
	if _, err := store.Pragmas(ctx); err != nil {
		t.Fatalf("Pragmas() after GORM transaction: %v", err)
	}
}

func TestModerncDialectorOnConflictAndReturning(t *testing.T) {
	store := openTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	createGORMProbeTable(t, ctx, store.db)

	row := gormAdapterProbe{
		ID: 1, Tenant: "tenant", Value: "before", Version: 1, UpdatedAtUS: 100,
	}
	result := store.orm.WithContext(ctx).
		Clauses(clause.Returning{}).
		Create(&row)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("Create(Returning) = rows %d error %v", result.RowsAffected, result.Error)
	}
	if row.State != "active" {
		t.Fatalf("Create(Returning) state = %q, want active", row.State)
	}

	replacement := gormAdapterProbe{
		ID: 1, Tenant: "tenant", Value: "after", Version: 2, UpdatedAtUS: 200,
	}
	result = store.orm.WithContext(ctx).
		Clauses(
			clause.OnConflict{
				Columns: []clause.Column{{Name: "id"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"tenant", "value", "version", "updated_at_us",
				}),
			},
			clause.Returning{},
		).
		Create(&replacement)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("Create(OnConflict, Returning) = rows %d error %v", result.RowsAffected, result.Error)
	}
	if replacement.Value != "after" || replacement.Version != 2 || replacement.State != "active" {
		t.Fatalf("Create(OnConflict, Returning) = %+v", replacement)
	}

	var count int
	if err := store.db.QueryRowContext(ctx,
		"SELECT count(*) FROM gorm_adapter_probe WHERE id = 1 AND value = 'after' AND version = 2").
		Scan(&count); err != nil {
		t.Fatalf("read back upsert row: %v", err)
	}
	if count != 1 {
		t.Fatalf("upsert row count = %d, want 1", count)
	}

	updated := replacement
	result = store.orm.WithContext(ctx).
		Model(&updated).
		Clauses(clause.Returning{}).
		Where("id = ?", replacement.ID).
		Updates(map[string]any{
			"value":         "returned-update",
			"version":       3,
			"updated_at_us": 300,
		})
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("Updates(Returning) = rows %d error %v", result.RowsAffected, result.Error)
	}
	if updated.Value != "returned-update" || updated.Version != 3 || updated.State != "active" {
		t.Fatalf("Updates(Returning) = %+v", updated)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT count(*) FROM gorm_adapter_probe WHERE id = 1 AND value = 'returned-update' AND version = 3").
		Scan(&count); err != nil {
		t.Fatalf("raw read after Updates(Returning): %v", err)
	}
	if count != 1 {
		t.Fatalf("updated returning row count = %d, want 1", count)
	}

	deleted := gormAdapterProbe{ID: updated.ID}
	result = store.orm.WithContext(ctx).
		Clauses(clause.Returning{}).
		Where("id = ?", updated.ID).
		Delete(&deleted)
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("Delete(Returning) = rows %d error %v", result.RowsAffected, result.Error)
	}
	if deleted.Value != "returned-update" || deleted.Version != 3 || deleted.State != "active" {
		t.Fatalf("Delete(Returning) = %+v", deleted)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT count(*) FROM gorm_adapter_probe WHERE id = 1").Scan(&count); err != nil {
		t.Fatalf("raw read after Delete(Returning): %v", err)
	}
	if count != 0 {
		t.Fatalf("deleted returning row count = %d, want 0", count)
	}

	dryRun := store.orm.Session(&gorm.Session{DryRun: true}).
		Clauses(clause.Insert{Modifier: "OR IGNORE"}).
		Create(&gormAdapterProbe{ID: 2, Tenant: "tenant", Value: "probe", Version: 1, UpdatedAtUS: 300})
	if sqlText := dryRun.Statement.SQL.String(); !strings.HasPrefix(sqlText, "INSERT OR IGNORE INTO ") {
		t.Fatalf("INSERT modifier SQL = %q", sqlText)
	}
}

func TestStoreCloseOwnsGORMPoolLifetime(t *testing.T) {
	store := openTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	orm := store.orm
	db := store.db

	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if err := db.PingContext(ctx); err == nil {
		t.Fatal("raw pool remained usable after Store.Close()")
	}
	var value int
	if err := orm.WithContext(ctx).Raw("SELECT 1").Scan(&value).Error; err == nil {
		t.Fatal("GORM remained usable after Store.Close()")
	}
}

func TestModerncDialectorQuoteBindAndClauses(t *testing.T) {
	dialector := moderncDialector{}
	for input, want := range map[string]string{
		"probe":        "`probe`",
		"probe.value":  "`probe`.`value`",
		"select":       "`select`",
		"bad`name":     "`bad``name`",
		"`weird.name`": "`weird.name`",
		"*":            "*",
	} {
		var output strings.Builder
		dialector.QuoteTo(&output, input)
		if got := output.String(); got != want {
			t.Errorf("QuoteTo(%q) = %q, want %q", input, got, want)
		}
	}

	var bind strings.Builder
	dialector.BindVarTo(&bind, nil, "secret")
	if bind.String() != "?" {
		t.Fatalf("BindVarTo() = %q, want ?", bind.String())
	}
	if got := dialector.Explain("SELECT ?", "secret"); got != "SELECT ?" {
		t.Fatalf("Explain() = %q, want placeholders intact", got)
	}

	store := openTestStore(t)
	query := store.orm.Session(&gorm.Session{DryRun: true}).
		Offset(5).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Find(&[]gormAdapterProbe{})
	sqlText := query.Statement.SQL.String()
	if !strings.Contains(sqlText, "LIMIT -1 OFFSET 5") || strings.Contains(sqlText, "FOR UPDATE") {
		t.Fatalf("SQLite clauses SQL = %q", sqlText)
	}
}

type gormAdapterProbe struct {
	ID          int64  `gorm:"column:id;primaryKey;autoIncrement:false"`
	Tenant      string `gorm:"column:tenant"`
	Value       string `gorm:"column:value"`
	Version     int64  `gorm:"column:version"`
	State       string `gorm:"column:state;->"`
	UpdatedAtUS int64  `gorm:"column:updated_at_us"`
}

func (gormAdapterProbe) TableName() string { return "gorm_adapter_probe" }

type failingDialector struct {
	moderncDialector
	err error
}

func (d failingDialector) Initialize(*gorm.DB) error { return d.err }

type rejectingConnector struct {
	err          error
	connectCalls atomic.Int64
}

func (c *rejectingConnector) Connect(context.Context) (driver.Conn, error) {
	c.connectCalls.Add(1)
	return nil, c.err
}

func (*rejectingConnector) Driver() driver.Driver { return rejectingDriver{} }

type rejectingDriver struct{}

func (rejectingDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("unexpected driver open")
}

func createGORMProbeTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE gorm_adapter_probe (
			id INTEGER PRIMARY KEY,
			tenant TEXT NOT NULL,
			value TEXT NOT NULL,
			version INTEGER NOT NULL CHECK (version > 0),
			state TEXT NOT NULL DEFAULT 'active',
			updated_at_us INTEGER NOT NULL,
			UNIQUE (tenant, value)
		) STRICT`); err != nil {
		t.Fatalf("create GORM probe table: %v", err)
	}
}

func gormSchemaFingerprint(t *testing.T, ctx context.Context, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT type, name, tbl_name, coalesce(sql, '')
		FROM sqlite_schema
		WHERE name NOT LIKE 'sqlite_%'
		ORDER BY type, name`)
	if err != nil {
		t.Fatalf("query schema fingerprint: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close schema fingerprint rows: %v", err)
		}
	}()

	var fingerprint []string
	for rows.Next() {
		var objectType, name, tableName, sqlText string
		if err := rows.Scan(&objectType, &name, &tableName, &sqlText); err != nil {
			t.Fatalf("scan schema fingerprint: %v", err)
		}
		fingerprint = append(fingerprint, strings.Join([]string{objectType, name, tableName, sqlText}, "\x00"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate schema fingerprint: %v", err)
	}
	return fingerprint
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsAny(values []any, want any) bool {
	for _, value := range values {
		if reflect.DeepEqual(value, want) {
			return true
		}
	}
	return false
}
