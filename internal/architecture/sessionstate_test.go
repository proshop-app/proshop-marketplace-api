package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// sessionLockFile — единственное место сеансовых advisory lock: прямой сеанс в обход
// пулера (postgres.DirectSessions).
const sessionLockFile = "internal/infrastructure/postgres/index-rebuild-lock.go"

// sessionRule — приём SQL, который держит состояние сеанса. За пулером в режиме транзакций
// оно остаётся на серверном соединении и достаётся чужой транзакции или теряется.
type sessionRule struct {
	pattern *regexp.Regexp
	reason  string
}

// setStatement — SET/RESET в начале оператора; SET LOCAL живёт до конца транзакции и допустим.
var setStatement = regexp.MustCompile(`(?i)(?:^|;)\s*(SET|RESET)\s+(\w+)`)

var sessionRules = []sessionRule{
	{regexp.MustCompile(`(?i)\bDISCARD\b`), "DISCARD сбрасывает сеанс"},
	{
		regexp.MustCompile(`(?i)set_config\s*\([^)]*,\s*false\s*\)`),
		"set_config(…, false) меняет сеанс — true или SET LOCAL",
	},
	{regexp.MustCompile(`(?i)\b(?:UN)?LISTEN\b`), "LISTEN живёт в сеансе"},
	{regexp.MustCompile(`(?i)\bWITH\s+HOLD\b`), "курсор WITH HOLD переживает транзакцию"},
	{regexp.MustCompile(`(?i)\bCREATE\s+(?:TEMP|TEMPORARY)\b`), "временная таблица живёт в сеансе"},
	{regexp.MustCompile(`(?i)(?:^|;)\s*(?:PREPARE|EXECUTE|DEALLOCATE)\s`), "SQL-уровень PREPARE пулер не отслеживает"},
}

// sessionLock — сеансовая advisory lock; транзакционная pg_advisory_xact_lock допустима везде.
var sessionLock = regexp.MustCompile(`(?i)\bpg_(?:try_)?advisory_(?:lock|unlock)`)

// sessionState — строки SQL файла, которые держат состояние сеанса вне разрешённых мест.
func sessionState(fset *token.FileSet, file *ast.File, lockFile bool) []string {
	var found []string
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(literal.Value)
		if err != nil {
			return true
		}
		for _, reason := range sessionReasons(text, lockFile) {
			found = append(found, fset.Position(literal.Pos()).String()+": "+reason)
		}
		return true
	})
	return found
}

func sessionReasons(text string, lockFile bool) []string {
	var reasons []string
	for _, match := range setStatement.FindAllStringSubmatch(text, -1) {
		if strings.EqualFold(match[1], "RESET") || !strings.EqualFold(match[2], "LOCAL") {
			reasons = append(reasons, strings.ToUpper(match[1])+" без LOCAL меняет сеанс")
		}
	}
	for _, rule := range sessionRules {
		if rule.pattern.MatchString(text) {
			reasons = append(reasons, rule.reason)
		}
	}
	if !lockFile && sessionLock.MatchString(text) {
		reasons = append(reasons, "сеансовая advisory lock — только postgres.IndexRebuildLock по прямому сеансу")
	}
	return reasons
}

// TestNoSessionStateBehindPooler — SQL процесса не держит состояние сеанса: пулы идут
// через пулер в режиме транзакций, сеансовые блокировки — только прямым сеансом.
func TestNoSessionStateBehindPooler(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range goFiles(t) {
		relative, _ := filepath.Rel(root, path)
		inProcess := strings.HasPrefix(relative, "internal/") || strings.HasPrefix(relative, "cmd/")
		if !inProcess || strings.HasSuffix(path, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, problem := range sessionState(fset, file, relative == sessionLockFile) {
			t.Error(problem)
		}
	}
}

func TestSessionStateGuardCatchesViolations(t *testing.T) {
	const source = "package sample\n\n" +
		"const a = `SET statement_timeout = '1s'`\n" +
		"const b = `SET LOCAL statement_timeout = '1s'`\n" +
		"const c = `UPDATE t\n SET a = 1`\n" +
		"const d = `SELECT pg_try_advisory_lock(1)`\n" +
		"const e = `SELECT pg_advisory_xact_lock(1)`\n" +
		"const f = `LISTEN changes`\n" +
		"const g = `DECLARE c CURSOR WITH HOLD FOR SELECT 1`\n" +
		"const h = `SELECT set_config('search_path', 'x', false)`\n" +
		"const i = `CREATE TEMP TABLE x (a int)`\n" +
		"const j = `RESET ALL`\n" +
		"const k = `DECLARE c NO SCROLL CURSOR FOR SELECT 1`\n"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sample.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionState(fset, file, false); len(got) != 7 {
		t.Fatalf("сторож ловит SET, сеансовую блокировку, LISTEN, HOLD, set_config, TEMP, RESET: %v", got)
	}
	if got := sessionState(fset, file, true); len(got) != 6 {
		t.Fatalf("сеансовая блокировка разрешена только своему файлу: %v", got)
	}
}
