package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// unboundedLabels — метки, число значений которых растёт с площадкой: тысячи магазинов дают
// тысячи рядов на реплику. Такие значения пишутся в журнал, счётчик агрегируется.
var unboundedLabels = map[string]bool{
	"tenant":     true,
	"tenantid":   true,
	"tenant_id":  true,
	"shop":       true,
	"buyer":      true,
	"buyerid":    true,
	"buyer_id":   true,
	"checkout":   true,
	"checkoutid": true,
	"order":      true,
	"orderid":    true,
	"order_id":   true,
	"product":    true,
	"productid":  true,
	"offer":      true,
	"offerid":    true,
}

// unboundedMetricLabels — метки конструкторов prometheus.New*Vec из списка неограниченных.
func unboundedMetricLabels(fset *token.FileSet, file *ast.File) []string {
	var found []string
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || !isVecConstructor(call) || len(call.Args) < 2 {
			return true
		}
		labels, ok := call.Args[1].(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, element := range labels.Elts {
			literal, ok := element.(*ast.BasicLit)
			if !ok {
				continue
			}
			label, _ := strconv.Unquote(literal.Value)
			if unboundedLabels[strings.ToLower(label)] {
				found = append(found, fset.Position(literal.Pos()).String()+": метка "+label+
					" неограниченна — магазин и покупка в журнал, счётчик без неё")
			}
		}
		return true
	})
	return found
}

func isVecConstructor(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == "prometheus" && strings.HasPrefix(selector.Sel.Name, "New") &&
		strings.HasSuffix(selector.Sel.Name, "Vec")
}

func TestMetricLabelsAreBounded(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
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
		checked++
		for _, problem := range unboundedMetricLabels(fset, file) {
			t.Error(problem)
		}
	}
	if checked < 50 {
		t.Fatalf("проверено %d исходников: сторож ничего бы не проверил", checked)
	}
}

func TestMetricLabelGuardCatchesViolations(t *testing.T) {
	const source = `package sample

func f() {
	prometheus.NewCounterVec(prometheus.CounterOpts{}, []string{"kind", "tenant"})
	prometheus.NewGaugeVec(prometheus.GaugeOpts{}, []string{"tenantId"})
	prometheus.NewHistogramVec(prometheus.HistogramOpts{}, []string{"topic"})
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sample.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := unboundedMetricLabels(fset, file)
	if len(got) != 2 || !strings.Contains(got[0], "метка tenant ") || !strings.Contains(got[1], "метка tenantId") {
		t.Fatalf("сторож ловит метку магазина у счётчика и gauge: %v", got)
	}
}
