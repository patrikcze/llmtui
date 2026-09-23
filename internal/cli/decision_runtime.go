package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/patrikcze/llmtui/internal/decision"
	"github.com/spf13/cobra"
)

func newDecisionRuntimeCmd(r *Root) *cobra.Command {
	var python string
	cmd := &cobra.Command{Use: "runtime", Short: "Inspect explicit decision execution backends"}
	status := &cobra.Command{
		Use: "status mlx", Short: "Probe Python and MLX without loading or downloading a model",
		Long: `Probe the configured Python executable (or python3 on PATH).
Requires macOS arm64, Python 3.11+, and laya-mlx==0.2.0 with Metal.

Install explicitly in an isolated environment, for example:
  python3 -m venv ~/.local/share/llmtui/runtimes/laya-mlx-0.2.0
  ~/.local/share/llmtui/runtimes/laya-mlx-0.2.0/bin/python -m pip install 'laya-mlx==0.2.0'
Then set decision_engine.laya.mlx_python to that environment's Python path.
Normal startup never probes Python, installs packages, or downloads models.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "mlx" {
				return fmt.Errorf("unknown decision runtime %q", args[0])
			}
			if !cmd.Flags().Changed("python") {
				python = r.cfg.DecisionEngine.Laya.MLXPython
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			info, err := (decision.MLXRuntimeLoader{Python: python}).Probe(ctx)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(info)
		},
	}
	status.Flags().StringVar(&python, "python", "", "Python executable path (overrides configuration)")
	cmd.AddCommand(status)
	return cmd
}

// An explicit CLI prediction is opt-in even when chat decision policy is off.
// Keeping this entry point independent makes runtime parity testable before
// decisions can influence the agent loop.
func newDecisionPredictCmd(r *Root) *cobra.Command {
	var input, python string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use: "predict [laya:<model>]", Short: "Run a local typed decision from a JSON request",
		Long: `Read {"state": ..., "questions": {...}} from --input or stdin.
Loads only an installed, verified MLX checkpoint. Results are JSON; no agent
policy or tool approvals are changed. Use an MLX model alias such as
laya:english-mlx, laya:multilingual-mlx, or laya:typed-decisions-mlx.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout <= 0 {
				return fmt.Errorf("--timeout must be positive")
			}
			reader := cmd.InOrStdin()
			if input != "-" {
				f, err := os.Open(input)
				if err != nil {
					return err
				}
				defer f.Close()
				reader = f
			}
			state, questions, err := readDecisionRequest(reader)
			if err != nil {
				return err
			}
			manager, err := newDecisionManager(r)
			if err != nil {
				return err
			}
			model := r.cfg.DecisionEngine.Laya.DefaultModel
			if len(args) > 0 {
				model = args[0]
			}
			if !cmd.Flags().Changed("python") {
				python = r.cfg.DecisionEngine.Laya.MLXPython
			}
			router, err := decision.NewRouter(decision.RouterOptions{Store: manager, Loader: decision.MLXRuntimeLoader{Python: python}, DefaultModel: model, MaxLoaded: r.cfg.DecisionEngine.Laya.MaxLoaded})
			if err != nil {
				return err
			}
			defer func() { _ = router.Close() }()
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			result, err := decision.NewService(true, router).Predict(ctx, state, questions, decision.PredictOptions{})
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
	cmd.Flags().StringVar(&input, "input", "-", "JSON request file (- reads stdin)")
	cmd.Flags().StringVar(&python, "python", "", "Python executable path (overrides configuration)")
	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "maximum load and prediction duration")
	return cmd
}

func readDecisionRequest(reader io.Reader) (any, map[string]decision.Question, error) {
	data, err := io.ReadAll(io.LimitReader(reader, (8<<20)+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > 8<<20 {
		return nil, nil, fmt.Errorf("decision request exceeds 8 MiB")
	}
	var request struct {
		State     json.RawMessage              `json:"state"`
		Questions map[string]decision.Question `json:"questions"`
	}
	if err := json.Unmarshal(data, &request); err != nil {
		return nil, nil, fmt.Errorf("parse decision request: %w", err)
	}
	// json.RawMessage preserves state numbers and unicode across the bridge.
	// JSON choice lists decode as []any; normalize to the public []string form.
	for id, q := range request.Questions {
		if q.Type != decision.QuestionChoice {
			continue
		}
		if values, ok := q.Criteria.([]any); ok {
			labels := make([]string, len(values))
			for i, v := range values {
				label, ok := v.(string)
				if !ok {
					return nil, nil, fmt.Errorf("choice labels must be strings")
				}
				labels[i] = label
			}
			q.Criteria = labels
			request.Questions[id] = q
		}
	}
	var state any
	if len(request.State) > 0 {
		state = request.State
	}
	if err := decision.Validate(state, request.Questions); err != nil {
		return nil, nil, err
	}
	return state, request.Questions, nil
}
