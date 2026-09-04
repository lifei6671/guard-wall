package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestStoreBusinessFilesDoNotUseRawSQLAPIs(t *testing.T) {
	forbidden := regexp.MustCompile("(?:\\.(?:Raw|ExecContext|QueryContext|QueryRowContext)\\s*\\(|gorm\\.Expr\\s*\\(|clause\\.Expr\\s*\\{|\\.(?:Where|Order)\\s*\\(\\s*(?:\\\"|`))")
	exceptions := map[string]struct{}{
		"gorm_adapter.go": {},
		"migration.go":    {},
		"pragma.go":       {},
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate boundary test source")
	}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(currentFile), "*.go"))
	if err != nil {
		t.Fatalf("list store Go files: %v", err)
	}
	columns := runtimeColumnNames(t, paths)
	for _, path := range paths {
		name := filepath.Base(path)
		if len(name) >= len("_test.go") && name[len(name)-len("_test.go"):] == "_test.go" {
			continue
		}
		if _, ok := exceptions[name]; ok {
			continue
		}
		if strings.HasSuffix(name, "_columns.go") {
			continue
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		if forbidden.Match(contents) {
			t.Errorf("%s uses a forbidden raw SQL API", path)
		}
		assertRuntimeColumnReferencesUseMappings(t, path, contents)
		for _, literal := range hardcodedRuntimeColumnLiterals(t, path, contents, columns) {
			t.Errorf("%s:%d: runtime column %q must use a Columns mapping", path, literal.line, literal.value)
		}
	}
}

func TestGORMModelColumnsHaveMappings(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate boundary test source")
	}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(currentFile), "*.go"))
	if err != nil {
		t.Fatalf("list store Go files: %v", err)
	}
	columns := runtimeColumnNames(t, paths)
	for _, path := range paths {
		name := filepath.Base(path)
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		for _, column := range gormModelColumns(t, path, contents) {
			if _, mapped := columns[column.value]; !mapped {
				t.Errorf("%s:%d: GORM model column %q has no Columns mapping", path, column.line, column.value)
			}
		}
	}
}

type runtimeColumnLiteral struct {
	line  int
	value string
}

func runtimeColumnNames(t *testing.T, paths []string) map[string]struct{} {
	t.Helper()
	columns := make(map[string]struct{})
	for _, path := range paths {
		if !strings.HasSuffix(filepath.Base(path), "_columns.go") {
			continue
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, path, contents, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatalf("unquote %s:%d: %v", path, fileSet.Position(literal.Pos()).Line, err)
			}
			columns[value] = struct{}{}
			return true
		})
	}
	return columns
}

func hardcodedRuntimeColumnLiterals(t *testing.T, path string, contents []byte, columns map[string]struct{}) []runtimeColumnLiteral {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, contents, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	literals := make([]runtimeColumnLiteral, 0)
	// 仅区分明确审计行上的直接业务值，不豁免同词列名或间接表达式。
	auditValues := make(map[*ast.BasicLit]struct{})
	ast.Inspect(file, func(node ast.Node) bool {
		if composite, ok := node.(*ast.CompositeLit); ok && isNamedIdentifier(composite.Type, "criticalAuditRow") {
			for _, element := range composite.Elts {
				field, ok := element.(*ast.KeyValueExpr)
				if !ok || (keyValueKey(field) != "Category" && keyValueKey(field) != "ActorType") {
					continue
				}
				if value, ok := field.Value.(*ast.BasicLit); ok && value.Kind == token.STRING {
					auditValues[value] = struct{}{}
				}
			}
		}
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		if _, businessValue := auditValues[literal]; businessValue {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil {
			t.Fatalf("unquote %s:%d: %v", path, fileSet.Position(literal.Pos()).Line, err)
		}
		if _, exists := columns[value]; exists {
			literals = append(literals, runtimeColumnLiteral{line: fileSet.Position(literal.Pos()).Line, value: value})
		}
		return true
	})
	return literals
}

func gormModelColumns(t *testing.T, path string, contents []byte) []runtimeColumnLiteral {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, contents, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	columns := make([]runtimeColumnLiteral, 0)
	ast.Inspect(file, func(node ast.Node) bool {
		field, ok := node.(*ast.Field)
		if !ok || field.Tag == nil || field.Tag.Kind != token.STRING {
			return true
		}
		tag, err := strconv.Unquote(field.Tag.Value)
		if err != nil {
			t.Fatalf("unquote %s:%d: %v", path, fileSet.Position(field.Tag.Pos()).Line, err)
		}
		for _, option := range strings.Split(reflect.StructTag(tag).Get("gorm"), ";") {
			if column, found := strings.CutPrefix(option, "column:"); found && column != "" {
				columns = append(columns, runtimeColumnLiteral{line: fileSet.Position(field.Tag.Pos()).Line, value: column})
			}
		}
		return true
	})
	return columns
}

func assertRuntimeColumnReferencesUseMappings(t *testing.T, path string, contents []byte) {
	violations, err := runtimeColumnReferenceViolations(path, contents)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, violation := range violations {
		t.Errorf("%s:%d: %s must use a Columns mapping, not %s", path, violation.line, violation.surface, violation.value)
	}
}

type runtimeColumnReferenceViolation struct {
	line    int
	surface string
	value   string
}

func runtimeColumnReferenceViolations(path string, contents []byte) ([]runtimeColumnReferenceViolation, error) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, contents, 0)
	if err != nil {
		return nil, err
	}
	violations := make([]runtimeColumnReferenceViolation, 0)
	record := func(surface string, expression ast.Expr) {
		violations = append(violations, runtimeColumnReferenceViolation{
			line: fileSet.Position(expression.Pos()).Line, surface: surface, value: "an unmapped expression",
		})
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				validateRuntimeColumnCall(function.Name.Name, call, record)
			}
			if literal, ok := node.(*ast.CompositeLit); ok {
				validateRuntimeClauseColumn(function.Name.Name, literal, record)
			}
			return true
		})
	}
	return violations, nil
}

func validateRuntimeColumnCall(function string, call *ast.CallExpr, record func(string, ast.Expr)) {
	var method string
	switch function := call.Fun.(type) {
	case *ast.SelectorExpr:
		method = function.Sel.Name
	case *ast.Ident:
		method = function.Name
	default:
		return
	}
	switch method {
	case "Select", "Distinct":
		for _, argument := range call.Args {
			if !isColumnsSelector(argument) {
				record(method, argument)
			}
		}
	case "Order":
		if len(call.Args) > 0 && !isColumnsSelector(call.Args[0]) && !isOrderByColumnExpression(call.Args[0]) {
			record(method, call.Args[0])
		}
	case "Pluck", "Group", "Having", "Joins", "Update", "UpdateColumn":
		if len(call.Args) > 0 && !isColumnsSelector(call.Args[0]) {
			record(method, call.Args[0])
		}
	case "AssignmentColumns", "orderByColumns":
		if function == "reconcileUpsertConflict" && method == "AssignmentColumns" && len(call.Args) == 1 && isNamedIdentifier(call.Args[0], "updateColumns") {
			return
		}
		for _, argument := range call.Args {
			if !isMappedColumnList(argument) {
				record(method, argument)
			}
		}
	case "reconcileUpsertConflict":
		if len(call.Args) != 2 || !isMappedColumnList(call.Args[0]) || !isReconcileStateUpdateColumnsCall(call.Args[1]) {
			record(method, call)
		}
	case "Where", "Or", "Not", "Updates", "UpdateColumns":
		for _, argument := range call.Args {
			if !isMappedColumnMap(argument) && !isModelCondition(argument) && !isClauseCondition(argument) {
				record(method, argument)
			}
		}
	case "Find", "First", "Take", "Last", "Delete":
		if len(call.Args) > 1 {
			condition := call.Args[1]
			if !isMappedColumnMap(condition) && !isModelCondition(condition) && !isClauseCondition(condition) {
				record(method, condition)
			}
		}
	}
}

func validateRuntimeClauseColumn(function string, literal *ast.CompositeLit, record func(string, ast.Expr)) {
	if isClauseColumn(literal) {
		for _, element := range literal.Elts {
			keyValue, ok := element.(*ast.KeyValueExpr)
			if !ok || keyValueKey(keyValue) != "Name" {
				continue
			}
			if (function == "orderByColumns" || function == "reconcileUpsertConflict") && isNamedIdentifier(keyValue.Value, "name") {
				continue
			}
			if !isColumnsSelector(keyValue.Value) {
				record("clause.Column.Name", keyValue.Value)
			}
		}
	}
	if !isClauseComparison(literal) {
		return
	}
	for _, element := range literal.Elts {
		keyValue, ok := element.(*ast.KeyValueExpr)
		if !ok || keyValueKey(keyValue) != "Column" {
			continue
		}
		if !isClauseColumnExpression(keyValue.Value) {
			record("clause comparison Column", keyValue.Value)
		}
	}
}

func isColumnsSelector(expression ast.Expr) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	base, ok := selector.X.(*ast.Ident)
	return ok && strings.HasSuffix(base.Name, "Columns")
}

func isMappedColumnList(expression ast.Expr) bool {
	if isColumnsSelector(expression) {
		return true
	}
	literal, ok := expression.(*ast.CompositeLit)
	if !ok {
		return false
	}
	for _, element := range literal.Elts {
		value, ok := element.(ast.Expr)
		if !ok || !isMappedColumnList(value) {
			return false
		}
	}
	return true
}

func isMappedColumnMap(expression ast.Expr) bool {
	literal, ok := expression.(*ast.CompositeLit)
	if !ok {
		return false
	}
	if _, ok := literal.Type.(*ast.MapType); !ok {
		return false
	}
	for _, element := range literal.Elts {
		keyValue, ok := element.(*ast.KeyValueExpr)
		if !ok || !isColumnsSelector(keyValue.Key) {
			return false
		}
	}
	return true
}

func isModelCondition(expression ast.Expr) bool {
	if unary, ok := expression.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		return isStructLiteral(unary.X)
	}
	return isStructLiteral(expression)
}

func isClauseCondition(expression ast.Expr) bool {
	if literal, ok := expression.(*ast.CompositeLit); ok {
		return isClauseComparison(literal)
	}
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok || packageName.Name != "clause" {
		return false
	}
	switch selector.Sel.Name {
	case "And", "Or", "Not":
		for _, argument := range call.Args {
			if !isClauseCondition(argument) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func isStructLiteral(expression ast.Expr) bool {
	literal, ok := expression.(*ast.CompositeLit)
	if !ok {
		return false
	}
	switch literal.Type.(type) {
	case *ast.Ident, *ast.SelectorExpr:
		return true
	default:
		return false
	}
}

func isOrderByColumnExpression(expression ast.Expr) bool {
	literal, ok := expression.(*ast.CompositeLit)
	if !ok {
		return false
	}
	selector, ok := literal.Type.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "OrderByColumn" {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok || packageName.Name != "clause" {
		return false
	}
	for _, element := range literal.Elts {
		keyValue, ok := element.(*ast.KeyValueExpr)
		if ok && keyValueKey(keyValue) == "Column" {
			return isClauseColumnExpression(keyValue.Value)
		}
	}
	return false
}

func isClauseColumnExpression(expression ast.Expr) bool {
	if isColumnsSelector(expression) {
		return true
	}
	literal, ok := expression.(*ast.CompositeLit)
	if !ok || !isClauseColumn(literal) {
		return false
	}
	for _, element := range literal.Elts {
		keyValue, ok := element.(*ast.KeyValueExpr)
		if ok && keyValueKey(keyValue) == "Name" {
			return isColumnsSelector(keyValue.Value)
		}
	}
	return false
}

func isReconcileStateUpdateColumnsCall(expression ast.Expr) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok || !isNamedIdentifier(call.Fun, "reconcileStateUpdateColumns") {
		return false
	}
	for _, argument := range call.Args {
		if !isColumnsSelector(argument) {
			return false
		}
	}
	return true
}

func isNamedIdentifier(expression ast.Expr, name string) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && identifier.Name == name
}

func isClauseColumn(literal *ast.CompositeLit) bool {
	selector, ok := literal.Type.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Column" {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	return ok && packageName.Name == "clause"
}

func isClauseComparison(literal *ast.CompositeLit) bool {
	selector, ok := literal.Type.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok || packageName.Name != "clause" {
		return false
	}
	switch selector.Sel.Name {
	case "Eq", "Neq", "Gt", "Gte", "Lt", "Lte", "IN", "Like":
		return true
	default:
		return false
	}
}

func keyValueKey(value *ast.KeyValueExpr) string {
	identifier, ok := value.Key.(*ast.Ident)
	if !ok {
		return ""
	}
	return identifier.Name
}

func TestRuntimeColumnReferenceGuardRejectsBypasses(t *testing.T) {
	source := []byte(`package store
func bypass(db any, value string) {
	db.Select([]string{"node_id"})
	db.Where(clause.Eq{Column: "node_id", Value: value})
	db.UpdateColumn("node_id", value)
	db.UpdateColumns(map[string]any{"node_id": value})
	db.Order([]string{"node_id"})
	clause.AssignmentColumns([]string{"node_id"})
}`)
	violations, err := runtimeColumnReferenceViolations("bypass.go", source)
	if err != nil {
		t.Fatalf("parse bypass fixture: %v", err)
	}
	if len(violations) != 6 {
		t.Fatalf("guard violations = %#v, want six", violations)
	}
}

func TestRuntimeColumnLiteralGuardRejectsAliasesAndUnlistedSurfaces(t *testing.T) {
	source := []byte(`package store
func bypass(db any, value string) {
	column := "node_id"
	filter := map[string]any{"node_id": value}
	db.Select(column)
	db.Where(filter)
	db.Pluck("node_id", &value)
}`)
	literals := hardcodedRuntimeColumnLiterals(t, "bypass.go", source, map[string]struct{}{"node_id": {}})
	if len(literals) != 3 {
		t.Fatalf("hardcoded column literals = %#v, want three", literals)
	}
}

func TestRuntimeColumnLiteralGuardDistinguishesAuditValues(t *testing.T) {
	columns := map[string]struct{}{"source": {}}
	legal := []byte(`package store
func report() {
	_ = criticalAuditRow{Category: "source", ActorType: "source"}
}`)
	if literals := hardcodedRuntimeColumnLiterals(t, "audit.go", legal, columns); len(literals) != 0 {
		t.Fatalf("audit business values treated as columns: %#v", literals)
	}
	mixed := []byte(`package store
func report(db any, value string) {
	_ = criticalAuditRow{Category: "source", ActorType: "source"}
	column := "source"
	filter := map[string]any{"source": value}
	_ = clause.Column{Name: "source"}
	_ = otherRow{Category: "source"}
	_ = criticalAuditRow{Action: "source"}
	_ = criticalAuditRow{Category: string("source")}
	db.Select(column)
	db.Where(filter)
}`)
	literals := hardcodedRuntimeColumnLiterals(t, "mixed.go", mixed, columns)
	want := []runtimeColumnLiteral{
		{line: 4, value: "source"}, {line: 5, value: "source"}, {line: 6, value: "source"},
		{line: 7, value: "source"}, {line: 8, value: "source"}, {line: 9, value: "source"},
	}
	if !reflect.DeepEqual(literals, want) {
		t.Fatalf("mixed column literals = %#v, want %#v", literals, want)
	}
	violations, err := runtimeColumnReferenceViolations("mixed.go", mixed)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 3 || violations[0].surface != "clause.Column.Name" || violations[1].surface != "Select" || violations[2].surface != "Where" {
		t.Fatalf("column-reference guards changed: %#v", violations)
	}
}

func TestRuntimeColumnReferenceGuardRejectsConstructedAliases(t *testing.T) {
	source := []byte(`package store
func bypass(db any, value string) {
	column := "node" + "_id"
	filter := map[string]any{column: value}
	db.Select(column)
	db.Where(filter)
	db.Pluck(column, &value)
}`)
	violations, err := runtimeColumnReferenceViolations("bypass.go", source)
	if err != nil {
		t.Fatalf("parse bypass fixture: %v", err)
	}
	if len(violations) != 3 {
		t.Fatalf("guard violations = %#v, want three", violations)
	}
}

func TestRuntimeColumnReferenceGuardRejectsInlineTerminalConditions(t *testing.T) {
	source := []byte(`package store
func bypass(db any, row any, rows any, value string) {
	db.Take(&row, "node_id = ?", value)
	db.Find(&rows, map[string]any{"node_id": value})
	db.Delete(&row, clause.Eq{Column: "node_id", Value: value})
}`)
	violations, err := runtimeColumnReferenceViolations("bypass.go", source)
	if err != nil {
		t.Fatalf("parse bypass fixture: %v", err)
	}
	if len(violations) != 3 {
		t.Fatalf("guard violations = %#v, want three", violations)
	}
}
