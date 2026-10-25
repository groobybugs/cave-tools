package read

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/testenv"
)

// This file is the handler-level differential required by the wave-3 task: the
// TS handler (dist/tools/read.js) and the Go handler run the same scenario
// against twin fixture trees, and every ToolResult JSON must match byte for
// byte. It skips instead of failing when node or dist/ is missing, like the CLI
// parity harness.

// parityStep is one scenario step, interpreted identically by both sides.
type parityStep struct {
	Op      string         `json:"op"`
	Args    map[string]any `json:"args,omitempty"`
	Path    string         `json:"path"`
	Content string         `json:"content"`
	Bytes   []int          `json:"bytes,omitempty"`
	Size    int64          `json:"size,omitempty"`
}

// parityScenario is a named sequence of steps.
type parityScenario struct {
	Name  string       `json:"name"`
	Steps []parityStep `json:"steps"`
}

func readStep(args map[string]any) parityStep {
	return parityStep{Op: "read", Args: args}
}

func writeStep(path string, content string) parityStep {
	return parityStep{Op: "write", Path: "$ROOT/" + path, Content: content}
}

func numberedLines(prefix string, count int) string {
	lines := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		lines = append(lines, fmt.Sprintf("%s-%03d", prefix, i))
	}
	return strings.Join(lines, "\n")
}

// parityScenarios covers the read tool's observable surface.
func parityScenarios() []parityScenario {
	notes := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		notes = append(notes, fmt.Sprintf("line %d of the original notes file", i))
	}
	notesV2 := append([]string{}, notes...)
	notesV2[50] = "line 50 CHANGED"
	notesV4 := make([]string, 0, len(notes))
	for _, line := range notes {
		notesV4 = append(notesV4, strings.ToUpper(line))
	}
	bigLines := make([]string, 0, 150)
	for i := 0; i < 150; i++ {
		bigLines = append(bigLines, fmt.Sprintf("%d%s", i, strings.Repeat("x", 1900)))
	}

	return []parityScenario{
		{
			Name: "basic",
			Steps: []parityStep{
				writeStep("a.txt", numberedLines("line", 50)),
				readStep(map[string]any{"file_path": "$ROOT/a.txt"}),
				readStep(map[string]any{"file_path": "$ROOT/a.txt"}),
				readStep(map[string]any{"file_path": "$ROOT/a.txt", "force": true}),
				readStep(map[string]any{"file_path": "$ROOT/a.txt", "offset": 10, "limit": 3}),
				readStep(map[string]any{"file_path": "$ROOT/a.txt", "offset": 10, "limit": 3}),
				readStep(map[string]any{"file_path": "$ROOT/a.txt", "line_numbers": true}),
				readStep(map[string]any{"file_path": "$ROOT/a.txt", "line_numbers": true, "limit": 2}),
				readStep(map[string]any{"file_path": "$ROOT/a.txt", "offset": 99}),
				readStep(map[string]any{"file_path": "$ROOT/a.txt", "offset": 0}),
				readStep(map[string]any{"file_path": "$ROOT/a.txt", "limit": 0}),
			},
		},
		{
			Name: "modes",
			Steps: []parityStep{
				writeStep("sample.ts", "export async function loadUser(id: string): Promise<User> {\n  return db.get(id);\n}\n\nclass UserService {\n  constructor() {}\n}\n\nexport interface User {\n  id: string;\n}\n"),
				writeStep("sample.py", "#!/usr/bin/env python3\n# This is a comment\n\ndef hello():\n    # inline comment\n    return 'world'\n\n\nclass Foo:\n    pass\n"),
				writeStep("code.js", "// header comment\nconst x = 1;\n\n\n\nconst y = 2;\n"),
				writeStep("plain.txt", "alpha\nbeta\n"),
				readStep(map[string]any{"file_path": "$ROOT/sample.ts", "mode": "signatures"}),
				readStep(map[string]any{"file_path": "$ROOT/sample.ts", "mode": "signatures"}),
				readStep(map[string]any{"file_path": "$ROOT/sample.py", "mode": "aggressive"}),
				readStep(map[string]any{"file_path": "$ROOT/code.js", "mode": "aggressive"}),
				readStep(map[string]any{"file_path": "$ROOT/plain.txt", "mode": "signatures"}),
				readStep(map[string]any{"file_path": "$ROOT/plain.txt", "mode": "aggressive"}),
				readStep(map[string]any{"file_path": "$ROOT/plain.txt", "mode": "bogus"}),
			},
		},
		{
			Name: "sessions",
			Steps: []parityStep{
				writeStep("shared.txt", "shared content\n"),
				readStep(map[string]any{"file_path": "$ROOT/shared.txt", "__sessionId": "sessionA"}),
				readStep(map[string]any{"file_path": "$ROOT/shared.txt", "__sessionId": "sessionA"}),
				readStep(map[string]any{"file_path": "$ROOT/shared.txt", "__sessionId": "sessionB"}),
				readStep(map[string]any{"file_path": "$ROOT/shared.txt", "__sessionId": "sessionB"}),
				readStep(map[string]any{"file_path": "$ROOT/shared.txt"}),
				readStep(map[string]any{"file_path": "$ROOT/shared.txt"}),
				readStep(map[string]any{"file_path": "$ROOT/shared.txt", "__sessionId": 42}),
				readStep(map[string]any{"file_path": "$ROOT/shared.txt", "__sessionId": 42}),
			},
		},
		{
			Name: "diff",
			Steps: []parityStep{
				writeStep("notes.txt", strings.Join(notes, "\n")),
				readStep(map[string]any{"file_path": "$ROOT/notes.txt", "__sessionId": "diff-a"}),
				writeStep("notes.txt", strings.Join(notesV2, "\n")),
				readStep(map[string]any{"file_path": "$ROOT/notes.txt", "__sessionId": "diff-a"}),
				readStep(map[string]any{"file_path": "$ROOT/notes.txt", "__sessionId": "diff-a"}),
				readStep(map[string]any{"file_path": "$ROOT/notes.txt", "__sessionId": "diff-b"}),
				readStep(map[string]any{"file_path": "$ROOT/notes.txt", "__sessionId": "diff-b", "line_numbers": true}),
				readStep(map[string]any{"file_path": "$ROOT/notes.txt", "__sessionId": "diff-a", "force": true}),
				writeStep("notes.txt", strings.Join(notesV4, "\n")),
				readStep(map[string]any{"file_path": "$ROOT/notes.txt", "__sessionId": "diff-a"}),
				parityStep{Op: "recordEdit", Path: "$ROOT/notes.txt"},
				writeStep("notes.txt", strings.Replace(strings.Join(notesV4, "\n"), "LINE 70 OF THE ORIGINAL NOTES FILE", "edited seventy", 1)),
				readStep(map[string]any{"file_path": "$ROOT/notes.txt", "__sessionId": "diff-a"}),
				writeStep("big.txt", strings.Join(bigLines, "\n")),
				readStep(map[string]any{"file_path": "$ROOT/big.txt", "limit": 1000, "__sessionId": "diff-a"}),
			},
		},
		{
			Name: "media",
			Steps: []parityStep{
				parityStep{Op: "writeBytes", Path: "$ROOT/bin.dat", Bytes: []int{0, 1, 2, 3}},
				writeStep("blob.zip", "PK\x03\x04"),
				parityStep{Op: "writeBytes", Path: "$ROOT/pixel.png", Bytes: []int{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52}},
				parityStep{Op: "writeBytes", Path: "$ROOT/photo.jpg", Bytes: []int{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}},
				parityStep{Op: "writeBytes", Path: "$ROOT/anim.gif", Bytes: []int{0x47, 0x49, 0x46, 0x38, 0x39, 0x61}},
				parityStep{Op: "writeBytes", Path: "$ROOT/pic.webp", Bytes: append([]int{0x52, 0x49, 0x46, 0x46, 0, 0, 0, 0}, []int{0x57, 0x45, 0x42, 0x50}...)},
				writeStep("icon.svg", "<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>\n"),
				writeStep("doc.pdf", "%PDF-1.4\n"),
				parityStep{Op: "writeBytes", Path: "$ROOT/bad.txt", Bytes: []int{0xc3, 0x28}},
				parityStep{Op: "writeBytes", Path: "$ROOT/bom.txt", Bytes: []int{0xef, 0xbb, 0xbf, 0x61, 0x0a}},
				writeStep("empty.txt", ""),
				readStep(map[string]any{"file_path": "$ROOT/bin.dat"}),
				readStep(map[string]any{"file_path": "$ROOT/blob.zip"}),
				readStep(map[string]any{"file_path": "$ROOT/pixel.png"}),
				readStep(map[string]any{"file_path": "$ROOT/photo.jpg"}),
				readStep(map[string]any{"file_path": "$ROOT/anim.gif"}),
				readStep(map[string]any{"file_path": "$ROOT/pic.webp"}),
				readStep(map[string]any{"file_path": "$ROOT/icon.svg"}),
				readStep(map[string]any{"file_path": "$ROOT/icon.svg"}),
				readStep(map[string]any{"file_path": "$ROOT/doc.pdf"}),
				readStep(map[string]any{"file_path": "$ROOT/doc.pdf"}),
				readStep(map[string]any{"file_path": "$ROOT/bad.txt"}),
				readStep(map[string]any{"file_path": "$ROOT/bom.txt"}),
				readStep(map[string]any{"file_path": "$ROOT/empty.txt"}),
				parityStep{Op: "truncate", Path: "$ROOT/huge.png", Size: 20*1024*1024 + 1},
				parityStep{Op: "truncate", Path: "$ROOT/huge.pdf", Size: 20*1024*1024 + 1},
				readStep(map[string]any{"file_path": "$ROOT/huge.png"}),
				readStep(map[string]any{"file_path": "$ROOT/huge.pdf"}),
			},
		},
		{
			Name: "directory",
			Steps: []parityStep{
				writeStep("list/zeta/keep.txt", "z"),
				writeStep("list/Alpha/keep.txt", "a"),
				writeStep("list/b.txt", "x"),
				writeStep("list/a.txt", "y"),
				writeStep("list/.hidden", "z"),
				writeStep("list/B.md", "w"),
				writeStep("list/~tilde", "t"),
				writeStep("list/_under", "u"),
				writeStep("list/0zero", "0"),
				parityStep{Op: "mkdir", Path: "$ROOT/emptydir"},
				readStep(map[string]any{"file_path": "$ROOT/list"}),
				readStep(map[string]any{"file_path": "$ROOT/list", "limit": 2}),
				readStep(map[string]any{"file_path": "$ROOT/list", "offset": 3, "limit": 2}),
				readStep(map[string]any{"file_path": "$ROOT/list", "offset": 9}),
				readStep(map[string]any{"file_path": "$ROOT/list", "offset": 99}),
				readStep(map[string]any{"file_path": "$ROOT/emptydir"}),
			},
		},
		{
			Name: "large-and-caps",
			Steps: []parityStep{
				writeStep("large.txt", numberedLines("ZZZ-large-read", 400)),
				readStep(map[string]any{"file_path": "$ROOT/large.txt", "limit": 400}),
				readStep(map[string]any{"file_path": "$ROOT/large.txt", "force": true}),
				readStep(map[string]any{"file_path": "$ROOT/large.txt", "offset": 300, "limit": 5}),
				writeStep("long.txt", strings.Repeat("a", 3000)+"\nshort\n"),
				readStep(map[string]any{"file_path": "$ROOT/long.txt"}),
				writeStep("bytes.txt", strings.Join(makeLines(300, strings.Repeat("b", 300)), "\n")),
				readStep(map[string]any{"file_path": "$ROOT/bytes.txt", "limit": 300}),
			},
		},
		{
			Name: "codebook",
			Steps: []parityStep{
				writeStep("imports.ts", strings.Join(makeLines(300, "import { useState } from 'react';"), "\n")),
				readStep(map[string]any{"file_path": "$ROOT/imports.ts", "limit": 300}),
				readStep(map[string]any{"file_path": "$ROOT/imports.ts", "force": true, "limit": 300}),
			},
		},
		{
			Name: "missing-and-fractional",
			Steps: []parityStep{
				writeStep("linnes", "x"),
				writeStep("frac.txt", "a\nb\nc\n"),
				readStep(map[string]any{"file_path": "$ROOT/does-not-exist.txt"}),
				readStep(map[string]any{"file_path": "$ROOT/linnes.txt"}),
				readStep(map[string]any{"file_path": "$ROOT/frac.txt", "offset": 2.5}),
				readStep(map[string]any{"file_path": "$ROOT/frac.txt", "limit": 2.5}),
				readStep(map[string]any{"file_path": "$ROOT/frac.txt", "offset": "3"}),
				readStep(map[string]any{"file_path": "$ROOT/frac.txt", "line_numbers": "true"}),
			},
		},
		{
			Name: "numbered-windows",
			Steps: []parityStep{
				writeStep("ten.txt", numberedLines("row", 10)),
				readStep(map[string]any{"file_path": "$ROOT/ten.txt", "line_numbers": true, "offset": 3, "limit": 2}),
				readStep(map[string]any{"file_path": "$ROOT/ten.txt", "line_numbers": true, "offset": 20}),
				readStep(map[string]any{"file_path": "$ROOT/ten.txt", "line_numbers": true, "limit": 4, "force": true}),
			},
		},
	}
}

func makeLines(count int, line string) []string {
	lines := make([]string, 0, count)
	for i := 0; i < count; i++ {
		lines = append(lines, line)
	}
	return lines
}

// TestHandlerParityWithTS runs every scenario against both implementations.
func TestHandlerParityWithTS(t *testing.T) {
	root := repoRoot(t)
	distRead := filepath.Join(root, "dist", "tools", "read.js")
	distUtils := filepath.Join(root, "dist", "compression", "utils.js")
	if _, err := os.Stat(distRead); err != nil {
		t.Skip("dist/tools/read.js is missing: run `pnpm build` first")
	}
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH")
	}

	driver := writeDriver(t, distRead, distUtils)
	totalReads := 0

	for _, scenario := range parityScenarios() {
		t.Run(scenario.Name, func(t *testing.T) {
			totalReads += countReads(scenario)

			goRoot := fixtureRoot(t)
			tsRoot := fixtureRoot(t)
			tsData := t.TempDir()
			tsConfig := t.TempDir()

			// TS side: one process per scenario keeps dedup/snapshot state clean.
			scenarioJSON, err := json.Marshal(map[string]any{
				"root":  tsRoot,
				"steps": scenario.Steps,
			})
			if err != nil {
				t.Fatal(err)
			}
			scenarioFile := filepath.Join(t.TempDir(), "scenario.json")
			if err := os.WriteFile(scenarioFile, scenarioJSON, 0o600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(nodePath, driver, scenarioFile)
			command.Dir = t.TempDir()
			command.Env = append(isolatedEnv(), "CAVE_TOOLS_DATA_DIR="+tsData, "CLAUDE_CONFIG_DIR="+tsConfig)
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			if err := command.Run(); err != nil {
				t.Fatalf("node driver failed: %v\n%s", err, stderr.String())
			}
			tsResults := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
			for index := range tsResults {
				tsResults[index] = strings.ReplaceAll(tsResults[index], tsRoot, "$ROOT")
			}

			goResults := runScenarioInGo(t, scenario, goRoot)

			if len(goResults) != len(tsResults) {
				t.Fatalf("result count: go=%d ts=%d\nstderr: %s", len(goResults), len(tsResults), stderr.String())
			}
			for index := range goResults {
				if goResults[index] != tsResults[index] {
					t.Fatalf("result %d differs\ngo: %s\nts: %s", index, goResults[index], tsResults[index])
				}
			}
		})
	}

	if totalReads < 40 {
		t.Fatalf("differential only covered %d reads", totalReads)
	}
	t.Logf("handler differential: %d scenarios, %d read calls, 0 mismatches", len(parityScenarios()), totalReads)
}

func countReads(scenario parityScenario) int {
	count := 0
	for _, step := range scenario.Steps {
		if step.Op == "read" {
			count++
		}
	}
	return count
}

// runScenarioInGo executes the scenario steps against the Go handler.
func runScenarioInGo(t *testing.T, scenario parityScenario, root string) []string {
	t.Helper()
	ctx := resetState(t)
	results := make([]string, 0, countReads(scenario))
	resolve := func(value string) string { return strings.ReplaceAll(value, "$ROOT", root) }
	requireRooted := func(path string) string {
		if !strings.Contains(path, "$ROOT") {
			t.Fatalf("scenario path %q is not rooted at $ROOT", path)
		}
		return resolve(path)
	}

	for _, step := range scenario.Steps {
		switch step.Op {
		case "read":
			args := make(map[string]any, len(step.Args))
			for key, value := range step.Args {
				if text, ok := value.(string); ok {
					args[key] = resolve(text)
					continue
				}
				args[key] = value
			}
			result := callRead(t, ctx, args)
			var buffer bytes.Buffer
			encoder := json.NewEncoder(&buffer)
			encoder.SetEscapeHTML(false)
			if err := encoder.Encode(result); err != nil {
				t.Fatal(err)
			}
			results = append(results, strings.ReplaceAll(strings.TrimSuffix(buffer.String(), "\n"), root, "$ROOT"))
		case "write":
			mustWrite(t, requireRooted(step.Path), step.Content)
		case "writeBytes":
			raw := make([]byte, len(step.Bytes))
			for index, value := range step.Bytes {
				raw[index] = byte(value)
			}
			if err := os.WriteFile(requireRooted(step.Path), raw, 0o600); err != nil {
				t.Fatal(err)
			}
		case "truncate":
			path := requireRooted(step.Path)
			mustWrite(t, path, "")
			if err := os.Truncate(path, step.Size); err != nil {
				t.Fatal(err)
			}
		case "mkdir":
			if err := os.MkdirAll(requireRooted(step.Path), 0o700); err != nil {
				t.Fatal(err)
			}
		case "remove":
			if err := os.Remove(resolve(step.Path)); err != nil {
				t.Fatal(err)
			}
		case "recordEdit":
			compression.RecordEdit(ctx, resolve(step.Path))
		default:
			t.Fatalf("unknown scenario op %q", step.Op)
		}
	}
	return results
}

// isolatedEnv is the test process environment without cave-tools state, so the
// node child never reads the developer's registry or stats and never reports a
// subagent-like signal it inherited from the parent shell.
func isolatedEnv() []string {
	forbidden := map[string]bool{
		"CAVE_TOOLS_DATA_DIR": true,
		"CLAUDE_CONFIG_DIR":   true,
	}
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range testenv.Scrub(os.Environ()) {
		key, _, ok := strings.Cut(entry, "=")
		if ok && forbidden[key] {
			continue
		}
		env = append(env, entry)
	}
	return env
}

// repoRoot finds the repository root from this package's directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join(workingDirectory, "..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// writeDriver writes the node scenario runner once per test.
func writeDriver(t *testing.T, distRead string, distUtils string) string {
	t.Helper()
	readURL := fileURL(distRead)
	utilsURL := fileURL(distUtils)
	script := `import fs from "node:fs";
import path from "node:path";
import { readTool } from "` + readURL + `";
import { recordEdit } from "` + utilsURL + `";

const scenario = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
const root = scenario.root;
const resolve = (value) => (typeof value === "string" ? value.split("$ROOT").join(root) : value);
const resolvePath = (value) => {
  if (typeof value !== "string" || !value.includes("$ROOT")) throw new Error("unrooted scenario path: " + value);
  return resolve(value);
};
const out = [];
for (const step of scenario.steps) {
  if (step.op === "read") {
    const args = {};
    for (const [key, value] of Object.entries(step.args ?? {})) args[key] = resolve(value);
    out.push(JSON.stringify(await readTool.handler(args)));
  } else if (step.op === "write") {
    const target = resolvePath(step.path);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.writeFileSync(target, step.content);
  } else if (step.op === "writeBytes") {
    fs.writeFileSync(resolvePath(step.path), Buffer.from(step.bytes));
  } else if (step.op === "truncate") {
    const target = resolvePath(step.path);
    fs.writeFileSync(target, "");
    fs.truncateSync(target, step.size);
  } else if (step.op === "mkdir") {
    fs.mkdirSync(resolvePath(step.path), { recursive: true });
  } else if (step.op === "remove") {
    fs.rmSync(resolvePath(step.path), { force: true });
  } else if (step.op === "recordEdit") {
    recordEdit(resolvePath(step.path));
  } else {
    throw new Error("unknown scenario op " + step.op);
  }
}
process.stdout.write(out.join("\n"));
`
	path := filepath.Join(t.TempDir(), "driver.mjs")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func fileURL(path string) string {
	return "file://" + filepath.ToSlash(path)
}
