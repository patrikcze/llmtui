package llamart

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ardanlabs/jinja"
	"github.com/hybridgroup/yzma/pkg/llama"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

type nativeTemplateRenderer func(string, []llama.ChatMessage) (string, error)

type renderedPrompt struct {
	text            string
	startsReasoning bool
}

func applyTemplate(template string, messages []llama.ChatMessage) (string, error) {
	probe := make([]byte, 1)
	required := llama.ChatApplyTemplate(template, messages, true, probe)
	if required <= 0 {
		return "", errors.New("apply model chat template: template is invalid or unsupported")
	}

	buf := make([]byte, int(required)+1)
	written := llama.ChatApplyTemplate(template, messages, true, buf)
	if written <= 0 {
		return "", errors.New("apply model chat template: template rendering failed")
	}
	if int(written) > len(buf) {
		buf = make([]byte, int(written)+1)
		written = llama.ChatApplyTemplate(template, messages, true, buf)
	}
	if written <= 0 || int(written) > len(buf) {
		return "", fmt.Errorf("apply model chat template: invalid rendered length %d", written)
	}
	return string(buf[:written]), nil
}

// renderChatTemplate uses llama.cpp's fast native renderer when the request
// needs no extended Jinja variables. If llama.cpp does not recognize a valid
// model template (notably Gemma 4), it falls back to the complete Jinja
// implementation used by yzma.
func renderChatTemplate(
	template string,
	messages []provider.Message,
	tools []provider.ToolSpec,
	reasoning string,
	native nativeTemplateRenderer,
) (renderedPrompt, error) {
	mode := strings.ToLower(strings.TrimSpace(reasoning))
	auto := mode == "" || mode == "auto"

	var nativeErr error
	if auto && len(tools) == 0 && nativeCompatibleMessages(messages) {
		nativeMessages, err := chatMessages(messages)
		if err == nil {
			text, err := native(template, nativeMessages)
			if err == nil {
				return newRenderedPrompt(text), nil
			}
			nativeErr = err
		}
	}

	data, err := jinjaTemplateData(messages, tools, mode)
	if err != nil {
		return renderedPrompt{}, err
	}
	compiled, err := jinja.Compile(template)
	if err != nil {
		return renderedPrompt{}, templateRenderError(nativeErr, fmt.Errorf("compile Jinja template: %w", err))
	}
	text, err := compiled.Render(data)
	if err != nil {
		return renderedPrompt{}, templateRenderError(nativeErr, fmt.Errorf("render Jinja template: %w", err))
	}
	return newRenderedPrompt(text), nil
}

func nativeCompatibleMessages(messages []provider.Message) bool {
	for _, message := range messages {
		if len(message.Images) > 0 || len(message.ToolCalls) > 0 || message.Role == provider.RoleTool ||
			message.ToolCallID != "" || message.ToolName != "" {
			return false
		}
	}
	return true
}

func newRenderedPrompt(text string) renderedPrompt {
	trimmed := strings.TrimSpace(text)
	return renderedPrompt{
		text: text,
		startsReasoning: strings.HasSuffix(trimmed, "<think>") ||
			strings.HasSuffix(trimmed, "<|channel>thought") ||
			strings.HasSuffix(trimmed, "<channel>thought"),
	}
}

func templateRenderError(nativeErr, jinjaErr error) error {
	advice := errors.New("set providers.<name>.chat_template to a template supported by this model")
	if nativeErr == nil {
		return errors.Join(errors.New("apply model chat template"), jinjaErr, advice)
	}
	return errors.Join(
		fmt.Errorf("llama.cpp template renderer: %w", nativeErr),
		fmt.Errorf("jinja fallback: %w", jinjaErr),
		advice,
	)
}

func jinjaTemplateData(messages []provider.Message, tools []provider.ToolSpec, reasoning string) (map[string]any, error) {
	if len(messages) == 0 {
		return nil, errors.New("chat request has no messages")
	}
	result := map[string]any{
		"messages":              templateMessages(messages),
		"add_generation_prompt": true,
	}
	switch reasoning {
	case "", "auto":
		// Omission is intentional: model templates can distinguish their own
		// default from an explicit enable/disable request.
	case "on":
		result["enable_thinking"] = true
	case "off":
		result["enable_thinking"] = false
	default:
		return nil, fmt.Errorf("invalid reasoning mode %q (supported: auto, on, off)", reasoning)
	}
	if len(tools) > 0 {
		mapped, err := templateTools(tools)
		if err != nil {
			return nil, err
		}
		result["tools"] = mapped
	}
	return result, nil
}

func templateMessages(messages []provider.Message) []any {
	result := make([]any, 0, len(messages))
	for _, message := range messages {
		mapped := map[string]any{
			"role":    string(message.Role),
			"content": message.Content,
		}
		if len(message.Images) > 0 {
			content := make([]any, 0, len(message.Images)+1)
			for range message.Images {
				content = append(content, map[string]any{"type": "image"})
			}
			if message.Content != "" {
				content = append(content, map[string]any{"type": "text", "text": message.Content})
			}
			mapped["content"] = content
		}
		if len(message.ToolCalls) > 0 {
			calls := make([]any, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				calls = append(calls, map[string]any{
					"id":   call.ID,
					"type": "function",
					"function": map[string]any{
						"name":      call.Name,
						"arguments": jsonValue(call.Arguments),
					},
				})
			}
			mapped["tool_calls"] = calls
		}
		if message.ToolCallID != "" {
			mapped["tool_call_id"] = message.ToolCallID
		}
		if message.ToolName != "" {
			mapped["name"] = message.ToolName
		}
		result = append(result, mapped)
	}
	return result
}

func templateTools(tools []provider.ToolSpec) ([]any, error) {
	result := make([]any, 0, len(tools))
	for _, tool := range tools {
		parameters, err := decodeJSON(tool.Parameters)
		if err != nil {
			return nil, fmt.Errorf("tool %q parameters are not valid JSON: %w", tool.Name, err)
		}
		result = append(result, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  parameters,
			},
		})
	}
	return result, nil
}

func jsonValue(raw string) any {
	value, err := decodeJSON([]byte(raw))
	if err != nil {
		return raw
	}
	return value
}

func decodeJSON(raw []byte) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func generationBudget(promptTokens, requested, contextSize int) (int, error) {
	if promptTokens >= contextSize {
		return 0, fmt.Errorf(
			"prompt uses %d tokens but the context holds %d; raise context_size or shorten the conversation",
			promptTokens,
			contextSize,
		)
	}
	remaining := contextSize - promptTokens
	if requested < 0 {
		return 0, fmt.Errorf("max_tokens must not be negative: %d", requested)
	}
	if requested == 0 {
		return remaining, nil
	}
	if requested > remaining {
		return 0, fmt.Errorf(
			"prompt (%d tokens) plus max_tokens (%d) exceeds the %d-token context; raise context_size, lower max_tokens, or shorten the conversation",
			promptTokens,
			requested,
			contextSize,
		)
	}
	return requested, nil
}

func (r *Runtime) preparePrompt(prompt []llama.Token) ([]llama.Token, error) {
	if len(r.kvTokens) == 0 {
		return slices.Clone(prompt), nil
	}

	prefix := commonPrefix(r.kvTokens, prompt)
	// If the complete new prompt is already cached, re-decode its final token
	// so the logits used by the fresh sampler definitely correspond to it.
	if prefix == len(prompt) && prefix > 0 {
		prefix--
	}

	if prefix == 0 {
		if err := llama.MemoryClear(r.mem, true); err != nil {
			return nil, fmt.Errorf("clear model memory: %w", err)
		}
		r.kvTokens = []llama.Token{}
		return slices.Clone(prompt), nil
	}

	if prefix < len(r.kvTokens) {
		removed, err := llama.MemorySeqRm(r.mem, 0, llama.Pos(prefix), -1)
		if err != nil || !removed {
			trimErr := err
			if trimErr == nil {
				trimErr = errors.New("llama.cpp rejected partial memory removal")
			}
			if clearErr := llama.MemoryClear(r.mem, true); clearErr != nil {
				return nil, errors.Join(
					fmt.Errorf("trim model memory at token %d: %w", prefix, trimErr),
					fmt.Errorf("clear model memory after trim failure: %w", clearErr),
				)
			}
			r.kvTokens = []llama.Token{}
			return slices.Clone(prompt), nil
		}
		r.kvTokens = slices.Clone(r.kvTokens[:prefix])
	}
	return slices.Clone(prompt[prefix:]), nil
}

func commonPrefix(left, right []llama.Token) int {
	limit := min(len(left), len(right))
	for index := range limit {
		if left[index] != right[index] {
			return index
		}
	}
	return limit
}

func (r *Runtime) decodePrompt(
	ctx context.Context,
	pending []llama.Token,
	total int,
	progress func(string),
) error {
	processed := total - len(pending)
	for len(pending) > 0 {
		chunkSize := min(r.batchSize, len(pending))
		chunk := pending[:chunkSize]
		emitProgress(progress, fmt.Sprintf("processing prompt %d/%d", processed+chunkSize, total))
		if err := r.decode(ctx, chunk); err != nil {
			return err
		}
		r.kvTokens = append(r.kvTokens, chunk...)
		processed += chunkSize
		pending = pending[chunkSize:]
	}
	return nil
}

func (r *Runtime) decode(ctx context.Context, tokens []llama.Token) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	code, err := llama.Decode(r.lctx, llama.BatchGetOne(tokens))
	if err != nil {
		return fmt.Errorf("decode tokens: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("decode tokens: llama.cpp returned status %d", code)
	}
	return nil
}

func (r *Runtime) newSampler(req embedded.GenRequest) (llama.Sampler, error) {
	chain := llama.SamplerChainInit(llama.SamplerChainDefaultParams())
	if chain == 0 {
		return 0, errors.New("initialize sampler chain")
	}

	add := func(name string, sampler llama.Sampler) error {
		if sampler == 0 {
			return fmt.Errorf("initialize %s sampler", name)
		}
		llama.SamplerChainAdd(chain, sampler)
		return nil
	}
	fail := func(err error) (llama.Sampler, error) {
		llama.SamplerFree(chain)
		return 0, err
	}

	if req.Temperature <= 0 {
		if err := add("greedy", llama.SamplerInitGreedy()); err != nil {
			return fail(err)
		}
		return chain, nil
	}

	if r.opts.Sampling.RepeatPenalty > 0 {
		if err := add("penalties", llama.SamplerInitPenalties(
			int32(r.opts.Sampling.RepeatLastN),
			float32(r.opts.Sampling.RepeatPenalty),
			0,
			0,
		)); err != nil {
			return fail(err)
		}
	}
	if r.opts.Sampling.TopK > 0 {
		if err := add("top-k", llama.SamplerInitTopK(int32(r.opts.Sampling.TopK))); err != nil {
			return fail(err)
		}
	}
	if req.TopP > 0 && req.TopP < 1 {
		if err := add("top-p", llama.SamplerInitTopP(float32(req.TopP), 1)); err != nil {
			return fail(err)
		}
	}
	if r.opts.Sampling.MinP > 0 {
		if err := add("min-p", llama.SamplerInitMinP(float32(r.opts.Sampling.MinP), 1)); err != nil {
			return fail(err)
		}
	}
	if err := add("temperature", llama.SamplerInitTempExt(float32(req.Temperature), 0, 1)); err != nil {
		return fail(err)
	}
	seed := r.opts.Sampling.Seed
	if seed == 0 {
		seed = llama.DefaultSeed
	}
	if err := add("distribution", llama.SamplerInitDist(seed)); err != nil {
		return fail(err)
	}
	return chain, nil
}

func tokenPiece(vocab llama.Vocab, token llama.Token) ([]byte, error) {
	buf := make([]byte, tokenPieceBufSize)
	written := llama.TokenToPiece(vocab, token, buf, 0, false)
	if written < 0 {
		buf = make([]byte, int(-written))
		written = llama.TokenToPiece(vocab, token, buf, 0, false)
	}
	if written < 0 || int(written) > len(buf) {
		return nil, fmt.Errorf("convert token %d to text: invalid length %d", token, written)
	}
	return slices.Clone(buf[:written]), nil
}
