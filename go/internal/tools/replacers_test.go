package tools

import (
	"strings"
	"testing"
)

// Ports scripts/test-edit-v2.mjs (sections A and C), the fuzzy replacer
// assertions in test.mjs section 6e, and the upstream-tier assertions in
// section 6e2 at helper level.

func TestFindReplacement(t *testing.T) {
	ctx := toolsTestCtx(map[string]string{})

	t.Run("A1 search and index", func(t *testing.T) {
		m := FindReplacement(ctx, "hello world\n", "world", false)
		if m.Search == nil || *m.Search != "world" || m.Index == nil || *m.Index != 6 {
			t.Fatalf("match = %+v", m)
		}
	})
	t.Run("A2 ambiguous", func(t *testing.T) {
		m := FindReplacement(ctx, "a a a", "a", false)
		if !m.NonUnique || m.Error != "non-unique" {
			t.Fatalf("match = %+v", m)
		}
		all := FindReplacement(ctx, "a a a", "a", true)
		if all.Search == nil || *all.Search != "a" || all.Index == nil || *all.Index != 0 {
			t.Fatalf("replaceAll match = %+v", all)
		}
	})
	t.Run("A3 unicode search", func(t *testing.T) {
		content := "say \u201Chello\u201D ok"
		m := FindReplacement(ctx, content, "\"hello\"", false)
		if m.Search == nil || *m.Search != "\u201Chello\u201D" {
			t.Fatalf("search = %+v", m.Search)
		}
		if m.Index == nil || *m.Index != strings.Index(content, "\u201Chello\u201D") {
			t.Fatalf("index = %+v", m.Index)
		}
	})
	t.Run("A4 trailing tier", func(t *testing.T) {
		m := FindReplacement(ctx, "foo   \n", "foo", false)
		if m.Search == nil || *m.Search != "foo" || m.Index == nil || *m.Index != 0 {
			t.Fatalf("match = %+v", m)
		}
	})
	t.Run("A5 not found", func(t *testing.T) {
		m := FindReplacement(ctx, "abc", "zzz", false)
		if m.Error == "" || m.NonUnique {
			t.Fatalf("match = %+v", m)
		}
	})
	t.Run("A6 empty", func(t *testing.T) {
		if m := FindReplacement(ctx, "abc", "", false); m.Error == "" {
			t.Fatalf("match = %+v", m)
		}
	})
}

func TestComputeEditResultUpstream(t *testing.T) {
	// C3: upstream hits are never marked fuzzy.
	r := ComputeEditResult(toolsTestCtx(map[string]string{}), "hello world\n", "world", "there", false)
	if !r.OK || r.Fuzzy {
		t.Fatalf("result = %+v", r)
	}
	if r.Updated != "hello there\n" {
		t.Fatalf("updated = %q", r.Updated)
	}
	// Upstream tiers: unicode punctuation.
	uni := ComputeEditResult(toolsTestCtx(map[string]string{}),
		"it\u2019s a \u201Ctest\u201D \u2014 done\n", "it's a \"test\" - done", "it's a \"test\" - changed", false)
	if !uni.OK || uni.Fuzzy || !strings.Contains(uni.Updated, "changed") {
		t.Fatalf("unicode tier = %+v", uni)
	}
	// Upstream tier: trailing-whitespace line window.
	trail := ComputeEditResult(toolsTestCtx(map[string]string{}),
		"alpha   \nbeta\t\n", "alpha\nbeta", "alpha\nGAMMA", false)
	if !trail.OK || trail.Fuzzy || !strings.Contains(trail.Updated, "GAMMA") {
		t.Fatalf("trailing tier = %+v", trail)
	}
}

func TestComputeEditResultFuzzy(t *testing.T) {
	enabled := toolsTestCtx(map[string]string{})
	disabled := toolsTestCtx(map[string]string{"CAVE_TOOLS_FUZZY_EDIT": "0"})

	t.Run("C1 fuzzy fallback", func(t *testing.T) {
		src := "    foo\n    bar\n"
		r := ComputeEditResult(enabled, src, "foo\nbar", "x\ny", false)
		if !r.OK || !r.Fuzzy {
			t.Fatalf("result = %+v", r)
		}
		if r.Updated != "x\ny\n" {
			t.Fatalf("updated = %q", r.Updated)
		}
	})
	t.Run("C2 strict miss", func(t *testing.T) {
		src := "    foo\n    bar\n"
		r := ComputeEditResult(disabled, src, "foo\nbar", "x\ny", false)
		if r.OK || !strings.Contains(r.Error, "Could not find oldString") {
			t.Fatalf("result = %+v", r)
		}
		if m := FindReplacement(disabled, src, "foo\nbar", false); !strings.Contains(m.Error, "Could not find oldString") {
			t.Fatalf("findReplacement = %+v", m)
		}
	})
	t.Run("C4 fuzzy ambiguous", func(t *testing.T) {
		src := "  foo\n  bar\n  foo\n  bar\n"
		amb := ComputeEditResult(enabled, src, "foo\nbar", "x\ny", false)
		if amb.OK || !amb.NonUnique || !amb.Fuzzy {
			t.Fatalf("result = %+v", amb)
		}
		if strings.Contains(amb.Error, "Could not find") {
			t.Fatalf("ambiguous error = %q", amb.Error)
		}
		dis := ComputeEditResult(disabled, src, "foo\nbar", "x\ny", false)
		if dis.OK || dis.NonUnique || !strings.Contains(dis.Error, "Could not find oldString") {
			t.Fatalf("disabled result = %+v", dis)
		}
	})
	t.Run("C5 upstream rejects stay pure", func(t *testing.T) {
		amb := ComputeEditResult(enabled, "a a a", "a", "b", false)
		if amb.OK || !amb.NonUnique || amb.Fuzzy {
			t.Fatalf("ambiguous result = %+v", amb)
		}
		un := ComputeEditResult(enabled, "foo\n", "foo   ", "foo", false)
		if un.OK || !strings.Contains(un.Error, "unchanged") || un.Fuzzy {
			t.Fatalf("unchanged result = %+v", un)
		}
	})
}

func TestFuzzyReplacers(t *testing.T) {
	ctx := toolsTestCtx(map[string]string{})

	t.Run("line trimmed", func(t *testing.T) {
		// Indentation drift that no upstream tier can see.
		src := "function f() {\n\treturn 1;\n}\n"
		r := ComputeEditResult(ctx, src, "  return 1;  ", "  return 2;", false)
		if !r.OK || !r.Fuzzy {
			t.Fatalf("result = %+v", r)
		}
		if r.Updated != "function f() {\n  return 2;\n}\n" {
			t.Fatalf("updated = %q", r.Updated)
		}
	})
	t.Run("whitespace normalized", func(t *testing.T) {
		src := "const   x   =   1;\n"
		r := ComputeEditResult(ctx, src, "const x = 1;", "const x = 2;", false)
		if !r.OK || !r.Fuzzy || r.Updated != "const x = 2;\n" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("block anchor", func(t *testing.T) {
		src := "if (cond) {\n  doSomethingElse();\n}\n"
		r := ComputeEditResult(ctx, src, "if (cond) {\n  doSomething();\n}", "if (cond) {\n  done();\n}", false)
		if !r.OK || !r.Fuzzy {
			t.Fatalf("result = %+v", r)
		}
		if r.Updated != "if (cond) {\n  done();\n}\n" {
			t.Fatalf("updated = %q", r.Updated)
		}
	})
	t.Run("escape normalized", func(t *testing.T) {
		src := "line one\nline two\n"
		r := ComputeEditResult(ctx, src, `line one\nline two`, "changed", false)
		if !r.OK || !r.Fuzzy || r.Updated != "changed\n" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("fuzzy disabled", func(t *testing.T) {
		src := "const   x   =   1;\n"
		off := toolsTestCtx(map[string]string{"CAVE_TOOLS_FUZZY_EDIT": "0"})
		r := ComputeEditResult(off, src, "const x = 1;", "const x = 2;", false)
		if r.OK || r.Fuzzy || !strings.Contains(r.Error, "Could not find oldString") {
			t.Fatalf("result = %+v", r)
		}
	})
}

func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"", "abc", 3},
		{"abc", "", 3},
		{"kitten", "sitting", 3},
		{"doSomething();", "doSomethingElse();", 4},
	}
	for _, tt := range tests {
		if got := levenshtein(tt.a, tt.b); got != tt.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
