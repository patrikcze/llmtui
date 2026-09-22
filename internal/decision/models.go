package decision

import (
	"fmt"
	"strings"
)

// ModelDescriptor is the application-facing description of one decision
// checkpoint. Source layout is kept here so callers use stable aliases rather
// than Hugging Face directory names.
type ModelDescriptor struct {
	Alias        string
	Repository   string
	Subdirectory string
	Description  string
}

// LayaModels describes the checkpoints published by the upstream Laya package
// at the time this integration was started. It is intentionally data, not a
// set of hard-coded paths spread through the CLI or engine.
var LayaModels = []ModelDescriptor{
	{Alias: "english", Repository: "convaiinnovations/laya", Description: "Laya English decision checkpoint"},
	{Alias: "multilingual", Repository: "convaiinnovations/laya", Subdirectory: "multilingual", Description: "Laya multilingual decision checkpoint"},
	{Alias: "typed-decisions", Repository: "convaiinnovations/laya", Subdirectory: "typed-decisions", Description: "Laya typed-decisions checkpoint"},
}

func modelID(alias string) string { return "laya:" + alias }

func descriptorForID(id string, catalog []ModelDescriptor) (ModelDescriptor, error) {
	id = strings.TrimSpace(id)
	if !strings.HasPrefix(id, "laya:") {
		return ModelDescriptor{}, fmt.Errorf("unsupported decision model %q: expected laya:<name>", id)
	}
	alias := strings.TrimPrefix(id, "laya:")
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
	return nil
}
