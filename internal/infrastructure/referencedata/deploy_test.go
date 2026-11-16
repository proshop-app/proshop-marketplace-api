package referencedata

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeployBundles(t *testing.T) {
	root := "../../../deploy/reference"
	registry := migrationDictionaries(t)
	directories, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, directory := range directories {
		if !directory.IsDir() {
			continue
		}
		count++
		t.Run(directory.Name(), func(t *testing.T) {
			delivery, _, err := Load(os.DirFS(filepath.Join(root, directory.Name())))
			if err != nil {
				t.Fatal(err)
			}
			dictionary, exists := registry[delivery.Dictionary]
			if !exists {
				t.Fatal("поставка не регистрирует словарь: заголовка нет в миграциях", delivery.Dictionary)
			}
			dictionary.entries = delivery.Entries
			registry[delivery.Dictionary] = dictionary
			if delivery.Dictionary != directory.Name() {
				t.Fatal("код не совпадает с каталогом")
			}
		})
	}
	if count != len(registry) {
		t.Fatalf("словарей: %d", count)
	}
	assertTaxonomyDictionaries(t, registry)
}
