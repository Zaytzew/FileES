# Private developer build; no installation, publication or live pair changes.
param(
    [Parameter(Mandatory=$true)][string]$Archive,
    [Parameter(Mandatory=$true)][string]$VcpkgPrefix,
    [Parameter(Mandatory=$true)][string]$Python,
    [Parameter(Mandatory=$true)][string]$MSBuild,
    [Parameter(Mandatory=$true)][string]$OutputDir
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repo = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$tmpRoot = [IO.Path]::GetFullPath((Join-Path $repo 'tmp')) + [IO.Path]::DirectorySeparatorChar
$output = [IO.Path]::GetFullPath($OutputDir)
if (-not $output.StartsWith($tmpRoot, [StringComparison]::OrdinalIgnoreCase) -or (Test-Path -LiteralPath $output)) {
    throw 'Output must be a new directory beneath workspace tmp'
}
$archivePath = (Resolve-Path -LiteralPath $Archive).Path
if ((Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash -ne '9D805D79521E994E5F38417705DA5CE4930827C8C4A7A2E9DC81E2F5D1E85A91') {
    throw 'Archive is not the reviewed Apache Subversion 1.14.5 source archive'
}
$vcpkg = (Resolve-Path -LiteralPath $VcpkgPrefix).Path
$pythonPath = (Resolve-Path -LiteralPath $Python).Path
$msbuildPath = (Resolve-Path -LiteralPath $MSBuild).Path
New-Item -ItemType Directory -Path $output | Out-Null
Expand-Archive -LiteralPath $archivePath -DestinationPath $output
$tree = Join-Path $output 'subversion-1.14.5'
& node (Join-Path $repo 'native/subversion-filees/apply-runtime.cjs') $tree
if ($LASTEXITCODE -ne 0) { throw 'Conditional runtime transformation failed' }
Push-Location -LiteralPath $tree
try {
    & $pythonPath gen-make.py --release --vsnet-version=2019 "--with-apr=$vcpkg" "--with-apr-util=$vcpkg" `
        "--with-zlib=$vcpkg" "--with-sqlite=$vcpkg" "--with-openssl=$vcpkg" "--with-serf=$vcpkg"
    if ($LASTEXITCODE -ne 0) { throw 'Subversion project generation failed' }
    & $msbuildPath build/win32/vcnet-vcproj/libsvn_client_dll.vcxproj /m /t:Build /p:Configuration=Release /p:Platform=x64 /p:PlatformToolset=v143 /p:WholeProgramOptimization=false "/p:SolutionDir=$tree\" /verbosity:minimal
    if ($LASTEXITCODE -ne 0) { throw 'Conditional Subversion build failed' }
} finally { Pop-Location }
$prefix = Join-Path $output 'prefix'
New-Item -ItemType Directory -Path "$prefix/include/subversion-1","$prefix/lib","$prefix/bin" | Out-Null
Get-ChildItem -LiteralPath "$tree/subversion/include" -Filter '*.h' -File | Copy-Item -Destination "$prefix/include/subversion-1"
Get-ChildItem -LiteralPath "$tree/Release" -Recurse -File | Where-Object { $_.Name -match '^(libsvn_|svn_).*\.lib$' } | Copy-Item -Destination "$prefix/lib"
Get-ChildItem -LiteralPath "$tree/Release" -Recurse -File | Where-Object { $_.Name -match '^(libsvn_|svn_).*\.dll$' } | Copy-Item -Destination "$prefix/bin"
if (-not (Test-Path -LiteralPath "$prefix/bin/libsvn_wc-1.dll")) { throw 'Missing conditional WC library' }
Copy-Item -LiteralPath (Join-Path $repo 'native/subversion-filees/filees_release.inc') -Destination "$prefix/FileES-SVN-modification.inc"
Copy-Item -LiteralPath (Join-Path $repo 'native/subversion-filees/filees_tree.inc') -Destination "$prefix/FileES-SVN-tree-modification.inc"
Copy-Item -LiteralPath (Join-Path $repo 'native/subversion-filees/apply-runtime.cjs') -Destination "$prefix/FileES-SVN-transformation.cjs"
Write-Output $prefix
