param(
    [string]$SourceDir = $PSScriptRoot,
    [string]$Output = (Join-Path $PSScriptRoot "filees-gui.msi")
)

$ErrorActionPreference = "Stop"

if (-not (Get-Command wix -ErrorAction SilentlyContinue)) {
    throw "WiX Toolset v4 command 'wix' is required"
}

$source = (Resolve-Path $SourceDir).Path
& wix build (Join-Path $source "filees-gui.wxs") `
    -arch x64 `
    -d "SourceDir=$source" `
    -out $Output

if ($LASTEXITCODE -ne 0) {
    throw "WiX build failed with exit code $LASTEXITCODE"
}
