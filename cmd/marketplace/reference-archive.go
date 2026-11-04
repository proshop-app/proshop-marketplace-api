package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/proshop-app/proshop-marketplace/internal/config"
	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/postgres"
)

// runReferenceArchive — явная операция cutover: прежде остановить старый
// emitter. Файл не перезаписывается, исходные payload остаются и в outbox.
func runReferenceArchive(ctx context.Context, cfg config.Config, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("reference archive-pending", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("out", "", "новый файл экспорта старых reference.changed")
	if err := flags.Parse(args); err != nil || *path == "" || flags.NArg() != 0 {
		return errors.New("использование: marketplace reference archive-pending --out <новый файл>")
	}
	pool, err := postgres.Open(ctx, cfg.Database.URL, 2)
	if err != nil {
		return errors.New("не удалось подключиться к PostgreSQL для экспорта задач")
	}
	defer pool.Close()
	tasks, err := postgres.PendingReferenceTasks(ctx, pool)
	if err != nil {
		return errors.New("не удалось прочитать задачи реестра")
	}
	file, err := os.OpenFile(*path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("нужен новый доступный файл экспорта задач")
	}
	defer func() { _ = file.Close() }()
	if err := json.NewEncoder(file).Encode(tasks); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	ids := make([]int64, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	if err := postgres.ParkReferenceTasks(ctx, pool, ids); err != nil {
		return errors.New("экспорт сохранён, но парковка задач не завершена; повторить новым файлом")
	}
	_, err = fmt.Fprintf(output, "{\"exported\":%d,\"reason\":\"ownership-transferred\"}\n", len(tasks))
	return err
}
