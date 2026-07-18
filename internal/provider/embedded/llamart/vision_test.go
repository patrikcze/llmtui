package llamart

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/jpeg"
	"strings"
	"testing"

	"github.com/hybridgroup/yzma/pkg/llama"
	"github.com/hybridgroup/yzma/pkg/mtmd"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

func TestCollectAndValidateImages(t *testing.T) {
	pngData := pngWithDimensions(2, 3)
	var jpegData bytes.Buffer
	if err := jpeg.Encode(&jpegData, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}

	t.Run("valid ordered PNG and JPEG", func(t *testing.T) {
		messages := []provider.Message{
			{Role: provider.RoleUser, Images: []provider.Image{{Data: pngData, MIME: "image/png"}}},
			{Role: provider.RoleUser, Images: []provider.Image{{Data: jpegData.Bytes(), MIME: "image/jpeg"}}},
		}
		images, err := collectAndValidateImages(messages)
		if err != nil || len(images) != 2 || images[0].MIME != "image/png" || images[1].MIME != "image/jpeg" {
			t.Fatalf("images=%+v err=%v", images, err)
		}
	})

	for _, test := range []struct {
		name  string
		image provider.Image
		want  string
	}{
		{name: "empty", image: provider.Image{MIME: "image/png"}, want: "empty"},
		{name: "missing MIME", image: provider.Image{Data: pngData}, want: "unsupported MIME"},
		{name: "unsupported MIME", image: provider.Image{Data: pngData, MIME: "image/gif"}, want: "unsupported MIME"},
		{name: "MIME mismatch", image: provider.Image{Data: pngData, MIME: "image/jpeg"}, want: "does not match"},
		{name: "corrupt", image: provider.Image{Data: []byte("not an image"), MIME: "image/png"}, want: "not a valid PNG or JPEG"},
		{name: "dimension", image: provider.Image{Data: pngWithDimensions(maxVisionDimension+1, 1), MIME: "image/png"}, want: "per-dimension limit"},
		{name: "pixels", image: provider.Image{Data: pngWithDimensions(7000, 6000), MIME: "image/png"}, want: "decoded pixels"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := collectAndValidateImages([]provider.Message{{Images: []provider.Image{test.image}}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}

	t.Run("image count", func(t *testing.T) {
		images := make([]provider.Image, maxVisionImages+1)
		for index := range images {
			images[index] = provider.Image{Data: pngData, MIME: "image/png"}
		}
		_, err := collectAndValidateImages([]provider.Message{{Images: images}})
		if err == nil || !strings.Contains(err.Error(), "limit is 8") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("per-image bytes", func(t *testing.T) {
		large := make([]byte, maxVisionImageBytes+1)
		copy(large, pngData)
		_, err := collectAndValidateImages([]provider.Message{{Images: []provider.Image{{Data: large, MIME: "image/png"}}}})
		if err == nil || !strings.Contains(err.Error(), "per-image limit") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("total bytes", func(t *testing.T) {
		large := make([]byte, 17<<20)
		copy(large, pngData)
		attachments := []provider.Image{
			{Data: large, MIME: "image/png"},
			{Data: large, MIME: "image/png"},
			{Data: large, MIME: "image/png"},
			{Data: large, MIME: "image/png"},
		}
		_, err := collectAndValidateImages([]provider.Message{{Images: attachments}})
		if err == nil || !strings.Contains(err.Error(), "total-byte limit") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestInjectVisionMarkersPreservesMessageAndAttachmentOrder(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleUser, Content: "first", Images: []provider.Image{{Data: []byte{1}}, {Data: []byte{2}}}},
		{Role: provider.RoleAssistant, Content: "answer"},
		{Role: provider.RoleUser, Content: "last", Images: []provider.Image{{Data: []byte{3}}}},
	}
	got := injectVisionMarkers(messages, "<image>")
	if got[0].Content != "<image><image>first" || got[1].Content != "answer" || got[2].Content != "<image>last" {
		t.Errorf("marked messages = %+v", got)
	}
	for _, message := range got {
		if len(message.Images) != 0 {
			t.Errorf("marked message still has images: %+v", message)
		}
	}
	if messages[0].Content != "first" || len(messages[0].Images) != 2 {
		t.Errorf("input messages mutated: %+v", messages)
	}
}

func TestEvaluateVisionPromptUsageCleanupAndKVState(t *testing.T) {
	freedBitmaps := 0
	freedChunks := 0
	cleared := 0
	runtime := &Runtime{mctx: 1, lctx: 2, mem: 3, nCtx: 100, batchSize: 16, kvTokens: []llama.Token{1, 2}}
	runtime.vision = fakeVisionNative(
		[]uint64{5, 20, 7},
		[]llama.Pos{5, 4, 7},
		func() { freedBitmaps++ },
		func() { freedChunks++ },
		func() { cleared++ },
	)

	result, err := runtime.evaluateVisionPrompt(
		context.Background(),
		"<image>describe",
		[]provider.Image{{Data: []byte{1, 2, 3}, MIME: "image/png"}},
		10,
		nil,
	)
	if err != nil {
		t.Fatalf("evaluateVisionPrompt: %v", err)
	}
	if result.promptTokens != 32 || result.positions != 16 || result.maxNew != 10 || result.nextPosition != 16 {
		t.Errorf("result = %+v", result)
	}
	if freedBitmaps != 1 || freedChunks != 1 || cleared != 1 {
		t.Errorf("cleanup bitmaps=%d chunks=%d clears=%d", freedBitmaps, freedChunks, cleared)
	}
	if !runtime.kvContaminated || len(runtime.kvTokens) != 0 {
		t.Errorf("KV state contaminated=%t tokens=%v", runtime.kvContaminated, runtime.kvTokens)
	}
}

func TestEvaluateVisionPromptErrorsStillCleanUp(t *testing.T) {
	for _, test := range []struct {
		name           string
		tokenizeStatus int32
		evalStatus     int32
		positions      []llama.Pos
		want           string
	}{
		{name: "marker mismatch", tokenizeStatus: 1, want: "markers do not match"},
		{name: "preprocess", tokenizeStatus: 2, want: "preprocessing failed"},
		{name: "context overflow", positions: []llama.Pos{100}, want: "context holds"},
		{name: "evaluation", evalStatus: 7, positions: []llama.Pos{1}, want: "status 7"},
	} {
		t.Run(test.name, func(t *testing.T) {
			freedBitmaps := 0
			freedChunks := 0
			native := fakeVisionNative([]uint64{10}, test.positions, func() { freedBitmaps++ }, func() { freedChunks++ }, func() {})
			native.tokenize = func(mtmd.Context, mtmd.InputChunks, *mtmd.InputText, []mtmd.Bitmap) int32 { return test.tokenizeStatus }
			native.helperEvalChunks = func(_ mtmd.Context, _ llama.Context, _ mtmd.InputChunks, _ llama.Pos, _ llama.SeqId, _ int32, _ bool, next *llama.Pos) int32 {
				*next = 1
				return test.evalStatus
			}
			runtime := &Runtime{mctx: 1, lctx: 2, mem: 3, nCtx: 100, batchSize: 16, vision: native}
			_, err := runtime.evaluateVisionPrompt(context.Background(), "<image>", []provider.Image{{Data: []byte{1}}}, 1, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
			if freedBitmaps != 1 || freedChunks != 1 {
				t.Errorf("cleanup bitmaps=%d chunks=%d", freedBitmaps, freedChunks)
			}
		})
	}
}

func TestEvaluateVisionPromptBitmapFailureDoesNotLeak(t *testing.T) {
	chunksInitialized := 0
	native := fakeVisionNative(nil, nil, func() {}, func() {}, func() {})
	native.bitmapInit = func(mtmd.Context, *byte, uint64, bool) mtmd.BitmapWrapper { return mtmd.BitmapWrapper{} }
	native.chunksInit = func() mtmd.InputChunks { chunksInitialized++; return 1 }
	runtime := &Runtime{mctx: 1, vision: native}
	_, err := runtime.evaluateVisionPrompt(context.Background(), "x", []provider.Image{{Data: []byte{1}}}, 1, nil)
	if err == nil || !strings.Contains(err.Error(), "could not create a bitmap") {
		t.Fatalf("error = %v", err)
	}
	if chunksInitialized != 0 {
		t.Errorf("chunks initialized after bitmap failure: %d", chunksInitialized)
	}
}

func TestGenerateImageWithoutProjectorIsActionable(t *testing.T) {
	runtime := &Runtime{model: 1, lctx: 1, vocab: 1, mem: 1, template: "template"}
	_, err := runtime.Generate(context.Background(), embedded.GenRequest{
		Messages: []provider.Message{{
			Role:   provider.RoleUser,
			Images: []provider.Image{{Data: pngWithDimensions(1, 1), MIME: "image/png"}},
		}},
	}, func(embedded.GenDelta) {})
	if err == nil || !strings.Contains(err.Error(), "mmproj_path") {
		t.Fatalf("Generate error = %v, want mmproj_path guidance", err)
	}
}

func TestEvaluateVisionPromptCancellationBoundaries(t *testing.T) {
	t.Run("before native work", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		bitmapCalls := 0
		native := fakeVisionNative(nil, nil, func() {}, func() {}, func() {})
		native.bitmapInit = func(mtmd.Context, *byte, uint64, bool) mtmd.BitmapWrapper {
			bitmapCalls++
			return mtmd.BitmapWrapper{Bitmap: 1}
		}
		runtime := &Runtime{mctx: 1, vision: native}
		if _, err := runtime.evaluateVisionPrompt(ctx, "x", []provider.Image{{Data: []byte{1}}}, 1, nil); err != context.Canceled {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if bitmapCalls != 0 {
			t.Errorf("bitmap calls = %d", bitmapCalls)
		}
	})

	t.Run("after evaluation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		clears := 0
		native := fakeVisionNative([]uint64{1}, []llama.Pos{1}, func() {}, func() {}, func() { clears++ })
		native.helperEvalChunks = func(_ mtmd.Context, _ llama.Context, _ mtmd.InputChunks, _ llama.Pos, _ llama.SeqId, _ int32, _ bool, _ *llama.Pos) int32 {
			cancel()
			return 0
		}
		runtime := &Runtime{mctx: 1, lctx: 2, mem: 3, nCtx: 10, batchSize: 4, vision: native}
		if _, err := runtime.evaluateVisionPrompt(ctx, "x", []provider.Image{{Data: []byte{1}}}, 1, nil); err != context.Canceled {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if !runtime.kvContaminated {
			t.Error("KV state must remain contaminated after canceled evaluation")
		}
		pending, err := runtime.preparePrompt([]llama.Token{7, 8})
		if err != nil {
			t.Fatalf("prepare text after canceled image: %v", err)
		}
		if clears != 2 || runtime.kvContaminated || len(pending) != 2 {
			t.Errorf("recovery clears=%d contaminated=%t pending=%v", clears, runtime.kvContaminated, pending)
		}
	})
}

func TestGenerationBudgetWithPositions(t *testing.T) {
	if got, err := generationBudgetWithPositions(50, 20, 10, 100); err != nil || got != 10 {
		t.Fatalf("budget = %d, %v", got, err)
	}
	if got, err := generationBudgetWithPositions(50, 20, 0, 100); err != nil || got != 80 {
		t.Fatalf("remaining budget = %d, %v", got, err)
	}
	if _, err := generationBudgetWithPositions(0, 20, 1, 100); err == nil || !strings.Contains(err.Error(), "invalid usage") {
		t.Fatalf("invalid usage error = %v", err)
	}
	if _, err := generationBudgetWithPositions(50, 100, 1, 100); err == nil || !strings.Contains(err.Error(), "50 tokens across 100 positions") {
		t.Fatalf("overflow error = %v", err)
	}
	if _, err := generationBudgetWithPositions(50, 20, 81, 100); err == nil || !strings.Contains(err.Error(), "plus max_tokens") {
		t.Fatalf("requested budget error = %v", err)
	}
}

func TestPreparePromptClearsImageContamination(t *testing.T) {
	cleared := 0
	runtime := &Runtime{
		mem:            1,
		kvTokens:       []llama.Token{1, 2},
		kvContaminated: true,
		vision:         fakeVisionNative(nil, nil, func() {}, func() {}, func() { cleared++ }),
	}
	prompt := []llama.Token{9, 10}
	pending, err := runtime.preparePrompt(prompt)
	if err != nil {
		t.Fatalf("preparePrompt: %v", err)
	}
	if cleared != 1 || runtime.kvContaminated || len(runtime.kvTokens) != 0 || len(pending) != 2 {
		t.Errorf("cleared=%d contaminated=%t kv=%v pending=%v", cleared, runtime.kvContaminated, runtime.kvTokens, pending)
	}
}

func fakeVisionNative(tokens []uint64, positions []llama.Pos, freeBitmap, freeChunks, clear func()) visionNative {
	return visionNative{
		bitmapInit: func(mtmd.Context, *byte, uint64, bool) mtmd.BitmapWrapper {
			return mtmd.BitmapWrapper{Bitmap: 1}
		},
		bitmapFree: func(mtmd.Bitmap) { freeBitmap() },
		chunksInit: func() mtmd.InputChunks { return 1 },
		chunksFree: func(mtmd.InputChunks) { freeChunks() },
		tokenize:   func(mtmd.Context, mtmd.InputChunks, *mtmd.InputText, []mtmd.Bitmap) int32 { return 0 },
		chunksSize: func(mtmd.InputChunks) uint64 {
			return uint64(max(len(tokens), len(positions)))
		},
		chunkGet: func(_ mtmd.InputChunks, index uint64) mtmd.InputChunk { return mtmd.InputChunk(index + 1) },
		chunkTokens: func(chunk mtmd.InputChunk) uint64 {
			index := int(chunk - 1)
			if index < len(tokens) {
				return tokens[index]
			}
			return 0
		},
		chunkPositions: func(chunk mtmd.InputChunk) llama.Pos {
			index := int(chunk - 1)
			if index < len(positions) {
				return positions[index]
			}
			return 0
		},
		helperEvalChunks: func(_ mtmd.Context, _ llama.Context, _ mtmd.InputChunks, _ llama.Pos, _ llama.SeqId, _ int32, _ bool, next *llama.Pos) int32 {
			var total llama.Pos
			for _, position := range positions {
				total += position
			}
			*next = total
			return 0
		},
		memoryClear: func(llama.Memory, bool) error { clear(); return nil },
		marker:      func(mtmd.Context) string { return "<image>" },
	}
}

func pngWithDimensions(width, height int) []byte {
	var output bytes.Buffer
	output.Write([]byte("\x89PNG\r\n\x1a\n"))
	data := make([]byte, 13)
	binary.BigEndian.PutUint32(data[0:4], uint32(width))
	binary.BigEndian.PutUint32(data[4:8], uint32(height))
	data[8] = 8
	data[9] = 2
	writePNGChunk(&output, "IHDR", data)
	writePNGChunk(&output, "IEND", nil)
	return output.Bytes()
}

func writePNGChunk(output *bytes.Buffer, kind string, data []byte) {
	_ = binary.Write(output, binary.BigEndian, uint32(len(data)))
	output.WriteString(kind)
	output.Write(data)
	checksum := crc32.NewIEEE()
	_, _ = checksum.Write([]byte(kind))
	_, _ = checksum.Write(data)
	_ = binary.Write(output, binary.BigEndian, checksum.Sum32())
}
