# Compatibility only for explicitly invoking the historical script path.
# Current MSI shortcuts and the legacy VBS shim call filees-launch.exe directly.
# All supervision, configuration and restart logic now lives in Go.
param([switch]$ShowGUI)
$ErrorActionPreference = 'Stop'
$launcher = Join-Path $PSScriptRoot 'filees-launch.exe'
if ($ShowGUI) { & $launcher --show } else { & $launcher }
exit $LASTEXITCODE
