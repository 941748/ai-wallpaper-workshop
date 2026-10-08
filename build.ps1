# AI 壁纸工坊 — 一键构建脚本
# 用法: powershell -ExecutionPolicy Bypass -File build.ps1
# 产物: dist\wallpaper.exe(客户端, GUI 无窗口) + dist\aiwallpaper-cloud.exe(云服务)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $root

Write-Host "=== AI 壁纸工坊 构建 ===" -ForegroundColor Cyan

Write-Host "[1/4] 单元测试..."
go test ./...

Write-Host "[2/4] 构建客户端 (GUI 子系统, 无控制台窗口)..."
New-Item -ItemType Directory -Force dist | Out-Null
# 嵌入 Common Controls 6 manifest(walk 窗口库依赖, 缺失则窗口创建时 TTM_ADDTOOL 失败)
# 同时嵌入程序图标(assets\appicon.ico, 内为多尺寸 PNG; 由 tools\png2ico.ps1 生成)
$rsrc = Join-Path (go env GOPATH) 'bin\rsrc.exe'
if (-not (Test-Path $rsrc)) {
    Write-Host "  安装 rsrc 工具..."
    go install github.com/akavel/rsrc@latest
}
& $rsrc -manifest app.manifest -ico assets\appicon.ico -arch amd64 -o rsrc_windows_amd64.syso
go build -trimpath -ldflags "-s -w -H=windowsgui" -o dist\wallpaper.exe .

Write-Host "[3/4] 构建云服务 (Linux 部署用 GOOS=linux / Windows 调试用默认)..."
$os = $env:AW_BUILD_OS
if ($os -eq "linux") {
    $env:GOOS = "linux"; $env:GOARCH = "amd64"
    go build -trimpath -ldflags "-s -w" -o dist\aiwallpaper-cloud ./cloud
    Remove-Item Env:GOOS; Remove-Item Env:GOARCH
    Write-Host "  已生成 Linux 版云服务 dist\aiwallpaper-cloud"
} else {
    go build -trimpath -ldflags "-s -w" -o dist\aiwallpaper-cloud.exe ./cloud
}

Write-Host "[4/4] 产物列表:"
Get-ChildItem dist | Format-Table Name, @{Name="MB";Expression={[math]::Round($_.Length/1MB,1)}} -AutoSize

Write-Host ""
Write-Host "客户端分发: 把 dist\wallpaper.exe 发给用户双击运行(首次进入初始化向导)。"
Write-Host "云服务发布: 见 README.md 的「云服务部署」章节。"
Write-Host "=== 构建完成 ===" -ForegroundColor Green
