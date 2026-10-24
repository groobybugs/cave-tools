// Package read ports src/tools/read.ts: the cave__read MCP tool.
//
// Layout rule for wave 3: each TS tool file becomes its own Go package
// (go/internal/tools/<name>/<name>.go) exporting var Tool. Byte-for-byte
// parity notes for this tool:
//
//   - JavaScript string lengths are UTF-16 code units, so every length check,
//     ratio and emittedChars value TS computes with String.length uses
//     utf16Len here (never len, which counts UTF-8 bytes).
//   - offset/limit keep JS number semantics: Number(x) || 1, fractional
//     offsets fail the way V8 does, and numbers printed into messages or dedup
//     keys go through jsNumberString.
//   - Path helpers use Node's path.extname/dirname/basename semantics (see
//     path.go), not filepath's: dotfiles and trailing slashes differ.
package read

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/tools"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// imageMIME maps a lowercased extension to its image mime type.
// port of src/tools/read.ts:IMAGE_MIME
var imageMIME = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".svg":  "image/svg+xml",
}

const (
	// Hard cap to avoid blowing up the MCP transport with multi-MB base64
	// payloads. Matches opencode 2.0 MAX_MEDIA_INGEST_BYTES (20 MB).
	// port of src/tools/read.ts:MAX_IMAGE_BYTES
	maxImageBytes = 20 * 1024 * 1024
	// Same cap reused for PDFs returned as embedded resources.
	// port of src/tools/read.ts:MAX_PDF_BYTES
	maxPDFBytes = 20 * 1024 * 1024

	// Per-line and per-read byte caps, ported from opencode.
	// port of src/tools/read.ts:MAX_LINE_LENGTH
	maxLineLength = 2000
	// port of src/tools/read.ts:MAX_BYTES
	maxBytes = 50 * 1024

	// Re-read of a changed file returns a unified diff only when it is smaller
	// than this fraction of the full content.
	// port of src/tools/read.ts:DIFF_MAX_RATIO
	diffMaxRatio = 0.4
	// port of src/tools/read.ts:DIFF_HEADER
	diffHeader = "[cave: changed since last read in this session — unified diff; no prior copy? re-read with force=true]"
)

// binaryExtensions are always binary; rejected before attempting a utf-8
// decode.
// port of src/tools/read.ts:BINARY_EXTENSIONS
var binaryExtensions = map[string]struct{}{
	".zip": {}, ".tar": {}, ".gz": {}, ".tgz": {}, ".bz2": {}, ".xz": {}, ".7z": {}, ".rar": {},
	".exe": {}, ".dll": {}, ".so": {}, ".dylib": {}, ".bin": {}, ".dat": {}, ".obj": {}, ".o": {}, ".a": {}, ".lib": {},
	".class": {}, ".jar": {}, ".war": {}, ".wasm": {}, ".pyc": {}, ".pyo": {},
	".doc": {}, ".docx": {}, ".xls": {}, ".xlsx": {}, ".ppt": {}, ".pptx": {}, ".odt": {}, ".ods": {}, ".odp": {},
	".woff": {}, ".woff2": {}, ".ttf": {}, ".otf": {}, ".eot": {},
	".mp3": {}, ".mp4": {}, ".avi": {}, ".mov": {}, ".mkv": {}, ".wav": {}, ".flac": {}, ".ogg": {},
	".ico": {}, ".tiff": {}, ".tif": {}, ".heic": {}, ".psd": {},
}

// startsWith reports whether bytes begins with every byte of prefix.
// port of src/tools/read.ts:startsWith
func startsWith(bytes []byte, prefix []byte) bool {
	if len(bytes) < len(prefix) {
		return false
	}
	for index, value := range prefix {
		if bytes[index] != value {
			return false
		}
	}
	return true
}

// sniffImageMime inspects magic bytes, falling back to the extension mime.
// port of src/tools/read.ts:sniffImageMime
func sniffImageMime(filePath string, ext string) (string, bool) {
	if ext == ".svg" {
		mime, ok := imageMIME[ext]
		return mime, ok
	}
	extensionMime, hasExtensionMime := imageMIME[ext]
	handle, err := os.Open(filePath)
	if err != nil {
		return extensionMime, hasExtensionMime
	}
	defer handle.Close()

	buffer := make([]byte, 16)
	bytesRead, readErr := handle.Read(buffer)
	if readErr != nil && readErr != io.EOF {
		return extensionMime, hasExtensionMime
	}
	bytes := buffer[:bytesRead]
	if startsWith(bytes, []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}) {
		return "image/png", true
	}
	if startsWith(bytes, []byte{0xff, 0xd8, 0xff}) {
		return "image/jpeg", true
	}
	if startsWith(bytes, []byte{0x47, 0x49, 0x46, 0x38}) {
		return "image/gif", true
	}
	if len(bytes) >= 12 && startsWith(bytes, []byte{0x52, 0x49, 0x46, 0x46}) && startsWith(bytes[8:], []byte{0x57, 0x45, 0x42, 0x50}) {
		return "image/webp", true
	}
	return extensionMime, hasExtensionMime
}

// readUtf8StrictSnapshot reads a file as strict UTF-8 (a BOM is stripped, like
// TextDecoder) and hashes the original bytes (BOM included) so the
// numbered-read footer agrees with cave__edit's expected_hash.
// port of src/tools/read.ts:readUtf8StrictSnapshot
func readUtf8StrictSnapshot(filePath string) (string, string, error) {
	bytes, err := os.ReadFile(filePath)
	if err != nil {
		return "", "", err
	}
	content, ok := decodeUTF8Strict(bytes)
	if !ok {
		return "", "", errors.New("File is not valid UTF-8: " + filePath)
	}
	return content, compression.HashContent(bytes), nil
}

// decodeUTF8Strict mirrors `new TextDecoder("utf-8", {fatal: true}).decode`:
// invalid UTF-8 fails, and a leading BOM is dropped (ignoreBOM defaults to
// false in the WHATWG decoder).
func decodeUTF8Strict(bytes []byte) (string, bool) {
	if !utf8.Valid(bytes) {
		return "", false
	}
	const bom = "\xef\xbb\xbf"
	return strings.TrimPrefix(string(bytes), bom), true
}

// isBinaryFile detects binary content: known binary extensions short-circuit;
// otherwise the first 4 KB are sniffed for a NUL byte or a high ratio of
// non-printable bytes.
// port of src/tools/read.ts:isBinaryFile
func isBinaryFile(filePath string, ext string) bool {
	if _, ok := binaryExtensions[ext]; ok {
		return true
	}

	handle, err := os.Open(filePath)
	if err != nil {
		return false
	}
	defer handle.Close()

	buffer := make([]byte, 4096)
	bytesRead, readErr := handle.Read(buffer)
	if readErr != nil && readErr != io.EOF {
		return false
	}
	if bytesRead == 0 {
		return false
	}

	nonPrintable := 0
	for i := 0; i < bytesRead; i++ {
		byteValue := buffer[i]
		if byteValue == 0 {
			return true
		}
		// Printable: tab(9) newline(10) cr(13) and >= 32. Count the rest.
		if byteValue < 9 || (byteValue > 13 && byteValue < 32) {
			nonPrintable++
		}
	}
	return float64(nonPrintable)/float64(bytesRead) > 0.3
}

// suggestPaths builds a "did you mean" hint for a missing path.
// port of src/tools/read.ts:suggestPaths
func suggestPaths(filePath string) string {
	dir := nodeDirname(filePath)
	base := strings.ToLower(nodeBasename(filePath))

	handle, err := os.Open(dir)
	if err != nil {
		return ""
	}
	defer handle.Close()
	entries, err := handle.ReadDir(-1)
	if err != nil {
		return ""
	}
	// Node's readdir uses libuv's scandir, which sorts entries bytewise
	// (strcmp) on POSIX; Go's File.ReadDir preserves filesystem order.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	matches := make([]string, 0, 3)
	for _, entry := range entries {
		name := entry.Name()
		lower := strings.ToLower(name)
		if !strings.Contains(lower, base) && !strings.Contains(base, lower) {
			continue
		}
		matches = append(matches, filepath.Join(dir, name))
		if len(matches) == 3 {
			break
		}
	}
	if len(matches) == 0 {
		return ""
	}
	return "\n\nDid you mean one of these?\n" + strings.Join(matches, "\n")
}

// formatReadError renders the TS "File not found" / "Error reading file"
// messages.
// port of src/tools/read.ts:formatReadError
func formatReadError(err error, filePath string) string {
	if errors.Is(err, fs.ErrNotExist) {
		return "File not found: " + filePath + suggestPaths(filePath)
	}
	return "Error reading file: " + nodeErrorMessage(err)
}

// nodeErrorMessage renders an OS error the way Node's libuv error string does
// ("ENOENT: no such file or directory, open '/path'"). Unix errno text matches
// libuv's for the common cases; anything unmatched keeps Go's message.
func nodeErrorMessage(err error) string {
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		return err.Error()
	}
	code, description, ok := nodeErrno(pathErr.Err)
	if !ok {
		return err.Error()
	}
	return code + ": " + description + ", " + pathErr.Op + " '" + pathErr.Path + "'"
}

// nodeErrno maps the errno shapes reachable through the read tool.
func nodeErrno(err error) (string, string, bool) {
	if errors.Is(err, fs.ErrPermission) {
		return "EACCES", "permission denied", true
	}
	message := err.Error()
	switch message {
	case "not a directory":
		return "ENOTDIR", "not a directory", true
	case "is a directory":
		return "EISDIR", "illegal operation on a directory", true
	case "too many levels of symbolic links":
		return "ELOOP", "too many symbolic links encountered", true
	case "file name too long":
		return "ENAMETOOLONG", "name too long", true
	case "no space left on device":
		return "ENOSPC", "no space left on device", true
	case "too many open files":
		return "EMFILE", "too many open files", true
	}
	return "", "", false
}

// selectResult is the outcome of selectLines.
// port of src/tools/read.ts:SelectResult
type selectResult struct {
	body             string
	lastReadLine     int
	truncated        bool
	truncatedByBytes bool
}

// errUndefinedLength reproduces the V8 TypeError a fractional offset throws
// when TS indexes lines[1.5].length; the handler's catch turns it into
// "Error reading file: <message>".
var errUndefinedLength = errors.New("Cannot read properties of undefined (reading 'length')")

// selectLines slices lines with offset/limit, capping long lines and total
// bytes. Optionally prefixes opencode-style line numbers (`00001| ...`).
// port of src/tools/read.ts:selectLines
func selectLines(lines []string, offset float64, limit float64, lineNumbers bool) (selectResult, error) {
	start := math.Max(0, offset-1)
	end := math.Min(float64(len(lines)), start+limit)
	raw := make([]string, 0, 16)
	bytes := 0
	truncatedByBytes := false
	startIndex := 0

	// JS indexes lines[1.5] -> undefined, and `.length` on it throws.
	if start < end {
		if start != math.Trunc(start) {
			return selectResult{}, errUndefinedLength
		}
		// start < end <= len(lines), so the conversion is in range.
		startIndex = int(start)
		for i := startIndex; float64(i) < end; i++ {
			line := lines[i]
			if utf16Len(line) > maxLineLength {
				line = utf16Prefix(line, maxLineLength) + "..."
			}
			size := len(line)
			if len(raw) > 0 {
				size++
			}
			if bytes+size > maxBytes {
				truncatedByBytes = true
				break
			}
			bytes += size
			raw = append(raw, line)
		}
	}

	// TS computes start + raw.length even when the window is empty; keep that
	// for the (unobservable) empty case without overflowing on huge offsets.
	startValue := 0
	if start <= float64(math.MaxInt32) {
		startValue = int(start)
	}
	lastReadLine := startValue + len(raw)
	if len(raw) == 0 && offset != 1 {
		return selectResult{body: "", lastReadLine: lastReadLine, truncated: false, truncatedByBytes: truncatedByBytes}, nil
	}
	var body string
	if lineNumbers {
		numbered := make([]string, 0, len(raw))
		for index, line := range raw {
			numbered = append(numbered, tools.FormatNumberedLine(startIndex+index+1, line, true))
		}
		body = strings.Join(numbered, "\n")
	} else {
		body = strings.Join(raw, "\n")
	}

	return selectResult{
		body:             body,
		lastReadLine:     lastReadLine,
		truncated:        lastReadLine < len(lines) || truncatedByBytes,
		truncatedByBytes: truncatedByBytes,
	}, nil
}

// rememberIfFull snapshots content for later re-read diffs, but only when this
// read handed the session the entire file verbatim.
// port of src/tools/read.ts:rememberIfFull
func rememberIfFull(sessionID string, filePath string, hash string, content string, sel selectResult, offset float64, lineNumbers bool) {
	if offset != 1 || lineNumbers || sel.truncated || sel.body != content {
		return
	}
	compression.RememberSnapshot(sessionID, filePath, hash, content)
}

// truncationHint renders the bounce-safe "use offset/limit or force" hint.
// port of src/tools/read.ts:truncationHint
func truncationHint(sel selectResult) string {
	if !sel.truncated {
		return ""
	}
	next := sel.lastReadLine + 1
	if sel.truncatedByBytes {
		return fmt.Sprintf("\n\n(Output truncated at %d bytes. Use offset=%d and limit, or force=true to re-read.)", maxBytes, next)
	}
	return fmt.Sprintf("\n\n(File has more lines. Use offset=%d and limit, or force=true to re-read.)", next)
}

// updateFileCache forwards one delivery to the compression cache. read.ts
// always passes every field, so the Go update mirrors that exactly.
func updateFileCache(ctx context.Context, filePath string, sessionID string, deliveryKey string, hash string, mtimeMs *float64, emittedChars int, content []byte) {
	key := deliveryKey
	chars := emittedChars
	compression.UpdateFileCache(ctx, filePath, sessionID, compression.FileCacheUpdate{
		DeliveryKey:  &key,
		Content:      content,
		Hash:         hash,
		MtimeMs:      mtimeMs,
		EmittedChars: &chars,
	})
}

// textResult wraps one text block.
func textResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text(text)}}
}

// errorResult wraps one text block with isError.
func errorResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text(text)}, IsError: true}
}

// Tool is the cave__read MCP tool definition.
// port of src/tools/read.ts:readTool
var Tool = types.ToolDefinition{
	Name:        "cave__read",
	Annotations: json.RawMessage(`{"readOnlyHint":true}`),
	Description: "Use instead of Read. Returns a stub if the file is unchanged since the last read. Images return as image blocks, PDFs as resources; binary files are rejected.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string","description":"Absolute path"},"offset":{"type":"number","description":"1-indexed start line","default":1},"limit":{"type":"number","default":200},"mode":{"type":"string","enum":["full","signatures","aggressive"],"description":"signatures: TS/JS/Rust signatures only; aggressive: drop comments/blank lines. Both ignore offset/limit.","default":"full"},"line_numbers":{"type":"boolean","description":"Tag lines and append a file_hash/range_checksum footer for cave__edit range mode. Disables codebook compression.","default":false},"force":{"type":"boolean","description":"Bypass dedup cache","default":false}},"required":["file_path"]}`),
	Handler:     handleRead,
}

// handleRead is the cave__read handler. TS writes it as an inline async arrow
// on readTool.handler; it never throws because every failure is caught and
// returned as an isError result.
// port of src/tools/read.ts:readTool.handler
func handleRead(ctx context.Context, args map[string]any) (types.ToolResult, error) {
	filePath := jsString(args["file_path"])
	offset := jsNumber(args["offset"])
	if offset == 0 || math.IsNaN(offset) {
		offset = 1
	}
	limit := jsNumber(args["limit"])
	if limit == 0 || math.IsNaN(limit) {
		limit = 200
	}
	mode := jsString(orDefault(args["mode"], "full"))
	lineNumbers := args["line_numbers"] == true
	force := args["force"] == true
	textDeliveryKey := fmt.Sprintf("text:%s:offset=%s:limit=%s:numbered=%t",
		mode, jsNumberString(offset), jsNumberString(limit), lineNumbers)
	// Session id injected by dispatch from MCP _meta.sessionID.
	// Falls back to "default" for clients that don't send _meta.
	sessionID := compression.DefaultSession
	if raw, ok := args["__sessionId"]; ok && raw != nil {
		sessionID = jsString(raw)
	}

	ext := nodeExtname(filePath)
	mediaDeliveryKey := "media:" + orUnknown(ext)

	fileStat, statErr := os.Stat(filePath)
	if statErr == nil && fileStat.IsDir() {
		if result, handled := listDirectory(ctx, filePath, offset, limit); handled {
			return result, nil
		}
	}

	if imageMime, isImage := sniffImageMime(filePath, ext); isImage {
		imageError := func(err error) types.ToolResult {
			return errorResult("Error reading image: " + nodeErrorMessage(err))
		}
		if !force && compression.IsFileUnchanged(ctx, filePath, sessionID, mediaDeliveryKey) {
			return textResult(compression.ReadStubFor(filePath, sessionID)), nil
		}

		imageStat := fileStat
		if imageStat == nil {
			var err error
			imageStat, err = os.Stat(filePath)
			if err != nil {
				return imageError(err), nil
			}
		}
		if imageStat.Size() > maxImageBytes {
			return errorResult(fmt.Sprintf("Image too large (%d bytes, max %d). Resize or crop before reading.", imageStat.Size(), maxImageBytes)), nil
		}

		// SVG: send as text (it's just XML), keeps the image route for raster only.
		if ext == ".svg" {
			svg, svgHash, err := readUtf8StrictSnapshot(filePath)
			if err != nil {
				return imageError(err), nil
			}
			updateFileCache(ctx, filePath, sessionID, mediaDeliveryKey, svgHash, mtimeMillis(imageStat), utf16Len(svg), []byte(svg))
			return textResult(svg), nil
		}

		buffer, err := os.ReadFile(filePath)
		if err != nil {
			return imageError(err), nil
		}
		data := base64.StdEncoding.EncodeToString(buffer)
		updateFileCache(ctx, filePath, sessionID, mediaDeliveryKey, compression.HashContent(buffer), mtimeMillis(imageStat), len(data), buffer)
		return types.ToolResult{Content: []types.Content{types.Image(data, imageMime)}}, nil
	}

	if ext == ".pdf" {
		pdfError := func(err error) types.ToolResult {
			return errorResult("Error reading PDF: " + nodeErrorMessage(err))
		}
		if !force && compression.IsFileUnchanged(ctx, filePath, sessionID, mediaDeliveryKey) {
			return textResult(compression.ReadStubFor(filePath, sessionID)), nil
		}

		pdfStat, err := os.Stat(filePath)
		if err != nil {
			return pdfError(err), nil
		}
		if pdfStat.Size() > maxPDFBytes {
			return errorResult(fmt.Sprintf("PDF too large (%d bytes, max %d).", pdfStat.Size(), maxPDFBytes)), nil
		}

		buffer, err := os.ReadFile(filePath)
		if err != nil {
			return pdfError(err), nil
		}
		blob := base64.StdEncoding.EncodeToString(buffer)
		updateFileCache(ctx, filePath, sessionID, mediaDeliveryKey, compression.HashContent(buffer), mtimeMillis(pdfStat), len(blob), buffer)
		return types.ToolResult{Content: []types.Content{types.ResourceBlock("file://"+filePath, "application/pdf", blob)}}, nil
	}

	// Guard every text path against binary content.
	if isBinaryFile(filePath, ext) {
		return errorResult("Cannot read binary file: " + filePath), nil
	}

	// Plain full re-read of a file whose last full content this session
	// received is snapshotted: answer with a diff when it changed. Checked
	// before the recently-edited bypass on purpose — a diff is lossless, and
	// re-reading right after an edit is where it saves the most.
	if mode == "full" && offset == 1 && !lineNumbers && !force && !compression.IsSubagentContext(ctx) {
		if previous := compression.GetSnapshot(sessionID, filePath); previous != nil {
			// Errors fall through; the normal path reports read errors.
			if content, contentHash, err := readUtf8StrictSnapshot(filePath); err == nil {
				if contentHash != previous.Hash {
					if sel, selErr := selectLines(strings.Split(content, "\n"), 1, limit, false); selErr == nil {
						var diff *string
						if !sel.truncated && sel.body == content {
							diff = compression.UnifiedDiff(previous.Content, content)
						}
						if diff != nil && float64(utf16Len(*diff)) < diffMaxRatio*float64(utf16Len(content)) {
							text := diffHeader + "\n" + *diff
							compression.RememberSnapshot(sessionID, filePath, contentHash, content)
							updateFileCache(ctx, filePath, sessionID, textDeliveryKey, contentHash, fileStatMtime(fileStat), utf16Len(text), []byte(content))
							compression.RecordRead(ctx, filePath, false, int64(utf16Len(text)))
							return textResult(text), nil
						}
					}
				}
			}
		}
	}

	if compression.ShouldForceFull(ctx, filePath) {
		content, contentHash, err := readUtf8StrictSnapshot(filePath)
		if err != nil {
			return errorResult(formatReadError(err, filePath)), nil
		}
		lines := strings.Split(content, "\n")
		sel, err := selectLines(lines, offset, limit, lineNumbers)
		if err != nil {
			return errorResult(formatReadError(err, filePath)), nil
		}
		if sel.body == "" && offset != 1 {
			return errorResult(fmt.Sprintf("Offset %s is out of range", jsNumberString(offset))), nil
		}
		text := sel.body + truncationHint(sel)
		rememberIfFull(sessionID, filePath, contentHash, content, sel, offset, lineNumbers)
		updateFileCache(ctx, filePath, sessionID, textDeliveryKey, contentHash, fileStatMtime(fileStat), utf16Len(text), []byte(content))
		compression.RecordRead(ctx, filePath, false, int64(utf16Len(text)))
		return textResult(text), nil
	}

	if !force && compression.IsFileUnchanged(ctx, filePath, sessionID, textDeliveryKey) {
		stub := compression.ReadStubFor(filePath, sessionID)
		compression.RecordRead(ctx, filePath, false, int64(utf16Len(stub)))
		return textResult(stub), nil
	}

	content, contentHash, err := readUtf8StrictSnapshot(filePath)
	if err != nil {
		return errorResult(formatReadError(err, filePath)), nil
	}

	if mode == "aggressive" {
		compressedContent := compression.AggressiveCompress(content, ext)
		output := content
		if utf16Len(compressedContent) < utf16Len(content) {
			output = compressedContent
		}
		compressed := compression.ApplyBudget(ctx, output, "read")
		wasCompressed := utf16Len(compressed) < utf16Len(output)
		updateFileCache(ctx, filePath, sessionID, textDeliveryKey, contentHash, fileStatMtime(fileStat), utf16Len(compressed), []byte(content))
		compression.RecordRead(ctx, filePath, wasCompressed, int64(utf16Len(compressed)))
		return textResult(compressed), nil
	}

	if mode == "signatures" {
		signatures := compression.FormatSignatures(content, ext)
		output := signatures
		if output == "" {
			output = "(no signatures extracted for this file type)"
		}
		compressed := compression.ApplyBudget(ctx, output, "read")
		wasCompressed := utf16Len(compressed) < utf16Len(output)
		updateFileCache(ctx, filePath, sessionID, textDeliveryKey, contentHash, fileStatMtime(fileStat), utf16Len(compressed), []byte(content))
		compression.RecordRead(ctx, filePath, wasCompressed, int64(utf16Len(compressed)))
		return textResult(compressed), nil
	}

	lines := strings.Split(content, "\n")
	sel, err := selectLines(lines, offset, limit, lineNumbers)
	if err != nil {
		return errorResult(formatReadError(err, filePath)), nil
	}
	if sel.body == "" && offset != 1 {
		return errorResult(fmt.Sprintf("Offset %s is out of range", jsNumberString(offset))), nil
	}
	skipPipeline := compression.FitsBudget(sel.body, "read", maxBytes)

	var outText string
	if lineNumbers {
		// Numbered lines defeat cross-file codebook matching; skip it.
		outText = sel.body
		// Footer for line-range cave__edit: file_hash + range_checksum + line tags above.
		hash := contentHash
		totalLines := len(lines)
		first := int(math.Max(1, offset))
		last := sel.lastReadLine
		windowChecksum := ""
		if last >= first {
			windowChecksum = tools.RangeChecksum(tools.ToLfLines(content), first, last)
		}
		outText += "\n--- cave_edit_meta ---\n" +
			"path: " + filePath + "\n" +
			fmt.Sprintf("lines: %d-%d of %d\n", first, last, totalLines)
		if hash != "" {
			outText += "file_hash: " + hash[:16] + "\n"
		}
		if windowChecksum != "" {
			outText += fmt.Sprintf("range_checksum: %d-%d:%s\n", first, last, windowChecksum)
		}
		outText += "// cave__edit: start_line/end_line/content + expected_hash and/or expected_range_checksum\n"
	} else if skipPipeline {
		outText = sel.body
	} else {
		compression.AddCodebookFile(filePath, content)
		codebookText, legend, _ := compression.CompressWithCodebook(sel.body)
		if legend != "" {
			outText = codebookText + "\n\n" + legend
		} else {
			outText = codebookText
		}
	}
	outText += truncationHint(sel)

	if skipPipeline {
		rememberIfFull(sessionID, filePath, contentHash, content, sel, offset, lineNumbers)
		updateFileCache(ctx, filePath, sessionID, textDeliveryKey, contentHash, fileStatMtime(fileStat), utf16Len(outText), []byte(content))
		compression.RecordRead(ctx, filePath, false, int64(utf16Len(outText)))
		return textResult(outText), nil
	}

	compressed := compression.ApplyBudget(ctx, outText, "read")
	wasCompressed := utf16Len(compressed) < utf16Len(sel.body)
	updateFileCache(ctx, filePath, sessionID, textDeliveryKey, contentHash, fileStatMtime(fileStat), utf16Len(compressed), []byte(content))
	compression.RecordRead(ctx, filePath, wasCompressed, int64(utf16Len(compressed)))
	return textResult(compressed), nil
}

// listDirectory renders a directory listing with offset/limit pagination.
// It is the body of read.ts's `if (fileStat.isDirectory())` block; handled is
// false when readdir fails so the caller falls through to the file read path
// exactly like the TS try/catch.
func listDirectory(ctx context.Context, filePath string, offset float64, limit float64) (types.ToolResult, bool) {
	handle, err := os.Open(filePath)
	if err != nil {
		return types.ToolResult{}, false
	}
	defer handle.Close()
	entries, err := handle.ReadDir(-1)
	if err != nil {
		return types.ToolResult{}, false
	}

	rows := make([]directoryRow, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		fullPath := filepath.Join(filePath, name)
		target, err := filepath.EvalSymlinks(fullPath)
		if err != nil {
			continue
		}
		if !rt.ContainsPath(filePath, target) {
			continue
		}
		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}
		if info.IsDir() {
			rows = append(rows, directoryRow{name: name + "/", kind: "directory"})
		} else if info.Mode().IsRegular() {
			rows = append(rows, directoryRow{name: name, kind: "file"})
		}
	}
	sort.SliceStable(rows, func(i int, j int) bool {
		if rows[i].kind == rows[j].kind {
			return jsLocaleCompare(rows[i].name, rows[j].name) < 0
		}
		return rows[i].kind == "directory"
	})

	from, to := jsSliceBounds(offset-1, offset-1+limit, len(rows))
	selected := make([]string, 0, to-from)
	for _, row := range rows[from:to] {
		selected = append(selected, row.name)
	}
	if len(selected) == 0 && offset != 1 {
		return errorResult(fmt.Sprintf("Offset %s is out of range", jsNumberString(offset))), true
	}

	text := "(empty directory)"
	if len(selected) > 0 {
		text = strings.Join(selected, "\n")
	}
	if offset-1+float64(len(selected)) < float64(len(rows)) {
		text += fmt.Sprintf("\n\n(Directory has more entries. Use 'offset'=%s)", jsNumberString(offset+float64(len(selected))))
	}
	return textResult(compression.ApplyBudget(ctx, text, "read")), true
}

// directoryRow is one directory listing entry.
type directoryRow struct {
	name string
	kind string // "directory" or "file"
}

func orDefault(value any, fallback string) any {
	if isFalsy(value) {
		return fallback
	}
	return value
}

func orUnknown(ext string) string {
	if ext == "" {
		return "unknown"
	}
	return ext
}

// fileStatMtime renders Node's stat.mtimeMs for an optional stat result.
func fileStatMtime(info os.FileInfo) *float64 {
	if info == nil {
		return nil
	}
	return mtimeMillis(info)
}

func mtimeMillis(info os.FileInfo) *float64 {
	value := float64(info.ModTime().UnixNano()) / 1e6
	return &value
}
