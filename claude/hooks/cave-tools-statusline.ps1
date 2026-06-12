# cave-tools — Windows PowerShell statusline mirror.
# Outputs the same badge as cave-tools-statusline.sh.

$ErrorActionPreference = 'SilentlyContinue'

$claudeDir = if ($env:CLAUDE_CONFIG_DIR) { $env:CLAUDE_CONFIG_DIR } else { Join-Path $HOME '.claude' }
$flagPath = Join-Path $claudeDir '.cave-tools-active'

if (-not (Test-Path $flagPath -PathType Leaf)) { exit 0 }
$item = Get-Item $flagPath -Force
if ($item.LinkType) { exit 0 }
if ($item.Length -gt 32) { exit 0 }

$mode = (Get-Content $flagPath -Raw -TotalCount 32).Trim().ToLower()
$mode = ($mode -replace '[^a-z0-9-]', '')

$valid = @('off','hint','enforce','strict')
if ($valid -notcontains $mode) { exit 0 }
if ($mode -eq 'off') { exit 0 }

$esc = [char]27
$reset = "$esc[0m"
$color = "$esc[38;5;244m"

if ($mode -eq 'enforce') {
    Write-Host -NoNewline "$color[CAVE-TOOLS]$reset"
} else {
    Write-Host -NoNewline "$color[CAVE-TOOLS:$($mode.ToUpper())]$reset"
}

if ($env:CAVE_TOOLS_STATUSLINE_SAVINGS -ne '0') {
    $suffixPath = Join-Path $claudeDir '.cave-tools-statusline-suffix'
    if (Test-Path $suffixPath -PathType Leaf) {
        $suffixItem = Get-Item $suffixPath -Force
        if (-not $suffixItem.LinkType -and $suffixItem.Length -le 64) {
            $suffix = (Get-Content $suffixPath -Raw -TotalCount 64) -replace '[\x00-\x1F]', ''
            if ($suffix) {
                Write-Host -NoNewline " $color$suffix$reset"
            }
        }
    }
}
