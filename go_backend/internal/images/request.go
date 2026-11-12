// Package images 拥有同步图片输入、准入与生成生命周期。
package images

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/draw"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"chatgpt2api/go_backend/internal/assettransfer"
	_ "golang.org/x/image/webp"
)

const MaxN = 4
const MaxInputBytes = 100 << 20
const MaxRequestBytes = 140 << 20
const MaxInputs = 16

var ErrInput = errors.New("图片参数或参考图无效，n 为 1–4")
var Models = []string{"gpt-image-2", "gpt-image-2.5", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "gpt-image-2.6", "gpt-image-2.6-flare", "gpt-image-2.6-sunburst"}

const CodexModel = "codex-gpt-image-2"

// 图片模型约束与 Python split_image_model 保持一致，网页模型不限制来源。
func modelConstraints(model string) (string, []string) {
	if model == CodexModel {
		return "codex", []string{"plus", "team", "pro"}
	}
	for _, plan := range []string{"plus", "team", "pro"} {
		if model == plan+"-"+CodexModel {
			return "codex", []string{plan}
		}
	}
	return "", nil
}

func SupportedModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if source, _ := modelConstraints(model); source != "" {
		return true
	}
	for _, name := range Models {
		if model == name {
			return true
		}
	}
	return false
}

// InputError is a safe public message for a rejected image business parameter.
type InputError struct{ Message string }

func (e *InputError) Error() string     { return e.Message }
func (e *InputError) Unwrap() error     { return ErrInput }
func invalidInput(message string) error { return &InputError{Message: message} }

type Source struct {
	URL  string
	Data []byte
}
type Input struct {
	Data          []byte
	Media, Name   string
	Width, Height int
}
type Request struct {
	Endpoint                                            string
	ClientTaskID                                        string
	BaseURL                                             string
	Prompt, Model, UpstreamModel, Size, Quality, Format string
	OutputFormat                                        string
	FrameCount, FrameDurationMS                         json.RawMessage
	N                                                   int
	Edit, Stream, HistoryDisabled                       bool
	Sources                                             []Source
	MaskSources                                         []Source
}

// ParseError identifies a generations JSON schema error before business validation.
// Its fields correspond to the public Python/Pydantic validation projection.
type ParseError struct {
	Field, Type, Message string
	Input                any
	Errors               []*ParseError
}

func (e *ParseError) Error() string {
	if len(e.Errors) == 0 {
		return e.Message
	}
	parts := make([]string, 0, len(e.Errors))
	for _, item := range e.Errors {
		message := item.Message
		if item.Field != "" {
			message = item.Field + ": " + message
		}
		parts = append(parts, message)
	}
	return strings.Join(parts, "; ")
}
func (e *ParseError) Unwrap() error { return ErrInput }

func parseError(field, kind, message string, raw json.RawMessage) error {
	var input any
	_ = json.Unmarshal(raw, &input)
	return &ParseError{Field: field, Type: kind, Message: message, Input: input}
}

func ParseRequest(r *http.Request, edit bool) (Request, error) {
	return parseRequest(r, edit, false)
}
func ParseTaskRequest(r *http.Request, edit bool) (string, Request, error) {
	req, err := parseRequest(r, edit, true)
	if req.ClientTaskID == "" {
		err = ErrInput
	}
	return req.ClientTaskID, req, err
}
func ValidateRequest(req Request) error { return validate(&req) }
func parseRequest(r *http.Request, edit, task bool) (Request, error) {
	req := Request{Model: "gpt-image-2", UpstreamModel: "gpt-5-3", Quality: "auto", Format: "b64_json", N: 1, Edit: edit, HistoryDisabled: true}
	if task {
		req.Format = "url"
	}
	fields := map[string]json.RawMessage{}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media == "multipart/form-data" && edit {
		reader, err := r.MultipartReader()
		if err != nil {
			return req, ErrInput
		}
		var total int64
		seen := map[string]bool{}
		for count := 0; ; count++ {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil || count > 64 {
				return req, ErrInput
			}
			name := part.FormName()
			isImage := name == "image" || name == "image[]" || name == "images" || name == "images[]" || name == "image_url" || name == "image_url[]"
			isMask := name == "mask" || name == "mask[]"
			if task && isMask {
				return req, ErrInput
			}
			limit := int64(1 << 20)
			if isImage || isMask {
				limit = assettransfer.MaxBytes
			}
			if remaining := int64(MaxInputBytes) - total; remaining < limit {
				limit = remaining
			}
			data, readErr := io.ReadAll(io.LimitReader(part, limit+1))
			total += int64(len(data))
			if readErr != nil || int64(len(data)) > limit || total > MaxInputBytes {
				return req, ErrInput
			}
			part.Close()
			if isImage || isMask {
				sources := &req.Sources
				if isMask {
					sources = &req.MaskSources
				}
				if part.FileName() != "" {
					*sources = append(*sources, Source{Data: data})
				} else {
					if err := addSource(sources, string(data), 0); err != nil {
						return req, err
					}
				}
			} else {
				if part.FileName() != "" || (task && seen[name]) {
					return req, ErrInput
				}
				seen[name] = true
				fields[name], _ = json.Marshal(string(data))
			}
			if len(req.Sources) > MaxInputs || len(req.MaskSources) > MaxInputs {
				return req, ErrInput
			}
		}
	} else {
		data, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes+1))
		if err != nil || len(data) > MaxRequestBytes {
			return req, ErrInput
		}
		if err = json.Unmarshal(data, &fields); err != nil || fields == nil {
			if !edit && !task {
				kind, message := "json_invalid", "JSON decode error"
				if json.Valid(data) {
					kind, message = "model_attributes_type", "Input should be a valid dictionary or object to extract fields from"
				}
				return req, parseError("", kind, message, data)
			}
			if edit && !task {
				if !json.Valid(data) {
					return req, invalidInput("invalid JSON body")
				}
				return req, invalidInput("JSON body must be an object")
			}
			return req, ErrInput
		}
	}
	if task {
		for key := range fields {
			switch key {
			case "client_task_id", "prompt", "model", "size", "quality", "output_format", "n", "stream", "image", "images", "image_url":
			default:
				return req, ErrInput
			}
		}
	}
	schema := !edit && !task
	var schemaErrors []*ParseError
	appendSchemaError := func(err error) {
		var validation *ParseError
		if errors.As(err, &validation) {
			schemaErrors = append(schemaErrors, validation)
		}
	}
	if _, ok := fields["prompt"]; !ok && schema {
		appendSchemaError(parseError("prompt", "missing", "Field required", nil))
	}
	for _, field := range []struct {
		key      string
		target   *string
		nullable bool
	}{
		{"prompt", &req.Prompt, false}, {"model", &req.Model, false}, {"size", &req.Size, true},
		{"quality", &req.Quality, false}, {"response_format", &req.Format, false}, {"output_format", &req.OutputFormat, true},
		{"client_task_id", &req.ClientTaskID, false},
	} {
		raw, ok := fields[field.key]
		if !ok || (task && field.key == "response_format") || (schema && field.key == "client_task_id") {
			continue
		}
		if schema {
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				if field.nullable {
					*field.target = ""
					continue
				}
				appendSchemaError(parseError(field.key, "string_type", "Input should be a valid string", raw))
				continue
			}
			if json.Unmarshal(raw, field.target) != nil {
				appendSchemaError(parseError(field.key, "string_type", "Input should be a valid string", raw))
				continue
			}
			if field.key == "prompt" && *field.target == "" {
				appendSchemaError(parseError(field.key, "string_too_short", "String should have at least 1 character", raw))
			}
		} else {
			var value any
			if json.Unmarshal(raw, &value) != nil {
				return req, ErrInput
			}
			if field.key == "output_format" && !imageOptionTruthy(value) {
				*field.target = ""
			} else {
				*field.target = strings.TrimSpace(imageOptionText(value))
			}
		}
	}
	if req.Model == "" {
		req.Model = "gpt-image-2"
	}
	if req.Quality == "" {
		req.Quality = "auto"
	}
	if req.Format == "" {
		req.Format = "b64_json"
	}
	if raw, ok := fields["n"]; ok {
		var err error
		req.N, err = parseImageCount(raw, schema)
		if err != nil {
			if schema {
				appendSchemaError(err)
			} else {
				return req, err
			}
		}
	}
	if raw, ok := fields["stream"]; ok {
		var err error
		req.Stream, err = parseImageBool(raw, schema, true)
		if err != nil {
			if schema {
				appendSchemaError(err)
			} else {
				return req, err
			}
		}
		if task && req.Stream {
			return req, ErrInput
		}
	}
	if schema {
		if raw, ok := fields["history_disabled"]; ok {
			value, err := parseImageBool(raw, true, false)
			if err != nil {
				var validation *ParseError
				if errors.As(err, &validation) {
					validation.Field = "history_disabled"
				}
				appendSchemaError(err)
			}
			req.HistoryDisabled = value
		}
	}
	if len(schemaErrors) > 0 {
		if len(schemaErrors) == 1 {
			return req, schemaErrors[0]
		}
		ordered := make([]*ParseError, 0, len(schemaErrors))
		for _, field := range []string{"prompt", "model", "n", "size", "quality", "response_format", "output_format", "history_disabled", "stream"} {
			for _, item := range schemaErrors {
				if item.Field == field {
					ordered = append(ordered, item)
				}
			}
		}
		return req, &ParseError{Errors: ordered}
	}
	if raw, ok := fields["frame_count"]; ok {
		req.FrameCount = append(json.RawMessage(nil), raw...)
	}
	if raw, ok := fields["frame_duration_ms"]; ok {
		req.FrameDurationMS = append(json.RawMessage(nil), raw...)
	}
	if edit {
		for _, key := range []string{"images", "image", "image_url", "mask"} {
			if raw, ok := fields[key]; ok {
				var value any
				out := &req.Sources
				if key == "mask" {
					out = &req.MaskSources
				}
				if json.Unmarshal(raw, &value) != nil {
					return req, ErrInput
				}
				if err := addSource(out, value, 0); err != nil {
					return req, err
				}
			}
		}
	}
	total := 0
	for _, sources := range [][]Source{req.Sources, req.MaskSources} {
		for _, source := range sources {
			total += len(source.Data)
		}
	}
	if total > MaxInputBytes {
		return req, ErrInput
	}
	return req, validate(&req)
}

func parseImageCount(raw json.RawMessage, schema bool) (int, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return 0, invalidInput("n must be an integer")
	}
	invalid := func(kind, message string) (int, error) {
		if schema {
			return 0, parseError("n", kind, message, raw)
		}
		return 0, invalidInput("n must be an integer")
	}
	if value == nil {
		if schema {
			return invalid("int_type", "Input should be a valid integer")
		}
		return 1, nil
	}
	var numeric float64
	var err error
	switch v := value.(type) {
	case bool:
		if v {
			numeric = 1
		}
	case json.Number:
		numeric, err = strconv.ParseFloat(string(v), 64)
		if schema && numeric != math.Trunc(numeric) {
			return invalid("int_from_float", "Input should be a valid integer, got a number with a fractional part")
		}
	case string:
		v = strings.TrimSpace(v)
		if v == "" && !schema {
			return 1, nil
		}
		if schema {
			if strings.ContainsAny(v, "eExXpP") {
				err = ErrInput
			} else {
				numeric, err = strconv.ParseFloat(v, 64)
			}
			if numeric != math.Trunc(numeric) {
				err = ErrInput
			}
		} else {
			var integer int64
			integer, err = strconv.ParseInt(v, 10, 64)
			numeric = float64(integer)
		}
	default:
		return invalid("int_type", "Input should be a valid integer")
	}
	if err != nil || math.IsNaN(numeric) || math.IsInf(numeric, 0) || numeric > float64(math.MaxInt) || numeric < float64(math.MinInt) {
		return invalid("int_parsing", "Input should be a valid integer, unable to parse string as an integer")
	}
	n := int(numeric)
	if schema && n < 1 {
		return invalid("greater_than_equal", "Input should be greater than or equal to 1")
	}
	return n, nil
}

func parseImageBool(raw json.RawMessage, schema, nullable bool) (bool, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return false, ErrInput
	}
	if value == nil && nullable {
		return false, nil
	}
	if value == nil && !schema {
		return false, nil
	}
	if v, ok := value.(bool); ok {
		return v, nil
	}
	if !schema && value == "" {
		return false, nil
	}
	text := imageOptionText(value)
	if number, ok := value.(json.Number); ok {
		if schema {
			if numeric, err := number.Float64(); err == nil && (numeric == 0 || numeric == 1) {
				return numeric == 1, nil
			}
		} else if strings.ContainsAny(string(number), ".eE") {
			return false, invalidInput("stream must be a boolean")
		} else if numeric, err := number.Int64(); err == nil {
			text = strconv.FormatInt(numeric, 10)
		}
	}
	if !schema {
		text = strings.TrimSpace(text)
	}
	switch strings.ToLower(text) {
	case "true", "1", "yes", "y", "on":
		return true, nil
	case "false", "0", "no", "n", "off":
		return false, nil
	case "t":
		if schema {
			return true, nil
		}
	case "f":
		if schema {
			return false, nil
		}
	}
	if schema {
		kind := "bool_parsing"
		message := "Input should be a valid boolean, unable to interpret input"
		switch value.(type) {
		case nil, []any, map[string]any:
			kind = "bool_type"
			message = "Input should be a valid boolean"
		}
		return false, parseError("stream", kind, message, raw)
	}
	return false, invalidInput("stream must be a boolean")
}

func imageOptionText(value any) string {
	if value == nil {
		return ""
	}
	if v, ok := value.(string); ok {
		return v
	}
	if v, ok := value.(bool); ok {
		if v {
			return "True"
		}
		return "False"
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func imageOptionTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0
	case []any:
		return len(v) != 0
	case map[string]any:
		return len(v) != 0
	}
	return true
}

func validate(req *Request) error {
	req.Model = strings.ToLower(strings.TrimSpace(req.Model))
	req.OutputFormat = strings.ToLower(strings.TrimSpace(req.OutputFormat))
	if req.OutputFormat == "jpg" {
		req.OutputFormat = "jpeg"
	}
	switch req.OutputFormat {
	case "", "gif", "png", "jpeg", "webp", "svg":
	default:
		return invalidInput("unsupported output_format: " + req.OutputFormat)
	}
	if (req.OutputFormat == "gif" || req.OutputFormat == "svg") && req.Stream {
		return invalidInput(strings.ToUpper(req.OutputFormat) + " output does not support stream=true")
	}
	if req.OutputFormat == "svg" && (req.Edit || len(req.Sources) > 0 || len(req.MaskSources) > 0) {
		return invalidInput("SVG output is supported only for image generations")
	}
	if req.Prompt == "" {
		return invalidInput("prompt is required")
	}
	if req.N < 1 || req.N > MaxN {
		return invalidInput("n must be between 1 and " + strconv.Itoa(MaxN))
	}
	if req.Edit && len(req.Sources) == 0 {
		return invalidInput("image file or image_url is required")
	}
	if len(req.Sources) > MaxInputs || len(req.MaskSources) > MaxInputs {
		return invalidInput("at most " + strconv.Itoa(MaxInputs) + " image inputs are supported")
	}
	if len(req.Prompt) > 1<<20 || (!req.Edit && (len(req.Sources) > 0 || len(req.MaskSources) > 0)) {
		return ErrInput
	}
	if !SupportedModel(req.Model) {
		return ErrInput
	}
	req.UpstreamModel = "gpt-5-3"
	if req.Model != "gpt-image-2" {
		req.UpstreamModel = "auto"
	}
	if source, _ := modelConstraints(req.Model); source == "codex" {
		req.UpstreamModel = "gpt-5.5"
	}
	return nil
}

// ValidateOutput applies the shared Python GIF/SVG model capability setting.
// The execution service supplies the current settings snapshot before account IO.
func (req Request) ValidateOutput(gifModels []string) error {
	format := strings.ToLower(strings.TrimSpace(req.OutputFormat))
	if format != "gif" && format != "svg" {
		return nil
	}
	for _, model := range gifModels {
		if strings.ToLower(strings.TrimSpace(model)) == strings.ToLower(strings.TrimSpace(req.Model)) {
			if req.Stream {
				return invalidInput(strings.ToUpper(format) + " output does not support stream=true")
			}
			if format == "svg" && (req.Edit || len(req.Sources) > 0 || len(req.MaskSources) > 0) {
				return invalidInput("SVG output is supported only for image generations")
			}
			return nil
		}
	}
	return invalidInput(strings.ToUpper(format) + " output is not enabled for this image model")
}

// ParseSources expands public image references without starting network IO.
func ParseSources(value any) ([]Source, error) {
	var sources []Source
	if err := addSource(&sources, value, 0); err != nil {
		return nil, err
	}
	total := 0
	for _, source := range sources {
		total += len(source.Data)
	}
	if total > MaxInputBytes {
		return nil, ErrInput
	}
	return sources, nil
}

func addSource(out *[]Source, value any, depth int) error {
	if depth > 8 {
		return ErrInput
	}
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return nil
		}
		if len(v) > 70<<20 {
			return invalidInput("image URL exceeds 50MB limit")
		}
		if len(*out) >= MaxInputs {
			return invalidInput("at most " + strconv.Itoa(MaxInputs) + " image inputs are supported")
		}
		if v[0] == '[' || v[0] == '{' {
			var nested any
			if json.Unmarshal([]byte(v), &nested) == nil {
				return addSource(out, nested, depth+1)
			}
		}
		lower := strings.ToLower(v)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
			*out = append(*out, Source{URL: v})
			return nil
		}
		if strings.HasPrefix(v, "data:") {
			head, body, ok := strings.Cut(v, ",")
			media := strings.TrimPrefix(strings.SplitN(head, ";", 2)[0], "data:")
			if media == "" {
				media = "image/png"
			}
			if !ok {
				return invalidInput("invalid data image URL")
			}
			if !strings.HasPrefix(media, "image/") {
				return invalidInput("image_url must point to an image")
			}
			if body == "" {
				return invalidInput("image URL is empty")
			}
			if !strings.Contains(head, ";base64") {
				decoded, err := url.PathUnescape(body)
				if err != nil {
					return invalidInput("invalid data image URL")
				}
				if len(decoded) > assettransfer.MaxBytes {
					return invalidInput("image URL exceeds 50MB limit")
				}
				return appendImageSource(out, Source{Data: []byte(decoded)})
			}
			return addBase64Source(out, body, "invalid data image URL")
		}
		return addBase64Source(out, v, "invalid base64 image data")
	case []any:
		for _, item := range v {
			if err := addSource(out, item, depth+1); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		if imageOptionTruthy(v["file_id"]) {
			return invalidInput("file_id image references are not supported; use image_url instead")
		}
		for _, key := range []string{"b64_json", "base64"} {
			if item := v[key]; imageOptionTruthy(item) {
				return addBase64Source(out, strings.TrimSpace(imageOptionText(item)), "invalid base64 image data")
			}
		}
		item, ok := v["image_url"]
		if !ok {
			item, ok = v["url"]
		}
		if !ok {
			return invalidInput("image reference must include image_url")
		}
		if nested, ok := item.(map[string]any); ok {
			item = nested["url"]
		}
		return addSource(out, item, depth+1)
	}
	return invalidInput("invalid image reference")
}

func appendImageSource(out *[]Source, source Source) error {
	if len(*out) >= MaxInputs || len(source.Data) > assettransfer.MaxBytes {
		return ErrInput
	}
	total := len(source.Data)
	for _, previous := range *out {
		total += len(previous.Data)
	}
	if total > MaxInputBytes {
		return ErrInput
	}
	*out = append(*out, source)
	return nil
}

func addBase64Source(out *[]Source, encoded, invalidMessage string) error {
	if len(*out) >= MaxInputs {
		return invalidInput("at most " + strconv.Itoa(MaxInputs) + " image inputs are supported")
	}
	if len(encoded) > ((assettransfer.MaxBytes+2)/3)*4+4 || base64.StdEncoding.DecodedLen(len(encoded)) > assettransfer.MaxBytes+2 {
		return invalidInput("image URL exceeds 50MB limit")
	}
	if strings.ContainsAny(encoded, "\r\n") {
		return invalidInput(invalidMessage)
	}
	decoded := base64.StdEncoding.DecodedLen(len(encoded))
	if strings.HasSuffix(encoded, "==") {
		decoded -= 2
	} else if strings.HasSuffix(encoded, "=") {
		decoded--
	}
	for _, previous := range *out {
		decoded += len(previous.Data)
	}
	if decoded > MaxInputBytes {
		return invalidInput("combined image inputs exceed 100MB limit")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return invalidInput(invalidMessage)
	}
	if len(data) == 0 {
		return invalidInput("image file is empty")
	}
	if len(data) > assettransfer.MaxBytes {
		return invalidInput("image URL exceeds 50MB limit")
	}
	return appendImageSource(out, Source{Data: data})
}

func Inspect(data []byte) (Input, error) {
	if width, height, ok := svgDimensions(data); ok {
		return Input{Data: data, Media: "image/svg+xml", Name: "image.svg", Width: width, Height: height}, nil
	}
	decoded, kind, err := decodeImage(data)
	if err != nil {
		return Input{}, err
	}
	return Input{Data: data, Media: "image/" + kind, Name: "image." + kind, Width: decoded.Bounds().Dx(), Height: decoded.Bounds().Dy()}, nil
}

func decodeImage(data []byte) (image.Image, string, error) {
	cfg, kind, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || len(data) == 0 || len(data) > assettransfer.MaxBytes || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
		return nil, "", ErrInput
	}
	if kind == "gif" {
		if !boundedGIF(data) {
			return nil, "", ErrInput
		}
		animation, err := gif.DecodeAll(bytes.NewReader(data))
		if err != nil || len(animation.Image) == 0 {
			return nil, "", ErrInput
		}
		frame := animation.Image[0]
		canvas := image.NewNRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
		if int(animation.BackgroundIndex) < len(frame.Palette) {
			draw.Draw(canvas, canvas.Bounds(), image.NewUniform(frame.Palette[animation.BackgroundIndex]), image.Point{}, draw.Src)
		}
		draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)
		return canvas, kind, nil
	}
	if kind != "png" && kind != "jpeg" && kind != "webp" {
		return nil, "", ErrInput
	}
	decoded, format, err := image.Decode(bytes.NewReader(data))
	if err != nil || format != kind {
		return nil, "", ErrInput
	}
	return decoded, kind, nil
}
func ReadInputs(ctx context.Context, sources []Source, proxy string) ([]Input, error) {
	total := 0
	return readInputs(ctx, sources, proxy, &total)
}

// ReadEditInputs shares a single input-byte budget between references and masks.
func ReadEditInputs(ctx context.Context, sources, masks []Source, proxy string) ([]Input, []Input, error) {
	total := 0
	inputs, err := readInputs(ctx, sources, proxy, &total)
	if err != nil {
		return nil, nil, err
	}
	maskInputs, err := readInputs(ctx, masks, proxy, &total)
	return inputs, maskInputs, err
}

func readInputs(ctx context.Context, sources []Source, proxy string, total *int) ([]Input, error) {
	var inputs []Input
	if len(sources) > MaxInputs {
		return nil, ErrInput
	}
	for _, source := range sources {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if *total >= MaxInputBytes {
			return nil, ErrInput
		}
		data := source.Data
		if source.URL != "" {
			var err error
			data, err = assettransfer.Transfer(ctx, "GET", source.URL, proxy, nil, "")
			if err != nil {
				return nil, err
			}
		}
		*total += len(data)
		if *total > MaxInputBytes {
			return nil, ErrInput
		}
		input, err := Inspect(data)
		if err != nil {
			return nil, err
		}
		if input.Media == "image/svg+xml" {
			return nil, ErrInput
		}
		inputs = append(inputs, input)
	}
	return inputs, nil
}
