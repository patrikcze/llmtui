package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/patrikcze/llmtui/internal/config"
)

func TestDiskEntityStorageFallsBackToMemoryWhenRootUnsupported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file-root")
	if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, warning := newEntityRegistry(&config.Config{Entities: config.EntitiesConfig{
		OutputStorage: "disk", OutputStoragePath: path, MaxOutputBytes: 1024, MaxTotalOutputBytes: 2048,
	}})
	if registry == nil || warning == "" {
		t.Fatalf("registry=%v warning=%q, want memory fallback warning", registry, warning)
	}
	if status := registry.DiskStorageStatus(); status.SessionDir != "" {
		t.Fatalf("fallback unexpectedly uses disk: %+v", status)
	}
}
