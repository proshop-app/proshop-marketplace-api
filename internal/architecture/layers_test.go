// Package architecture_test — сторож слоистости. Граф импортов строится
// командой go list -deps -json; нарушение правила слоя — красный тест.
package architecture_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const module = "github.com/proshop-app/proshop-marketplace"

// pkg — пакет из go list.
type pkg struct {
	ImportPath   string
	Standard     bool
	Deps         []string
	TestImports  []string
	XTestImports []string
}

// rule — правило слоя: какие зависимости запрещены его пакетам.
type rule struct {
	layer string
	// prefix — пакеты слоя.
	prefix string
	// onlyStdAnd — если задано, разрешены только стандартная библиотека и эти префиксы.
	onlyStdAnd []string
	// forbidden — запрещённые префиксы.
	forbidden []string
	// tests — проверять и импорты тестов.
	tests bool
}

var rules = []rule{
	{
		layer:      "домен",
		prefix:     module + "/internal/domain/",
		onlyStdAnd: []string{module + "/pkg/", module + "/internal/domain/"},
		tests:      true,
	},
	{
		layer:      "pkg",
		prefix:     module + "/pkg/",
		onlyStdAnd: []string{module + "/pkg/"},
		tests:      true,
	},
	{
		layer:  "приложение",
		prefix: module + "/internal/application/",
		forbidden: []string{
			module + "/internal/infrastructure/",
			module + "/internal/interfaces/",
			module + "/internal/config",
			module + "/cmd/",
		},
		tests: true,
	},
	{
		layer:     "интерфейсы",
		prefix:    module + "/internal/interfaces/",
		forbidden: []string{module + "/internal/infrastructure/", module + "/internal/config", module + "/cmd/"},
	},
	{
		layer:     "инфраструктура",
		prefix:    module + "/internal/infrastructure/",
		forbidden: []string{module + "/internal/interfaces/", module + "/internal/config", module + "/cmd/"},
	},
}

func within(path, prefix string) bool {
	return strings.HasPrefix(path+"/", prefix) || strings.HasPrefix(path, prefix)
}

// violations — нарушения правил слоёв в графе пакетов.
func violations(packages map[string]pkg) []string {
	var found []string

	for _, p := range packages {
		for _, r := range rules {
			if !within(p.ImportPath, r.prefix) {
				continue
			}

			deps := map[string]string{}
			for _, dep := range p.Deps {
				deps[dep] = "зависит от"
			}
			if r.tests {
				for _, dep := range append(append([]string{}, p.TestImports...), p.XTestImports...) {
					if _, ok := deps[dep]; !ok && dep != p.ImportPath {
						deps[dep] = "в тестах импортирует"
					}
				}
			}

			for dep, how := range deps {
				if broken(r, dep, packages) {
					found = append(found, r.layer+": "+p.ImportPath+" "+how+" "+dep)
				}
			}
		}
	}

	sort.Strings(found)

	return found
}

func broken(r rule, dep string, packages map[string]pkg) bool {
	for _, prefix := range r.forbidden {
		if within(dep, prefix) {
			return true
		}
	}

	if r.onlyStdAnd == nil || packages[dep].Standard || dep == "C" || dep == "unsafe" {
		return false
	}

	for _, prefix := range r.onlyStdAnd {
		if within(dep, prefix) {
			return false
		}
	}

	return true
}

func listPackages(t *testing.T) map[string]pkg {
	t.Helper()

	gomod, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}

	cmd := exec.Command("go", "list", "-deps", "-test", "-json", "./...")
	cmd.Dir = filepath.Dir(strings.TrimSpace(string(gomod)))

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}

	packages := map[string]pkg{}
	decoder := json.NewDecoder(bytes.NewReader(out))

	for {
		var p pkg
		if err := decoder.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("разбор go list: %v", err)
		}

		// варианты пакетов для тестов: "path [path.test]" и "path.test"
		if strings.Contains(p.ImportPath, " ") || strings.HasSuffix(p.ImportPath, ".test") {
			continue
		}

		packages[p.ImportPath] = p
	}

	return packages
}

func TestLayers(t *testing.T) {
	packages := listPackages(t)

	if _, ok := packages[module+"/internal/domain/catalog"]; !ok {
		t.Fatal("go list не вернул пакеты модуля: сторож ничего бы не проверил")
	}

	for _, violation := range violations(packages) {
		t.Error(violation)
	}
}

func TestGuardCatchesViolations(t *testing.T) {
	packages := map[string]pkg{
		"fmt":                     {ImportPath: "fmt", Standard: true},
		"github.com/x/thirdparty": {ImportPath: "github.com/x/thirdparty"},
		module + "/pkg/money":     {ImportPath: module + "/pkg/money", Deps: []string{"fmt"}},
		module + "/internal/domain/catalog": {
			ImportPath: module + "/internal/domain/catalog",
			Deps:       []string{"fmt", module + "/pkg/money", "github.com/x/thirdparty"},
		},
		module + "/internal/domain/participant": {
			ImportPath:   module + "/internal/domain/participant",
			Deps:         []string{"fmt"},
			XTestImports: []string{module + "/internal/application/fake"},
		},
		module + "/internal/application/catalog": {
			ImportPath: module + "/internal/application/catalog",
			Deps:       []string{module + "/internal/domain/catalog", module + "/internal/infrastructure/postgres"},
		},
		module + "/internal/interfaces/http": {
			ImportPath: module + "/internal/interfaces/http",
			Deps: []string{
				module + "/internal/application/catalog",
				module + "/internal/infrastructure/opensearch",
			},
		},
		module + "/internal/infrastructure/postgres": {
			ImportPath: module + "/internal/infrastructure/postgres",
			Deps:       []string{module + "/internal/application/ports", module + "/internal/interfaces/kafka"},
		},
	}

	got := violations(packages)
	internal := module + "/internal"
	want := []string{
		"домен: " + internal + "/domain/catalog зависит от github.com/x/thirdparty",
		"домен: " + internal + "/domain/participant в тестах импортирует " + internal + "/application/fake",
		"интерфейсы: " + internal + "/interfaces/http зависит от " + internal + "/infrastructure/opensearch",
		"инфраструктура: " + internal + "/infrastructure/postgres зависит от " + internal + "/interfaces/kafka",
		"приложение: " + internal + "/application/catalog зависит от " + internal + "/infrastructure/postgres",
	}

	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("сторож нашёл:\n%s\n\nожидалось:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
