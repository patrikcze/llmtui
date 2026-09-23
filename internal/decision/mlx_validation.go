package decision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MLX already calibrates and rounds answers. Never call DecodeLogits here.
func convertMLXAnswers(questions map[string]Question, answers map[string]mlxAnswer) (Result, error) {
	result := Result{Answers: make(map[string]Answer, len(answers))}
	for id, raw := range answers {
		if raw.Confidence == nil {
			return Result{}, errors.New("missing confidence")
		}
		answer := Answer{Type: raw.Type, Confidence: *raw.Confidence, Probabilities: raw.Probabilities}
		switch raw.Type {
		case QuestionChoice:
			if raw.Choice == nil || raw.Probabilities == nil {
				return Result{}, errors.New("missing choice or probabilities")
			}
			answer.Choice = *raw.Choice
			if _, ok := raw.Probabilities[answer.Choice]; !ok {
				return Result{}, errors.New("choice absent from probabilities")
			}
			labels := choiceLabels(questions[id])
			if len(labels) != len(raw.Probabilities) {
				return Result{}, errors.New("choice labels mismatch")
			}
			for _, label := range labels {
				if _, ok := raw.Probabilities[label]; !ok {
					return Result{}, errors.New("unknown choice label")
				}
			}
		case QuestionScore:
			if raw.Score == nil || raw.Probabilities == nil {
				return Result{}, errors.New("missing score or probabilities")
			}
			answer.Score = *raw.Score
			criteria, err := RenderOptions(questions[id])
			if err != nil {
				return Result{}, err
			}
			if len(criteria) != len(raw.Probabilities) || answer.Score < 0 || answer.Score > float64(len(criteria)-1) {
				return Result{}, errors.New("score outside rubric")
			}
			for i := range criteria {
				if _, ok := raw.Probabilities[fmt.Sprint(i)]; !ok {
					return Result{}, errors.New("score labels mismatch")
				}
			}
		case QuestionNoul:
			if raw.Noul == nil {
				return Result{}, errors.New("missing noul probability")
			}
			answer.Probability = *raw.Noul
		}
		result.Answers[id] = answer
	}
	return result, ValidateResult(questions, result)
}

func verifyMLXInstallation(ctx context.Context, installation Installation) error {
	fail := func(err error) error {
		return fmt.Errorf("%w: MLX installation: %v", ErrRuntimeArtifactUnavailable, err)
	}
	if !installation.Valid || installation.Manifest.Runtime.Format != RuntimeFormatMLX {
		return fail(errors.New("invalid format or installation"))
	}
	if err := validateRuntimeArtifact(installation.Manifest.Runtime); err != nil {
		return fail(err)
	}
	root, err := filepath.Abs(installation.Path)
	if err != nil {
		return fail(err)
	}
	// Resolve the store's ancestors (e.g. macOS /tmp), but never a symlink at
	// the installation itself or inside its model inputs.
	info, err := os.Lstat(root)
	if err != nil {
		return fail(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fail(errors.New("model root must be a real directory"))
	}
	required := map[string]bool{"model.safetensors": false, "mlx_config.json": false, "encoder/config.json": false, "rl_agent_config.json": false, "tokenizer/tokenizer.json": false, "tokenizer/tokenizer_config.json": false}
	seen := make(map[string]bool)
	for _, file := range installation.Manifest.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		path, err := safeJoin(root, file.Path)
		if err != nil {
			return fail(err)
		}
		if seen[file.Path] || !isSHA256(file.SHA256) || file.Size <= 0 {
			return fail(errors.New("invalid file metadata"))
		}
		seen[file.Path] = true
		if err := rejectSymlinkChain(path, root); err != nil {
			return fail(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fail(err)
		}
		if !info.Mode().IsRegular() {
			return fail(errors.New("model input must be a regular file"))
		}
		if err := verifyFile(path, file.Size, file.SHA256); err != nil {
			return fail(err)
		}
		if _, ok := required[file.Path]; ok {
			required[file.Path] = true
		}
	}
	for path, exists := range required {
		if !exists {
			return fail(fmt.Errorf("missing verified %s", path))
		}
	}
	// The tokenizer reads optional files too: reject unverified inputs instead
	// of allowing a new file to silently change inference.
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink in checkpoint")
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if (strings.HasPrefix(rel, "tokenizer/") || strings.HasPrefix(rel, "encoder/")) && !seen[rel] {
			return fmt.Errorf("unverified model input %s", rel)
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	return nil
}
