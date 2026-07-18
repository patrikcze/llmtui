package llamart

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	goruntime "runtime"
	"strings"

	"github.com/hybridgroup/yzma/pkg/llama"
	"github.com/hybridgroup/yzma/pkg/mtmd"

	"github.com/patrikcze/llmtui/internal/provider"
)

const (
	maxVisionImages        = 8
	maxVisionImageBytes    = 20 << 20
	maxVisionTotalBytes    = 64 << 20
	maxVisionDimension     = 8192
	maxVisionDecodedPixels = 40_000_000
)

type visionNative struct {
	bitmapInit       func(mtmd.Context, *byte, uint64, bool) mtmd.BitmapWrapper
	bitmapFree       func(mtmd.Bitmap)
	chunksInit       func() mtmd.InputChunks
	chunksFree       func(mtmd.InputChunks)
	tokenize         func(mtmd.Context, mtmd.InputChunks, *mtmd.InputText, []mtmd.Bitmap) int32
	chunksSize       func(mtmd.InputChunks) uint64
	chunkGet         func(mtmd.InputChunks, uint64) mtmd.InputChunk
	chunkTokens      func(mtmd.InputChunk) uint64
	chunkPositions   func(mtmd.InputChunk) llama.Pos
	helperEvalChunks func(mtmd.Context, llama.Context, mtmd.InputChunks, llama.Pos, llama.SeqId, int32, bool, *llama.Pos) int32
	memoryClear      func(llama.Memory, bool) error
	marker           func(mtmd.Context) string
}

func defaultVisionNative() visionNative {
	return visionNative{
		bitmapInit:       mtmd.BitmapInitFromBuf,
		bitmapFree:       mtmd.BitmapFree,
		chunksInit:       mtmd.InputChunksInit,
		chunksFree:       mtmd.InputChunksFree,
		tokenize:         mtmd.Tokenize,
		chunksSize:       mtmd.InputChunksSize,
		chunkGet:         mtmd.InputChunksGet,
		chunkTokens:      mtmd.InputChunkGetNTokens,
		chunkPositions:   mtmd.InputChunkGetNPos,
		helperEvalChunks: mtmd.HelperEvalChunks,
		memoryClear:      llama.MemoryClear,
		marker:           mtmd.GetMarker,
	}
}

type visionPromptResult struct {
	promptTokens int
	positions    int
	maxNew       int
	nextPosition llama.Pos
}

func collectAndValidateImages(messages []provider.Message) ([]provider.Image, error) {
	count := 0
	for _, message := range messages {
		count += len(message.Images)
		if count > maxVisionImages {
			return nil, fmt.Errorf("vision request has %d images; the limit is %d", count, maxVisionImages)
		}
	}
	if count == 0 {
		return nil, nil
	}

	images := make([]provider.Image, 0, count)
	totalBytes := 0
	for _, message := range messages {
		for _, attachment := range message.Images {
			index := len(images) + 1
			if len(attachment.Data) == 0 {
				return nil, fmt.Errorf("image %d is empty", index)
			}
			if len(attachment.Data) > maxVisionImageBytes {
				return nil, fmt.Errorf("image %d is %d bytes; the per-image limit is %d MiB", index, len(attachment.Data), maxVisionImageBytes>>20)
			}
			if totalBytes > maxVisionTotalBytes-len(attachment.Data) {
				return nil, fmt.Errorf("vision attachments exceed the %d MiB total-byte limit", maxVisionTotalBytes>>20)
			}
			totalBytes += len(attachment.Data)

			declared := strings.ToLower(strings.TrimSpace(strings.SplitN(attachment.MIME, ";", 2)[0]))
			if declared != "image/png" && declared != "image/jpeg" && declared != "image/jpg" {
				return nil, fmt.Errorf("image %d declares unsupported MIME %q; supported formats are image/png and image/jpeg", index, attachment.MIME)
			}
			config, format, err := image.DecodeConfig(bytes.NewReader(attachment.Data))
			if err != nil {
				return nil, fmt.Errorf("image %d is not a valid PNG or JPEG: %w", index, err)
			}
			detectedMIME := "image/" + format
			if format == "jpeg" && declared == "image/jpg" {
				declared = "image/jpeg"
			}
			if detectedMIME != declared || (format != "png" && format != "jpeg") {
				return nil, fmt.Errorf("image %d MIME %q does not match detected %s data", index, attachment.MIME, detectedMIME)
			}
			if config.Width <= 0 || config.Height <= 0 || config.Width > maxVisionDimension || config.Height > maxVisionDimension {
				return nil, fmt.Errorf("image %d dimensions %dx%d exceed the %d-pixel-per-dimension limit", index, config.Width, config.Height, maxVisionDimension)
			}
			pixels := int64(config.Width) * int64(config.Height)
			if pixels > maxVisionDecodedPixels {
				return nil, fmt.Errorf("image %d has %d decoded pixels; the limit is %d", index, pixels, maxVisionDecodedPixels)
			}
			images = append(images, attachment)
		}
	}
	return images, nil
}

func injectVisionMarkers(messages []provider.Message, marker string) []provider.Message {
	result := append([]provider.Message(nil), messages...)
	for index := range result {
		count := len(result[index].Images)
		if count == 0 {
			continue
		}
		result[index].Content = strings.Repeat(marker, count) + result[index].Content
		result[index].Images = nil
	}
	return result
}

func (r *Runtime) evaluateVisionPrompt(
	ctx context.Context,
	prompt string,
	images []provider.Image,
	requestedTokens int,
	progress func(string),
) (result visionPromptResult, err error) {
	if r.mctx == 0 {
		return result, errors.New("image messages require providers.<name>.mmproj_path with a projector matching the model")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	bitmaps := make([]mtmd.Bitmap, 0, len(images))
	defer func() {
		for _, bitmap := range bitmaps {
			r.vision.bitmapFree(bitmap)
		}
	}()
	for index := range images {
		data := images[index].Data
		wrapper := r.vision.bitmapInit(r.mctx, &data[0], uint64(len(data)), false)
		goruntime.KeepAlive(data)
		if wrapper.Bitmap == 0 {
			return result, fmt.Errorf("preprocess image %d: mtmd could not create a bitmap", index+1)
		}
		bitmaps = append(bitmaps, wrapper.Bitmap)
	}

	chunks := r.vision.chunksInit()
	if chunks == 0 {
		return result, errors.New("initialize mtmd input chunks")
	}
	defer r.vision.chunksFree(chunks)

	input := mtmd.NewInputText(prompt, true, true)
	status := r.vision.tokenize(r.mctx, chunks, input, bitmaps)
	goruntime.KeepAlive(prompt)
	goruntime.KeepAlive(images)
	if status != 0 {
		switch status {
		case 1:
			return result, fmt.Errorf("tokenize multimodal prompt: %d image markers do not match %d image attachments", strings.Count(prompt, r.vision.marker(r.mctx)), len(images))
		case 2:
			return result, errors.New("tokenize multimodal prompt: mtmd image preprocessing failed")
		default:
			return result, fmt.Errorf("tokenize multimodal prompt: mtmd returned status %d", status)
		}
	}

	promptTokens, positions, err := visionChunkUsage(r.vision, chunks)
	if err != nil {
		return result, err
	}
	maxNew, err := generationBudgetWithPositions(promptTokens, positions, requestedTokens, r.nCtx)
	if err != nil {
		return result, err
	}
	result.promptTokens = promptTokens
	result.positions = positions
	result.maxNew = maxNew

	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := r.vision.memoryClear(r.mem, true); err != nil {
		return result, fmt.Errorf("clear model memory before multimodal prompt: %w", err)
	}
	r.kvTokens = []llama.Token{}
	r.kvContaminated = true
	emitProgress(progress, fmt.Sprintf("processing multimodal prompt (%d images, %d positions)", len(images), positions))
	var next llama.Pos
	status = r.vision.helperEvalChunks(r.mctx, r.lctx, chunks, 0, 0, int32(r.batchSize), true, &next)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if status != 0 {
		return result, fmt.Errorf("evaluate multimodal prompt: mtmd returned status %d", status)
	}
	if next < 0 || int64(next) > int64(r.nCtx) {
		return result, fmt.Errorf("evaluate multimodal prompt returned invalid next position %d for context size %d", next, r.nCtx)
	}
	if int64(next)+int64(maxNew) > int64(r.nCtx) {
		return result, fmt.Errorf("multimodal prompt evaluation ended at position %d, leaving insufficient context for max_tokens %d in a %d-position context", next, maxNew, r.nCtx)
	}
	result.nextPosition = next
	return result, nil
}

func visionChunkUsage(native visionNative, chunks mtmd.InputChunks) (tokens, positions int, err error) {
	var tokenTotal uint64
	var positionTotal int64
	for index := uint64(0); index < native.chunksSize(chunks); index++ {
		chunk := native.chunkGet(chunks, index)
		if chunk == 0 {
			return 0, 0, fmt.Errorf("mtmd returned an invalid input chunk at index %d", index)
		}
		chunkTokens := native.chunkTokens(chunk)
		if math.MaxUint64-tokenTotal < chunkTokens {
			return 0, 0, errors.New("multimodal prompt token count overflow")
		}
		tokenTotal += chunkTokens
		chunkPositions := int64(native.chunkPositions(chunk))
		if chunkPositions < 0 || math.MaxInt64-positionTotal < chunkPositions {
			return 0, 0, errors.New("multimodal prompt position count overflow")
		}
		positionTotal += chunkPositions
	}
	maxInt := uint64(^uint(0) >> 1)
	if tokenTotal > maxInt || positionTotal > int64(maxInt) {
		return 0, 0, errors.New("multimodal prompt size exceeds this platform's integer range")
	}
	return int(tokenTotal), int(positionTotal), nil
}

func generationBudgetWithPositions(promptTokens, promptPositions, requested, contextSize int) (int, error) {
	if promptTokens <= 0 || promptPositions <= 0 {
		return 0, fmt.Errorf("multimodal prompt produced invalid usage: %d tokens across %d positions", promptTokens, promptPositions)
	}
	if promptPositions >= contextSize {
		return 0, fmt.Errorf("multimodal prompt uses %d tokens across %d positions but the context holds %d; raise context_size or shorten the conversation", promptTokens, promptPositions, contextSize)
	}
	remaining := contextSize - promptPositions
	if requested < 0 {
		return 0, fmt.Errorf("max_tokens must not be negative: %d", requested)
	}
	if requested == 0 {
		return remaining, nil
	}
	if requested > remaining {
		return 0, fmt.Errorf("multimodal prompt (%d tokens, %d positions) plus max_tokens (%d) exceeds the %d-position context; raise context_size, lower max_tokens, or shorten the conversation", promptTokens, promptPositions, requested, contextSize)
	}
	return requested, nil
}

func (r *Runtime) decodeAt(ctx context.Context, token llama.Token, position llama.Pos) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	batch := llama.BatchGetOne([]llama.Token{token})
	batch.Pos = &position
	code, err := llama.Decode(r.lctx, batch)
	goruntime.KeepAlive(position)
	if err != nil {
		return fmt.Errorf("decode multimodal continuation token: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("decode multimodal continuation token: llama.cpp returned status %d", code)
	}
	return nil
}
