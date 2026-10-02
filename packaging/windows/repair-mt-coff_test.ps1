$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'repair-mt-coff.ps1')
function New-TestPe([uint32]$SymbolOffset) {
    $bytes = New-Object byte[] 1024
    $bytes[0] = 77; $bytes[1] = 90
    [BitConverter]::GetBytes([uint32]64).CopyTo($bytes, 60)
    [Text.Encoding]::ASCII.GetBytes("PE`0`0").CopyTo($bytes, 64)
    [BitConverter]::GetBytes([uint16]2).CopyTo($bytes, 70)
    [BitConverter]::GetBytes([uint32]512).CopyTo($bytes, 76)
    [BitConverter]::GetBytes([uint32]1).CopyTo($bytes, 80)
    [BitConverter]::GetBytes([uint16]240).CopyTo($bytes, 84)
    [BitConverter]::GetBytes([uint32]123).CopyTo($bytes, 152)
    foreach ($section in @(@('.text', 408, 64), @('.symtab', $SymbolOffset, 64))) {
        $at = if ($section[0] -eq '.text') { 328 } else { 368 }
        [Text.Encoding]::ASCII.GetBytes($section[0]).CopyTo($bytes, $at)
        [BitConverter]::GetBytes([uint32]$section[2]).CopyTo($bytes, $at + 16)
        [BitConverter]::GetBytes([uint32]$section[1]).CopyTo($bytes, $at + 20)
        for ($j=0; $j -lt 64; $j++) { $bytes[$section[1]+$j] = $j }
    }
    return ,$bytes
}
$testFile = Join-Path ([IO.Path]::GetTempPath()) ('filees-coff-test-' + [Guid]::NewGuid().ToString('N') + '.exe')
try {
    $before = New-TestPe 512
    $after = New-TestPe 640
    [IO.File]::WriteAllBytes($testFile, $after)
    Repair-FileesMtCoffPointer $before $testFile
    $fixed = [IO.File]::ReadAllBytes($testFile)
    if ([BitConverter]::ToUInt32($fixed,76) -ne 640 -or [BitConverter]::ToUInt32($fixed,152) -ne 0) { throw 'Repair failed' }
    Repair-FileesMtCoffPointer $before $testFile
    if ([Convert]::ToBase64String($fixed) -ne [Convert]::ToBase64String([IO.File]::ReadAllBytes($testFile))) { throw 'Repair is not idempotent' }
    foreach ($variant in @('code change', 'symbol count', 'unexpected pointer', 'truncated')) {
        $bad = New-TestPe 640
        switch ($variant) {
            'code change' { $bad[410] = 255 }
            'symbol count' { [BitConverter]::GetBytes([uint32]2).CopyTo($bad,80) }
            'unexpected pointer' { [BitConverter]::GetBytes([uint32]600).CopyTo($bad,76) }
            'truncated' { $bad = $bad[0..50] }
        }
        [IO.File]::WriteAllBytes($testFile,$bad)
        $rejected = $false
        try { Repair-FileesMtCoffPointer $before $testFile } catch { $rejected = $true }
        if (-not $rejected -or [Convert]::ToBase64String($bad) -ne [Convert]::ToBase64String([IO.File]::ReadAllBytes($testFile))) { throw "Unsafe repair accepted: $variant" }
    }
    Write-Output 'COFF repair: relocation, idempotence and refusal variants PASS'
} finally { Remove-Item -LiteralPath $testFile -ErrorAction SilentlyContinue }
