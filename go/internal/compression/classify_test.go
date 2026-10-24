package compression

import "testing"

// TestClassifyCommand ports the command-classification assertions from
// test.mjs (2d) and the surrounding cases pinned against the TS reference.
func TestClassifyCommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    OutputPolicy
	}{
		// test.mjs 2d assertions.
		{name: "dev server passthrough", command: "npm run dev", want: PolicyPassthrough},
		{name: "auth passthrough", command: "gh auth login", want: PolicyPassthrough},
		{name: "curl verbatim", command: "curl https://api.example.com", want: PolicyVerbatim},
		{name: "gh api verbatim", command: "gh api repos/owner/repo/issues", want: PolicyVerbatim},
		{name: "test compressible", command: "cargo test", want: PolicyCompressible},
		{name: "git status compressible", command: "git status", want: PolicyCompressible},

		// Additional cases pinned against the TS reference.
		{name: "trimmed ssh passthrough", command: "  ssh host  ", want: PolicyPassthrough},
		{name: "pnpm dev passthrough", command: "pnpm dev", want: PolicyPassthrough},
		{name: "vite passthrough", command: "vite", want: PolicyPassthrough},
		{name: "git commit passthrough", command: "git commit -m x", want: PolicyPassthrough},
		{name: "docker inspect verbatim", command: "docker inspect x", want: PolicyVerbatim},
		{name: "docker exec passthrough", command: "docker exec -it x sh", want: PolicyPassthrough},
		{name: "cat verbatim", command: "cat file", want: PolicyVerbatim},
		{name: "aws output json verbatim", command: "aws s3api list-buckets --output json", want: PolicyVerbatim},
		{name: "aws output json equals verbatim", command: "aws s3 ls --output=json", want: PolicyVerbatim},
		{name: "aws without output stays compressible", command: "aws s3 ls", want: PolicyCompressible},
		{name: "kubectl get verbatim", command: "kubectl get pods", want: PolicyVerbatim},
		{name: "npm install compressible", command: "npm install", want: PolicyCompressible},
		{name: "echo compressible", command: " echo hi ", want: PolicyCompressible},

		// Muse regression: JS \s matches NBSP and U+FEFF, so classification
		// must not depend on strings.TrimSpace's ASCII/unicode.IsSpace set.
		{name: "nbsp ssh passthrough", command: "ssh\u00a0host", want: PolicyPassthrough},
		{name: "nbsp cat verbatim", command: "cat\u00a0file", want: PolicyVerbatim},
		{name: "nbsp padded ssh passthrough", command: "\u00a0ssh\u00a0host\u00a0", want: PolicyPassthrough},
		{name: "bom padded ssh passthrough", command: "\ufeffssh\u00a0host", want: PolicyPassthrough},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyCommand(test.command); got != test.want {
				t.Fatalf("ClassifyCommand(%q) = %q, want %q", test.command, got, test.want)
			}
		})
	}
}
