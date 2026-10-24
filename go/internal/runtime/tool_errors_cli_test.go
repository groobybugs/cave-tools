package runtime

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestRunErrorsCommandEmpty(t *testing.T) {
	toolErrorsTestEnv(t)
	var stdout, stderr bytes.Buffer
	code := runErrorsCommandTo(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "error log: ") {
		t.Errorf("missing error log header: %q", out)
	}
	if !strings.Contains(out, "total: 0") {
		t.Errorf("missing zero total: %q", out)
	}
	if !strings.Contains(out, "No tool errors recorded.") {
		t.Errorf("missing empty message: %q", out)
	}
}

func TestRunErrorsCommandPopulated(t *testing.T) {
	toolErrorsTestEnv(t)
	observeSoft(t, "cave__ls", "/nonexistent-cli")
	FlushToolErrorQueue()

	var stdout, stderr bytes.Buffer
	code := runErrorsCommandTo([]string{"--limit", "5"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	out := stdout.String()
	for _, want := range []string{"total: 1", "by code:", "by tool:", "recent (last 5):", "tool=cave__ls", "outcome=soft", "code=not_found"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if !regexp.MustCompile(`id=[0-9a-f-]{36}`).MatchString(out) {
		t.Errorf("event id missing:\n%s", out)
	}
	if !regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z`).MatchString(out) {
		t.Errorf("ISO timestamp missing:\n%s", out)
	}
	if !strings.Contains(out, "by code: not_found=1") {
		t.Errorf("by code order/content wrong:\n%s", out)
	}
}

func TestRunErrorsCommandLimitForms(t *testing.T) {
	toolErrorsTestEnv(t)
	observeSoft(t, "cave__ls", "/nonexistent-cli")
	FlushToolErrorQueue()
	var stdout, stderr bytes.Buffer
	if code := runErrorsCommandTo([]string{"--limit=5"}, &stdout, &stderr); code != 0 {
		t.Fatalf("--limit=5 code = %d", code)
	}
	if !strings.Contains(stdout.String(), "recent (last 5):") {
		t.Errorf("output = %q", stdout.String())
	}
}

func TestRunErrorsCommandRejectsBadLimits(t *testing.T) {
	toolErrorsTestEnv(t)
	cases := [][]string{
		{"--limit", "0"},
		{"--limit", "2.5"},
		{"--limit", "abc"},
		{"--bogus"},
		{"extra"},
		{"--limit"},
	}
	for _, argv := range cases {
		var stdout, stderr bytes.Buffer
		if code := runErrorsCommandTo(argv, &stdout, &stderr); code != 2 {
			t.Errorf("args %v: code = %d, want 2", argv, code)
		}
		if !strings.Contains(stderr.String(), "Usage: cave-tools errors [--limit N]") {
			t.Errorf("args %v: missing usage: %q", argv, stderr.String())
		}
	}
}

func TestParseLimitClamps(t *testing.T) {
	var stderr bytes.Buffer
	limit, code, ok := parseLimit([]string{"--limit", "999"}, &stderr)
	if !ok || code != 0 || limit != 500 {
		t.Errorf("limit = %d code = %d ok = %v, want 500/0/true", limit, code, ok)
	}
	limit, _, ok = parseLimit(nil, &stderr)
	if !ok || limit != 20 {
		t.Errorf("default limit = %d ok = %v, want 20/true", limit, ok)
	}
	if _, code, ok := parseLimit([]string{"--limit", "9007199254740992"}, &stderr); ok || code != 2 {
		t.Errorf("unsafe integer accepted: code = %d ok = %v", code, ok)
	}
}
