package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkReadFileSmall(b *testing.B) {
	root := b.TempDir()
	if err := os.WriteFile(filepath.Join(root, "small.txt"), []byte("alpha\nbeta\ngamma\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	r := NewRunner(root, 64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res := r.Execute(Call{Tool: ToolReadFile, Path: "small.txt"})
		if res.Err != nil {
			b.Fatal(res.Err)
		}
	}
}

func BenchmarkReadFileLarge(b *testing.B) {
	root := b.TempDir()
	var content strings.Builder
	for i := 0; i < 100_000; i++ {
		fmt.Fprintf(&content, "line %d: bounded benchmark payload\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(content.String()), 0o600); err != nil {
		b.Fatal(err)
	}
	r := NewRunner(root, 64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res := r.Execute(Call{Tool: ToolReadFile, Path: "large.txt", Offset: 90_000, Limit: 50})
		if res.Err != nil {
			b.Fatal(res.Err)
		}
	}
}

func BenchmarkReadFileRanged(b *testing.B) {
	root := b.TempDir()
	var content strings.Builder
	for i := 0; i < 20_000; i++ {
		fmt.Fprintf(&content, "line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, "ranged.txt"), []byte(content.String()), 0o600); err != nil {
		b.Fatal(err)
	}
	r := NewRunner(root, 64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res := r.Execute(Call{Tool: ToolReadFile, Path: "ranged.txt", Offset: 10_000, Limit: 20})
		if res.Err != nil {
			b.Fatal(res.Err)
		}
	}
}
