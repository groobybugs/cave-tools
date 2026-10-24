package compression

import (
	"reflect"
	"strings"
	"testing"
)

// TestFormatSignatures ports the signatures read-mode assertions from test.mjs
// (2e and the cave__read edge case) plus the Rust and declaration-kind cases
// pinned against the TS reference.
func TestFormatSignatures(t *testing.T) {
	tsSample := "export async function loadUser(id: string): Promise<User> {\n  return db.get(id);\n}\n\nclass UserService {\n  constructor() {}\n}\n\nexport interface User {\n  id: string;\n}\n"
	tsSample2 := "export function add(a: number, b: number): number {\n  return a + b;\n}\n\nexport class Box {\n  constructor(public v: number) {}\n}\n"
	tsSample3 := strings.Join([]string{
		"export type ID = string | number;",
		"enum Color { Red }",
		"const MAX = 10;",
		"let count: number = 0;",
		"export const load = async (id: string): Promise<User> => {",
		"  return db.get(id);",
		"};",
		"  export abstract class Base<T> {",
		"}",
		"var legacy: any = 1;",
		"const typed: Loader = (x) => x;",
	}, "\n")
	rustSample := strings.Join([]string{
		"pub async fn fetch(id: u64) -> Result<User> {",
		"  let x = 1;",
		"}",
		"",
		"pub struct User {",
		"    id: u64,",
		"}",
		"pub enum Kind {",
		"    A,",
		"}",
		"pub trait Store {",
		"}",
		"pub type ID = u64;",
		"impl Store for User {",
		"}",
		"impl User {",
		"}",
		"pub const MAX: usize = 10;",
		"static GLOBAL: u8 = 1;",
		"    fn indented(a: u8) -> u8 {",
	}, "\n")

	tests := []struct {
		name    string
		content string
		ext     string
		want    string
	}{
		{
			name:    "typescript sample",
			content: tsSample,
			ext:     ".ts",
			want:    "export async fn loadUser(id: string) → Promise<User>\nclass UserService\nexport interface User",
		},
		{
			name:    "typescript sample two",
			content: tsSample2,
			ext:     ".ts",
			want:    "export fn add(a: number, b: number) → number\nexport class Box",
		},
		{
			name:    "typescript declaration kinds",
			content: tsSample3,
			ext:     ".ts",
			want: strings.Join([]string{
				"export type ID",
				"enum Color",
				"const MAX",
				"const count: number",
				"export const load",
				"  export class Base",
				"const legacy: any",
				"const typed: Loader",
			}, "\n"),
		},
		{
			name:    "rust declarations",
			content: rustSample,
			ext:     ".rs",
			want: strings.Join([]string{
				"export async fn fetch(id: u64) → Result<User>",
				"export class User",
				"export enum Kind",
				"export interface Store",
				"export type ID",
				"impl User for Store",
				"impl User",
				"export const MAX: usize",
				"const GLOBAL: u8",
				"    fn indented(a: u8) → u8",
			}, "\n"),
		},
		{
			name:    "unknown extension",
			content: "plain text",
			ext:     ".md",
			want:    "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := FormatSignatures(test.content, test.ext)
			if got != test.want {
				t.Fatalf("FormatSignatures() = %q, want %q", got, test.want)
			}
		})
	}

	// test.mjs also asserts the body line is absent.
	if sigs := FormatSignatures(tsSample, ".ts"); strings.Contains(sigs, "return db.get") {
		t.Errorf("signatures leaked a function body: %q", sigs)
	}
	if sigs := FormatSignatures(tsSample2, ".ts"); strings.Contains(sigs, "return a + b") {
		t.Errorf("signatures leaked a function body: %q", sigs)
	}

	// extractSignatures keeps the structured shape.
	wantExtracted := []Signature{
		{Kind: SigFn, Name: "loadUser", Params: "id: string", ReturnType: "Promise<User>", IsAsync: true, IsExported: true, Indent: 0},
		{Kind: SigClass, Name: "UserService", Indent: 0},
		{Kind: SigInterface, Name: "User", IsExported: true, Indent: 0},
	}
	if got := ExtractSignatures(tsSample, ".ts"); !reflect.DeepEqual(got, wantExtracted) {
		t.Errorf("ExtractSignatures() = %+v, want %+v", got, wantExtracted)
	}

	impl := FormatSignature(Signature{Kind: SigImpl, Name: "User", ReturnType: "for Store", Indent: 2})
	if want := "  impl User for Store"; impl != want {
		t.Errorf("FormatSignature(impl) = %q, want %q", impl, want)
	}
}

// TestSignatureJSWhitespace is the Muse regression: JS \s matches NBSP and
// U+FEFF, so an NBSP-indented declaration is still a signature and U+FEFF is
// collapsed by compactType. Expected strings are the TS reference output.
func TestSignatureJSWhitespace(t *testing.T) {
	t.Run("nbsp indent", func(t *testing.T) {
		content := "\u00a0export function foo(a: string): void {"
		if got, want := FormatSignatures(content, ".ts"), " export fn foo(a: string) → void"; got != want {
			t.Fatalf("FormatSignatures() = %q, want %q", got, want)
		}
		sigs := ExtractSignatures(content, ".ts")
		if len(sigs) != 1 || sigs[0].Indent != 1 || sigs[0].Name != "foo" {
			t.Fatalf("ExtractSignatures() = %+v, want one signature with Indent 1", sigs)
		}
	})

	t.Run("nbsp inside params and return type", func(t *testing.T) {
		content := "export function bar(a:\u00a0string):\u00a0\u00a0void {"
		if got, want := FormatSignatures(content, ".ts"), "export fn bar(a: string) → void"; got != want {
			t.Fatalf("FormatSignatures() = %q, want %q", got, want)
		}
	})

	t.Run("feff before a type", func(t *testing.T) {
		content := "export const x:\ufeffLoader = 1;"
		if got, want := FormatSignatures(content, ".ts"), "export const x: Loader"; got != want {
			t.Fatalf("FormatSignatures() = %q, want %q", got, want)
		}
	})

	t.Run("compactType", func(t *testing.T) {
		tests := []struct {
			input string
			want  string
		}{
			{"a\ufeffb", "a b"},
			{"  a   b  ", "a b"},
			{"a\u00a0\u00a0b", "a b"},
			{"a\u0085b", "a\u0085b"},
			{"\ufeffx\ufeff", "x"},
		}
		for _, test := range tests {
			if got := compactType(test.input); got != test.want {
				t.Errorf("compactType(%q) = %q, want %q", test.input, got, test.want)
			}
		}
	})
}
