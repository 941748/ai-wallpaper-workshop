# AI Wallpaper Workshop - Gitee release asset upload helper.
# Usage: powershell -ExecutionPolicy Bypass -File tools\gitee-release-upload.ps1 -ReleaseId 1187558 -File dist\AIWallpaper.exe
# Note: "-H Expect:" disables curl 100-continue, which previously hung the upload.
param(
    [Parameter(Mandatory = $true)][string]$ReleaseId,
    [Parameter(Mandatory = $true)][string]$File,
    [string]$Repo = "QQ941748/ai-wallpaper-workshop"
)

$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location $repoRoot

$tokenFile = Join-Path $env:USERPROFILE ".aiwallpaper\gitee.token"
if (-not (Test-Path $tokenFile)) {
    Write-Host "[error] token file not found: $tokenFile" -ForegroundColor Red
    exit 1
}
$tk = (Get-Content $tokenFile -Raw).Trim()
$filePath = (Resolve-Path $File).Path
$sizeMB = [math]::Round((Get-Item $filePath).Length / 1MB, 1)

Write-Host "=== Gitee release asset upload ===" -ForegroundColor Cyan
Write-Host "repo=$Repo release=$ReleaseId file=$filePath ($sizeMB MB)"

$url = "https://gitee.com/api/v5/repos/$Repo/releases/$ReleaseId/attach_files"
curl.exe -sS -X POST $url `
    -H "Authorization: token $tk" `
    -H "Expect:" `
    --connect-timeout 20 `
    --max-time 900 `
    -F "file=@$filePath" `
    -w "`nHTTP=%{http_code} time=%{time_total}s up=%{size_upload}B`n"

Write-Host "=== done ===" -ForegroundColor Cyan
