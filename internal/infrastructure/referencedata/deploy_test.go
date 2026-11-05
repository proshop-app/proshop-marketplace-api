package referencedata

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeployBundles(t *testing.T) {
	root := "../../../deploy/reference"
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
			if delivery.Dictionary != directory.Name() {
				t.Fatal("код не совпадает с каталогом")
			}
		})
	}
	if count != 19 {
		t.Fatalf("словарей: %d", count)
	}
}
