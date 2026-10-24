package storage

import (
	"path/filepath"
	"testing"
)

func TestCaveToolsDataDirResolution(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "caveman")
	tests := []struct {
		name     string
		override string
		home     string
		want     string
	}{
		{
			name: "default is homedir/.cache/cave-tools",
			want: filepath.Join(home, ".cache", "cave-tools"),
		},
		{
			name:     "override wins",
			override: "/var/tmp/cave-data",
			want:     "/var/tmp/cave-data",
		},
		{
			// TS: process.env.CAVE_TOOLS_DATA_DIR?.trim() — surrounding
			// whitespace is stripped and the trimmed value is used.
			name:     "override is trimmed",
			override: "  /var/tmp/cave-data  ",
			want:     "/var/tmp/cave-data",
		},
		{
			// Whitespace-only is falsy in TS after trim, so the default wins.
			name:     "whitespace-only override falls back",
			override: "   ",
			want:     filepath.Join(home, ".cache", "cave-tools"),
		},
		{
			name:     "relative override is kept as written",
			override: "data",
			want:     "data",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := caveToolsDataDir(test.override, home); got != test.want {
				t.Fatalf("caveToolsDataDir(%q) = %q, want %q", test.override, got, test.want)
			}
		})
	}
}

func TestCaveToolsDataDirReadsEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAVE_TOOLS_DATA_DIR", " "+dir+" ")
	if got := CaveToolsDataDir(); got != dir {
		t.Fatalf("CaveToolsDataDir() = %q, want %q", got, dir)
	}
}

func TestDataDirDerivedPaths(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CAVE_TOOLS_DATA_DIR", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))

	tests := map[string]struct{ got, want string }{
		"db":            {DBPath(), filepath.Join(dir, "cave-tools.db")},
		"sessions":      {SessionsDir(), filepath.Join(dir, "sessions")},
		"lifetime":      {LifetimeFile(), filepath.Join(dir, "lifetime.json")},
		"archives":      {ArchiveDir(), filepath.Join(dir, "archives")},
		"jobs":          {JobsDir(), filepath.Join(dir, "jobs")},
		"read registry": {ReadRegistryFile(), filepath.Join(dir, "claude", "cave-tools", "read-registry.txt")},
	}
	for name, test := range tests {
		if test.got != test.want {
			t.Errorf("%s path = %q, want %q", name, test.got, test.want)
		}
	}
}

func TestReadRegistryFileWithoutClaudeConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	want := filepath.Join(home, ".claude", "cave-tools", "read-registry.txt")
	if got := ReadRegistryFile(); got != want {
		t.Fatalf("ReadRegistryFile() = %q, want %q", got, want)
	}
}
