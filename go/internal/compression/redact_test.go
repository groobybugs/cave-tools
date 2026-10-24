package compression

import "testing"

// TestRedactSecrets ports the redaction assertions from test.mjs (1b, the
// archive redaction checks, and the single-group/idempotency block), with one
// case per pattern pinned against the TS reference.
func TestRedactSecrets(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "bearer token",
			input: "Authorization: Bearer sk-live-abc123xyz789secret",
			want:  "Authorization: Bearer [REDACTED:Bearer token]",
		},
		{
			name:  "lowercase bearer",
			input: "bearer abcdefgh12345678",
			want:  "bearer [REDACTED:Bearer token]",
		},
		{
			name:  "authorization header basic",
			input: "authorization: Basic dXNlcjpwYXNz",
			want:  "authorization: Basic [REDACTED:Authorization header]",
		},
		{
			name:  "aws key wins over api key param",
			input: "api_key=AKIAIOSFODNN7EXAMPLE",
			want:  "api_key=[REDACTED:AWS key]",
		},
		{
			name:  "aws key",
			input: "id AKIAABCDEFGHIJKLMNOP x",
			want:  "id [REDACTED:AWS key] x",
		},
		{
			name:  "github token",
			input: "export GH ghp_abcdefghijklmnopqrstuvwxyz0123456789 done",
			want:  "export GH [REDACTED:GitHub token] done",
		},
		{
			name:  "anthropic key",
			input: "my key sk-ant-abcdefghijklmnopqrstuvwxyz12345 end",
			want:  "my key [REDACTED:Anthropic key] end",
		},
		{
			name:  "openai key",
			input: "key sk-abcdefghijklmnopqrstuvwxyz12345 end",
			want:  "key [REDACTED:OpenAI key] end",
		},
		{
			name:  "gitlab pat",
			input: "glpat-abcdefghijklmnopqrst",
			want:  "[REDACTED:GitLab PAT]",
		},
		{
			name:  "npm token",
			input: "npm_abcdefghijklmnop",
			want:  "[REDACTED:npm token]",
		},
		{
			name:  "jwt",
			input: "jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghij",
			want:  "jwt [REDACTED:JWT]",
		},
		{
			name:  "database url keeps the at sign",
			input: "postgres://user:s3cr3tp4ss@host:5432/db",
			want:  "postgres://user:[REDACTED:Database URL]@host:5432/db",
		},
		{
			name:  "postgresql url",
			input: "postgresql://app:s3cret@db/x",
			want:  "postgresql://app:[REDACTED:Database URL]@db/x",
		},
		{
			name:  "mongodb srv url",
			input: "mongodb+srv://u:pw1@c.example.net/db",
			want:  "mongodb+srv://u:[REDACTED:Database URL]@c.example.net/db",
		},
		{
			name:  "redis url with empty user",
			input: "redis://:pw@cache:6379",
			want:  "redis://:[REDACTED:Database URL]@cache:6379",
		},
		{
			name:  "rediss url",
			input: "rediss://u:pw2@cache:6380",
			want:  "rediss://u:[REDACTED:Database URL]@cache:6380",
		},
		{
			name:  "api key param beats github token",
			input: "token=ghp_abcdefghijklmnopqrstuvwxyz0123456789",
			want:  "token=[REDACTED:API key param]",
		},
		{
			name:  "generic long secret double quotes",
			input: "password = \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"",
			want:  "password = \"[REDACTED:Generic long secret]\"",
		},
		{
			name:  "generic long secret single quotes",
			input: "client_secret: 'BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB'",
			want:  "client_secret: '[REDACTED:Generic long secret]'",
		},
		{
			name:  "rsa private key block",
			input: "-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----",
			want:  "-----BEGIN RSA PRIVATE KEY-----[REDACTED:Private key block]-----END RSA PRIVATE KEY-----",
		},
		{
			name:  "private key block",
			input: "-----BEGIN PRIVATE KEY-----\nMIIEow\n-----END PRIVATE KEY-----",
			want:  "-----BEGIN PRIVATE KEY-----[REDACTED:Private key block]-----END PRIVATE KEY-----",
		},
		{
			name:  "no secret",
			input: "nothing secret here",
			want:  "nothing secret here",
		},
		{
			name:  "already redacted github token",
			input: "export GH [REDACTED:GitHub token] done",
			want:  "export GH [REDACTED:GitHub token] done",
		},
		{
			name:  "already redacted anthropic key",
			input: "my key [REDACTED:Anthropic key] end",
			want:  "my key [REDACTED:Anthropic key] end",
		},
		{
			name:  "already redacted aws key",
			input: "id [REDACTED:AWS key] x",
			want:  "id [REDACTED:AWS key] x",
		},
		{
			name:  "already redacted api key param",
			input: "token=[REDACTED:API key param]",
			want:  "token=[REDACTED:API key param]",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := RedactSecrets(test.input)
			if got != test.want {
				t.Fatalf("RedactSecrets(%q) = %q, want %q", test.input, got, test.want)
			}
			if twice := RedactSecrets(got); twice != got {
				t.Fatalf("RedactSecrets is not idempotent: %q -> %q", got, twice)
			}
		})
	}
}

// TestRedactSecretsJSWhitespace is the Muse regression: JS \s matches NBSP and
// U+FEFF, so a token separated from its label by one of those is still
// redacted. Expected strings are the TS reference output.
func TestRedactSecretsJSWhitespace(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "nbsp bearer token",
			input: "Authorization: Bearer\u00a0abcdef123456",
			want:  "Authorization: Bearer\u00a0[REDACTED:Bearer token]",
		},
		{
			name:  "nbsp bearer token alone",
			input: "bearer\u00a0abcdefgh12345678",
			want:  "bearer\u00a0[REDACTED:Bearer token]",
		},
		{
			name:  "nbsp authorization header",
			input: "authorization: Basic\u00a0dXNlcjpwYXNz",
			want:  "authorization: Basic\u00a0[REDACTED:Authorization header]",
		},
		{
			name:  "bom before bearer",
			input: "Authorization:\ufeffBearer\u00a0abcdef123456",
			want:  "Authorization:\ufeffBearer\u00a0[REDACTED:Bearer token]",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := RedactSecrets(test.input)
			if got != test.want {
				t.Fatalf("RedactSecrets(%q) = %q, want %q", test.input, got, test.want)
			}
			if twice := RedactSecrets(got); twice != got {
				t.Fatalf("RedactSecrets is not idempotent: %q -> %q", got, twice)
			}
		})
	}
}
