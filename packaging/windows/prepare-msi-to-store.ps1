# Copy the MSI configuration without moving the transport identity or touching
# working copies. This preflight neither stops nor uninstalls the live MSI.
param([Parameter(Mandatory=$true)][string]$StoreDaemon)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$homePath = [IO.Path]::GetFullPath($env:USERPROFILE)
$legacy = Join-Path $env:LOCALAPPDATA 'Programs/FileES/config.json'
$store = Join-Path $homePath '.filees/store/config.json'
$backupRoot = Join-Path $homePath '.filees/migration-backups'
$daemon = (Resolve-Path -LiteralPath $StoreDaemon).Path
if (-not (Test-Path -LiteralPath $legacy -PathType Leaf)) { throw "MSI configuration not found: $legacy" }
if (Test-Path -LiteralPath $store) { throw "Store configuration already exists; refusing overwrite: $store" }
if (-not (Test-Path -LiteralPath (Join-Path $homePath '.local/share/filees') -PathType Container)) {
    throw 'Existing transport/profile state is missing; inspect before migrating'
}
& $daemon config-check --config $legacy
if ($LASTEXITCODE -ne 0) { throw 'MSI configuration did not pass validation in the Store daemon' }

$backup = Join-Path $backupRoot ((Get-Date -Format 'yyyyMMdd-HHmmss') + '-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $backup -ErrorAction Stop | Out-Null
$saved = Join-Path $backup 'config.json'
Copy-Item -LiteralPath $legacy -Destination $saved -ErrorAction Stop
if ((Get-FileHash -LiteralPath $legacy -Algorithm SHA256).Hash -ne
    (Get-FileHash -LiteralPath $saved -Algorithm SHA256).Hash) { throw 'MSI configuration backup differs' }

New-Item -ItemType Directory -Path (Split-Path -Parent $store) -Force -ErrorAction Stop | Out-Null
Copy-Item -LiteralPath $saved -Destination $store -ErrorAction Stop
if ((Get-FileHash -LiteralPath $saved -Algorithm SHA256).Hash -ne
    (Get-FileHash -LiteralPath $store -Algorithm SHA256).Hash) { throw 'Store configuration copy differs' }
& $daemon config-check --config $store
if ($LASTEXITCODE -ne 0) { throw 'Copied Store configuration failed validation' }
Write-Output "Original MSI configuration remains at: $legacy"
Write-Output "Recovery copy: $saved"
Write-Output "Store configuration: $store"
Write-Output 'Transport identity and working copies were not moved or modified.'
