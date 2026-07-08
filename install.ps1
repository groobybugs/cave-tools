$ErrorActionPreference = "Stop"

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path

$node = Get-Command node -ErrorAction SilentlyContinue
if (-not $node) {
  Write-Error "node >=20 is required"
  exit 1
}

$major = (& node -p "process.versions.node.split('.')[0]")
if ([int]$major -lt 20) {
  $version = (& node -v)
  Write-Error "node >=20 is required (found $version)"
  exit 1
}

& node (Join-Path $ScriptDir "scripts/install-agent-instructions.mjs") @args
exit $LASTEXITCODE
