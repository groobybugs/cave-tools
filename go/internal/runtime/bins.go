// Locating and lazily installing the ripgrep/fd search binaries.
//
// Port of src/runtime/bins.ts.
package runtime

import (
	"context"
	cryptosha256 "crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
)

// port of src/runtime/bins.ts:RG_VERSION
const RGVersion = "15.1.0"

// port of src/runtime/bins.ts:FD_VERSION
const FDVersion = "10.3.0"

// SearchBin mirrors src/runtime/bins.ts:SearchBin.
type SearchBin string

const (
	// BinRg is the "rg" search bin.
	BinRg SearchBin = "rg"
	// BinFd is the "fd" search bin.
	BinFd SearchBin = "fd"
)

type archiveSpec struct {
	triple    string
	extension string
	sha256    string
}

var rgArchives = map[string]archiveSpec{
	"x64-linux": {
		triple:    "x86_64-unknown-linux-musl",
		extension: "tar.gz",
		sha256:    "1c9297be4a084eea7ecaedf93eb03d058d6faae29bbc57ecdaf5063921491599",
	},
	"arm64-linux": {
		triple:    "aarch64-unknown-linux-gnu",
		extension: "tar.gz",
		sha256:    "2b661c6ef508e902f388e9098d9c4c5aca72c87b55922d94abdba830b4dc885e",
	},
	"x64-darwin": {
		triple:    "x86_64-apple-darwin",
		extension: "tar.gz",
		sha256:    "64811cb24e77cac3057d6c40b63ac9becf9082eedd54ca411b475b755d334882",
	},
	"arm64-darwin": {
		triple:    "aarch64-apple-darwin",
		extension: "tar.gz",
		sha256:    "378e973289176ca0c6054054ee7f631a065874a352bf43f0fa60ef079b6ba715",
	},
	"x64-win32": {
		triple:    "x86_64-pc-windows-msvc",
		extension: "zip",
		sha256:    "124510b94b6baa3380d051fdf4650eaa80a302c876d611e9dba0b2e18d87493a",
	},
	"arm64-win32": {
		triple:    "aarch64-pc-windows-msvc",
		extension: "zip",
		sha256:    "00d931fb5237c9696ca49308818edb76d8eb6fc132761cb2a1bd616b2df02f8e",
	},
	"ia32-win32": {
		triple:    "i686-pc-windows-msvc",
		extension: "zip",
		sha256:    "725be85a1e8f92878a548f40ee4f6df64bc93b809586462b3c6d884e1de1e83a",
	},
}

var fdArchives = map[string]archiveSpec{
	"x64-linux": {
		triple:    "x86_64-unknown-linux-musl",
		extension: "tar.gz",
		sha256:    "2b6bfaae8c48f12050813c2ffe1884c61ea26e750d803df9c9114550a314cd14",
	},
	"arm64-linux": {
		triple:    "aarch64-unknown-linux-musl",
		extension: "tar.gz",
		sha256:    "996b9b1366433b211cb3bbedba91c9dbce2431842144d925428ead0adf32020b",
	},
	"x64-darwin": {
		triple:    "x86_64-apple-darwin",
		extension: "tar.gz",
		sha256:    "50d30f13fe3d5914b14c4fff5abcbd4d0cdab4b855970a6956f4f006c17117a3",
	},
	"arm64-darwin": {
		triple:    "aarch64-apple-darwin",
		extension: "tar.gz",
		sha256:    "0570263812089120bc2a5d84f9e65cd0c25e4a4d724c80075c357239c74ae904",
	},
	"x64-win32": {
		triple:    "x86_64-pc-windows-msvc",
		extension: "zip",
		sha256:    "318aa2a6fa664325933e81fda60d523fff29444129e91ebf0726b5b3bcd8b059",
	},
	"arm64-win32": {
		triple:    "aarch64-pc-windows-msvc",
		extension: "zip",
		sha256:    "bf9b1e31bcac71c1e95d49c56f0d872f525b95d03854e94b1d4dd6786f825cc5",
	},
}

// EnsureBinsOptions mirrors src/runtime/bins.ts:EnsureBinsOptions.
type EnsureBinsOptions struct {
	Log func(message string)
}

// BinResolution mirrors src/runtime/bins.ts:BinResolution. TS null becomes "".
type BinResolution struct {
	Rg string `json:"rg"`
	Fd string `json:"fd"`
}

// port of src/runtime/bins.ts:platformKey
func platformKey() string {
	arch := goruntime.GOARCH
	switch arch {
	case "amd64":
		arch = "x64"
	case "386":
		arch = "ia32"
	}
	platform := goruntime.GOOS
	if platform == "windows" {
		platform = "win32"
	}
	return arch + "-" + platform
}

// port of src/runtime/bins.ts:exeName
func exeName(tool SearchBin) string {
	if goruntime.GOOS == "windows" {
		return string(tool) + ".exe"
	}
	return string(tool)
}

// port of src/runtime/bins.ts:binDir
func BinDir() string {
	if goruntime.GOOS == "windows" {
		// TS reads process.env directly here, outside any call context.
		root := os.Getenv("LOCALAPPDATA")
		if root == "" {
			root = filepath.Join(homeDir(), "AppData", "Local")
		}
		return filepath.Join(root, "cave-tools", "bin")
	}
	return filepath.Join(homeDir(), ".local", "share", "cave-tools", "bin")
}

// port of src/runtime/bins.ts:cachedBinPath
func CachedBinPath(tool SearchBin) string {
	return filepath.Join(BinDir(), exeName(tool))
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// port of src/runtime/bins.ts:whichOnPath
func WhichOnPath(ctx context.Context, names []string) string {
	pathEnv := GetEnv(ctx)["PATH"]
	var dirs []string
	for _, dir := range strings.Split(pathEnv, string(os.PathListSeparator)) {
		if dir != "" {
			dirs = append(dirs, dir)
		}
	}
	winExts := []string{""}
	if goruntime.GOOS == "windows" {
		winExts = []string{"", ".exe", ".cmd"}
	}
	for _, name := range names {
		if filepath.IsAbs(name) {
			if existsSync(name) {
				return name
			}
		}
		for _, dir := range dirs {
			for _, ext := range winExts {
				if ext != "" && strings.HasSuffix(strings.ToLower(name), ext) {
					continue
				}
				candidate := filepath.Join(dir, name+ext)
				if existsSync(candidate) {
					return candidate
				}
			}
		}
	}
	return ""
}

func existsSync(filePath string) bool {
	_, err := os.Stat(filePath)
	return err == nil
}

// port of src/runtime/bins.ts:whichUserRg
func WhichUserRg(ctx context.Context) string {
	if goruntime.GOOS == "windows" {
		return WhichOnPath(ctx, []string{"rg.exe", "rg"})
	}
	return WhichOnPath(ctx, []string{"rg"})
}

// port of src/runtime/bins.ts:whichUserFd
func WhichUserFd(ctx context.Context) string {
	if goruntime.GOOS == "windows" {
		return WhichOnPath(ctx, []string{"fd.exe", "fd"})
	}
	return WhichOnPath(ctx, []string{"fd", "fdfind"})
}

// port of src/runtime/bins.ts:run
func run(ctx context.Context, command string, args []string) (code int, stderr string) {
	cmd := exec.Command(command, args...)
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	// The TS helper spawns with windowsHide: true and without detached.
	configureSpawn(cmd, false)
	err := cmd.Run()
	stderr = errBuf.String()
	if cmd.ProcessState == nil {
		return -1, spawnError(command, err).Error()
	}
	return cmd.ProcessState.ExitCode(), stderr
}

// port of src/runtime/bins.ts:download
func download(url string) ([]byte, error) {
	response, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("download failed %d: %s", response.StatusCode, url)
	}
	bytes, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if len(bytes) == 0 {
		return nil, fmt.Errorf("empty download: %s", url)
	}
	return bytes, nil
}

// port of src/runtime/bins.ts:sha256
func sha256(buf []byte) string {
	sum := cryptosha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}

// port of src/runtime/bins.ts:extractArchive
func extractArchive(ctx context.Context, archivePath, destDir, extension string) error {
	if extension == "tar.gz" {
		code, stderr := run(ctx, "tar", []string{"-xzf", archivePath, "-C", destDir})
		if code != 0 {
			return fmt.Errorf("%s", firstNonEmpty(strings.TrimSpace(stderr), "tar extract failed"))
		}
		return nil
	}
	code, stderr := run(ctx, "tar", []string{"-xf", archivePath, "-C", destDir})
	if code == 0 {
		return nil
	}
	if goruntime.GOOS == "windows" {
		shell := WhichOnPath(ctx, []string{"powershell.exe", "pwsh.exe"})
		if shell == "" {
			shell = "powershell.exe"
		}
		script := fmt.Sprintf(
			"$global:ProgressPreference = 'SilentlyContinue'; Expand-Archive -LiteralPath '%s' -DestinationPath '%s' -Force",
			strings.ReplaceAll(archivePath, "'", "''"),
			strings.ReplaceAll(destDir, "'", "''"),
		)
		psCode, psStderr := run(ctx, shell, []string{"-NoProfile", "-NonInteractive", "-Command", script})
		if psCode != 0 {
			return fmt.Errorf("%s", firstNonEmpty(strings.TrimSpace(psStderr), "zip extract failed"))
		}
		return nil
	}
	return fmt.Errorf("%s", firstNonEmpty(strings.TrimSpace(stderr), "zip extract failed"))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// port of src/runtime/bins.ts:findExtractedBinary
func findExtractedBinary(root string, tool SearchBin) (string, error) {
	want := exeName(tool)
	stack := []string{root}
	for len(stack) > 0 {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			full := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				stack = append(stack, full)
				continue
			}
			if entry.Type().IsRegular() && entry.Name() == want {
				return full, nil
			}
		}
	}
	return "", fmt.Errorf("archive did not contain %s", want)
}

// installTool is the installFromGithub seam: tests replace it so no test ever
// reaches the network.
var installTool = installFromGithub

// port of src/runtime/bins.ts:installFromGithub
func installFromGithub(ctx context.Context, tool SearchBin, log func(message string)) (string, error) {
	specs := rgArchives
	version := RGVersion
	if tool == BinFd {
		specs = fdArchives
		version = FDVersion
	}
	spec, ok := specs[platformKey()]
	if !ok || spec.sha256 == "" {
		return "", nil
	}
	var filename, url string
	if tool == BinRg {
		filename = fmt.Sprintf("ripgrep-%s-%s.%s", RGVersion, spec.triple, spec.extension)
		url = fmt.Sprintf("https://github.com/BurntSushi/ripgrep/releases/download/%s/%s", RGVersion, filename)
	} else {
		filename = fmt.Sprintf("fd-v%s-%s.%s", FDVersion, spec.triple, spec.extension)
		url = fmt.Sprintf("https://github.com/sharkdp/fd/releases/download/v%s/%s", FDVersion, filename)
	}

	if log != nil {
		log(fmt.Sprintf("downloading %s %s → %s", tool, version, filename))
	}
	bytes, err := download(url)
	if err != nil {
		return "", err
	}
	digest := sha256(bytes)
	if digest != spec.sha256 {
		return "", fmt.Errorf("%s checksum mismatch: got %s, expected %s", tool, digest, spec.sha256)
	}

	work, err := os.MkdirTemp(os.TempDir(), fmt.Sprintf("cave-%s-", tool))
	if err != nil {
		return "", err
	}
	archivePath := filepath.Join(work, filename)
	defer os.RemoveAll(work)

	if err := os.WriteFile(archivePath, bytes, 0o666); err != nil {
		return "", err
	}
	if err := extractArchive(ctx, archivePath, work, spec.extension); err != nil {
		return "", err
	}
	extracted, err := findExtractedBinary(work, tool)
	if err != nil {
		return "", err
	}
	destDir := BinDir()
	if err := os.MkdirAll(destDir, 0o777); err != nil {
		return "", err
	}
	dest := CachedBinPath(tool)
	tmpDest := fmt.Sprintf("%s.%d.tmp", dest, os.Getpid())
	if err := copyFile(extracted, tmpDest); err != nil {
		return "", err
	}
	if goruntime.GOOS != "windows" {
		_ = os.Chmod(tmpDest, 0o755)
	}
	if err := os.Rename(tmpDest, dest); err != nil {
		return "", err
	}
	if log != nil {
		log(fmt.Sprintf("installed: %s", dest))
	}
	return dest, nil
}

// copyFile copies contents and preserves the source mode like fs.copyFile.
func copyFile(src, dest string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dest, data, info.Mode().Perm()); err != nil {
		return err
	}
	return nil
}

// port of src/runtime/bins.ts:resolveTool
func resolveTool(ctx context.Context, tool SearchBin, userPath string, log func(message string)) string {
	if userPath != "" {
		return userPath
	}
	cached := CachedBinPath(tool)
	if isExecutable(cached) {
		return cached
	}
	installed, err := installTool(ctx, tool, log)
	if err != nil {
		if log != nil {
			log(fmt.Sprintf("%s download failed: %s", tool, err.Error()))
		}
		return ""
	}
	return installed
}

const maxCachedPaths = 32

type binResolutionFuture struct {
	done  chan struct{}
	value BinResolution
}

var binsCache = struct {
	sync.Mutex
	pending map[string]*binResolutionFuture
}{pending: map[string]*binResolutionFuture{}}

// port of src/runtime/bins.ts:ensureBins
func EnsureBins(ctx context.Context, options EnsureBinsOptions) BinResolution {
	key := GetEnv(ctx)["PATH"]

	binsCache.Lock()
	future := binsCache.pending[key]
	if future == nil {
		if len(binsCache.pending) >= maxCachedPaths {
			binsCache.pending = map[string]*binResolutionFuture{}
		}
		future = &binResolutionFuture{done: make(chan struct{})}
		binsCache.pending[key] = future
		binsCache.Unlock()

		rg := resolveTool(ctx, BinRg, WhichUserRg(ctx), options.Log)
		fd := resolveTool(ctx, BinFd, WhichUserFd(ctx), options.Log)
		future.value = BinResolution{Rg: rg, Fd: fd}
		close(future.done)
		return future.value
	}
	binsCache.Unlock()
	<-future.done
	return future.value
}

// port of src/runtime/bins.ts:resolveRg
func ResolveRg(ctx context.Context, log func(message string)) string {
	return EnsureBins(ctx, EnsureBinsOptions{Log: log}).Rg
}

// port of src/runtime/bins.ts:resolveFd
func ResolveFd(ctx context.Context, log func(message string)) string {
	return EnsureBins(ctx, EnsureBinsOptions{Log: log}).Fd
}

// port of src/runtime/bins.ts:resetBinsCache
func ResetBinsCache() {
	binsCache.Lock()
	binsCache.pending = map[string]*binResolutionFuture{}
	binsCache.Unlock()
}
