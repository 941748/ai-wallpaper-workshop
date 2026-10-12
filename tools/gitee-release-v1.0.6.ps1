# One-shot: create Gitee Release v1.0.6 (tag already pushed). Asset upload uses gitee-release-upload.ps1.
# Token: %USERPROFILE%\.aiwallpaper\gitee.token. Body/name read as UTF-8 to avoid PS5.1 GBK mojibake.
$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

$tk = (Get-Content (Join-Path $env:USERPROFILE ".aiwallpaper\gitee.token") -Raw).Trim()
$notes = [System.IO.File]::ReadAllText("$repoRoot\dist\release-notes-v1.0.6.md", [System.Text.Encoding]::UTF8)
$name = [System.IO.File]::ReadAllText("$repoRoot\dist\release-name-v1.0.6.txt", [System.Text.Encoding]::UTF8).Trim()

$body = @{
    access_token    = $tk
    tag_name        = "v1.0.6"
    name            = $name
    body            = $notes
    target_commitish = "main"
} | ConvertTo-Json

$bodyFile = Join-Path $env:TEMP "gitee-release-body.json"
[System.IO.File]::WriteAllText($bodyFile, $body, (New-Object System.Text.UTF8Encoding($false)))

Write-Host "=== create Gitee release v1.0.6 ===" -ForegroundColor Cyan
$respFile = Join-Path $env:TEMP "gitee-release-resp.json"
curl.exe -sS -X POST "https://gitee.com/api/v5/repos/QQ941748/ai-wallpaper-workshop/releases" `
    -H "Content-Type: application/json" `
    --connect-timeout 20 --max-time 120 `
    --data-binary "@$bodyFile" `
    -o $respFile -w "HTTP=%{http_code}`n"

$resp = [System.IO.File]::ReadAllText($respFile, [System.Text.Encoding]::UTF8)
if ($resp -match '"id":\s*(\d+)') {
    Write-Host "release id: $($Matches[1])" -ForegroundColor Green
} else {
    Write-Host "[error] release creation failed:" -ForegroundColor Red
    Write-Host $resp.Substring(0, [Math]::Min(500, $resp.Length))
    exit 1
}
Remove-Item $bodyFile, $respFile -ErrorAction SilentlyContinue
Write-Host "=== done ===" -ForegroundColor Cyan
