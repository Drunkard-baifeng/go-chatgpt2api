package editablefiles

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"io"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"chatgpt2api/go_backend/internal/assettransfer"
	"chatgpt2api/go_backend/internal/images"
	"chatgpt2api/go_backend/internal/upstream"
)

const MaxRequestBytes = 44 << 20
const maxReferenceBytes = 10 << 20
const maxReferenceTotal = 30 << 20
const maxTasks = 10000

var ErrInput = errors.New("文件任务参数或参考图无效")
var ErrFormat = errors.New("上游文件格式、大小或压缩包路径无效")
var ErrFull = errors.New("文件任务队列已满，请稍后重试")
var ErrStorage = errors.New("文件任务存储暂不可用")
var ErrNotFound = errors.New("文件任务或文件不存在")
var ErrConflict = errors.New("任务正在执行，暂不能删除")
var clientID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,160}$`)
var storageID = regexp.MustCompile(`^asset-[a-f0-9]{32}$`)
var assetPath = regexp.MustCompile(`^(ppt|psd)/(asset-[a-f0-9]{32})/(output\.(pptx?|psd)|materials\.zip)$`)
var conversationID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

type Request struct {
	Kind         string   `json:"kind"`
	Prompt       string   `json:"prompt"`
	Images       []string `json:"base64_images"`
	ClientTaskID string   `json:"client_task_id"`
}

func validClientID(id string) bool { return clientID.MatchString(id) && id != "." && id != ".." }
func validateRequest(req Request) ([]upstream.ImageInput, error) {
	if (req.Kind != "ppt" && req.Kind != "psd") || len(req.Prompt) > 65536 || len(req.Images) > 4 || (req.Kind == "psd" && len(req.Images) == 0) || (req.ClientTaskID != "" && !validClientID(req.ClientTaskID)) {
		return nil, ErrInput
	}
	inputs := make([]upstream.ImageInput, 0, len(req.Images))
	total := 0
	for _, raw := range req.Images {
		if len(raw) > base64.StdEncoding.EncodedLen(maxReferenceBytes)+128 {
			return nil, ErrInput
		}
		if strings.HasPrefix(raw, "data:") {
			head, body, ok := strings.Cut(raw, ",")
			if !ok || !strings.HasPrefix(head, "data:image/") || !strings.HasSuffix(head, ";base64") {
				return nil, ErrInput
			}
			raw = body
		}
		if len(raw) > base64.StdEncoding.EncodedLen(maxReferenceBytes) {
			return nil, ErrInput
		}
		data, e := base64.StdEncoding.Strict().DecodeString(raw)
		total += len(data)
		if e != nil || len(data) > maxReferenceBytes || total > maxReferenceTotal {
			return nil, ErrInput
		}
		input, e := images.Inspect(data)
		if e != nil {
			return nil, ErrInput
		}
		inputs = append(inputs, upstream.ImageInput{Data: input.Data, Name: input.Name, Media: input.Media, Width: input.Width, Height: input.Height})
	}
	return inputs, nil
}

// ValidateArtifact checks the bytes and bounded container structure. Output names
// are assigned locally; neither remote filenames nor ZIP entries are extracted.
func ValidateArtifact(extension string, data []byte) error {
	if len(data) == 0 || len(data) > assettransfer.MaxBytes {
		return ErrFormat
	}
	switch extension {
	case "zip", "pptx":
		return validateZip(extension, data)
	case "psd":
		return validatePSD(data)
	case "ppt":
		return validatePPT(data)
	}
	return ErrFormat
}
func validateZip(extension string, data []byte) error {
	z, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil || len(z.File) == 0 || len(z.File) > 2048 {
		return ErrFormat
	}
	seen := map[string]bool{}
	var expanded uint64
	files := 0
	parts := map[string][]byte{}
	for _, f := range z.File {
		name := strings.TrimSuffix(f.Name, "/")
		if name == "" || len(name) > 1024 || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "../") || name == ".." || seen[strings.ToLower(name)] || f.Mode()&os.ModeSymlink != 0 || (!f.FileInfo().IsDir() && !f.Mode().IsRegular()) || f.Flags&1 != 0 {
			return ErrFormat
		}
		seen[strings.ToLower(name)] = true
		if f.FileInfo().IsDir() {
			continue
		}
		files++
		if f.UncompressedSize64 > 250<<20 || expanded > 250<<20-f.UncompressedSize64 {
			return ErrFormat
		}
		expanded += f.UncompressedSize64
		r, e := f.Open()
		if e != nil {
			return ErrFormat
		}
		capture := extension == "pptx" && (name == "[Content_Types].xml" || name == "_rels/.rels" || name == "ppt/presentation.xml" || name == "ppt/_rels/presentation.xml.rels" || strings.HasPrefix(name, "ppt/slides/") && strings.HasSuffix(name, ".xml"))
		if capture {
			if f.UncompressedSize64 > 4<<20 {
				r.Close()
				return ErrFormat
			}
			parts[name], e = io.ReadAll(io.LimitReader(r, 4<<20+1))
		} else {
			_, e = io.Copy(io.Discard, r)
		}
		closeErr := r.Close()
		if e != nil || closeErr != nil {
			return ErrFormat
		}
	}
	if files == 0 {
		return ErrFormat
	}
	if extension == "zip" {
		return nil
	}
	return validatePPTX(parts)
}

func validatePPTX(parts map[string][]byte) error {
	const mainType = "application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"
	const slideType = "application/vnd.openxmlformats-officedocument.presentationml.slide+xml"
	const relationshipNS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	const relationshipType = relationshipNS + "/"
	if !xmlRoot(parts["[Content_Types].xml"], "Types", "http://schemas.openxmlformats.org/package/2006/content-types") || !xmlRoot(parts["ppt/presentation.xml"], "presentation", "http://schemas.openxmlformats.org/presentationml/2006/main") {
		return ErrFormat
	}
	var types struct {
		Overrides []struct {
			Name string `xml:"PartName,attr"`
			Type string `xml:"ContentType,attr"`
		} `xml:"http://schemas.openxmlformats.org/package/2006/content-types Override"`
		Defaults []struct {
			Extension string `xml:"Extension,attr"`
			Type      string `xml:"ContentType,attr"`
		} `xml:"http://schemas.openxmlformats.org/package/2006/content-types Default"`
	}
	if xml.Unmarshal(parts["[Content_Types].xml"], &types) != nil {
		return ErrFormat
	}
	overrides, defaults := map[string]string{}, map[string]string{}
	for _, item := range types.Overrides {
		name := strings.TrimPrefix(item.Name, "/")
		if !strings.HasPrefix(item.Name, "/") || name == "" || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\:\x00") || overrides[name] != "" || item.Type == "" {
			return ErrFormat
		}
		overrides[name] = item.Type
	}
	for _, item := range types.Defaults {
		ext := strings.ToLower(item.Extension)
		if ext == "" || strings.ContainsAny(ext, "./\\:\x00") || defaults[ext] != "" || item.Type == "" {
			return ErrFormat
		}
		defaults[ext] = item.Type
	}
	contentType := func(name string) string {
		if value := overrides[name]; value != "" {
			return value
		}
		return defaults[strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")]
	}
	if contentType("ppt/presentation.xml") != mainType {
		return ErrFormat
	}
	root, ok := pptxRelationships(parts["_rels/.rels"])
	if !ok {
		return ErrFormat
	}
	officeDocuments := 0
	for _, relation := range root {
		if relation.Type == relationshipType+"officeDocument" {
			if relation.Mode == "External" || pptxTarget("", relation.Target) != "ppt/presentation.xml" {
				return ErrFormat
			}
			officeDocuments++
		}
	}
	if officeDocuments != 1 {
		return ErrFormat
	}
	relations, ok := pptxRelationships(parts["ppt/_rels/presentation.xml.rels"])
	if !ok {
		return ErrFormat
	}
	var presentation struct {
		Slides struct {
			IDs []struct {
				Attributes []xml.Attr `xml:",any,attr"`
			} `xml:"http://schemas.openxmlformats.org/presentationml/2006/main sldId"`
		} `xml:"http://schemas.openxmlformats.org/presentationml/2006/main sldIdLst"`
	}
	if xml.Unmarshal(parts["ppt/presentation.xml"], &presentation) != nil || len(presentation.Slides.IDs) == 0 || len(presentation.Slides.IDs) > 2048 {
		return ErrFormat
	}
	seenIDs, seenTargets := map[string]bool{}, map[string]bool{}
	for _, slide := range presentation.Slides.IDs {
		var number, reference string
		for _, attribute := range slide.Attributes {
			if attribute.Name == (xml.Name{Local: "id"}) {
				number = attribute.Value
			} else if attribute.Name == (xml.Name{Space: relationshipNS, Local: "id"}) {
				reference = attribute.Value
			}
		}
		id, e := strconv.ParseUint(number, 10, 32)
		relation, exists := relations[reference]
		target := pptxTarget("ppt", relation.Target)
		if e != nil || id < 256 || id > 2147483647 || seenIDs[number] || !exists || relation.Type != relationshipType+"slide" || relation.Mode == "External" || !strings.HasPrefix(target, "ppt/slides/") || seenTargets[target] || contentType(target) != slideType || !xmlRoot(parts[target], "sld", "http://schemas.openxmlformats.org/presentationml/2006/main") {
			return ErrFormat
		}
		seenIDs[number], seenTargets[target] = true, true
	}
	return nil
}

type pptxRelationship struct {
	ID     string `xml:"Id,attr"`
	Type   string `xml:"Type,attr"`
	Target string `xml:"Target,attr"`
	Mode   string `xml:"TargetMode,attr"`
}

func pptxRelationships(data []byte) (map[string]pptxRelationship, bool) {
	if !xmlRoot(data, "Relationships", "http://schemas.openxmlformats.org/package/2006/relationships") {
		return nil, false
	}
	var document struct {
		Items []pptxRelationship `xml:"http://schemas.openxmlformats.org/package/2006/relationships Relationship"`
	}
	if xml.Unmarshal(data, &document) != nil {
		return nil, false
	}
	items := map[string]pptxRelationship{}
	for _, item := range document.Items {
		if item.ID == "" || items[item.ID].ID != "" || (item.Mode != "" && item.Mode != "Internal" && item.Mode != "External") {
			return nil, false
		}
		items[item.ID] = item
	}
	return items, true
}

func pptxTarget(base, raw string) string {
	u, e := url.Parse(raw)
	if e != nil || raw == "" || u.Scheme != "" || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(u.Path, "\\:\x00") {
		return ""
	}
	name := path.Join(base, u.Path)
	if strings.HasPrefix(u.Path, "/") {
		name = path.Clean(strings.TrimPrefix(u.Path, "/"))
	}
	if name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return ""
	}
	return name
}
func xmlRoot(data []byte, local, space string) bool {
	d := xml.NewDecoder(bytes.NewReader(data))
	found := false
	depth := 0
	for {
		t, e := d.Token()
		if e == io.EOF {
			return found && depth == 0
		}
		if e != nil {
			return false
		}
		switch v := t.(type) {
		case xml.StartElement:
			if depth == 0 {
				if found || v.Name.Local != local || v.Name.Space != space {
					return false
				}
				found = true
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.Directive:
			return false
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(v)) != "" {
				return false
			}
		}
	}
}
func validatePSD(data []byte) error {
	if len(data) < 40 || string(data[:4]) != "8BPS" || binary.BigEndian.Uint16(data[4:6]) != 1 || !bytes.Equal(data[6:12], make([]byte, 6)) {
		return ErrFormat
	}
	channels := int(binary.BigEndian.Uint16(data[12:14]))
	height, width := int(binary.BigEndian.Uint32(data[14:18])), int(binary.BigEndian.Uint32(data[18:22]))
	depth := int(binary.BigEndian.Uint16(data[22:24]))
	color := binary.BigEndian.Uint16(data[24:26])
	if channels < 1 || channels > 56 || width < 1 || height < 1 || width > 30000 || height > 30000 || int64(width)*int64(height) > 100_000_000 || (depth != 1 && depth != 8 && depth != 16 && depth != 32) || color > 9 {
		return ErrFormat
	}
	pos := 26
	for i := 0; i < 3; i++ {
		if pos+4 > len(data) {
			return ErrFormat
		}
		n := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		pos += 4
		if n > len(data)-pos {
			return ErrFormat
		}
		pos += n
	}
	if pos+2 >= len(data) {
		return ErrFormat
	}
	compression := binary.BigEndian.Uint16(data[pos:])
	pos += 2
	if compression > 3 {
		return ErrFormat
	}
	rowBytes := (int64(width)*int64(depth) + 7) / 8
	expected := rowBytes * int64(height) * int64(channels)
	if expected > 250<<20 {
		return ErrFormat
	}
	switch compression {
	case 0:
		if int64(len(data)-pos) != expected {
			return ErrFormat
		}
	case 1:
		rows := height * channels
		if len(data)-pos < rows*2 {
			return ErrFormat
		}
		table := data[pos : pos+rows*2]
		pos += rows * 2
		for i := 0; i < rows; i++ {
			n := int(binary.BigEndian.Uint16(table[2*i:]))
			if n > len(data)-pos {
				return ErrFormat
			}
			packed := data[pos : pos+n]
			pos += n
			decoded := int64(0)
			for len(packed) > 0 {
				control := int(int8(packed[0]))
				packed = packed[1:]
				if control >= 0 {
					literal := control + 1
					if literal > len(packed) {
						return ErrFormat
					}
					decoded += int64(literal)
					packed = packed[literal:]
				} else if control != -128 {
					if len(packed) == 0 {
						return ErrFormat
					}
					decoded += int64(1 - control)
					packed = packed[1:]
				}
				if decoded > rowBytes {
					return ErrFormat
				}
			}
			if decoded != rowBytes {
				return ErrFormat
			}
		}
		if pos != len(data) {
			return ErrFormat
		}
	case 2, 3:
		r, e := zlib.NewReader(bytes.NewReader(data[pos:]))
		if e != nil {
			return ErrFormat
		}
		n, e := io.Copy(io.Discard, io.LimitReader(r, expected+1))
		closeErr := r.Close()
		if e != nil || closeErr != nil || n != expected {
			return ErrFormat
		}
	}
	return nil
}

// Legacy PPT is a CFB container. Validate its sector tables and the actual
// PowerPoint Document stream rather than accepting the shared OLE magic alone.
func validatePPT(data []byte) error {
	if len(data) < 512 || !bytes.Equal(data[:8], []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) || binary.LittleEndian.Uint16(data[28:30]) != 0xfffe {
		return ErrFormat
	}
	shift := binary.LittleEndian.Uint16(data[30:32])
	if shift != 9 && shift != 12 {
		return ErrFormat
	}
	sectorSize := 1 << shift
	if len(data) < sectorSize || len(data)%sectorSize != 0 {
		return ErrFormat
	}
	count := len(data)/sectorSize - 1
	sector := func(id uint32) []byte {
		if uint64(id) >= uint64(count) {
			return nil
		}
		start := (int(id) + 1) * sectorSize
		return data[start : start+sectorSize]
	}
	nfat := binary.LittleEndian.Uint32(data[44:48])
	if nfat == 0 || uint64(nfat) > uint64(count) {
		return ErrFormat
	}
	fatSectors := make([]uint32, 0, nfat)
	seenSectors := map[uint32]bool{}
	appendFAT := func(id uint32) bool {
		if uint64(id) >= uint64(count) || seenSectors[id] {
			return false
		}
		seenSectors[id] = true
		fatSectors = append(fatSectors, id)
		return true
	}
	for i := 0; i < 109 && len(fatSectors) < int(nfat); i++ {
		if !appendFAT(binary.LittleEndian.Uint32(data[76+4*i : 80+4*i])) {
			return ErrFormat
		}
	}
	difat := binary.LittleEndian.Uint32(data[68:72])
	difatCount := binary.LittleEndian.Uint32(data[72:76])
	if uint64(difatCount) > uint64(count) {
		return ErrFormat
	}
	for i := uint32(0); i < difatCount; i++ {
		if seenSectors[difat] {
			return ErrFormat
		}
		seenSectors[difat] = true
		b := sector(difat)
		if b == nil {
			return ErrFormat
		}
		for n := 0; n < sectorSize-4 && len(fatSectors) < int(nfat); n += 4 {
			if !appendFAT(binary.LittleEndian.Uint32(b[n : n+4])) {
				return ErrFormat
			}
		}
		difat = binary.LittleEndian.Uint32(b[sectorSize-4:])
	}
	if len(fatSectors) != int(nfat) || (difatCount > 0 && difat != 0xfffffffe) {
		return ErrFormat
	}
	fat := []uint32{}
	for _, id := range fatSectors {
		b := sector(id)
		if b == nil {
			return ErrFormat
		}
		for n := 0; n < len(b); n += 4 {
			fat = append(fat, binary.LittleEndian.Uint32(b[n:n+4]))
		}
	}
	chain := func(first uint32, maxBytes int) ([]byte, bool) {
		out := []byte{}
		visited := map[uint32]bool{}
		for first != 0xfffffffe {
			if visited[first] || uint64(first) >= uint64(len(fat)) {
				return nil, false
			}
			visited[first] = true
			b := sector(first)
			if b == nil || len(out) > maxBytes-len(b) {
				return nil, false
			}
			out = append(out, b...)
			first = fat[first]
		}
		return out, true
	}
	dir, ok := chain(binary.LittleEndian.Uint32(data[48:52]), 4<<20)
	if !ok {
		return ErrFormat
	}
	for i := 0; i+128 <= len(dir); i += 128 {
		d := dir[i : i+128]
		n := int(binary.LittleEndian.Uint16(d[64:66]))
		if d[66] != 2 || n < 2 || n > 64 || n%2 != 0 {
			continue
		}
		var name strings.Builder
		for p := 0; p < n-2; p += 2 {
			ch := binary.LittleEndian.Uint16(d[p : p+2])
			if ch > 127 {
				break
			}
			name.WriteByte(byte(ch))
		}
		if name.String() != "PowerPoint Document" {
			continue
		}
		size := binary.LittleEndian.Uint64(d[120:128])
		if size < 4096 || size > uint64(len(data)) {
			return ErrFormat
		}
		stream, ok := chain(binary.LittleEndian.Uint32(d[116:120]), len(data))
		if !ok || uint64(len(stream)) < size || len(stream) < 8 {
			return ErrFormat
		}
		typ := binary.LittleEndian.Uint16(stream[2:4])
		length := binary.LittleEndian.Uint32(stream[4:8])
		if (typ == 0x03e8 || typ == 0x0ff5) && uint64(length)+8 <= size {
			return nil
		}
		return ErrFormat
	}
	return ErrFormat
}
