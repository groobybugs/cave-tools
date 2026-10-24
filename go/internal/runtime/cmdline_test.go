package runtime

import "testing"

func TestCmdExeCommandLineIsVerbatim(t *testing.T) {
	got := cmdExeCommandLine(`C:\Windows\system32\cmd.exe`, `echo "a b" & dir "C:\x y"`)
	want := `C:\Windows\system32\cmd.exe /d /s /c "echo "a b" & dir "C:\x y""`
	if got != want {
		t.Fatalf("cmdExeCommandLine = %q, want %q", got, want)
	}
}
