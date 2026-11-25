package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	prometheusImport = "github.com/prometheus/client_golang/prometheus"
	promautoImport   = "github.com/prometheus/client_golang/prometheus/promauto"
)

// unboundedLabels — метки, число значений которых растёт с каждым покупателем, заказом или запросом:
// ряд на каждое значение. Магазин (tenant) — ограниченная и нужная метка: по ней видна нагрузка
// магазина. Сравнение — без регистра и подчёркиваний.
var unboundedLabels = map[string]bool{
	"buyer":        true,
	"buyerid":      true,
	"customer":     true,
	"customerid":   true,
	"user":         true,
	"userid":       true,
	"phone":        true,
	"email":        true,
	"order":        true,
	"orderid":      true,
	"ordernumber":  true,
	"checkout":     true,
	"checkoutid":   true,
	"purchase":     true,
	"purchaseid":   true,
	"path":         true,
	"url":          true,
	"uri":          true,
	"error":        true,
	"err":          true,
	"errormessage": true,
	"message":      true,
}

// metricPackage — объявления пакета, из которых сторож берёт значения меток.
type metricPackage struct {
	values map[string]ast.Expr
}

// unboundedMetricLabels — неограниченные метки конструкторов New*Vec пакетов prometheus и promauto
// в файлах одного пакета: литералом, константой или переменной, при любом имени импорта.
func unboundedMetricLabels(fset *token.FileSet, files []*ast.File) []string {
	pkg := packageValues(files)
	var found []string
	for _, file := range files {
		imports := metricImports(file)
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || !isVecConstructor(call, imports) || len(call.Args) < 2 {
				return true
			}
			for _, label := range pkg.labels(call.Args[len(call.Args)-1]) {
				if unboundedLabels[strings.ReplaceAll(strings.ToLower(label), "_", "")] {
					found = append(found, fset.Position(call.Pos()).String()+": метка "+label+
						" неограниченна — значение в журнал, счётчик без неё")
				}
			}
			return true
		})
	}
	sort.Strings(found)
	return found
}

func packageValues(files []*ast.File) metricPackage {
	pkg := metricPackage{values: map[string]ast.Expr{}}
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			spec, ok := node.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, name := range spec.Names {
				if i < len(spec.Values) {
					pkg.values[name.Name] = spec.Values[i]
				}
			}
			return true
		})
	}
	return pkg
}

// labels — строковые значения выражения списка меток; неизвестное выражение не даёт меток.
func (p metricPackage) labels(expr ast.Expr) []string {
	switch e := expr.(type) {
	case *ast.CompositeLit:
		var labels []string
		for _, element := range e.Elts {
			labels = append(labels, p.labels(element)...)
		}
		return labels
	case *ast.BasicLit:
		value, err := strconv.Unquote(e.Value)
		if err != nil {
			return nil
		}
		return []string{value}
	case *ast.Ident:
		if value, ok := p.values[e.Name]; ok {
			return p.labels(value)
		}
	case *ast.BinaryExpr:
		left, right := p.labels(e.X), p.labels(e.Y)
		if len(left) == 1 && len(right) == 1 {
			return []string{left[0] + right[0]}
		}
	}
	return nil
}

// metricImports — имена, под которыми файл импортирует prometheus и promauto.
func metricImports(file *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, spec := range file.Imports {
		path, _ := strconv.Unquote(spec.Path.Value)
		if path != prometheusImport && path != promautoImport {
			continue
		}
		name := filepath.Base(path)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		names[name] = true
	}
	return names
}

// isVecConstructor — pkg.New*Vec(...) или promauto.With(r).New*Vec(...).
func isVecConstructor(call *ast.CallExpr, imports map[string]bool) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !strings.HasPrefix(selector.Sel.Name, "New") || !strings.HasSuffix(selector.Sel.Name, "Vec") {
		return false
	}
	switch x := selector.X.(type) {
	case *ast.Ident:
		return imports[x.Name]
	case *ast.CallExpr:
		inner, ok := x.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		pkg, ok := inner.X.(*ast.Ident)
		return ok && imports[pkg.Name]
	}
	return false
}

func TestMetricLabelsAreBounded(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	byDir := map[string][]string{}
	for _, path := range goFiles(t) {
		relative, _ := filepath.Rel(root, path)
		inProcess := strings.HasPrefix(relative, "internal/") || strings.HasPrefix(relative, "cmd/")
		if inProcess && !strings.HasSuffix(path, "_test.go") {
			byDir[filepath.Dir(path)] = append(byDir[filepath.Dir(path)], path)
		}
	}
	checked := 0
	for _, paths := range byDir {
		fset := token.NewFileSet()
		files := make([]*ast.File, 0, len(paths))
		for _, path := range paths {
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, file)
			checked++
		}
		for _, problem := range unboundedMetricLabels(fset, files) {
			t.Error(problem)
		}
	}
	if checked < 50 {
		t.Fatalf("проверено %d исходников: сторож ничего бы не проверил", checked)
	}
}

func TestMetricLabelGuardCatchesViolations(t *testing.T) {
	dir := t.TempDir()
	const source = `package sample

import (
	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const labelBuyer = "buyer" + "Id"

var orderLabels = []string{"kind", "order_id"}

func f(r prom.Registerer) {
	prom.NewCounterVec(prom.CounterOpts{}, []string{"kind", "tenant"})
	prom.NewGaugeVec(prom.GaugeOpts{}, []string{labelBuyer})
	prom.NewHistogramVec(prom.HistogramOpts{}, orderLabels)
	promauto.NewCounterVec(prom.CounterOpts{}, []string{"checkoutId"})
	promauto.With(r).NewSummaryVec(prom.SummaryOpts{}, []string{"path", "error"})
	prom.NewCounterVec(prom.CounterOpts{}, []string{"topic", "tenantId", "reason"})
}
`
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(unboundedMetricLabels(fset, []*ast.File{file}), "\n")
	for _, label := range []string{"buyerId", "order_id", "checkoutId", "path", "error"} {
		if !strings.Contains(got, "метка "+label+" ") {
			t.Fatalf("сторож не поймал %s: %s", label, got)
		}
	}
	for _, allowed := range []string{"tenant ", "tenantId", "kind", "topic", "reason"} {
		if strings.Contains(got, "метка "+allowed) {
			t.Fatalf("сторож запретил ограниченную метку %s: %s", allowed, got)
		}
	}
}
