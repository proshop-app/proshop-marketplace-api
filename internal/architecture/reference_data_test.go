package architecture_test

import (
	"os"
	"testing"
)

func TestReferenceDataLivesOutsideMarketplace(t *testing.T) {
	_, err := os.Stat("../../deploy/reference")
	if !os.IsNotExist(err) {
		t.Fatalf("deploy/reference запрещён: данные словарей принадлежат uno-reference-data (stat: %v)", err)
	}
}
