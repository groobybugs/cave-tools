// CLI-only tool commands: schema-driven flag parsing, help rendering, result
// formatting, raw passthrough and the filter pipeline.
//
// Port of src/cli-tools.ts. The TS file imports dispatch.js and
// daemon-client.js lazily; the Go port reaches both through integration.go.
package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/jsjson"
	"github.com/groobybugs/cave-tools/go/internal/jsre"
	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/tools/bash"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// UsageError marks a command-line usage problem (exit 2).
// port of src/cli-tools.ts:UsageError
type UsageError struct{ Message string }

func (e *UsageError) Error() string { return e.Message }

func usageErrorf(format string, args ...any) *UsageError {
	return &UsageError{Message: fmt.Sprintf(format, args...)}
}

// commandTools take the whole command line after the first positional.
// port of src/cli-tools.ts:COMMAND_TOOLS
var commandTools = map[string]struct{}{
	"cave__bash":       {},
	"cave__bash_start": {},
}

// defaultCLIDescription is the description CLI bash calls get.
// port of src/cli-tools.ts:DEFAULT_CLI_DESCRIPTION
const defaultCLIDescription = "cave-tools CLI"

// shellSafe matches args that need no quoting.
// port of src/cli-tools.ts:SHELL_SAFE
var shellSafe = jsre.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`, "")

// decimalLiteralRE is the JS StrDecimalLiteral grammar; strconv.ParseFloat is
// looser (it accepts "inf", "nan", hex floats and underscores).
var decimalLiteralRE = jsre.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`, "")

// propSchema is the slice of a JSON-schema property the CLI reads.
type propSchema struct {
	Type        string
	Description string
	Default     json.RawMessage
	HasDefault  bool
	Enum        []any
	HasEnum     bool
	Items       *propSchema
}

// toolSchema preserves property declaration order: TS uses Object.keys(props)
// for flags, positional defaults and help rows, and Go maps would randomize it.
type toolSchema struct {
	props    []string
	byName   map[string]*propSchema
	required []string
}

func (s *toolSchema) prop(name string) *propSchema { return s.byName[name] }

// parseToolSchema decodes an inputSchema while keeping property order.
func parseToolSchema(raw json.RawMessage) toolSchema {
	shape := toolSchema{byName: map[string]*propSchema{}}
	if len(bytes.TrimSpace(raw)) == 0 {
		return shape
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return shape
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return shape
		}
		key, ok := keyToken.(string)
		if !ok {
			return shape
		}
		switch key {
		case "properties":
			value, err := decoder.Token()
			if err != nil || value != json.Delim('{') {
				var discard json.RawMessage
				_ = decoder.Decode(&discard)
				continue
			}
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return shape
				}
				name, ok := nameToken.(string)
				if !ok {
					return shape
				}
				var rawProp map[string]json.RawMessage
				if err := decoder.Decode(&rawProp); err != nil {
					return shape
				}
				shape.props = append(shape.props, name)
				shape.byName[name] = parsePropSchema(rawProp)
			}
			_, _ = decoder.Token() // closing '}'
		case "required":
			var required []string
			if err := decoder.Decode(&required); err == nil {
				shape.required = required
			}
		default:
			var discard json.RawMessage
			if err := decoder.Decode(&discard); err != nil {
				return shape
			}
		}
	}
	return shape
}

func parsePropSchema(raw map[string]json.RawMessage) *propSchema {
	prop := &propSchema{}
	if value, ok := raw["type"]; ok {
		var text string
		if json.Unmarshal(value, &text) == nil {
			prop.Type = text
		}
	}
	if value, ok := raw["description"]; ok {
		var text string
		if json.Unmarshal(value, &text) == nil {
			prop.Description = text
		}
	}
	if value, ok := raw["default"]; ok {
		prop.Default = append(json.RawMessage(nil), value...)
		prop.HasDefault = true
	}
	if value, ok := raw["enum"]; ok {
		prop.HasEnum = true
		_ = json.Unmarshal(value, &prop.Enum)
	}
	if value, ok := raw["items"]; ok {
		var items map[string]json.RawMessage
		if json.Unmarshal(value, &items) == nil {
			prop.Items = parsePropSchema(items)
		}
	}
	return prop
}

// schemaOf returns the tool's parsed input schema.
// port of src/cli-tools.ts:schemaOf
func schemaOf(tool types.ToolDefinition) toolSchema {
	return parseToolSchema(tool.InputSchema)
}

// joinCommandArgs joins CLI args into one shell command without letting the
// shell re-split or interpret them: every unsafe arg is POSIX single-quoted.
// port of src/cli-tools.ts:joinCommandArgs
func joinCommandArgs(argv []string, platform ...string) (string, error) {
	resolved := "linux"
	switch {
	case len(platform) > 0:
		resolved = platform[0]
	case isWindowsPlatform():
		resolved = "win32"
	}
	if resolved == "win32" {
		return "", usageErrorf(`on Windows pass the command as one quoted string, e.g. "dir | findstr x"`)
	}
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		if shellSafe.MatchString(arg) {
			parts = append(parts, arg)
			continue
		}
		parts = append(parts, "'"+strings.ReplaceAll(arg, "'", `'\''`)+"'")
	}
	return strings.Join(parts, " "), nil
}

// parseBoolean mirrors the CLI's true/1 and false/0 spellings.
// port of src/cli-tools.ts:parseBoolean
func parseBoolean(flag, value string) (bool, error) {
	switch value {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	}
	return false, usageErrorf(`--%s expects true or false, got "%s"`, flag, value)
}

// jsToNumber mirrors Number(string) for CLI flag values: JS whitespace trim,
// empty string 0, Infinity spellings and unsigned 0x/0b/0o prefixes.
func jsToNumber(value string) float64 {
	s := jsre.Trim(value)
	if s == "" {
		return 0
	}
	switch s {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	base := 0
	switch {
	case strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X"):
		base = 16
	case strings.HasPrefix(s, "0b") || strings.HasPrefix(s, "0B"):
		base = 2
	case strings.HasPrefix(s, "0o") || strings.HasPrefix(s, "0O"):
		base = 8
	}
	if base != 0 {
		number, ok := new(big.Int).SetString(s[2:], base)
		if !ok {
			return math.NaN()
		}
		value, _ := new(big.Float).SetInt(number).Float64()
		return value
	}
	if !decimalLiteralRE.MatchString(s) {
		return math.NaN()
	}
	number, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if rangeErr, ok := err.(*strconv.NumError); ok && rangeErr.Err == strconv.ErrRange {
			return number
		}
		return math.NaN()
	}
	return number
}

// coerceFlagValue converts one CLI string to the property's JS type.
// port of src/cli-tools.ts:coerce
func coerceFlagValue(flag string, prop *propSchema, value string) (any, error) {
	switch prop.Type {
	case "number":
		number := jsToNumber(value)
		if jsre.Trim(value) == "" || math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, usageErrorf(`--%s expects a number, got "%s"`, flag, value)
		}
		return number, nil
	case "boolean":
		return parseBoolean(flag, value)
	case "array":
		var parsed any
		if json.Unmarshal([]byte(value), &parsed) == nil {
			if array, ok := parsed.([]any); ok {
				return array, nil
			}
		}
		if prop.Items != nil && prop.Items.Type == "string" {
			return []any{value}, nil
		}
		return nil, usageErrorf("--%s expects a JSON array", flag)
	case "object":
		var parsed any
		if json.Unmarshal([]byte(value), &parsed) == nil {
			if object, ok := parsed.(map[string]any); ok && object != nil {
				return object, nil
			}
		}
		return nil, usageErrorf("--%s expects a JSON object", flag)
	default:
		return value, nil
	}
}

// parsedToolArgs is the parsed form of one `<tool> [positional...]` command.
// port of src/cli-tools.ts:ParsedToolArgs
type parsedToolArgs struct {
	Args       map[string]any
	SessionID  string
	HasSession bool
	Help       bool
}

// rawFlag keeps flag insertion order: TS's Object.entries(raw) preserves it
// and stdin resolution depends on which "-" value comes first.
type rawFlag struct {
	name    string
	value   string
	boolean bool
	isBool  bool
}

func setRawFlag(flags []rawFlag, flag rawFlag) []rawFlag {
	for i := range flags {
		if flags[i].name == flag.name {
			flags[i] = flag
			return flags
		}
	}
	return append(flags, flag)
}

// parseToolArgs parses the flags and positionals of one tool invocation.
// port of src/cli-tools.ts:parseToolArgs
func parseToolArgs(tool types.ToolDefinition, argv []string, readStdin func() (string, error)) (parsedToolArgs, error) {
	schema := schemaOf(tool)
	_, isCommandTool := commandTools[tool.Name]
	flags := []rawFlag{}
	positionals := []string{}
	sessionID := ""
	hasSession := false
	help := false
	flagsDone := false

	for i := 0; i < len(argv); i++ {
		token := argv[i]
		if !flagsDone && token == "--" {
			flagsDone = true
			continue
		}
		if flagsDone || !strings.HasPrefix(token, "--") {
			positionals = append(positionals, token)
			if isCommandTool {
				flagsDone = true
			}
			continue
		}
		eq := strings.IndexByte(token, '=')
		flag := token[2:]
		inline := ""
		hasInline := false
		if eq != -1 {
			flag = token[2:eq]
			inline = token[eq+1:]
			hasInline = true
		}
		name := strings.ReplaceAll(flag, "-", "_")
		takeValue := func() (string, error) {
			if hasInline {
				return inline, nil
			}
			if i+1 >= len(argv) {
				return "", usageErrorf("--%s needs a value", flag)
			}
			i++
			return argv[i], nil
		}
		if name == "help" {
			help = true
			continue
		}
		if name == "session" {
			value, err := takeValue()
			if err != nil {
				return parsedToolArgs{}, err
			}
			sessionID = value
			hasSession = true
			continue
		}
		prop := schema.prop(name)
		if prop == nil {
			return parsedToolArgs{}, usageErrorf("unknown flag --%s", flag)
		}
		if prop.Type == "boolean" {
			var value bool
			if hasInline {
				parsed, err := parseBoolean(flag, inline)
				if err != nil {
					return parsedToolArgs{}, err
				}
				value = parsed
			} else if i+1 < len(argv) && (argv[i+1] == "true" || argv[i+1] == "false") {
				i++
				value = argv[i] == "true"
			} else {
				value = true
			}
			flags = setRawFlag(flags, rawFlag{name: name, boolean: value, isBool: true})
			continue
		}
		value, err := takeValue()
		if err != nil {
			return parsedToolArgs{}, err
		}
		flags = setRawFlag(flags, rawFlag{name: name, value: value})
	}
	if help {
		return parsedToolArgs{Args: map[string]any{}, SessionID: sessionID, HasSession: hasSession, Help: true}, nil
	}

	args := map[string]any{}
	stdinUsed := false
	resolve := func(value string) (string, error) {
		if value != "-" {
			return value, nil
		}
		if stdinUsed {
			return "", usageErrorf("only one value can be read from stdin (-)")
		}
		stdinUsed = true
		return readStdin()
	}

	for _, flag := range flags {
		if flag.isBool {
			args[flag.name] = flag.boolean
			continue
		}
		value, err := resolve(flag.value)
		if err != nil {
			return parsedToolArgs{}, err
		}
		coerced, err := coerceFlagValue(flag.name, schema.prop(flag.name), value)
		if err != nil {
			return parsedToolArgs{}, err
		}
		args[flag.name] = coerced
	}

	if isCommandTool {
		if len(positionals) > 0 {
			if _, exists := args["command"]; exists {
				return parsedToolArgs{}, usageErrorf("give the command either positionally or with --command")
			}
			if len(positionals) == 1 {
				value, err := resolve(positionals[0])
				if err != nil {
					return parsedToolArgs{}, err
				}
				args["command"] = value
			} else {
				joined, err := joinCommandArgs(positionals)
				if err != nil {
					return parsedToolArgs{}, err
				}
				args["command"] = joined
			}
		}
		if _, exists := args["description"]; !exists {
			args["description"] = defaultCLIDescription
		}
	} else {
		// Positionals fill required props, then optional ones, in schema order.
		order := make([]string, 0, len(schema.props))
		order = append(order, schema.required...)
		for _, key := range schema.props {
			if !containsString(schema.required, key) {
				order = append(order, key)
			}
		}
		p := 0
		for _, key := range order {
			if _, exists := args[key]; exists {
				continue
			}
			if p >= len(positionals) {
				break
			}
			prop := schema.prop(key)
			if prop != nil && prop.Type == "array" {
				rest := make([]string, 0, len(positionals)-p)
				for j := p; j < len(positionals); j++ {
					value, err := resolve(positionals[j])
					if err != nil {
						return parsedToolArgs{}, err
					}
					rest = append(rest, value)
				}
				if prop.Items != nil && prop.Items.Type == "string" {
					values := make([]any, len(rest))
					for i, value := range rest {
						values[i] = value
					}
					args[key] = values
				} else {
					items := &propSchema{}
					if prop.Items != nil {
						items = prop.Items
					}
					values := make([]any, 0, len(rest))
					for _, value := range rest {
						coerced, err := coerceFlagValue(key, items, value)
						if err != nil {
							return parsedToolArgs{}, err
						}
						values = append(values, coerced)
					}
					args[key] = values
				}
				p = len(positionals)
				continue
			}
			value, err := resolve(positionals[p])
			p++
			if err != nil {
				return parsedToolArgs{}, err
			}
			coerced, err := coerceFlagValue(key, prop, value)
			if err != nil {
				return parsedToolArgs{}, err
			}
			args[key] = coerced
		}
		if p < len(positionals) {
			return parsedToolArgs{}, usageErrorf(`unexpected argument "%s"`, positionals[p])
		}
	}

	missing := make([]string, 0, len(schema.required))
	for _, key := range schema.required {
		if _, exists := args[key]; !exists {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return parsedToolArgs{}, usageErrorf("missing required: %s", strings.Join(missing, ", "))
	}
	return parsedToolArgs{Args: args, SessionID: sessionID, HasSession: hasSession}, nil
}

// shortToolName strips the MCP namespace.
// port of src/cli-tools.ts:shortName
func shortToolName(name string) string { return strings.TrimPrefix(name, "cave__") }

// usageLine renders the one-line usage for a tool.
// port of src/cli-tools.ts:usageLine
func usageLine(tool types.ToolDefinition, schema toolSchema) string {
	_, isCommandTool := commandTools[tool.Name]
	parts := []string{"cave-tools", shortToolName(tool.Name)}
	if isCommandTool {
		parts = append(parts, "<command...>")
	} else {
		for _, key := range schema.required {
			prop := schema.prop(key)
			if prop != nil && prop.Type == "array" {
				parts = append(parts, "<"+key+"...>")
			} else {
				parts = append(parts, "<"+key+">")
			}
		}
	}
	for _, key := range schema.props {
		if isCommandTool {
			if key == "command" {
				continue
			}
		} else if containsString(schema.required, key) {
			continue
		}
		prop := schema.prop(key)
		if prop != nil && prop.Type == "boolean" {
			parts = append(parts, "[--"+key+"]")
			continue
		}
		kind := "value"
		if prop != nil && prop.Type != "" {
			kind = prop.Type
		}
		parts = append(parts, "[--"+key+" <"+kind+">]")
	}
	return strings.Join(parts, " ")
}

// compactJSON is JSON.stringify(parsed default) for the scalar defaults the
// tool schemas declare: the authored literal is already compact.
func compactJSON(raw json.RawMessage) string {
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, raw); err != nil {
		return string(raw)
	}
	return buffer.String()
}

// jsDisplayString mirrors String(value) for enum values.
func jsDisplayString(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case json.Number:
		return string(v)
	default:
		return fmt.Sprint(v)
	}
}

// toolHelp renders `cave-tools help <tool>`.
// port of src/cli-tools.ts:toolHelp
func toolHelp(tool types.ToolDefinition) string {
	schema := schemaOf(tool)
	_, isCommandTool := commandTools[tool.Name]
	rows := make([]string, 0, len(schema.props))
	for _, key := range schema.props {
		prop := schema.prop(key)
		bits := []string{"any"}
		if prop != nil && prop.Type != "" {
			bits[0] = prop.Type
		}
		if containsString(schema.required, key) && !(isCommandTool && key == "description") {
			bits = append(bits, "required")
		}
		if prop != nil && prop.HasDefault {
			bits = append(bits, "default "+compactJSON(prop.Default))
		}
		if prop != nil && prop.HasEnum {
			values := make([]string, len(prop.Enum))
			for i, value := range prop.Enum {
				values[i] = jsDisplayString(value)
			}
			bits = append(bits, "one of "+strings.Join(values, "|"))
		}
		row := "  --" + padEndJS(key, 24) + " " + strings.Join(bits, ", ")
		if prop != nil && prop.Description != "" {
			row += " — " + prop.Description
		}
		rows = append(rows, row)
	}
	lines := []string{
		shortToolName(tool.Name) + ": " + tool.Description,
		"",
		"usage: " + usageLine(tool, schema),
		"",
	}
	lines = append(lines, rows...)
	lines = append(lines, "")
	if isCommandTool {
		lines = append(lines, `One argument runs verbatim as shell (e.g. "ls | wc -l"); several arguments are each quoted, so shell syntax inside them stays literal.`)
	} else {
		lines = append(lines, "Positionals fill required params, then optional ones, in the order above.")
	}
	lines = append(lines, "A value of - reads that param from stdin. --session <id> reuses read state across calls (needs CAVE_TOOLS_DAEMON=1).")
	return strings.Join(lines, "\n")
}

// firstSentence ports `(description ?? "").split(/(?<=\.)\s/)[0]`: the first
// chunk ends at a dot followed by JS whitespace.
func firstSentence(description string) string {
	for index := 0; index < len(description); index++ {
		if description[index] != '.' {
			continue
		}
		after := index + 1
		if after >= len(description) {
			return description
		}
		next, _ := utf8.DecodeRuneInString(description[after:])
		if jsre.IsSpace(next) {
			return description[:after]
		}
	}
	return description
}

// generalHelp renders `cave-tools help`.
// port of src/cli-tools.ts:generalHelp
func generalHelp() (string, error) {
	lines := []string{"cave-tools <tool> [args...] [--param value...]   (run `cave-tools help <tool>` for params)", ""}
	for _, name := range toolNamesRuntime() {
		tool, err := loadToolRuntime(name)
		if err != nil {
			return "", err
		}
		lines = append(lines, "  "+padEndJS(shortToolName(name), 12)+" "+firstSentence(tool.Description))
	}
	lines = append(lines,
		"",
		"The status tool runs as `cave-tools tool status` (`cave-tools status` is the savings report).",
		"Other commands: mcp, status, jobs, bins, init, raw <command...> (no rewrite or compression), filter --json (compress given output).",
	)
	return strings.Join(lines, "\n"), nil
}

// base64ByteLength mirrors Buffer.from(data, "base64").length: non-alphabet
// characters are ignored and padding is optional.
func base64ByteLength(data string) int {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '+', r == '/', r == '=':
			return r
		default:
			return -1
		}
	}, data)
	if decoded, err := base64.StdEncoding.DecodeString(cleaned); err == nil {
		return len(decoded)
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(cleaned, "=")); err == nil {
		return len(decoded)
	}
	return 0
}

// formatResult ports the CLI's content-block renderer.
// port of src/cli-tools.ts:formatResult
func formatResult(result types.ToolResult) string {
	parts := make([]string, 0, len(result.Content))
	for _, block := range result.Content {
		switch content := block.(type) {
		case types.TextContent:
			parts = append(parts, content.Text)
		case types.ImageContent:
			parts = append(parts, fmt.Sprintf("[image %s %d B]", content.MimeType, base64ByteLength(content.Data)))
		case types.ResourceContent:
			mimeType := content.Resource.MimeType
			if mimeType == "" {
				mimeType = "unknown"
			}
			parts = append(parts, fmt.Sprintf("[resource %s %d B]", mimeType, base64ByteLength(content.Resource.Blob)))
		default:
			parts = append(parts, "[unknown]")
		}
	}
	return strings.Join(parts, "\n")
}

// readAllStdin reads stdin the way TS does with process.stdin.setEncoding("utf8"):
// invalid UTF-8 becomes U+FFFD once per maximal invalid subpart, matching
// Node's StringDecoder for both whole-buffer and chunk-split input.
func readAllStdin(stdin io.Reader) (string, error) {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", err
	}
	return rt.DecodeUTF8Replace(data), nil
}

// finish is the controlled CLI exit funnel: persist the last tool failure
// best-effort before writing, then report the exit code to the caller. MCP and
// daemon paths never call finish (they rely on scheduled background flushes).
// port of src/cli-tools.ts:finish
func finish(stream io.Writer, text string, code int) int {
	rt.FlushToolErrorQueue()
	out := text
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	_, _ = io.WriteString(stream, out)
	return code
}

// newCLISessionID mirrors `cli-${process.pid}-${randomBytes(4).toString("hex")}`.
func newCLISessionID() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("cli-%d-00000000", os.Getpid())
	}
	return fmt.Sprintf("cli-%d-%s", os.Getpid(), hex.EncodeToString(buf[:]))
}

// jsIntValue converts a JS number-ish value to int.
func jsIntValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		parsed, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return 0
		}
		return int(parsed)
	default:
		return 0
	}
}

// runToolCommand runs one MCP tool in-process (or through the daemon).
// Exit codes: 0 ok, 1 tool error, 2 usage error.
// port of src/cli-tools.ts:runToolCommand
func runToolCommand(ctx context.Context, short *string, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	compression.SuppressSubagentEnvLog()
	shortName := ""
	if short != nil {
		shortName = *short
	}
	name := "cave__" + shortName
	if short == nil || !isToolNameRuntime(name) {
		return finish(stderr, fmt.Sprintf("cave-tools: unknown tool \"%s\". Run `cave-tools help` for the list.", shortName), 2)
	}
	tool, err := loadToolRuntime(name)
	if err != nil {
		return finish(stderr, fmt.Sprintf("cave-tools %s: %s", shortName, err.Error()), 1)
	}
	parsed, err := parseToolArgs(tool, argv, func() (string, error) { return readAllStdin(stdin) })
	if err != nil {
		var usageErr *UsageError
		if !errors.As(err, &usageErr) {
			return finish(stderr, fmt.Sprintf("cave-tools %s: %s", shortName, err.Error()), 1)
		}
		return finish(stderr, fmt.Sprintf("cave-tools %s: %s\nusage: %s", shortName, usageErr.Message, usageLine(tool, schemaOf(tool))), 2)
	}
	if parsed.Help {
		return finish(stdout, toolHelp(tool), 0)
	}
	if name == "cave__bash" && os.Getenv("CAVE_RAW") == "1" {
		command, _ := parsed.Args["command"].(string)
		var workdir *string
		if value, exists := parsed.Args["workdir"]; exists {
			if text, ok := value.(string); ok {
				workdir = &text
			}
		}
		return runRaw(ctx, command, workdir, stdin, stdout, stderr)
	}
	if name == "cave__bash" {
		parsed.Args["__cli"] = true
	}
	sessionID := parsed.SessionID
	if !parsed.HasSession {
		sessionID = newCLISessionID()
	}
	var result types.ToolResult
	ranLocally := false
	outcome := daemonCallOutcome{}
	if daemonEnabledRuntime() {
		outcome, err = daemonCallRuntime(ctx, name, parsed.Args, rt.LocalContext(sessionID), parsed.HasSession)
	}
	if err == nil && !outcome.Sent {
		ranLocally = true
		result, err = callToolRuntime(ctx, name, parsed.Args, &rt.CallContext{SessionID: sessionID})
	}
	if err != nil {
		if ranLocally {
			compression.FlushStats()
		}
		return finish(stderr, fmt.Sprintf("cave-tools %s: %s", shortName, err.Error()), 1)
	}
	if ranLocally {
		compression.FlushStats()
	}
	if outcome.Sent {
		result = outcome.Result
	}
	if meta, ok := result.Meta["caveCli"].(map[string]any); ok {
		metaStderr, _ := meta["stderr"].(string)
		if metaStderr != "" {
			if !strings.HasSuffix(metaStderr, "\n") {
				metaStderr += "\n"
			}
			_, _ = io.WriteString(stderr, metaStderr)
		}
		return finish(stdout, formatResult(result), jsIntValue(meta["exitCode"]))
	}
	if result.IsError {
		return finish(stderr, formatResult(result), 1)
	}
	return finish(stdout, formatResult(result), 0)
}

// runHelpCommand ports `cave-tools help [tool]`.
// port of src/cli-tools.ts:runHelpCommand
func runHelpCommand(short *string, stdout, stderr io.Writer) int {
	if short == nil {
		text, err := generalHelp()
		if err != nil {
			return finish(stderr, "cave-tools help: "+err.Error(), 1)
		}
		return finish(stdout, text, 0)
	}
	name := "cave__" + *short
	if !isToolNameRuntime(name) {
		return finish(stderr, fmt.Sprintf("cave-tools: unknown tool \"%s\". Run `cave-tools help` for the list.", *short), 2)
	}
	tool, err := loadToolRuntime(name)
	if err != nil {
		return finish(stderr, "cave-tools help: "+err.Error(), 1)
	}
	return finish(stdout, toolHelp(tool), 0)
}

// runRawCommand ports `cave-tools raw [--] <command...>`: one arg runs
// verbatim as shell, several are quoted individually.
// port of src/cli-tools.ts:runRawCommand
func runRawCommand(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	rest := argv
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return finish(stderr, "usage: cave-tools raw [--] <command...>", 2)
	}
	var command string
	if len(rest) == 1 {
		command = rest[0]
	} else {
		joined, err := joinCommandArgs(rest)
		if err != nil {
			return finish(stderr, "cave-tools raw: "+err.Error(), 2)
		}
		command = joined
	}
	return runRaw(ctx, command, nil, stdin, stdout, stderr)
}

// filteredOutputJSON preserves JSON.stringify's stdout,stderr key order.
type filteredOutputJSON struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

// marshalJS serializes with JSON.stringify's no-HTML-escaping behavior and no
// trailing newline.
func marshalJS(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

// v8JSONNumber converts one JSON number token to the float64 JSON.parse
// yields. V8 never throws on magnitude: overflow becomes ±Inf and underflow
// becomes ±0, so strconv.ErrRange is the expected result, not a failure.
func v8JSONNumber(number json.Number) float64 {
	value, err := strconv.ParseFloat(string(number), 64)
	if err != nil {
		var numErr *strconv.NumError
		if errors.As(err, &numErr) && numErr.Err == strconv.ErrRange {
			return value
		}
		return 0
	}
	return value
}

// convertV8JSONNumbers rewrites every json.Number in a decoded document to the
// float64 JSON.parse would have produced.
func convertV8JSONNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		return v8JSONNumber(typed)
	case []any:
		for i := range typed {
			typed[i] = convertV8JSONNumbers(typed[i])
		}
		return typed
	case map[string]any:
		for key := range typed {
			typed[key] = convertV8JSONNumbers(typed[key])
		}
		return typed
	default:
		return value
	}
}

// parseV8JSON decodes JSON with JSON.parse's acceptance rules: numbers keep
// their float64 semantics (UseNumber defers the conversion so an out-of-range
// literal cannot fail the decode), and, like JSON.parse/json.Unmarshal, exactly
// one top-level value is allowed.
func parseV8JSON(text string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = errors.New("unexpected data after top-level value")
		}
		return nil, err
	}
	return convertV8JSONNumbers(value), nil
}

// runFilterCommand ports `cave-tools filter --json [--session <id>]`: reads
// {command, stdout, stderr, exitCode?} on stdin, runs the cave__bash post-run
// pipeline (nothing is executed), writes {stdout, stderr} JSON. Any failure
// exits non-zero with empty stdout.
// port of src/cli-tools.ts:runFilterCommand
func runFilterCommand(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	compression.SuppressSubagentEnvLog()
	jsonOutput := false
	sessionID := newCLISessionID()
	for i := 0; i < len(argv); i++ {
		switch {
		case argv[i] == "--json":
			jsonOutput = true
		case argv[i] == "--session" && i+1 < len(argv):
			i++
			sessionID = argv[i]
		default:
			return finish(stderr, fmt.Sprintf("cave-tools filter: unexpected argument \"%s\"\nusage: cave-tools filter --json [--session <id>]", argv[i]), 2)
		}
	}
	if !jsonOutput {
		return finish(stderr, "usage: cave-tools filter --json [--session <id>]", 2)
	}
	text, err := readAllStdin(stdin)
	if err != nil {
		return finish(stderr, "cave-tools filter: "+err.Error(), 1)
	}
	parsed, err := parseV8JSON(text)
	if err != nil {
		// TS surfaces JSON.parse's V8 SyntaxError text; reproduce it instead
		// of encoding/json's wording.
		message := err.Error()
		if v8Message := jsjson.SyntaxErrorMessage(text); v8Message != "" {
			message = v8Message
		}
		return finish(stderr, "cave-tools filter: "+message, 1)
	}
	input, ok := parsed.(map[string]any)
	if !ok {
		return finish(stderr, "cave-tools filter: stdin must be JSON {command, stdout, stderr?, exitCode?}", 1)
	}
	command, commandOK := input["command"].(string)
	stdoutText, stdoutOK := input["stdout"].(string)
	if !commandOK || !stdoutOK {
		return finish(stderr, "cave-tools filter: stdin must be JSON {command, stdout, stderr?, exitCode?}", 1)
	}
	stderrText := ""
	if value, ok := input["stderr"].(string); ok {
		stderrText = value
	}
	var exitCode *int
	if value, ok := input["exitCode"].(float64); ok {
		// TS keeps every number, including ±Infinity, and the pipeline
		// ignores this field. Avoid Go's implementation-defined float→int
		// conversion for out-of-range values.
		converted := 0
		if !math.IsInf(value, 0) && !math.IsNaN(value) && value >= math.MinInt && value < math.MaxInt {
			converted = int(value)
		}
		exitCode = &converted
	}
	output := rt.RunWithContext(ctx, rt.LocalContext(sessionID), func(ctx context.Context) bash.FilteredShellOutput {
		return bash.FilterShellOutput(ctx, bash.ShellOutput{
			Command:  command,
			Stdout:   stdoutText,
			Stderr:   stderrText,
			ExitCode: exitCode,
		})
	})
	compression.FlushStats()
	encoded, err := marshalJS(filteredOutputJSON{Stdout: output.Stdout, Stderr: output.Stderr})
	if err != nil {
		return finish(stderr, "cave-tools filter: "+err.Error(), 1)
	}
	return finish(stdout, string(encoded), 0)
}

// padEndJS mirrors String.prototype.padEnd, which counts UTF-16 code units.
func padEndJS(value string, width int) string {
	units := utf16.Encode([]rune(value))
	if len(units) >= width {
		return value
	}
	return value + strings.Repeat(" ", width-len(units))
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// utf16RuneLength is the number of UTF-16 code units in r.
func utf16RuneLength(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}

func utf16Encode(value string) []uint16 { return utf16.Encode([]rune(value)) }

func utf16Decode(units []uint16) string { return string(utf16.Decode(units)) }
