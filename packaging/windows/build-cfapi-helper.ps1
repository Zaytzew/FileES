# Builds filees-cfapi.exe, the Explorer anchor helper, for an alpha Windows
# release (beta and the Store are built without the Cloud Files API).
#
# Until 2026-09-28 no release carried the helper: the only copy was a manual
# one on the owner's station, so an anchor could not exist on a clean install.
# The helper sits next to filees.exe, where the native runtime's DLLs are not,
# so it is linked against the static C runtime and may import system
# libraries only; this script refuses anything else.
param(
    [Parameter(Mandatory=$true)][string]$CMake,
    [Parameter(Mandatory=$true)][string]$OutputDir,
    [Parameter(Mandatory=$true)][string]$Dumpbin
)
$ErrorActionPreference = 'Stop'
$cmakePath = (Resolve-Path -LiteralPath $CMake).Path
$dumpbinPath = (Resolve-Path -LiteralPath $Dumpbin).Path
$source = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$build = Join-Path ([System.IO.Path]::GetTempPath()) ('filees-cfapi-build-' + [guid]::NewGuid().ToString('N'))
try {
    & $cmakePath -S (Join-Path $source 'native/filees-cfapi') -B $build -G 'Visual Studio 17 2022' -A x64 | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'filees-cfapi CMake configuration failed' }
    & $cmakePath --build $build --config Release | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'filees-cfapi build failed' }
    $exe = Join-Path $build 'Release\filees-cfapi.exe'
    if (-not (Test-Path -LiteralPath $exe)) { throw "filees-cfapi.exe was not produced: $exe" }

    $allowed = @('KERNEL32.dll', 'cldapi.dll', 'ADVAPI32.dll', 'ole32.dll', 'OLEAUT32.dll', 'SHELL32.dll', 'SHLWAPI.dll', 'USER32.dll', 'bcrypt.dll', 'RPCRT4.dll')
    $dependents = @(& $dumpbinPath /dependents $exe 2>&1 | ForEach-Object { $_.Trim() } | Where-Object { $_ -match '\.dll$' })
    foreach ($dll in $dependents) {
        if (-not ($allowed | Where-Object { $_ -ieq $dll })) {
            throw "filees-cfapi.exe imports $dll; only system libraries may be imported (static C runtime expected)"
        }
    }

    New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null
    Copy-Item -LiteralPath $exe -Destination (Join-Path $OutputDir 'filees-cfapi.exe') -Force
    $version = & (Join-Path $OutputDir 'filees-cfapi.exe') version
    if ($LASTEXITCODE -ne 0 -or -not ($version -match '"schema":"filees.cfapi/v1"')) { throw "filees-cfapi.exe version check failed: $version" }
    Write-Output (Join-Path $OutputDir 'filees-cfapi.exe')
    Write-Output ("imports: " + ($dependents -join ', '))
} finally {
    Remove-Item -LiteralPath $build -Recurse -Force -ErrorAction SilentlyContinue
}
