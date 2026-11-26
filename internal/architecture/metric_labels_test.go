package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const prometheusImport = "github.com/prometheus/client_golang/prometheus"

// allowedLabels — имена меток метрик: каждое ограничено словарём проекта или магазином (tenant —
// нагрузка магазина видна по нему). Новая метка — сознательное решение: имя добавляется сюда,
// а значение не растёт с покупателями, заказами, запросами и текстами ошибок.
var allowedLabels = map[string]bool{
	"action":     true,
	"class":      true,
	"code":       true,
	"decided_by": true,
	"dictionary": true,
	"direction":  true,
	"event":      true,
	"from":       true,
	"goroutine":  true,
	"job":        true,
	"kind":       true,
	"operation":  true,
	"outcome":    true,
	"partition":  true,
	"placement":  true,
	"reason":     true,
	"result":     true,
	"route":      true,
	"schema":     true,
	"selector":   true,
	"source":     true,
	"status":     true,
	"tenant":     true,
	"tenantId":   true,
	"to":         true,
	"topic":      true,
}

// vecConstructor — конструкторы векторов метрик под любым получателем: пакет, promauto или фабрика
// promauto.With(r) в переменной.
var vecConstructor = regexp.MustCompile(`^New(Counter|Gauge|Histogram|Summary)Vec$`)

// metricPackage — значения объявлений пакета, из которых сторож берёт метки. Имя, объявленное в
// пакете больше одного раза (одноимённые переменные функций, затенение), сторож не угадывает.
type metricPackage struct {
	values    map[string]ast.Expr
	ambiguous map[string]bool
}

// metricLabelProblems — метки вне белого списка, списки меток, которые сторож не смог разобрать
// до строк, и значения меток из текста ошибки — в файлах одного пакета.
func metricLabelProblems(fset *token.FileSet, files []*ast.File) []string {
	pkg := packageValues(files)
	var found []string
	for _, file := range files {
		prometheus := prometheusNames(file)
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			where := fset.Position(call.Pos()).String() + ": "
			if pkg.indirectConstructor(call) {
				found = append(found, where+"конструктор метрики вызван не через селектор — метки не разобраны")
			}
			if labels, ok := labelArgument(call, prometheus); ok {
				found = append(found, pkg.labelProblems(where, labels)...)
			}
			if problem := errorTextValue(call); problem != "" {
				found = append(found, where+problem)
			}
			return true
		})
	}
	sort.Strings(found)
	return found
}

// labelArgument — выражение списка меток конструктора вектора или prometheus.NewDesc.
func labelArgument(call *ast.CallExpr, prometheus map[string]bool) (ast.Expr, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	if vecConstructor.MatchString(selector.Sel.Name) && len(call.Args) >= 2 {
		return call.Args[len(call.Args)-1], true
	}
	if pkg, ok := selector.X.(*ast.Ident); ok && prometheus[pkg.Name] && selector.Sel.Name == "NewDesc" &&
		len(call.Args) >= 3 {
		return call.Args[2], true
	}
	return nil, false
}

func (p metricPackage) labelProblems(where string, expr ast.Expr) []string {
	labels, resolved := p.labels(expr)
	if !resolved {
		return []string{where + "список меток не разобран до строк — метки задаются литералом или константой пакета"}
	}
	var problems []string
	for _, label := range labels {
		if !allowedLabels[label] {
			problems = append(problems, where+"метка "+label+" вне белого списка ограниченных меток")
		}
	}
	return problems
}

// labels — строковые значения выражения; false — выражение не сводится к строкам.
func (p metricPackage) labels(expr ast.Expr) ([]string, bool) {
	switch e := expr.(type) {
	case *ast.CompositeLit:
		var labels []string
		for _, element := range e.Elts {
			values, ok := p.labels(element)
			if !ok {
				return nil, false
			}
			labels = append(labels, values...)
		}
		return labels, true
	case *ast.BasicLit:
		value, err := strconv.Unquote(e.Value)
		return []string{value}, err == nil
	case *ast.Ident:
		if value, ok := p.values[e.Name]; ok && !p.ambiguous[e.Name] {
			return p.labels(value)
		}
	case *ast.BinaryExpr:
		left, okLeft := p.labels(e.X)
		right, okRight := p.labels(e.Y)
		if okLeft && okRight && len(left) == 1 && len(right) == 1 {
			return []string{left[0] + right[0]}, true
		}
	}
	return nil, false
}

// indirectConstructor — вызов функции-значения, за которой стоит конструктор вектора:
// f := prometheus.NewCounterVec; f(...).
func (p metricPackage) indirectConstructor(call *ast.CallExpr) bool {
	name, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false
	}
	selector, ok := p.values[name.Name].(*ast.SelectorExpr)
	return ok && vecConstructor.MatchString(selector.Sel.Name)
}

// errorTextValue — значение метки из текста ошибки: каждый текст — новый ряд.
func errorTextValue(call *ast.CallExpr) string {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "WithLabelValues" {
		return ""
	}
	for _, arg := range call.Args {
		inner, ok := arg.(*ast.CallExpr)
		if !ok {
			continue
		}
		if method, ok := inner.Fun.(*ast.SelectorExpr); ok && method.Sel.Name == "Error" {
			return "значение метки — текст ошибки: ряд на каждый текст"
		}
	}
	return ""
}

// packageValues — значения констант, переменных и коротких объявлений пакета по имени.
func packageValues(files []*ast.File) metricPackage {
	pkg := metricPackage{values: map[string]ast.Expr{}, ambiguous: map[string]bool{}}
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.ValueSpec:
				for i, name := range n.Names {
					if i < len(n.Values) {
						pkg.declare(name.Name, n.Values[i])
					}
				}
			case *ast.AssignStmt:
				for i, left := range n.Lhs {
					if name, ok := left.(*ast.Ident); ok && i < len(n.Rhs) && n.Tok == token.DEFINE {
						pkg.declare(name.Name, n.Rhs[i])
					}
				}
			}
			return true
		})
	}
	return pkg
}

// declare запоминает значение имени; второе объявление того же имени делает его неоднозначным.
func (p metricPackage) declare(name string, value ast.Expr) {
	if _, seen := p.values[name]; seen {
		p.ambiguous[name] = true
	}
	p.values[name] = value
}

// prometheusNames — имена, под которыми файл импортирует пакет prometheus.
func prometheusNames(file *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, spec := range file.Imports {
		if path, _ := strconv.Unquote(spec.Path.Value); path != prometheusImport {
			continue
		}
		name := "prometheus"
		if spec.Name != nil {
			name = spec.Name.Name
		}
		names[name] = true
	}
	return names
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
		for _, problem := range metricLabelProblems(fset, files) {
			t.Error(problem)
		}
	}
	if checked < 50 {
		t.Fatalf("проверено %d исходников: сторож ничего бы не проверил", checked)
	}
}

func TestMetricLabelGuardCatchesViolations(t *testing.T) {
	const source = `package sample

import (
	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"other"
)

const labelKind = "ki" + "nd"

var shared = []string{labelKind, "tenant"}

func f(r prom.Registerer, err error, vec *prom.CounterVec) {
	local := []string{"product_id"}
	prom.NewCounterVec(prom.CounterOpts{}, local)
	factory := promauto.With(r)
	factory.NewGaugeVec(prom.GaugeOpts{}, []string{"request_id", "buyer_phone"})
	prom.NewDesc("name", "help", []string{"order_no", "raw_path"}, nil)
	promauto.NewHistogramVec(prom.HistogramOpts{}, []string{"ip", "query", "sku", "session"})
	prom.NewSummaryVec(prom.SummaryOpts{}, []string{other.Label})
	prom.NewCounterVec(prom.CounterOpts{}, append(shared, "kind"))
	prom.NewCounterVec(prom.CounterOpts{}, shared)
	vec.WithLabelValues(err.Error()).Inc()
	labels := []string{"kind"}
	prom.NewCounterVec(prom.CounterOpts{}, labels)
	newVec := prom.NewCounterVec
	newVec(prom.CounterOpts{}, []string{"kind"})
}

func g() {
	labels := []string{"buyer"}
	prom.NewCounterVec(prom.CounterOpts{}, labels)
	shared := []string{"buyer"}
	prom.NewCounterVec(prom.CounterOpts{}, shared)
}
`
	path := filepath.Join(t.TempDir(), "sample.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(metricLabelProblems(fset, []*ast.File{file}), "\n")
	for _, label := range []string{
		"product_id",
		"request_id",
		"buyer_phone",
		"order_no",
		"raw_path",
		"ip",
		"query",
		"sku",
		"session",
	} {
		if !strings.Contains(got, "метка "+label+" ") {
			t.Fatalf("сторож не поймал %s:\n%s", label, got)
		}
	}
	// Не разобраны: константа другого пакета, append, по два вызова одноимённых labels и затенённой
	// shared, конструктор через переменную.
	if strings.Count(got, "не разобран") != 7 || !strings.Contains(got, "текст ошибки") {
		t.Fatalf("неразобранные списки и текст ошибки — отдельные нарушения:\n%s", got)
	}
	if strings.Contains(got, "метка kind") || strings.Contains(got, "метка tenant") {
		t.Fatalf("сторож запретил разрешённую метку:\n%s", got)
	}
}
