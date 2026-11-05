package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/proshop-app/proshop-marketplace/internal/infrastructure/taxonomydata"
)

const sourceDirectory = "internal/infrastructure/taxonomydata"

func manifestCommand(args []string, output, diagnostics io.Writer) int {
	options := flag.NewFlagSet("manifest", flag.ContinueOnError)
	options.SetOutput(diagnostics)
	release := options.Int64("release", 0, "номер следующей поставки")
	write := options.Bool("write", false, "записать новый манифест")
	if err := options.Parse(args); err != nil || options.NArg() != 0 {
		return failure(diagnostics, "неверные параметры манифеста", 2)
	}
	explicitRelease := false
	options.Visit(func(f *flag.Flag) {
		if f.Name == "release" {
			explicitRelease = true
		}
	})
	if explicitRelease && *release < 1 {
		return failure(diagnostics, "неверный номер поставки", 2)
	}
	manifest, err := taxonomydata.PrepareManifest(os.DirFS(sourceDirectory), *release, *write)
	if err != nil {
		return failure(diagnostics, err.Error(), 1)
	}
	if *write {
		body, err := yaml.Marshal(manifest)
		if err != nil {
			return failure(diagnostics, err.Error(), 1)
		}
		if err := os.WriteFile(sourceDirectory+"/data/manifest.yaml", body, 0600); err != nil {
			return failure(diagnostics, err.Error(), 1)
		}
	}
	if err := json.NewEncoder(output).Encode(manifest); err != nil {
		return 1
	}
	return 0
}
