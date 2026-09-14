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
$magick = (Get-Command magick.exe -ErrorAction Stop).Source
if (-not (Test-Path -LiteralPath $makeappx -PathType Leaf)) { throw 'MakeAppx is missing from SdkBin' }
$signature = Get-AuthenticodeSignature -LiteralPath $makeappx
if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch 'Microsoft Corporation') {
    throw 'MakeAppx must have a valid Microsoft signature'
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
    & go build -tags native_svn_bundle -overlay $overlay -trimpath -buildvcs=false `
        -ldflags "-X main.version=$stamp -X main.injectedClientUpdateMode=store" `
        -o (Join-Path $payload 'filees.exe') ./cmd/filees
    if ($LASTEXITCODE -ne 0) { throw 'Store daemon build failed' }
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

$icon = Join-Path $source 'cmd/filees-gui-wails/assets/app-icon.png'
foreach ($item in @(@('StoreLogo.png', 50), @('Square150x150Logo.png', 150), @('Square44x44Logo.png', 44))) {
    & $magick $icon -resize "$($item[1])x$($item[1])" (Join-Path $assets $item[0])
    if ($LASTEXITCODE -ne 0) { throw "Icon generation failed: $($item[0])" }
}
$identity = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'store-identity.json') -Raw | ConvertFrom-Json
$template = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'AppxManifest.xml.in') -Raw
foreach ($value in @($identity.identityName, $identity.publisher, $identity.publisherDisplayName)) {
    if (-not $template.Contains($value)) { throw "Manifest and Store identity differ: $value" }
}
$manifest = $template.Replace('@MSIX_VERSION@', $Version)
[xml]$parsedManifest = $manifest
[IO.File]::WriteAllText((Join-Path $payload 'AppxManifest.xml'), $manifest, [Text.UTF8Encoding]::new($false))
$package = Join-Path $output ("FileES-Desktop-$Version-unsigned.msix")
& $makeappx pack /d $payload /p $package
if ($LASTEXITCODE -ne 0) { throw 'MakeAppx semantic validation/package creation failed' }
Get-FileHash -LiteralPath $package -Algorithm SHA256 | Format-List
Write-Output "Unsigned MSIX: $package"
