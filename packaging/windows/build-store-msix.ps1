# Assemble an unsigned Store MSIX in a fresh output directory. Installation,
# signing, trust-store changes and migration of the live MSI are separate gates.
param(
    [Parameter(Mandatory=$true)][string]$NativeRuntime,
    [Parameter(Mandatory=$true)][string]$SdkBin,
    [Parameter(Mandatory=$true)][string]$OutputDir,
    [Parameter(Mandatory=$true)][string]$Version
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ($Version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$') {
    throw 'Version must be a four-part numeric MSIX version'
}
$source = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '../..')).Path
$runtime = (Resolve-Path -LiteralPath $NativeRuntime).Path
$sdk = (Resolve-Path -LiteralPath $SdkBin).Path
$base = (Get-Content -LiteralPath (Join-Path $source 'VERSION') -First 1).Trim()
if (-not $Version.StartsWith($base + '.', [StringComparison]::Ordinal)) {
    throw "MSIX version must begin with the source version $base"
}
$revision = ($Version -split '\.')[3]
Push-Location $source
try {
    $wcRevision = (& svnversion -n .).Trim()
    if ($LASTEXITCODE -ne 0 -or $wcRevision -ne $revision) {
        throw "Build requires a clean, uniform source checkout at r$revision (found $wcRevision)"
    }
} finally {
    Pop-Location
}
$makeappx = Join-Path $sdk 'makeappx.exe'
$makepri = Join-Path $sdk 'makepri.exe'
$mt = Join-Path $sdk 'mt.exe'
# Icons are resized with System.Drawing, which every Windows PowerShell has.
# ImageMagick used to be required for this alone, and a build machine without
# it could not produce a package at all (2026-09-23).
Add-Type -AssemblyName System.Drawing
function Resize-Png([string]$From, [int]$Size, [string]$To) {
    $sourceImage = [System.Drawing.Image]::FromFile($From)
    try {
        $bitmap = New-Object System.Drawing.Bitmap $Size, $Size, ([System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
        try {
            $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
            try {
                $graphics.Clear([System.Drawing.Color]::Transparent)
                $graphics.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
                $graphics.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
                $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::HighQuality
                $graphics.CompositingQuality = [System.Drawing.Drawing2D.CompositingQuality]::HighQuality
                $graphics.DrawImage($sourceImage, 0, 0, $Size, $Size)
            } finally { $graphics.Dispose() }
            $bitmap.Save($To, [System.Drawing.Imaging.ImageFormat]::Png)
        } finally { $bitmap.Dispose() }
    } finally { $sourceImage.Dispose() }
    if (-not (Test-Path -LiteralPath $To -PathType Leaf)) { throw "Icon generation failed: $To" }
}
if (-not (Test-Path -LiteralPath $makeappx -PathType Leaf)) { throw 'MakeAppx is missing from SdkBin' }
foreach ($tool in @($makeappx, $makepri, $mt)) {
    if (-not (Test-Path -LiteralPath $tool -PathType Leaf)) { throw "Windows SDK tool is missing: $tool" }
    $signature = Get-AuthenticodeSignature -LiteralPath $tool
    if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch 'Microsoft Corporation') {
        throw "Windows SDK tool must have a valid Microsoft signature: $tool"
    }
}
$output = [IO.Path]::GetFullPath($OutputDir)
if (Test-Path -LiteralPath $output) { throw "Output already exists: $output" }
New-Item -ItemType Directory -Path $output -ErrorAction Stop | Out-Null
$payload = Join-Path $output 'payload'
$assets = Join-Path $payload 'Assets'
New-Item -ItemType Directory -Path $assets -ErrorAction Stop | Out-Null

Push-Location $source
try {
    & go test -count=1 ./internal/domaincatalog
    if ($LASTEXITCODE -ne 0) { throw 'Domain catalog validation failed' }
    & go run ./cmd/filees-native-package $source $runtime (Join-Path $output 'native-overlay')
    if ($LASTEXITCODE -ne 0) { throw 'Native source/payload validation failed' }
    $overlay = Join-Path $output 'native-overlay/overlay.json'
    $stamp = $base + '+r' + $revision
    # nocfapi: the Store package ships without Explorer anchors and without any
    # Cloud Files API code (owner's decision, 2026-09-24). Checked below.
    & go build -tags native_svn_bundle,nocfapi -overlay $overlay -trimpath -buildvcs=false `
        -ldflags "-X main.version=$stamp -X main.injectedClientUpdateMode=store" `
        -o (Join-Path $payload 'filees.exe') ./cmd/filees
    if ($LASTEXITCODE -ne 0) { throw 'Store daemon build failed' }
    $daemonText = [Text.Encoding]::ASCII.GetString([IO.File]::ReadAllBytes((Join-Path $payload 'filees.exe')))
    # The updater names the retired helper; exclude API/manager code, not that filename.
    foreach ($marker in @('cldapi', 'CfGetPlaceholderState', 'main.explorerAnchorManager')) {
        if ($daemonText.IndexOf($marker, [StringComparison]::OrdinalIgnoreCase) -ge 0) { throw "Store daemon still carries Cloud Files code: $marker" }
    }
    & go build -tags production -trimpath -buildvcs=false `
        -ldflags "-H=windowsgui -X main.version=$stamp" `
        -o (Join-Path $payload 'filees-gui-wails.exe') ./cmd/filees-gui-wails
    if ($LASTEXITCODE -ne 0) { throw 'Store GUI build failed' }
    foreach ($mode in @('interactive', 'startup')) {
        $name = if ($mode -eq 'interactive') { 'filees-store-launcher.exe' } else { 'filees-store-startup.exe' }
        & go build -trimpath -buildvcs=false -ldflags "-H=windowsgui -X main.launcherMode=$mode" `
            -o (Join-Path $payload $name) ./cmd/filees-store-launcher
        if ($LASTEXITCODE -ne 0) { throw "Store launcher build failed: $mode" }
    }
} finally {
    Pop-Location
}

# Embed before packaging/signing. An AppxManifest does not declare the Win32
# process DPI context; WACK inspects the executable's RT_MANIFEST resource.
# filees.exe has no window, but the package declares it as an application (the
# execution alias), so WACK checks it too: without a manifest it warned "not
# DPI aware" (DPIAwarenessValidation, 2026-09-24).
foreach ($name in @('filees-store-launcher.exe', 'filees-store-startup.exe', 'filees-gui-wails.exe', 'filees.exe')) {
    $exe = Join-Path $payload $name
    $win32Manifest = switch ($name) {
        'filees-gui-wails.exe' { 'filees-gui.exe.manifest' }
        'filees.exe' { 'filees-daemon.exe.manifest' }
        default { 'filees-store-launcher.exe.manifest' }
    }
    & $mt -nologo -manifest (Join-Path $PSScriptRoot $win32Manifest) "-outputresource:$exe;#1"
    if ($LASTEXITCODE -ne 0) { throw "Win32 manifest embedding failed: $name" }
    $extracted = Join-Path $output "$name.manifest"
    & $mt -nologo "-inputresource:$exe;#1" "-out:$extracted"
    if ($LASTEXITCODE -ne 0) { throw "Win32 manifest extraction failed: $name" }
    [xml]$embedded = Get-Content -LiteralPath $extracted -Raw
    $dpi = $embedded.SelectSingleNode("//*[local-name()='dpiAwareness' and namespace-uri()='http://schemas.microsoft.com/SMI/2016/WindowsSettings']")
    $execution = $embedded.SelectSingleNode("//*[local-name()='requestedExecutionLevel']")
    $legacyDpi = $embedded.SelectSingleNode("//*[local-name()='dpiAware' and namespace-uri()='http://schemas.microsoft.com/SMI/2005/WindowsSettings']")
    if ($null -eq $legacyDpi -or $legacyDpi.InnerText -ne 'true/pm') { throw "Embedded manifest lacks DPI compatibility declaration: $name" }
    if ($null -eq $dpi -or $dpi.InnerText -ne 'PerMonitorV2' -or $null -eq $execution -or $execution.GetAttribute('level') -ne 'asInvoker') {
        throw "Embedded manifest must declare PerMonitorV2 and asInvoker: $name"
    }
}

$icon = Join-Path $source 'cmd/filees-gui-wails/assets/app-icon.png'
foreach ($item in @(@('StoreLogo.png', 50), @('Square150x150Logo.png', 150), @('Square44x44Logo.png', 44))) {
    Resize-Png $icon $item[1] (Join-Path $assets $item[0])
}
# The base 44px logo is a tile asset. Without target-size unplated variants,
# Windows can shrink the taskbar icon and put it on an accent-coloured plate.
# Keep all variants based on the same transparent FileES artwork.
foreach ($size in @(16, 20, 24, 30, 32, 36, 40, 44, 48, 60, 64, 72, 80, 96, 256)) {
    foreach ($variant in @('', '_altform-unplated', '_altform-lightunplated')) {
        $name = "Square44x44Logo.targetsize-$size$variant.png"
        Resize-Png $icon $size (Join-Path $assets $name)
    }
}
$identity = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'store-identity.json') -Raw | ConvertFrom-Json
$template = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'AppxManifest.xml.in') -Raw
foreach ($value in @($identity.identityName, $identity.publisher, $identity.publisherDisplayName)) {
    if (-not $template.Contains($value)) { throw "Manifest and Store identity differ: $value" }
}
# The Store refuses a package whose fourth version field is not zero ("Apps
# are not allowed to have a Version with a revision number other than zero",
# Partner Center, 2026-09-24). The revision goes into the third field, as in
# the MSI ProductVersion (major.minor.REV): it alone grows globally, and it
# must stay within 0..65535.
$parts = $Version -split '\.'
if ([int]$parts[3] -gt 65535) { throw "Revision $($parts[3]) does not fit the MSIX build field" }
$packageVersion = '{0}.{1}.{2}.0' -f $parts[0], $parts[1], $parts[3]
$manifest = $template.Replace('@MSIX_VERSION@', $packageVersion)
[xml]$parsedManifest = $manifest
[IO.File]::WriteAllText((Join-Path $payload 'AppxManifest.xml'), $manifest, [Text.UTF8Encoding]::new($false))
$priConfig = Join-Path $output 'priconfig.xml'
& $makepri createconfig /cf $priConfig /dq en-US /o
if ($LASTEXITCODE -ne 0) { throw 'MakePri configuration failed' }
& $makepri new /pr $payload /cf $priConfig /of (Join-Path $payload 'resources.pri') /o
if ($LASTEXITCODE -ne 0) { throw 'MakePri resource indexing failed' }
# The file keeps the product version (0.1.17.REV) for traceability; the
# package identity inside carries $packageVersion (0.1.REV.0).
$package = Join-Path $output ("FileES-Desktop-$Version-unsigned.msix")
& $makeappx pack /d $payload /p $package
if ($LASTEXITCODE -ne 0) { throw 'MakeAppx semantic validation/package creation failed' }
Get-FileHash -LiteralPath $package -Algorithm SHA256 | Format-List
Write-Output "Unsigned MSIX: $package"
