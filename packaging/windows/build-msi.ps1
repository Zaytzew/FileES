param(
    [string]$SourceDir = $PSScriptRoot,
    [string]$Output = (Join-Path $PSScriptRoot "filees-gui.msi")
)

$ErrorActionPreference = "Stop"

if (-not (Get-Command wix -ErrorAction SilentlyContinue)) {
    throw "WiX Toolset v4 command 'wix' is required"
}

$source = (Resolve-Path $SourceDir).Path
$versionFile = Join-Path $source "VERSION"
if (-not (Test-Path $versionFile)) {
    throw "VERSION file is required in SourceDir"
}
$productVersion = (Get-Content $versionFile -Raw).Trim()
& wix build (Join-Path $source "filees-gui.wxs") `
    -arch x64 `
    -ext WixToolset.UI.wixext `
    -d "SourceDir=$source" `
    -d "ProductVersion=$productVersion" `
    -out $Output

if ($LASTEXITCODE -ne 0) {
    throw "WiX build failed with exit code $LASTEXITCODE"
}
