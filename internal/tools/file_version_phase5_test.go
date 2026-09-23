package tools

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/entity"
	"github.com/patrikcze/llmtui/internal/provider"
)

const phase5ResourceID = "ent_" + "a" + "aaaaaaaaaaaaaaaaaaaaaaaaa"

type phase5ResourceReader struct {
	view entity.ResourceView
	err  error
	body []byte
}

func (r *phase5ResourceReader) OpenBody(_ context.Context, _ entity.ID) (entity.ResourceView, entity.BodyLease, error) {
	if r.err != nil {
		return entity.ResourceView{}, nil, r.err
	}
	return r.view, &fakeBodyLease{data: r.body}, nil
}

func TestPhase5VersionedEditRejectsExternalModification(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "before\n")
	r := NewRunner(root, 64)
	read := r.Execute(Call{Tool: ToolReadFile, Path: "f.txt"})
	if read.Err != nil || read.Meta.FileVersion == nil {
		t.Fatalf("read = %+v", read)
	}
	if err := os.WriteFile(root+"/f.txt", []byte("external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := r.Execute(Call{Tool: ToolEditFile, Path: "f.txt", OldText: "before", NewText: "after", ExpectedVersion: read.Meta.FileVersion})
	if res.Err == nil || errorInfoFor(res.Err).Code != "stale_source" {
		t.Fatalf("edit = %+v, want stale_source", res)
	}
	got, _ := os.ReadFile(root + "/f.txt")
	if string(got) != "external\n" {
		t.Fatalf("stale edit changed file to %q", got)
	}
}

func TestPhase5ExpectedResourceIDValidation(t *testing.T) {
	version := &entity.FileVersion{Path: "f.txt", Digest: "digest", SizeBytes: 2, Complete: true}
	for _, tc := range []struct {
		name string
		view entity.ResourceView
		err  error
		want string
	}{
		{name: "wrong kind", view: entity.ResourceView{Kind: entity.KindToolOutput}, want: "wrong_resource_kind"},
		{name: "partial", view: entity.ResourceView{Kind: entity.KindFile, Resource: entity.ResourceMetadata{FileVersion: &entity.FileVersion{Path: "f.txt", Complete: false}}}, want: "snapshot_incomplete"},
		{name: "wrong path", view: entity.ResourceView{Kind: entity.KindFile, Resource: entity.ResourceMetadata{FileVersion: version}}, want: "invalid_arguments"},
		{name: "evicted", err: errors.New("entity body is not present in this session"), want: "resource_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRunner(t.TempDir(), 64)
			r.Resources = &phase5ResourceReader{view: tc.view, err: tc.err}
			path := "f.txt"
			if tc.name == "wrong path" {
				path = "other.txt"
			}
			res := r.Execute(Call{Tool: ToolEditFile, Path: path, OldText: "a", NewText: "b", ExpectedResourceID: phase5ResourceID})
			if res.Err == nil || errorInfoFor(res.Err).Code != tc.want {
				t.Fatalf("result = %+v, want %s", res, tc.want)
			}
		})
	}
}

func TestPhase5WriteSameBytesIsUnchanged(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "same\n")
	r := NewRunner(root, 64)
	read := r.Execute(Call{Tool: ToolReadFile, Path: "f.txt"})
	if read.Err != nil {
		t.Fatal(read.Err)
	}
	res := r.Execute(Call{Tool: ToolWriteFile, Path: "f.txt", Body: "same\n", ExpectedVersion: read.Meta.FileVersion})
	if res.Err != nil {
		t.Fatalf("write = %v", res.Err)
	}
	if res.Meta.Effect != EffectUnchanged {
		t.Fatalf("effect = %q, want unchanged", res.Meta.Effect)
	}
}

func TestPhase5ExpectedResourceIDParsesInBothProtocols(t *testing.T) {
	native := CallsFromNative([]provider.ToolCall{{ID: "w", Name: ToolWriteFile, Arguments: `{"path":"f.txt","content":"x","expected_resource_id":"` + phase5ResourceID + `"}`}})
	if len(native) != 1 || native[0].ExpectedResourceID != phase5ResourceID {
		t.Fatalf("native = %+v", native)
	}
	fenced := Parse("```tool write_file f.txt\n{\"content\":\"x\",\"expected_resource_id\":\"" + phase5ResourceID + "\"}\n```")
	if len(fenced) != 1 || fenced[0].Body != "x" || fenced[0].ExpectedResourceID != phase5ResourceID {
		t.Fatalf("fenced = %+v", fenced)
	}
}

func BenchmarkPhase5VersionedEdit(b *testing.B) {
	root := b.TempDir()
	const original = "package p\n\nconst marker = 1\n"
	if err := os.WriteFile(root+"/f.go", []byte(original), 0o644); err != nil {
		b.Fatal(err)
	}
	r := NewRunner(root, 64)
	for i := 0; i < b.N; i++ {
		if err := os.WriteFile(root+"/f.go", []byte(original), 0o644); err != nil {
			b.Fatal(err)
		}
		read := r.Execute(Call{Tool: ToolReadFile, Path: "f.go"})
		if read.Err != nil {
			b.Fatal(read.Err)
		}
		res := r.Execute(Call{Tool: ToolEditFile, Path: "f.go", OldText: "const marker = 1", NewText: "const marker = 2", ExpectedVersion: read.Meta.FileVersion})
		if res.Err != nil {
			b.Fatal(res.Err)
		}
	}
}

func TestPhase5LegacyRawJSONWriteRemainsCompatible(t *testing.T) {
	calls := Parse("```tool write_file data.json\n{\"hello\":\"world\"}\n```")
	if len(calls) != 1 || calls[0].Body != "{\"hello\":\"world\"}\n" {
		t.Fatalf("call = %+v", calls)
	}
	if strings.TrimSpace(calls[0].ExpectedResourceID) != "" {
		t.Fatalf("unexpected resource ID %q", calls[0].ExpectedResourceID)
	}
}
