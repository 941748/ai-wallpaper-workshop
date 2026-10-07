# 官网素材处理: 生成图 PNG → 压缩 JPG(一次性辅助脚本, 重新生成素材时改 $src 路径)
# 用法: powershell -ExecutionPolicy Bypass -File tools\make-site-assets.ps1
Add-Type -AssemblyName System.Drawing
$src = "C:\Users\SXSJ\.qoder-cn\vibe_images"
$dst = "e:\X1-fwt\arm64-c\wallpaper\site\assets"
New-Item -ItemType Directory -Force $dst | Out-Null

$jpeg = [System.Drawing.Imaging.ImageCodecInfo]::GetImageEncoders() | Where-Object { $_.MimeType -eq 'image/jpeg' }
$ep = New-Object System.Drawing.Imaging.EncoderParameters(1)
$ep.Param[0] = New-Object System.Drawing.Imaging.EncoderParameter([System.Drawing.Imaging.Encoder]::Quality, 82)

$map = @(
  @("$src\hero-inkwash-dawn_1791357243.png", "$dst\hero.jpg", 1792),
  @("$src\demo-realistic-cat_1791357286.png", "$dst\demo-cat.jpg", 1200),
  @("$src\demo-neochinese-mountain_1791357287.png", "$dst\demo-mountain.jpg", 1200),
  @("$src\demo-dreamy-aurora_1791357287.png", "$dst\demo-aurora.jpg", 1200)
)
foreach ($m in $map) {
  $img = [System.Drawing.Image]::FromFile($m[0])
  $w = $m[2]
  $h = [int]($img.Height * $w / $img.Width)
  $bmp = New-Object System.Drawing.Bitmap($w, $h)
  $g = [System.Drawing.Graphics]::FromImage($bmp)
  $g.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
  $g.DrawImage($img, 0, 0, $w, $h)
  $bmp.Save($m[1], $jpeg, $ep)
  $g.Dispose(); $bmp.Dispose(); $img.Dispose()
  Write-Host ("OK: {0} ({1} KB)" -f $m[1], [int]((Get-Item $m[1]).Length / 1KB))
}
