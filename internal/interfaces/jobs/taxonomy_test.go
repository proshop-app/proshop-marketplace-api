package jobs

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/domain/taxonomy"
)

func TestTaxonomyProgressLogsPeriodicTotalsAndCompletion(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	job := taxonomy.Revalidation{Version: 2, Category: "c1"}
	for page := int64(1); page <= 250; page++ {
		job.Processed = page * 50
		job.Done = page == 250
		logTaxonomyProgress(context.Background(), logger, job, page)
	}
	if strings.Count(output.String(), "\n") != 3 || !strings.Contains(output.String(), `"processed":12500`) {
		t.Fatal("Info должен содержать только два промежуточных итога и завершение", output.String())
	}
	output.Reset()
	logger = slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logTaxonomyProgress(context.Background(), logger, taxonomy.Revalidation{Processed: 50}, 1)
	if !strings.Contains(output.String(), `"level":"DEBUG"`) {
		t.Fatal("постраничное сообщение не в Debug", output.String())
	}
}
