//go:build windows

package runtime

import "testing"

// TestIsCmdExeJSRegex is the Muse regression for Node's cmd.exe detection:
// Node's /^(?:.*\\)?cmd(?:\.exe)?$/i uses JS `.`, which refuses \r, U+2028
// and U+2029, while RE2's `.` only refuses \n. Node-derived, so it goes
// through jsre.
func TestIsCmdExeJSRegex(t *testing.T) {
	tests := []struct {
		name  string
		shell string
		want  bool
	}{
		{"bare cmd", "cmd", true},
		{"bare cmd.exe", "cmd.exe", true},
		{"uppercase", "CMD.EXE", true},
		{"absolute", `C:\Windows\System32\cmd.exe`, true},
		{"relative with dir", `System32\cmd`, true},
		{"other binary", `C:\Windows\System32\cmdx.exe`, false},
		{"cmd in the middle", `C:\cmd.exe\other.exe`, false},
		{"carriage return in path", "C:\\a\rb\\cmd.exe", false},
		{"line separator in path", "C:\\a\u2028b\\cmd.exe", false},
		{"paragraph separator in path", "C:\\a\u2029b\\cmd.exe", false},
		{"newline in path", "C:\\a\nb\\cmd.exe", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isCmdExe(test.shell); got != test.want {
				t.Fatalf("isCmdExe(%q) = %v, want %v", test.shell, got, test.want)
			}
		})
	}
}
