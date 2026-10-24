package compression

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
)

// SignatureKind is the declaration kind a signature line describes.
// port of src/compression/signatures.ts:Signature["kind"]
type SignatureKind string

const (
	SigFn        SignatureKind = "fn"
	SigClass     SignatureKind = "class"
	SigInterface SignatureKind = "interface"
	SigType      SignatureKind = "type"
	SigEnum      SignatureKind = "enum"
	SigConst     SignatureKind = "const"
	SigImpl      SignatureKind = "impl"
)

// Signature is one extracted declaration.
// port of src/compression/signatures.ts:Signature
type Signature struct {
	Kind       SignatureKind
	Name       string
	Params     string
	ReturnType string
	IsAsync    bool
	IsExported bool
	Indent     int
}

// signaturePattern pairs a declaration kind with its line regex.
type signaturePattern struct {
	kind SignatureKind
	re   *regexp.Regexp
}

// tsPatterns mirrors TS_PATTERNS in source order.
// port of src/compression/signatures.ts:TS_PATTERNS
var tsPatterns = []signaturePattern{
	{
		kind: SigFn,
		re:   jsre.MustCompile(`^(\s*)(export\s+)?(async\s+)?function\s+(\w+)(?:\s*<[^>]*>)?\s*\(([^)]*)\)(?:\s*:\s*([^{]+))?\s*\{?`, ""),
	},
	{
		kind: SigClass,
		re:   jsre.MustCompile(`^(\s*)(export\s+)?(abstract\s+)?class\s+(\w+)`, ""),
	},
	{
		kind: SigInterface,
		re:   jsre.MustCompile(`^(\s*)(export\s+)?interface\s+(\w+)`, ""),
	},
	{
		kind: SigType,
		re:   jsre.MustCompile(`^(\s*)(export\s+)?type\s+(\w+)`, ""),
	},
	{
		kind: SigEnum,
		re:   jsre.MustCompile(`^(\s*)(export\s+)?enum\s+(\w+)`, ""),
	},
	{
		kind: SigConst,
		re:   jsre.MustCompile(`^(\s*)(export\s+)?(const|let|var)\s+(\w+)(?:\s*:\s*([^=]+))?\s*=`, ""),
	},
	{
		kind: SigFn,
		re:   jsre.MustCompile(`^(\s*)(export\s+)?(const|let|var)\s+(\w+)\s*=\s*(async\s+)?\(([^)]*)\)(?:\s*:\s*([^=]+))?\s*=>`, ""),
	},
}

// rustPatterns mirrors RUST_PATTERNS in source order.
// port of src/compression/signatures.ts:RUST_PATTERNS
var rustPatterns = []signaturePattern{
	{
		kind: SigFn,
		re:   jsre.MustCompile(`^(\s*)(pub\s+)?(async\s+)?fn\s+(\w+)(?:\s*<[^>]*>)?\s*\(([^)]*)\)(?:\s*->\s*([^{]+))?\s*\{?`, ""),
	},
	{
		kind: SigClass,
		re:   jsre.MustCompile(`^(\s*)(pub\s+)?struct\s+(\w+)`, ""),
	},
	{
		kind: SigEnum,
		re:   jsre.MustCompile(`^(\s*)(pub\s+)?enum\s+(\w+)`, ""),
	},
	{
		kind: SigInterface,
		re:   jsre.MustCompile(`^(\s*)(pub\s+)?trait\s+(\w+)`, ""),
	},
	{
		kind: SigType,
		re:   jsre.MustCompile(`^(\s*)(pub\s+)?type\s+(\w+)`, ""),
	},
	{
		kind: SigImpl,
		re:   jsre.MustCompile(`^(\s*)impl\s+(?:(\w+)\s+for\s+)?(\w+)`, ""),
	},
	{
		kind: SigConst,
		re:   jsre.MustCompile(`^(\s*)(pub\s+)?(const|static)\s+(\w+)(?:\s*:\s*([^=]+))?\s*=`, ""),
	},
}

// extractTsSignatures scans TS/JS lines in order.
// port of src/compression/signatures.ts:extractTsSignatures
func extractTsSignatures(content string) []Signature {
	sigs := make([]Signature, 0)
	for _, line := range strings.Split(content, "\n") {
		for _, pattern := range tsPatterns {
			match := pattern.re.FindStringSubmatch(line)
			if match == nil {
				continue
			}

			indent := utf16Length(match[1])
			isExported := match[2] != ""
			isAsync := jsre.Trim(match[3]) == "async"

			var name string
			var params string
			var returnType string

			switch {
			case pattern.kind == SigFn && strings.Contains(match[0], "function"):
				name = match[4]
				params = match[5]
				returnType = jsre.Trim(match[6])
			case pattern.kind == SigFn:
				name = match[4]
				params = match[6]
				returnType = jsre.Trim(match[7])
			case pattern.kind == SigConst:
				name = match[4]
				returnType = jsre.Trim(match[5])
			default:
				name = match[len(match)-1]
			}

			sigs = append(sigs, Signature{
				Kind:       pattern.kind,
				Name:       jsre.Trim(name),
				Params:     jsre.Trim(params),
				ReturnType: jsre.Trim(returnType),
				IsAsync:    isAsync,
				IsExported: isExported,
				Indent:     indent,
			})
			break
		}
	}
	return sigs
}

// extractRustSignatures scans Rust lines in order.
// port of src/compression/signatures.ts:extractRustSignatures
func extractRustSignatures(content string) []Signature {
	sigs := make([]Signature, 0)
	for _, line := range strings.Split(content, "\n") {
		for _, pattern := range rustPatterns {
			match := pattern.re.FindStringSubmatch(line)
			if match == nil {
				continue
			}

			indent := utf16Length(match[1])
			isExported := match[2] != ""
			isAsync := jsre.Trim(match[3]) == "async"

			var name string
			var params string
			var returnType string

			switch pattern.kind {
			case SigFn:
				name = match[4]
				params = match[5]
				returnType = jsre.Trim(match[6])
			case SigImpl:
				name = match[3]
				if forType := match[2]; forType != "" {
					returnType = "for " + jsre.Trim(forType)
				}
			case SigConst:
				name = match[4]
				returnType = jsre.Trim(match[5])
			default:
				name = match[len(match)-1]
			}

			sigs = append(sigs, Signature{
				Kind:       pattern.kind,
				Name:       jsre.Trim(name),
				Params:     jsre.Trim(params),
				ReturnType: jsre.Trim(returnType),
				IsAsync:    isAsync,
				IsExported: isExported,
				Indent:     indent,
			})
			break
		}
	}
	return sigs
}

// extractSignatures dispatches on the file extension.
// port of src/compression/signatures.ts:extractSignatures
func ExtractSignatures(content string, ext string) []Signature {
	switch strings.ToLower(ext) {
	case ".ts", ".tsx", ".js", ".jsx":
		return extractTsSignatures(content)
	case ".rs":
		return extractRustSignatures(content)
	default:
		return []Signature{}
	}
}

// whitespaceRunRE is the inline /\s+/g regex in compactType.
var whitespaceRunRE = jsre.MustCompile(`\s+`, "g")

// compactType collapses whitespace runs in a type expression:
// typeStr.replace(/\s+/g, " ").trim() in TS.
// port of src/compression/signatures.ts:compactType
func compactType(typeStr string) string {
	return jsre.Trim(whitespaceRunRE.ReplaceAllString(typeStr, " "))
}

// formatSignature renders one signature line.
// port of src/compression/signatures.ts:formatSignature
func FormatSignature(sig Signature) string {
	exportPrefix := ""
	if sig.IsExported {
		exportPrefix = "export "
	}
	asyncPrefix := ""
	if sig.IsAsync {
		asyncPrefix = "async "
	}
	indent := strings.Repeat(" ", sig.Indent)

	switch sig.Kind {
	case SigFn:
		ret := ""
		if sig.ReturnType != "" {
			ret = " → " + compactType(sig.ReturnType)
		}
		params := compactType(sig.Params)
		return fmt.Sprintf("%s%s%sfn %s(%s)%s", indent, exportPrefix, asyncPrefix, sig.Name, params, ret)
	case SigClass:
		return fmt.Sprintf("%s%sclass %s", indent, exportPrefix, sig.Name)
	case SigInterface:
		return fmt.Sprintf("%s%sinterface %s", indent, exportPrefix, sig.Name)
	case SigType:
		return fmt.Sprintf("%s%stype %s", indent, exportPrefix, sig.Name)
	case SigEnum:
		return fmt.Sprintf("%s%senum %s", indent, exportPrefix, sig.Name)
	case SigConst:
		ty := ""
		if sig.ReturnType != "" {
			ty = ": " + compactType(sig.ReturnType)
		}
		return fmt.Sprintf("%s%sconst %s%s", indent, exportPrefix, sig.Name, ty)
	case SigImpl:
		ret := ""
		if sig.ReturnType != "" {
			ret = " " + compactType(sig.ReturnType)
		}
		return fmt.Sprintf("%simpl %s%s", indent, sig.Name, ret)
	default:
		return fmt.Sprintf("%s%s %s", indent, sig.Kind, sig.Name)
	}
}

// formatSignatures renders every signature in content, one per line.
// port of src/compression/signatures.ts:formatSignatures
func FormatSignatures(content string, ext string) string {
	sigs := ExtractSignatures(content, ext)
	if len(sigs) == 0 {
		return ""
	}
	lines := make([]string, len(sigs))
	for i, sig := range sigs {
		lines[i] = FormatSignature(sig)
	}
	return strings.Join(lines, "\n")
}
