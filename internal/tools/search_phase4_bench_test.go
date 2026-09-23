package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func BenchmarkSearchGoSmall(b *testing.B) {
	root := b.TempDir()
	for i := 0; i < 32; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("small-%02d.go", i)), []byte("package p\nconst marker = true\n"), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	r := NewRunner(root, 512)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res := r.Execute(Call{Tool: ToolGrep, Body: "marker", SearchLimit: 100})
		if res.Err != nil {
			b.Fatal(res.Err)
		}
	}
}

func BenchmarkSearchRgOptional(b *testing.B) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		b.Skip("rg is not installed")
	}
	root := b.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large.log"), []byte("marker\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := exec.Command(rg, "-n", "--no-heading", "marker", root).Run(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSearchGoLarge(b *testing.B) {
	root := b.TempDir()
	data := make([]byte, 0, 2<<20)
	for i := 0; i < 100_000; i++ {
		data = append(data, fmt.Sprintf("line %d marker\n", i)...)
	}
	if err := os.WriteFile(filepath.Join(root, "large.log"), data, 0o600); err != nil {
		b.Fatal(err)
	}
	r := NewRunner(root, 4096)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res := r.Execute(Call{Tool: ToolGrep, Body: "marker", SearchLimit: 200})
		if res.Err != nil {
			b.Fatal(res.Err)
		}
	}
}
