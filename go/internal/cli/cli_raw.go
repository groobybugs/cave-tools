// Raw shell passthrough shared by `cave-tools raw`, `CAVE_RAW=1` and the
// Windows cmd.exe branch.
package cli

import (
	"context"
	"io"

	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/tools/bash"
)

// runRaw runs command untouched: no rtk rewrite, no compression, inherited
// stdio, shell-style exit status (128+n on signal).
// port of src/cli-tools.ts:runRaw
func runRaw(ctx context.Context, command string, workdir *string, stdin io.Reader, stdout, stderr io.Writer) int {
	return rt.RunWithContext(ctx, rt.LocalContext("cli-raw"), func(ctx context.Context) int {
		shell := bash.CliShell(ctx)
		cmd := rawCommand(shell, command)
		cmd.Stdin = stdin
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if workdir != nil {
			cmd.Dir = *workdir
		}
		if err := cmd.Start(); err != nil {
			return finish(stderr, "cave-tools raw: "+nodeSpawnMessage(shell, err), 127)
		}
		err := cmd.Wait()
		return rawExitCode(cmd, err)
	})
}
