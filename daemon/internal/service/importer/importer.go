package importer

import (
	"embed"
	"io/fs"
	"sort"
	"strings"

	"openyou/daemon/internal/infra/config"
)

//go:embed userdata
var seedFS embed.FS

// SeedFS exposes the embedded userdata FS so the matcher can seed docs on first boot.
var SeedFS fs.FS = seedFS

type ImportResult struct {
	Skipped bool
}

// importedFlagKey marks that the initial import has run, so it happens exactly once.
const importedFlagKey = "context_imported"

// Import sets the once-only flag; data stays file-backed (no DB imports).
func Import(cStore *config.Store) (*ImportResult, error) {
	alreadyImported, _ := cStore.Get(importedFlagKey)
	if alreadyImported == "true" {
		return &ImportResult{Skipped: true}, nil
	}
	if err := cStore.Set(importedFlagKey, "true"); err != nil {
		return &ImportResult{}, err
	}
	return &ImportResult{}, nil
}

func readDirMerged(seed fs.FS, dir string) (string, error) {
	entries, err := fs.ReadDir(seed, dir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		data, err := fs.ReadFile(seed, dir+"/"+n)
		if err != nil {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.TrimSpace(string(data)))
	}
	return b.String(), nil
}
