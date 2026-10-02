# Dot-source from the Store builder. Never repair arbitrary/corrupt binaries:
# the caller supplies the exact bytes from before the trusted SDK resource edit.
Set-StrictMode -Version Latest
function Get-FileesPeLayout([byte[]]$Bytes) {
    if ($Bytes.Length -lt 64 -or $Bytes[0] -ne 77 -or $Bytes[1] -ne 90) { throw 'Invalid DOS header' }
    $pe = [BitConverter]::ToUInt32($Bytes, 60)
    if ($pe -gt $Bytes.Length - 24 -or [Text.Encoding]::ASCII.GetString($Bytes, $pe, 4) -ne "PE`0`0") { throw 'Invalid PE header' }
    $count = [BitConverter]::ToUInt16($Bytes, $pe + 6)
    $optionalSize = [BitConverter]::ToUInt16($Bytes, $pe + 20)
    $first = [long]$pe + 24 + $optionalSize
    if ($optionalSize -lt 68 -or $first + 40 * $count -gt $Bytes.Length) { throw 'Invalid PE section bounds' }
    $sections = @{}
    for ($i = 0; $i -lt $count; $i++) {
        $at = $first + 40 * $i
        $name = [Text.Encoding]::ASCII.GetString($Bytes, $at, 8).TrimEnd([char]0)
        $offset = [BitConverter]::ToUInt32($Bytes, $at + 20)
        $size = [BitConverter]::ToUInt32($Bytes, $at + 16)
        if ($sections.ContainsKey($name) -or [long]$offset + $size -gt $Bytes.Length) { throw 'Duplicate or out-of-bounds PE section' }
        $sections[$name] = @{ Offset = $offset; Size = $size }
    }
    return @{ Pe = $pe; Sections = $sections; Symbols = [BitConverter]::ToUInt32($Bytes, $pe + 16) }
}

function Repair-FileesMtCoffPointer([byte[]]$Before, [string]$Path) {
    $after = [IO.File]::ReadAllBytes($Path)
    $old = Get-FileesPeLayout $Before
    $new = Get-FileesPeLayout $after
    if ($old.Symbols -ne $new.Symbols) { throw 'SDK changed the COFF symbol count' }
    $hash = [Security.Cryptography.SHA256]::Create()
    try {
        foreach ($name in $old.Sections.Keys) {
            if ($name -eq '.rsrc') { continue }
            if (-not $new.Sections.ContainsKey($name)) { throw "Section disappeared: $name" }
            $left = $old.Sections[$name]; $right = $new.Sections[$name]
            if ($left.Size -ne $right.Size) { throw "Section resized: $name" }
            $beforeHash = [BitConverter]::ToString($hash.ComputeHash($Before, $left.Offset, $left.Size))
            $afterHash = [BitConverter]::ToString($hash.ComputeHash($after, $right.Offset, $right.Size))
            if ($beforeHash -ne $afterHash) { throw "Section content changed: $name" }
        }
    } finally { $hash.Dispose() }
    $original = [BitConverter]::ToUInt32($Before, $old.Pe + 12)
    $current = [BitConverter]::ToUInt32($after, $new.Pe + 12)
    if (-not $old.Sections.ContainsKey('.symtab') -or -not $new.Sections.ContainsKey('.symtab') -or $original -ne $old.Sections['.symtab'].Offset) {
        throw 'Original symbol table is not bound to the .symtab section'
    }
    $expected = [uint32]$new.Sections['.symtab'].Offset
    if ([long]$new.Symbols * 18 + 4 -gt $new.Sections['.symtab'].Size) { throw 'COFF symbol count exceeds section bounds' }
    if ($current -eq $expected) { return }
    if ($current -ne $original) { throw 'Unexpected SDK COFF pointer mutation' }
    [BitConverter]::GetBytes($expected).CopyTo($after, $new.Pe + 12)
    # Zero is valid for these user-mode EXEs; a previous checksum is now stale.
    [BitConverter]::GetBytes([uint32]0).CopyTo($after, $new.Pe + 24 + 64)
    [IO.File]::WriteAllBytes($Path, $after)
    $verified = [IO.File]::ReadAllBytes($Path)
    if ([BitConverter]::ToUInt32($verified, $new.Pe + 12) -ne $expected) { throw 'COFF repair verification failed' }
}
