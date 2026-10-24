package runtime

// cmdExeCommandLine is the raw command line Node builds for
// spawn(command, { shell: cmd.exe }) on win32: normalizeSpawnArguments passes
// ['/d', '/s', '/c', `"${command}"`] with windowsVerbatimArguments, so libuv
// joins them with spaces and no further quoting. The command keeps its own
// quotes, which `cmd /s` handles by stripping only the outer pair.
func cmdExeCommandLine(shell, command string) string {
	return shell + ` /d /s /c "` + command + `"`
}
