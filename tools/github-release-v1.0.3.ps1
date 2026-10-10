# One-shot: create GitHub Release v1.0.3 (tag already pushed) + upload AIWallpaper.exe asset.
# PAT comes from git credential manager (never echoed / persisted). Body read as UTF-8 to avoid PS5.1 GBK mojibake.
$ErrorActionPreference = "Stop"
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

$fill = ('protocol=https' + [char]10 + 'host=github.com' + [char]10) | git credential fill
$token = ($fill | Where-Object { $_ -like 'password=*' }) -replace '^password=', ''
if (-not $token) { Write-Host "[error] no GitHub PAT from credential fill" -ForegroundColor Red; exit 1 }

$notes = [System.IO.File]::ReadAllText("$repoRoot\dist\release-notes-v1.0.3.md", [System.Text.Encoding]::UTF8)
# Release name lives in a separate UTF-8 file so this script stays pure ASCII (PS5.1 parses BOM-less .ps1 as ANSI).
$name = [System.IO.File]::ReadAllText("$repoRoot\dist\release-name-v1.0.3.txt", [System.Text.Encoding]::UTF8).Trim()
$body = @{
    tag_name = "v1.0.3"
    name     = $name
    body     = $notes
    prerelease = $false
} | ConvertTo-Json

$bodyFile = Join-Path $env:TEMP "gh-release-body.json"
[System.IO.File]::WriteAllText($bodyFile, $body, (New-Object System.Text.UTF8Encoding($false)))

Write-Host "=== create release ===" -ForegroundColor Cyan
$respFile = Join-Path $env:TEMP "gh-release-resp.json"
curl.exe -sS -X POST "https://api.github.com/repos/941748/ai-wallpaper-workshop/releases" `
    -H "Authorization: Bearer $token" `
    -H "Accept: application/vnd.github+json" `
    -H "Content-Type: application/json" `
    --data-binary "@$bodyFile" `
    -o $respFile -w "HTTP=%{http_code}`n"

$resp = [System.IO.File]::ReadAllText($respFile, [System.Text.Encoding]::UTF8)
if ($resp -match '"upload_url":\s*"([^"{?]+)') {
    $uploadUrl = $Matches[1]
    Write-Host "release created, uploading asset..." -ForegroundColor Green
    curl.exe -sS -X POST "$uploadUrl`?name=AIWallpaper.exe" `
        -H "Authorization: Bearer $token" `
        -H "Content-Type: application/octet-stream" `
        --data-binary "@$repoRoot\dist\AIWallpaper.exe" `
        -o "$env:TEMP\gh-asset-resp.json" -w "HTTP=%{http_code} time=%{time_total}s`n"
    $asset = [System.IO.File]::ReadAllText("$env:TEMP\gh-asset-resp.json", [System.Text.Encoding]::UTF8)
    if ($asset -match '"browser_download_url":\s*"([^"]+)"') { Write-Host "asset: $($Matches[1])" -ForegroundColor Green }
    if ($asset -match '"size":\s*(\d+)') { Write-Host "size: $($Matches[1])" }
} else {
    Write-Host "[error] release creation failed:" -ForegroundColor Red
    Write-Host $resp.Substring(0, [Math]::Min(500, $resp.Length))
    exit 1
}
Remove-Item $bodyFile, $respFile -ErrorAction SilentlyContinue
Write-Host "=== done ===" -ForegroundColor Cyan
