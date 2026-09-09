# Assemble only the measured PE dependency closure, from explicit SDK roots.
# No PATH lookup for DLLs, no installed SVN distribution and no SDK-wide copy.
param(
    [Parameter(Mandatory=$true)][string]$CMake,
    [Parameter(Mandatory=$true)][string]$OutputDir,
    [Parameter(Mandatory=$true)][string]$SvnPrefix,
    [Parameter(Mandatory=$true)][string]$SvnSource,
    [Parameter(Mandatory=$true)][string]$VcpkgPrefix,
    [Parameter(Mandatory=$true)][string]$CRTRoot,
    [Parameter(Mandatory=$true)][string]$Dumpbin,
    [Parameter(Mandatory=$true)][string]$RedistNotice
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$source = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$cmakePath = (Resolve-Path -LiteralPath $CMake).Path
$dumpbinPath = (Resolve-Path -LiteralPath $Dumpbin).Path
$roots = @((Join-Path (Resolve-Path -LiteralPath $SvnPrefix).Path 'bin'),
           (Join-Path (Resolve-Path -LiteralPath $VcpkgPrefix).Path 'bin'),
           (Resolve-Path -LiteralPath $CRTRoot).Path)
$output = [IO.Path]::GetFullPath($OutputDir)
if (Test-Path -LiteralPath $output) { throw "Output already exists: $output" }
# Never package a caller's potentially stale EXE as the current source build.
# Preserve the unique build directory for diagnostics; no broad cleanup.
$parent = Split-Path -Parent $output
New-Item -ItemType Directory -Force -Path $parent | Out-Null
$build = Join-Path $parent ('.native-build-' + [guid]::NewGuid().ToString('N'))
& $cmakePath -S (Join-Path $source 'native/filees-svn') -B $build -G 'Visual Studio 17 2022' -A x64 `
    "-DSVN_INCLUDE_DIR=$SvnPrefix/include/subversion-1" "-DAPR_INCLUDE_DIR=$VcpkgPrefix/include" `
    "-DSVN_CLIENT_LIBRARY=$SvnPrefix/lib/libsvn_client-1.lib" "-DSVN_WC_LIBRARY=$SvnPrefix/lib/libsvn_wc-1.lib" `
    "-DSVN_SUBR_LIBRARY=$SvnPrefix/lib/libsvn_subr-1.lib" "-DAPR_LIBRARY=$VcpkgPrefix/lib/libapr-1.lib"
if ($LASTEXITCODE -ne 0) { throw 'Native CMake configuration failed' }
& $cmakePath --build $build --config RelWithDebInfo
if ($LASTEXITCODE -ne 0) { throw 'Native helper build failed' }
$helperPath = Join-Path $build 'RelWithDebInfo/filees-svn.exe'

# Windows OS contracts, not a wildcard for anything found in System32.
$system = @('ADVAPI32.dll','CRYPT32.dll','KERNEL32.dll','ole32.dll','RPCRT4.dll',
    'Secur32.dll','SHELL32.dll','SHFOLDER.dll','USER32.dll','VERSION.dll','WS2_32.dll')
$ucrt = '^api-ms-win-crt-(conio|convert|environment|filesystem|heap|locale|math|runtime|stdio|string|time|utility)-l1-1-0\.dll$'
$queue = [Collections.Generic.Queue[string]]::new()
$queue.Enqueue($helperPath)
$resolved = @{}
$osImports = @{}
while ($queue.Count -gt 0) {
    $path = $queue.Dequeue()
    $name = [IO.Path]::GetFileName($path)
    if ($resolved.ContainsKey($name)) { continue }
    $headers = (& $dumpbinPath /headers $path 2>&1) -join "`n"
    if ($LASTEXITCODE -ne 0 -or $headers -notmatch '8664 machine \(x64\)') { throw "Not an x64 PE image: $path" }
    $resolved[$name] = $path
    $lines = @(& $dumpbinPath /dependents $path 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "Cannot inspect dependencies: $path" }
    foreach ($line in $lines) {
        if ($line -notmatch '^\s+([A-Za-z0-9_.-]+\.dll)\s*$') { continue }
        $dll = $Matches[1]
        if ($system -contains $dll -or $dll -match $ucrt) { $osImports[$dll] = $true; continue }
        $candidates = @($roots | ForEach-Object { Join-Path $_ $dll } | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf })
        if ($candidates.Count -eq 0) { throw "Unresolved dependency $dll required by $name (explicit roots only)" }
        $hashes = @($candidates | ForEach-Object { (Get-FileHash -LiteralPath $_ -Algorithm SHA256).Hash } | Select-Object -Unique)
        if ($hashes.Count -ne 1) { throw "Ambiguous dependency $dll has different bytes in SDK roots" }
        if (-not $resolved.ContainsKey($dll)) { $queue.Enqueue($candidates[0]) }
    }
}

$notices = [ordered]@{
    'FileES-LICENSE.txt' = (Join-Path $source 'LICENSE')
    'Subversion-LICENSE.txt' = (Join-Path $SvnSource 'LICENSE')
    'Subversion-NOTICE.txt' = (Join-Path $SvnSource 'NOTICE')
    'MSVC-Redist.txt' = $RedistNotice
    'RUNTIME-NOTICE.txt' = (Join-Path $PSScriptRoot 'native-runtime-NOTICE.txt')
}
# serf is linked into SVN RA even when it has no separate imported DLL.
foreach ($port in @('apr','apr-util','expat','openssl','serf','sqlite3','zlib')) {
    $notices["$port-copyright.txt"] = Join-Path $VcpkgPrefix "share/$port/copyright"
}
foreach ($notice in $notices.Values) {
    if (-not (Test-Path -LiteralPath $notice -PathType Leaf)) { throw "Missing dependency notice: $notice" }
}

# The final output is published only after a complete copy and loader check.
$parent = Split-Path -Parent $output
New-Item -ItemType Directory -Force -Path $parent | Out-Null
$stage = Join-Path $parent ('.native-stage-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $stage | Out-Null
try {
    New-Item -ItemType Directory -Path (Join-Path $stage 'notices') | Out-Null
    $inventory = @()
    foreach ($name in ($resolved.Keys | Sort-Object -CaseSensitive)) {
        Copy-Item -LiteralPath $resolved[$name] -Destination (Join-Path $stage $name)
        $inventory += [ordered]@{ name=$name; sha256=(Get-FileHash -LiteralPath (Join-Path $stage $name) -Algorithm SHA256).Hash.ToLowerInvariant() }
    }
    foreach ($name in $notices.Keys) {
        Copy-Item -LiteralPath $notices[$name] -Destination (Join-Path $stage "notices/$name")
    }
    $nativeSource = Join-Path $source 'native/filees-svn'
    $sourceFiles = @((Get-Item -LiteralPath (Join-Path $nativeSource 'CMakeLists.txt')))
    $sourceFiles += @(Get-ChildItem -LiteralPath (Join-Path $nativeSource 'src') -Recurse -File | Where-Object { $_.Extension -in @('.c','.h') })
    $sourceInventory = @($sourceFiles | Sort-Object FullName | ForEach-Object {
        [ordered]@{ name=$_.FullName.Substring($nativeSource.Length+1).Replace('\','/'); sha256=(Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant() }
    })
    $manifest = [ordered]@{ schema='filees.native-runtime/v1'; platform='windows-amd64'; files=$inventory; sources=$sourceInventory; system_imports=@($osImports.Keys | Sort-Object -CaseSensitive) }
    $utf8 = [Text.UTF8Encoding]::new($false)
    [IO.File]::WriteAllText((Join-Path $stage 'notices/DEPENDENCIES.json'), ($manifest | ConvertTo-Json -Depth 5), $utf8)
    $oldPath = $env:PATH
    $oldLocation = Get-Location
    try {
        # The copied executable must not borrow DLLs from the build tree, PATH,
        # or the current working directory. Fixture SVN is never used here.
        $env:PATH = Join-Path $env:SystemRoot 'System32'
        Set-Location -LiteralPath $stage
        $version = & (Join-Path $stage 'filees-svn.exe') --version
        if ($LASTEXITCODE -ne 0) { throw "Packaged helper loader failed: $LASTEXITCODE" }
        $receipt = $version | ConvertFrom-Json
        if ($receipt.ok -ne $true -or $receipt.svn_runtime -ne $receipt.svn_headers) { throw "Invalid helper version receipt: $version" }
        foreach ($feature in @('update_changes','commit_targets_stdin_v1','info_inspect_remote_v1','status_remote_locks_v1')) {
            if ($receipt.features -notcontains $feature) { throw "Helper missing required feature: $feature" }
        }
    } finally {
        $env:PATH = $oldPath
        Set-Location -LiteralPath $oldLocation.Path
    }
    # Unlike Move-Item, Directory.Move refuses a concurrently created target
    # instead of nesting our staging directory inside it.
    [IO.Directory]::Move($stage, $output)
} finally {
    # Only our GUID-named staging directory, never the requested output/root.
    if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
}
Write-Output $output
Write-Output ("runtime files: {0}; Windows contracts: {1}" -f $resolved.Count, $osImports.Count)
