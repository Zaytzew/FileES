# Builds the FileES Windows installer from a staged bundle directory.
#
# The bundle is what tools/prepare-client-release-windows.sh assembles, so the
# MSI and the self-update channel ship byte-identical binaries. Building the MSI
# from its own separate compile would let the two drift, and the first symptom
# would be an update that reports a change where nothing changed.
#
# Nothing here signs. This host holds only the public release key, and for the
# alpha the MSI is deliberately unsigned - see README-INSTALL.md, which tells
# the person installing it what SmartScreen will say and why.
param(
    [Parameter(Mandatory = $true)][string]$BundleDir,
    [string]$Output = "",
    [string]$Wxs = ""
)

$ErrorActionPreference = "Stop"

# Resolved here rather than as a parameter default: Windows PowerShell 5.1
# evaluates param() defaults before $PSScriptRoot exists, so the default was an
# empty path and the script failed on its own first line.
if ($Wxs -eq "") { $Wxs = Join-Path $PSScriptRoot "filees.wxs" }

# WiX is a dotnet global tool, and finding it is two problems rather than one.
#
# The tools directory is often not on PATH in a fresh shell. And on a machine
# where the SDK was installed per-user - which is the only way to install it
# without an administrator - wix.exe still resolves its runtime from the
# machine-wide C:\Program Files\dotnet, finds only the older runtimes there, and
# refuses to start with a message about a missing framework rather than about
# where it looked. DOTNET_ROOT is what points it at the right one.
$found = Get-Command wix -ErrorAction SilentlyContinue
$wixCommand = if ($found) { $found.Source } else { "" }
if (-not $wixCommand) {
    $candidate = Join-Path $env:USERPROFILE ".dotnet\tools\wix.exe"
    if (Test-Path $candidate) { $wixCommand = $candidate }
}
if (-not $wixCommand) {
    throw @"
WiX Toolset is required and 'wix' was not found.
Install it with:  dotnet tool install --global wix
That needs the .NET SDK. Without an administrator, install the SDK per-user:
  curl -sSL https://dot.net/v1/dotnet-install.ps1 -o dotnet-install.ps1
  ./dotnet-install.ps1 -Channel 8.0 -InstallDir "$env:USERPROFILE\.dotnet"
"@
}
if (-not $env:DOTNET_ROOT -and (Test-Path (Join-Path $env:USERPROFILE ".dotnet\dotnet.exe"))) {
    $env:DOTNET_ROOT = Join-Path $env:USERPROFILE ".dotnet"
}

$bundle = (Resolve-Path $BundleDir).Path
foreach ($required in @("bin\filees.exe", "bin\filees-gui-wails.exe",
                        "autostart\start-filees.ps1", "autostart\start-filees.vbs", "VERSION")) {
    if (-not (Test-Path (Join-Path $bundle $required))) {
        throw "bundle is missing $required - build it with tools/prepare-client-release-windows.sh"
    }
}

# Windows Installer compares only three fields: major.minor.build. A fourth
# field is accepted by some tooling but ignored by MSI, so 0.1.15.819 and
# 0.1.15.825 would not order as upgrades. The bundle keeps the readable four
# fields; MSI maps its globally increasing SVN revision into the build field.
#
# The bundle's VERSION already carries that form, written by the release script,
# so this maps that value rather than reconstructing a revision from the source
# tree. Two independent sources are how an installer comes to disagree with the
# thing it installs.
$bundleVersion = (Get-Content (Join-Path $bundle "VERSION") -Raw).Trim()
if ($bundleVersion -notmatch '^\d+\.\d+\.\d+\.\d+$') {
    throw "bundle VERSION '$bundleVersion' is not major.minor.patch.revision"
}
$versionParts = $bundleVersion.Split('.')
$major = [uint32]$versionParts[0]
$minor = [uint32]$versionParts[1]
$revision = [uint32]$versionParts[3]
if ($major -gt 255 -or $minor -gt 255 -or $revision -gt 65535) {
    throw "bundle VERSION '$bundleVersion' cannot be represented as an MSI major.minor.build version"
}
$msiVersion = "$major.$minor.$revision"
if ($Output -eq "") {
    $Output = Join-Path (Split-Path -Parent $bundle) "filees-$bundleVersion.msi"
}

# WiX reads every payload from one directory, so the bundle's layout is
# flattened into a staging copy rather than teaching the .wxs about two source
# roots. Nothing is rebuilt: these are the same files the channel ships.
$staging = Join-Path ([System.IO.Path]::GetTempPath()) ("filees-msi-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $staging | Out-Null
try {
    Copy-Item (Join-Path $bundle "bin\filees.exe") $staging
    Copy-Item (Join-Path $bundle "bin\filees-gui-wails.exe") $staging
    Copy-Item (Join-Path $bundle "autostart\start-filees.ps1") $staging
    Copy-Item (Join-Path $bundle "autostart\start-filees.vbs") $staging
    Copy-Item (Join-Path $PSScriptRoot "License.rtf") $staging
    Copy-Item (Join-Path $PSScriptRoot "..\..\cmd\filees\assets\filees-folder.ico") $staging

    & $wixCommand build $Wxs `
        -arch x64 `
        -ext WixToolset.UI.wixext `
        -d "SourceDir=$staging" `
        -d "ProductVersion=$msiVersion" `
        -d "BundleVersion=$bundleVersion" `
        -out $Output
    if ($LASTEXITCODE -ne 0) {
        throw "WiX build failed with exit code $LASTEXITCODE"
    }
}
finally {
    Remove-Item -Recurse -Force $staging -ErrorAction SilentlyContinue
}

Write-Output $Output
Write-Output "unsigned: SmartScreen will warn on download - see packaging/windows/README-INSTALL.md"
