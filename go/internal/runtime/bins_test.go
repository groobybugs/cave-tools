package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPlatformKey(t *testing.T) {
	if goruntime.GOARCH != "amd64" || goruntime.GOOS != "linux" {
		t.Skipf("platform key assertions are written for x64-linux, running %s-%s", goruntime.GOARCH, goruntime.GOOS)
	}
	if got := platformKey(); got != "x64-linux" {
		t.Fatalf("platformKey = %q, want x64-linux", got)
	}
}

func TestBinDirAndCachedBinPath(t *testing.T) {
	home := t.TempDir()
	ctx := w1bCtx(t, t.TempDir(), map[string]string{"HOME": home, "USERPROFILE": home, "LOCALAPPDATA": ""})
	t.Setenv("LOCALAPPDATA", "")

	got := BinDir(ctx)
	want := filepath.Join(home, ".local", "share", "cave-tools", "bin")
	if goruntime.GOOS == "windows" {
		want = filepath.Join(home, "AppData", "Local", "cave-tools", "bin")
	}
	if got != want {
		t.Fatalf("BinDir = %q, want %q", got, want)
	}
	if got := CachedBinPath(ctx, BinRg); got != filepath.Join(want, exeName(BinRg)) {
		t.Fatalf("CachedBinPath = %q", got)
	}

	// TS bug: binDir read process.env directly, so a daemon client got the
	// server's bin dir. It must use the call env.
	callHome := t.TempDir()
	callCtx := w1bCtx(t, t.TempDir(), map[string]string{
		"HOME": callHome, "USERPROFILE": callHome, "LOCALAPPDATA": filepath.Join(callHome, "AppData", "Local"),
	})
	callWant := filepath.Join(callHome, ".local", "share", "cave-tools", "bin")
	if goruntime.GOOS == "windows" {
		callWant = filepath.Join(callHome, "AppData", "Local", "cave-tools", "bin")
	}
	if got := BinDir(callCtx); got != callWant {
		t.Fatalf("BinDir(call env) = %q, want %q", got, callWant)
	}

	// Without a call env it falls back to the process home.
	t.Setenv("HOME", home)
	if got := BinDir(w1bCtx(t, t.TempDir(), nil)); got != want {
		t.Fatalf("BinDir(fallback home) = %q, want %q", got, want)
	}
}

func TestWhichOnPath(t *testing.T) {
	dir := t.TempDir()
	rg := writeScript(t, dir, "rg", "#!/bin/sh\nexit 0\n")
	fdfind := writeScript(t, dir, "fdfind", "#!/bin/sh\nexit 0\n")
	// existsSync does not require the executable bit.
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+string(os.PathListSeparator))
	ctx := w1bCtx(t, t.TempDir(), nil)

	tests := []struct {
		name  string
		names []string
		want  string
	}{
		{"finds rg", []string{"rg"}, rg},
		{"falls through names", []string{"fd", "fdfind"}, fdfind},
		{"absolute existing", []string{plain}, plain},
		{"absolute missing", []string{filepath.Join(dir, "missing")}, ""},
		{"missing", []string{"nope"}, ""},
		{"does not need exec bit", []string{"plain"}, plain},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := WhichOnPath(ctx, test.names); got != test.want {
				t.Fatalf("WhichOnPath(%v) = %q, want %q", test.names, got, test.want)
			}
		})
	}

	t.Run("empty PATH", func(t *testing.T) {
		t.Setenv("PATH", "")
		if got := WhichOnPath(w1bCtx(t, t.TempDir(), nil), []string{"rg"}); got != "" {
			t.Fatalf("WhichOnPath with empty PATH = %q", got)
		}
	})
}

func TestWhichUserBins(t *testing.T) {
	dir := t.TempDir()
	rg := writeScript(t, dir, "rg", "#!/bin/sh\nexit 0\n")
	fd := writeScript(t, dir, "fd", "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", dir)
	ctx := w1bCtx(t, t.TempDir(), nil)
	if got := WhichUserRg(ctx); got != rg {
		t.Fatalf("WhichUserRg = %q, want %q", got, rg)
	}
	if got := WhichUserFd(ctx); got != fd {
		t.Fatalf("WhichUserFd = %q, want %q", got, fd)
	}
}

func TestIsExecutable(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "bin")
	if err := os.WriteFile(file, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isExecutable(file) {
		t.Fatal("0644 file reported executable")
	}
	if err := os.Chmod(file, 0o755); err != nil {
		t.Fatal(err)
	}
	if !isExecutable(file) {
		t.Fatal("0755 file reported non-executable")
	}
	if isExecutable(filepath.Join(dir, "missing")) {
		t.Fatal("missing file reported executable")
	}
}

func TestSHA256(t *testing.T) {
	if got := sha256(nil); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("sha256(nil) = %q", got)
	}
	if got := sha256([]byte("abc")); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("sha256Hex(abc) = %q", got)
	}
}

func TestDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ok":
			_, _ = writer.Write([]byte("hello"))
		case "/empty":
			writer.WriteHeader(http.StatusOK)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	body, err := download(server.URL + "/ok")
	if err != nil || string(body) != "hello" {
		t.Fatalf("download(/ok) = (%q, %v)", body, err)
	}
	if _, err := download(server.URL + "/empty"); err == nil ||
		err.Error() != "empty download: "+server.URL+"/empty" {
		t.Fatalf("download(/empty) error = %v", err)
	}
	if _, err := download(server.URL + "/missing"); err == nil ||
		err.Error() != "download failed 404: "+server.URL+"/missing" {
		t.Fatalf("download(/missing) error = %v", err)
	}
}

func TestExtractArchiveAndFindExtractedBinary(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "nested", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := writeScript(t, filepath.Join(source, "nested", "bin"), "rg", "#!/bin/sh\nexit 0\n")

	archive := filepath.Join(t.TempDir(), "rg.tar.gz")
	cmd := exec.Command("tar", "-czf", archive, "-C", source, ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tar create: %v\n%s", err, output)
	}

	dest := t.TempDir()
	if err := extractArchive(w1bCtx(t, dest, nil), archive, dest, "tar.gz"); err != nil {
		t.Fatalf("extractArchive: %v", err)
	}
	found, err := findExtractedBinary(dest, BinRg)
	if err != nil {
		t.Fatalf("findExtractedBinary: %v", err)
	}
	got, err := os.ReadFile(found)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("extracted rg = %q, want %q", got, original)
	}

	if _, err := findExtractedBinary(t.TempDir(), BinRg); err == nil ||
		err.Error() != "archive did not contain rg" {
		t.Fatalf("findExtractedBinary error = %v", err)
	}

	t.Run("zip without bsdtar fails on linux", func(t *testing.T) {
		if goruntime.GOOS == "windows" {
			t.Skip("windows uses powershell for zip")
		}
		zipPath := filepath.Join(t.TempDir(), "archive.zip")
		if err := os.WriteFile(zipPath, []byte("not a zip"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := extractArchive(w1bCtx(t, dest, nil), zipPath, dest, "zip"); err == nil {
			t.Fatal("extractArchive must fail for a broken zip on linux")
		}
	})
}

// TestExtractArchiveTrimsStderrLikeJS is the Muse regression for the
// "tar/zip extract failed" messages: TS uses stderr.trim(), which strips
// U+FEFF, while strings.TrimSpace keeps it.
func TestExtractArchiveTrimsStderrLikeJS(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the fake tar is a /bin/sh script; the zip fallback uses powershell on windows")
	}
	binDir := t.TempDir()
	// stderr is "boom\uFEFF" (EF BB BF are the UTF-8 bytes of U+FEFF).
	writeScript(t, binDir, "tar", "#!/bin/sh\nprintf 'boom\\357\\273\\277' >&2\nexit 1\n")
	t.Setenv("PATH", binDir)

	ctx := w1bCtx(t, t.TempDir(), nil)
	tests := []struct {
		extension string
		want      string
	}{
		{"tar.gz", "boom"},
		{"zip", "boom"},
	}
	for _, test := range tests {
		t.Run(test.extension, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "archive."+test.extension)
			err := extractArchive(ctx, archive, t.TempDir(), test.extension)
			if err == nil || err.Error() != test.want {
				t.Fatalf("extractArchive(%s) error = %q, want %q", test.extension, err, test.want)
			}
		})
	}
}

func TestEnsureBinsResolvesFromPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	first := t.TempDir()
	firstRg := writeScript(t, first, "rg", "#!/bin/sh\nexit 0\n")
	firstFd := writeScript(t, first, "fd", "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", first)
	ctx := w1bCtx(t, t.TempDir(), nil)

	ResetBinsCache()
	bins := EnsureBins(ctx, EnsureBinsOptions{})
	if bins.Rg != firstRg || bins.Fd != firstFd {
		t.Fatalf("EnsureBins = %#v, want %q/%q", bins, firstRg, firstFd)
	}

	// Same PATH is served from the per-PATH cache.
	if again := EnsureBins(ctx, EnsureBinsOptions{}); again != bins {
		t.Fatalf("EnsureBins cache miss: %#v != %#v", again, bins)
	}

	second := t.TempDir()
	secondRg := writeScript(t, second, "rg", "#!/bin/sh\nexit 0\n")
	secondFd := writeScript(t, second, "fd", "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", second)
	ctxSecond := w1bCtx(t, t.TempDir(), nil)
	if got := EnsureBins(ctxSecond, EnsureBinsOptions{}); got.Rg != secondRg || got.Fd != secondFd {
		t.Fatalf("EnsureBins(second PATH) = %#v", got)
	}

	ResetBinsCache()
	t.Setenv("PATH", first)
	ctxFirst := w1bCtx(t, t.TempDir(), nil)
	if got := EnsureBins(ctxFirst, EnsureBinsOptions{}); got.Rg != firstRg || got.Fd != firstFd {
		t.Fatalf("EnsureBins after reset = %#v", got)
	}
}

func TestResolveTool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx := w1bCtx(t, t.TempDir(), map[string]string{"HOME": home, "USERPROFILE": home, "LOCALAPPDATA": ""})
	cached := CachedBinPath(ctx, BinRg)
	if err := os.MkdirAll(filepath.Dir(cached), 0o700); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Dir(cached), exeName(BinRg), "#!/bin/sh\nexit 0\n")

	previous := installTool
	defer func() { installTool = previous }()
	installs := 0
	installTool = func(context.Context, SearchBin, func(string)) (string, error) {
		installs++
		return "", nil
	}

	ctx = w1bCtx(t, t.TempDir(), map[string]string{"PATH": ""})
	if got := resolveTool(ctx, BinRg, "", nil); got != cached {
		t.Fatalf("resolveTool cached = %q, want %q", got, cached)
	}
	if installs != 0 {
		t.Fatalf("cached resolution attempted %d installs", installs)
	}

	if got := resolveTool(ctx, BinRg, "/user/rg", nil); got != "/user/rg" {
		t.Fatalf("resolveTool(user) = %q", got)
	}

	installTool = func(context.Context, SearchBin, func(string)) (string, error) {
		return "", os.ErrNotExist
	}
	var logged []string
	if got := resolveTool(ctx, BinFd, "", func(message string) { logged = append(logged, message) }); got != "" {
		t.Fatalf("resolveTool failure = %q, want empty", got)
	}
	if len(logged) != 1 || !strings.HasPrefix(logged[0], "fd download failed: ") {
		t.Fatalf("log = %#v", logged)
	}
}
