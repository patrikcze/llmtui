package decision

import (
	"fmt"
	"strings"
)

// ModelDescriptor is the application-facing description of one decision
// checkpoint. Source layout is kept here so callers use stable aliases rather
// than Hugging Face directory names.
type ModelDescriptor struct {
	Alias         string
	Repository    string
	Subdirectory  string
	Description   string
	RuntimeFormat string
	RuntimeNote   string
}

// LayaModels describes the original SafeTensors checkpoints published by the
// upstream Laya package. It is intentionally data, not a set of hard-coded
// paths spread through the CLI or engine.
var LayaModels = []ModelDescriptor{
	{Alias: "english", Repository: "convaiinnovations/laya", Description: "Laya English decision checkpoint", RuntimeFormat: RuntimeFormatSafeTensors, RuntimeNote: "source checkpoint only; no verified Go runtime artifact is installed"},
	{Alias: "multilingual", Repository: "convaiinnovations/laya", Subdirectory: "multilingual", Description: "Laya multilingual decision checkpoint", RuntimeFormat: RuntimeFormatSafeTensors, RuntimeNote: "source checkpoint only; no verified Go runtime artifact is installed"},
	{Alias: "typed-decisions", Repository: "convaiinnovations/laya", Subdirectory: "typed-decisions", Description: "Laya typed-decisions checkpoint", RuntimeFormat: RuntimeFormatSafeTensors, RuntimeNote: "source checkpoint only; no verified Go runtime artifact is installed"},
}

// LayaMLXModels are independently published FP16 conversions for the MLX
// Python runtime on Apple Silicon. They are downloadable and checksummed by
// this manager, but remain unavailable to Go until an explicit MLX bridge is
// configured.
var LayaMLXModels = []ModelDescriptor{
	{Alias: "english-mlx", Repository: "aac6fef/laya-mlx", Description: "Laya English MLX checkpoint", RuntimeFormat: RuntimeFormatMLX, RuntimeNote: "MLX checkpoint acquired; requires the external laya-mlx Python runtime"},
	{Alias: "multilingual-mlx", Repository: "aac6fef/laya-multilingual-mlx", Description: "Laya multilingual MLX checkpoint", RuntimeFormat: RuntimeFormatMLX, RuntimeNote: "MLX checkpoint acquired; requires the external laya-mlx Python runtime"},
	{Alias: "typed-decisions-mlx", Repository: "aac6fef/laya-typed-decisions-mlx", Description: "Laya typed-decisions MLX checkpoint", RuntimeFormat: RuntimeFormatMLX, RuntimeNote: "MLX checkpoint acquired; requires the external laya-mlx Python runtime"},
}

func defaultModelCatalog() []ModelDescriptor {
	catalog := make([]ModelDescriptor, 0, len(LayaModels)+len(LayaMLXModels))
	catalog = append(catalog, LayaModels...)
	catalog = append(catalog, LayaMLXModels...)
	return catalog
}

func (descriptor ModelDescriptor) runtimeFormat() string {
	if descriptor.RuntimeFormat == "" {
		return RuntimeFormatSafeTensors
	}
	return descriptor.RuntimeFormat
}

func (descriptor ModelDescriptor) runtimeNote() string {
	if descriptor.RuntimeNote != "" {
		return descriptor.RuntimeNote
	}
	return "source checkpoint only; no verified Go runtime artifact is installed"
}

func modelID(alias string) string { return "laya:" + alias }

func descriptorForID(id string, catalog []ModelDescriptor) (ModelDescriptor, error) {
	id = strings.TrimSpace(id)
	prefix, alias, ok := strings.Cut(strings.TrimSpace(id), ":")
	if !ok || prefix != "laya" {
		return ModelDescriptor{}, fmt.Errorf("unsupported decision model %q: expected laya:<name>", id)
	}
	for _, descriptor := range catalog {
		if descriptor.Alias == alias {
			return descriptor, nil
		}
	}
	return ModelDescriptor{}, fmt.Errorf("unknown Laya model %q", alias)
}

func validateDescriptor(descriptor ModelDescriptor) error {
	if descriptor.Alias == "" || strings.ContainsAny(descriptor.Alias, `/\\`) || descriptor.Alias == "." || descriptor.Alias == ".." {
		return fmt.Errorf("invalid model alias %q", descriptor.Alias)
	}
	if descriptor.Repository == "" || strings.ContainsAny(descriptor.Repository, `\\`) {
		return fmt.Errorf("invalid model repository %q", descriptor.Repository)
	}
	parts := strings.Split(descriptor.Repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
		return fmt.Errorf("invalid model repository %q", descriptor.Repository)
	}
	if descriptor.Subdirectory != "" && !safeRelativePath(descriptor.Subdirectory) {
		return fmt.Errorf("invalid model subdirectory %q", descriptor.Subdirectory)
	}
	if descriptor.RuntimeFormat != "" && descriptor.RuntimeFormat != RuntimeFormatSafeTensors && descriptor.RuntimeFormat != RuntimeFormatMLX {
		return fmt.Errorf("invalid model runtime format %q", descriptor.RuntimeFormat)
	}
	return nil
}
