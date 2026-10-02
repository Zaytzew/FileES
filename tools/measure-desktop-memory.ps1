# One read-only sample. No IPC, process control, GC or access to memory contents.
param([string]$OutputDirectory)
$ErrorActionPreference = 'Stop'
$sampleErrors = [System.Collections.Generic.List[string]]::new()
$snapshot = @(Get-Process -ErrorAction Stop)
$roots = @($snapshot | Where-Object { $_.ProcessName -in @('filees', 'filees-gui-wails', 'filees-launch') })
$selected = [System.Collections.Generic.HashSet[int]]::new()
foreach ($proc in $roots) { [void]$selected.Add($proc.Id) }
$webviewDiscovery = 'available'
try {
    # Request only process identity/parent metadata, never command lines.
    $tree = @(Get-CimInstance Win32_Process -Property Name, ProcessId, ParentProcessId)
    $parents = [System.Collections.Generic.HashSet[int]]::new()
    foreach ($proc in $roots | Where-Object ProcessName -eq 'filees-gui-wails') { [void]$parents.Add($proc.Id) }
    do {
        $added = $false
        foreach ($node in $tree) {
            if ($node.Name -eq 'msedgewebview2.exe' -and $parents.Contains([int]$node.ParentProcessId) -and $parents.Add([int]$node.ProcessId)) {
                [void]$selected.Add([int]$node.ProcessId)
                $added = $true
            }
        }
    } while ($added)
} catch {
    $webviewDiscovery = 'unavailable'
    $sampleErrors.Add('WebView2 parent discovery: ' + $_.Exception.Message)
}
$entries = @(
    foreach ($proc in $snapshot | Where-Object { $selected.Contains($_.Id) }) {
        try {
            $proc.Refresh()
            $image = $proc.MainModule
            [ordered]@{
                name = $proc.ProcessName; pid = $proc.Id
                started_utc = $proc.StartTime.ToUniversalTime().ToString('o')
                private_bytes = $proc.PrivateMemorySize64; working_set_bytes = $proc.WorkingSet64
                handles = $proc.HandleCount; threads = $proc.Threads.Count
                image = $image.FileName; image_version = $image.FileVersionInfo.FileVersion
            }
        } catch { $sampleErrors.Add('Process ' + $proc.Id + ': ' + $_.Exception.Message) }
    }
)
$systemMemory = $null
try {
    $os = Get-CimInstance Win32_OperatingSystem -Property TotalVisibleMemorySize, FreePhysicalMemory
    $perf = Get-CimInstance Win32_PerfFormattedData_PerfOS_Memory -Property CommittedBytes, CommitLimit
    $systemMemory = [ordered]@{
        total_physical_bytes = [long]$os.TotalVisibleMemorySize * 1024
        free_physical_bytes = [long]$os.FreePhysicalMemory * 1024
        committed_bytes = [long]$perf.CommittedBytes; commit_limit_bytes = [long]$perf.CommitLimit
    }
} catch { $sampleErrors.Add('System memory: ' + $_.Exception.Message) }
$now = [DateTime]::UtcNow
$sample = [ordered]@{
    schema = 'filees.desktop-memory-sample/v1'; sampled_utc = $now.ToString('o')
    webview_discovery = $webviewDiscovery; processes = $entries
    system_memory = $systemMemory; errors = @($sampleErrors.ToArray())
}
$json = $sample | ConvertTo-Json -Depth 6
if ($OutputDirectory) {
    $target = [IO.Path]::GetFullPath($OutputDirectory)
    $workspaceTmp = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../tmp'))
    if (-not $target.StartsWith($workspaceTmp + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Sample output must be a subdirectory of this working copy tmp directory.'
    }
    [void][IO.Directory]::CreateDirectory($target)
    $file = Join-Path $target ($now.ToString('yyyyMMddTHHmmssfffffffZ') + '.json')
    $stream = [IO.File]::Open($file, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::Read)
    try {
        $bytes = [Text.UTF8Encoding]::new($false).GetBytes($json)
        $stream.Write($bytes, 0, $bytes.Length)
        $stream.Flush($true)
    } finally { $stream.Dispose() }
}
$json
