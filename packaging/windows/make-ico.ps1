# Builds a multi-size Windows .ico from one square PNG.
#
# It exists because the brand mark lives as an SVG and a PNG, and Windows wants
# an .ico in three places that Go cannot supply from a PNG: the Start Menu and
# desktop shortcuts, the entry in the list of installed programs, and the folder
# decoration Explorer reads out of desktop.ini.
#
# Kept as a script rather than done once by hand so the day the mark changes,
# the icon changes with it instead of drifting - which is exactly what had
# happened: the window icon was the current PNG while every shortcut still
# showed an .ico from a month earlier.
#
# Sizes are explicit. Windows picks the nearest entry and scales it, so a file
# carrying only 256 looks soft at 16, which is the size a person actually sees
# most of the time - in the Start Menu list, in Explorer's details view, on the
# taskbar at 100% scaling.
param(
    [Parameter(Mandatory = $true)][string]$Png,
    [Parameter(Mandatory = $true)][string]$Ico,
    [int[]]$Sizes = @(16, 24, 32, 48, 64, 128, 256)
)

$ErrorActionPreference = "Stop"
Add-Type -AssemblyName System.Drawing

$source = [System.Drawing.Image]::FromFile((Resolve-Path $Png).Path)
try {
    if ($source.Width -ne $source.Height) {
        throw "source is $($source.Width)x$($source.Height); a non-square icon is letterboxed by Windows"
    }

    $images = @()
    foreach ($size in $Sizes) {
        $bitmap = New-Object System.Drawing.Bitmap($size, $size, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
        $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
        try {
            $graphics.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
            $graphics.PixelOffsetMode   = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
            $graphics.SmoothingMode     = [System.Drawing.Drawing2D.SmoothingMode]::HighQuality
            $graphics.CompositingQuality = [System.Drawing.Drawing2D.CompositingQuality]::HighQuality
            $graphics.Clear([System.Drawing.Color]::Transparent)
            $graphics.DrawImage($source, 0, 0, $size, $size)
        } finally { $graphics.Dispose() }

        $stream = New-Object System.IO.MemoryStream
        # PNG-compressed entries, which Windows has read since Vista and which
        # keep the alpha channel intact without a mask bitmap.
        $bitmap.Save($stream, [System.Drawing.Imaging.ImageFormat]::Png)
        $images += ,@{ Size = $size; Bytes = $stream.ToArray() }
        $stream.Dispose(); $bitmap.Dispose()
    }

    $out = New-Object System.IO.MemoryStream
    $w = New-Object System.IO.BinaryWriter($out)
    try {
        $w.Write([uint16]0)                 # reserved
        $w.Write([uint16]1)                 # type: icon
        $w.Write([uint16]$images.Count)
        # Every entry is 16 bytes, so the first image starts after the directory.
        $offset = 6 + (16 * $images.Count)
        foreach ($image in $images) {
            # 256 is written as 0: the field is one byte and 256 does not fit.
            $dim = if ($image.Size -ge 256) { 0 } else { $image.Size }
            $w.Write([byte]$dim)            # width
            $w.Write([byte]$dim)            # height
            $w.Write([byte]0)               # palette entries: none, it is truecolour
            $w.Write([byte]0)               # reserved
            $w.Write([uint16]1)             # colour planes
            $w.Write([uint16]32)            # bits per pixel
            $w.Write([uint32]$image.Bytes.Length)
            $w.Write([uint32]$offset)
            $offset += $image.Bytes.Length
        }
        foreach ($image in $images) { $w.Write($image.Bytes) }
        $w.Flush()
        [System.IO.File]::WriteAllBytes($Ico, $out.ToArray())
    } finally { $w.Dispose(); $out.Dispose() }
}
finally { $source.Dispose() }

Write-Output ("$Ico  " + (Get-Item $Ico).Length + " B  rozmiary: " + ($Sizes -join ", "))
