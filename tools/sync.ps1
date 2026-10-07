# AI Wallpaper Workshop - Dual-remote sync script
# Usage: powershell -ExecutionPolicy Bypass -File tools\sync.ps1
# Pushes current branch to Gitee (direct) and GitHub (via frp proxy, auto on/off).
# Gitee token is read from %USERPROFILE%\.aiwallpaper\gitee.token (outside repo).

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location $repoRoot

Write-Host "=== Sync to Gitee + GitHub ===" -ForegroundColor Cyan

# ---------- 1) Gitee (direct; one-shot oauth2 URL, not stored in git config) ----------
$tokenFile = Join-Path $env:USERPROFILE ".aiwallpaper\gitee.token"
if (Test-Path $tokenFile) {
    $tk = (Get-Content $tokenFile -Raw).Trim()
    Write-Host "[1/2] Push Gitee..."
    git push "https://oauth2:$tk@gitee.com/QQ941748/ai-wallpaper-workshop.git" main
    Write-Host "  Gitee done" -ForegroundColor Green
} else {
    Write-Host "  [skip] token file not found: $tokenFile" -ForegroundColor Yellow
}

# ---------- 2) GitHub (via Singapore frp proxy; auto start/stop) ----------
Write-Host "[2/2] Push GitHub (via proxy)..."
$frpDir = "e:\X1-fwt\树莓派\frp-反向代理服务器\新加坡-frp-配置"
$frpcExe = Join-Path $frpDir "frpc.exe"
if (Test-Path $frpcExe) {
    $weStartedProxy = -not (Get-Process frpc -ErrorAction SilentlyContinue)
    if ($weStartedProxy) {
        Start-Process $frpcExe "-c frpc.toml" -WorkingDirectory $frpDir -WindowStyle Hidden
        Start-Sleep 4
    }
    $env:HTTP_PROXY = "http://127.0.0.1:8877"
    $env:HTTPS_PROXY = "http://127.0.0.1:8877"
    try {
        git push origin main
        Write-Host "  GitHub done" -ForegroundColor Green
    } finally {
        Remove-Item Env:HTTP_PROXY, Env:HTTPS_PROXY -ErrorAction SilentlyContinue
        if ($weStartedProxy) {
            Get-Process frpc -ErrorAction SilentlyContinue | Stop-Process -Force
            Write-Host "  proxy stopped"
        }
    }
} else {
    # fallback: try to resolve frp dir with wildcard (robust against path edits)
    $alt = Get-ChildItem "e:\X1-fwt" -Recurse -Filter "frpc.exe" -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($alt) {
        Write-Host "  [info] using frpc at: $($alt.FullName)"
        $frpDir = $alt.DirectoryName
        $weStartedProxy = -not (Get-Process frpc -ErrorAction SilentlyContinue)
        if ($weStartedProxy) {
            Start-Process $alt.FullName "-c frpc.toml" -WorkingDirectory $frpDir -WindowStyle Hidden
            Start-Sleep 4
        }
        $env:HTTP_PROXY = "http://127.0.0.1:8877"
        $env:HTTPS_PROXY = "http://127.0.0.1:8877"
        try {
            git push origin main
            Write-Host "  GitHub done" -ForegroundColor Green
        } finally {
            Remove-Item Env:HTTP_PROXY, Env:HTTPS_PROXY -ErrorAction SilentlyContinue
            if ($weStartedProxy) { Get-Process frpc -ErrorAction SilentlyContinue | Stop-Process -Force }
        }
    } else {
        Write-Host "  [skip] frpc.exe not found under e:\X1-fwt" -ForegroundColor Yellow
    }
}

Write-Host "=== Sync finished ===" -ForegroundColor Cyan
