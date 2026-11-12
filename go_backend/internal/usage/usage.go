// Package usage projects the same local token estimates as the Python protocols.
package usage

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

var encodings sync.Map
var imageSize = regexp.MustCompile(`(\d{2,5})\D+(\d{2,5})`)

// Text uses embedded vocabularies only; unknown models fall back to o200k_base.
func Text(text, model string) int {
	if text == "" {
		return 0
	}
	encoding, err := tokenizer.ForModel(tokenizer.Model(model))
	if err != nil {
		encoding, _ = tokenizer.Get(tokenizer.O200kBase)
	}
	value, _ := encodings.LoadOrStore(encoding.GetName(), encoding)
	n, _ := value.(tokenizer.Codec).Count(text)
	return n
}

// ImageInput mirrors Python's fixed gpt-5.4-mini patch estimate.
func ImageInput(width, height int) int {
	if width <= 0 || height <= 0 {
		return 0
	}
	w, h := float64(width), float64(height)
	scale := math.Min(1, 2048/math.Max(w, h))
	w, h = w*scale, h*scale
	patches := func(a, b float64) float64 { return math.Ceil(a/32) * math.Ceil(b/32) }
	if patches(w, h) > 1536 {
		shrink := math.Sqrt(32 * 32 * 1536 / (w * h))
		wu, hu := w*shrink/32, h*shrink/32
		shrink *= math.Min(math.Floor(wu)/wu, math.Floor(hu)/hu)
		w, h = w*shrink, h*shrink
	}
	return int(math.Ceil(math.Min(patches(math.Max(1, w), math.Max(1, h)), 1536) * 1.62))
}

func Tokens(inputText, inputImage, outputText, outputImage int) map[string]any {
	return map[string]any{
		"input_tokens": inputText + inputImage, "output_tokens": outputText + outputImage, "total_tokens": inputText + inputImage + outputText + outputImage,
		"input_tokens_details":  map[string]any{"text_tokens": inputText, "image_tokens": inputImage, "cached_tokens": 0},
		"output_tokens_details": map[string]any{"text_tokens": outputText, "image_tokens": outputImage, "reasoning_tokens": 0},
	}
}

func Chat(inputText, inputImage, outputText, outputImage int) map[string]any {
	u := Tokens(inputText, inputImage, outputText, outputImage)
	return map[string]any{"prompt_tokens": u["input_tokens"], "completion_tokens": u["output_tokens"], "total_tokens": u["total_tokens"], "prompt_tokens_details": u["input_tokens_details"], "completion_tokens_details": u["output_tokens_details"]}
}

// Image takes measured base64 output dimensions. A zero pair uses requested size,
// matching Python's URL-result fallback rather than decoding a remote asset.
func Image(prompt, model, size, quality string, inputs, outputs [][2]int) map[string]any {
	inputTokens, outputTokens := 0, 0
	for _, d := range inputs {
		inputTokens += ImageInput(d[0], d[1])
	}
	fallback := [2]int{1024, 1024}
	if match := imageSize.FindStringSubmatch(size); match != nil {
		w, _ := strconv.Atoi(match[1])
		h, _ := strconv.Atoi(match[2])
		if w > 0 && h > 0 {
			fallback = [2]int{w, h}
		}
	}
	for _, d := range outputs {
		if d[0] <= 0 || d[1] <= 0 {
			d = fallback
		}
		patches := math.Ceil(float64(d[0])/32) * math.Ceil(float64(d[1])/32)
		rate := 33.0 / 32
		switch strings.ToLower(strings.TrimSpace(quality)) {
		case "low":
			rate = 17.0 / 64
		case "high", "hd":
			rate = 65.0 / 16
		}
		outputTokens += int(math.Ceil(patches * rate))
	}
	return Tokens(Text(prompt, model), inputTokens, 0, outputTokens)
}
