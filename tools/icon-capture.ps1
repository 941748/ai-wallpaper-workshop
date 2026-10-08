# Temporary icon verification helper: launch exe, read back the window icon it actually set (WM_GETICON), save it as PNG, kill it.
# No screen capture involved. Pure ASCII. Removed after verification.
param([string]$Exe)
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing
$sig = @"
using System;
using System.Runtime.InteropServices;
public class W34 {
  [DllImport("user32.dll")] public static extern IntPtr SendMessageW(IntPtr hWnd, uint msg, IntPtr wParam, IntPtr lParam);
}
"@
Add-Type -TypeDefinition $sig
$p = Start-Process $Exe -PassThru
$hwnd = [IntPtr]::Zero
for ($i = 0; $i -lt 60; $i++) {
    Start-Sleep -Milliseconds 300
    $p.Refresh()
    $h = $p.MainWindowHandle
    if ($null -ne $h) { $hwnd = [IntPtr]$h; break }
}
if ($hwnd -eq [IntPtr]::Zero) {
    Write-Output "NO_WINDOW"
    Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
    exit 1
}
Write-Output ("HWND=$hwnd title=[" + $p.MainWindowTitle + "]")
$WM_GETICON = 0x007F
$iconSmall = [W34]::SendMessageW($hwnd, $WM_GETICON, [IntPtr]0, [IntPtr]0)
$iconBig = [W34]::SendMessageW($hwnd, $WM_GETICON, [IntPtr]1, [IntPtr]0)
Write-Output ("WM_GETICON small=$iconSmall big=$iconBig (非零 = SetIcon 已生效)")
$hIco = if ($iconBig -ne [IntPtr]::Zero) { $iconBig } else { $iconSmall }
if ($hIco -ne [IntPtr]::Zero) {
    $ico = [System.Drawing.Icon]::FromHandle($hIco)
    $bmp = $ico.ToBitmap()
    $out = Join-Path $env:TEMP "icon-test.png"
    $bmp.Save($out, [System.Drawing.Imaging.ImageFormat]::Png)
    Write-Output ("SAVED=" + $out + " " + $bmp.Width + "x" + $bmp.Height)
    $bmp.Dispose()
} else {
    Write-Output "WINDOW_HAS_NO_ICON (SetIcon 未生效)"
}
Stop-Process -Id $p.Id -Force
Write-Output "TEST_INSTANCE_CLOSED"
