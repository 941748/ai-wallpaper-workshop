# png2ico.ps1 - Convert a PNG to a multi-size PNG-compressed ICO (Vista+).
# Usage: powershell -ExecutionPolicy Bypass -File tools\png2ico.ps1 -Png assets\appicon.png -Ico assets\appicon.ico
# Pure ASCII on purpose: PowerShell 5.1 reads non-BOM scripts as ANSI.

param(
    [Parameter(Mandatory = $true)][string]$Png,
    [Parameter(Mandatory = $true)][string]$Ico
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing

$srcPath = (Resolve-Path $Png).Path
$src = [System.Drawing.Image]::FromFile($srcPath)
$sizes = @(16, 24, 32, 48, 64, 128, 256)
$datas = New-Object System.Collections.ArrayList

foreach ($s in $sizes) {
    $bmp = New-Object System.Drawing.Bitmap($s, $s)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
    $g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::HighQuality
    $g.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
    $g.DrawImage($src, 0, 0, $s, $s)
    $g.Dispose()
    $ms = New-Object System.IO.MemoryStream
    $bmp.Save($ms, [System.Drawing.Imaging.ImageFormat]::Png)
    $bmp.Dispose()
    [void]$datas.Add($ms.ToArray())
    $ms.Dispose()
}
$src.Dispose()

# ICO container: ICONDIR (6 bytes) + N x ICONDIRENTRY (16 bytes) + PNG payloads
$fs = [System.IO.File]::Create($Ico)
$bw = New-Object System.IO.BinaryWriter($fs)
$bw.Write([UInt16]0)               # reserved
$bw.Write([UInt16]1)               # type: icon
$bw.Write([UInt16]$sizes.Count)    # image count
$offset = 6 + 16 * $sizes.Count
for ($i = 0; $i -lt $sizes.Count; $i++) {
    $s = $sizes[$i]
    $dim = if ($s -ge 256) { 0 } else { $s }
    $bw.Write([Byte]$dim)          # width (0 = 256)
    $bw.Write([Byte]$dim)          # height
    $bw.Write([Byte]0)             # palette colors
    $bw.Write([Byte]0)             # reserved
    $bw.Write([UInt16]1)           # color planes
    $bw.Write([UInt16]32)          # bits per pixel
    $len = $datas[$i].Length
    $bw.Write([UInt32]$len)        # payload size
    $bw.Write([UInt32]$offset)     # payload offset
    $offset += $len
}
foreach ($d in $datas) { $bw.Write($d) }
$bw.Close()
$fs.Close()

Write-Host ("ICO written: " + $Ico + " (" + (Get-Item $Ico).Length + " bytes, " + $sizes.Count + " sizes)")
