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
    [string]$Wxs = (Join-Path $PSScriptRoot "filees.wxs")
)

$ErrorActionPreference = "Stop"

if (-not (Get-Command wix -ErrorAction SilentlyContinue)) {
    throw @"
WiX Toolset v4 is required and 'wix' is not on PATH.
Install it with:  dotnet tool install --global wix
That needs the .NET SDK, which this machine does not have; the runtime alone is
not enough.
"@
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

    & wix build $Wxs `
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
