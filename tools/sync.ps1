# AI Wallpaper Workshop - Dual-remote sync script
# Usage: powershell -ExecutionPolicy Bypass -File tools\sync.ps1
# Pushes current branch to Gitee (direct) and GitHub (direct first; frp proxy fallback, auto on/off).
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

# ---------- 2) GitHub (direct first; fallback: frp proxy, auto start/stop) ----------
Write-Host "[2/2] Push GitHub..."
git push origin main
if ($LASTEXITCODE -eq 0) {
    Write-Host "  GitHub done (direct)" -ForegroundColor Green
} else {
    Write-Host "  direct push failed, falling back to frp proxy..." -ForegroundColor Yellow
    # Pick frpc.exe whose frpc.toml listens on :8877 (the Singapore config).
    # Path comes from the filesystem (not hardcoded) to stay ASCII-safe.
    $frpc = Get-ChildItem "e:\X1-fwt" -Recurse -Filter "frpc.exe" -ErrorAction SilentlyContinue |
        Where-Object {
            $toml = Join-Path $_.DirectoryName "frpc.toml"
            (Test-Path $toml) -and (Select-String -Path $toml -Pattern "8877" -Quiet)
        } |
        Select-Object -First 1
    if ($frpc) {
        $frpDir = $frpc.DirectoryName
        Write-Host "  using frpc at: $($frpc.FullName)"
        Start-Process $frpc.FullName "-c frpc.toml" -WorkingDirectory $frpDir -WindowStyle Hidden
        Start-Sleep 5
        $env:HTTP_PROXY = "http://127.0.0.1:8877"
        $env:HTTPS_PROXY = "http://127.0.0.1:8877"
        try {
            git push origin main
            if ($LASTEXITCODE -eq 0) {
                Write-Host "  GitHub done (via proxy)" -ForegroundColor Green
            } else {
                Write-Host "  [fail] proxy push also failed" -ForegroundColor Red
            }
        } finally {
            Remove-Item Env:HTTP_PROXY, Env:HTTPS_PROXY -ErrorAction SilentlyContinue
            Get-Process frpc -ErrorAction SilentlyContinue | Stop-Process -Force
            Write-Host "  proxy stopped"
        }
    } else {
        Write-Host "  [fail] no frpc.exe with frpc.toml found under e:\X1-fwt" -ForegroundColor Red
    }
}

Write-Host "=== Sync finished ===" -ForegroundColor Cyan
