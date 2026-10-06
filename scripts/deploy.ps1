# AI 壁纸工坊 — 正式部署脚本: bin 副本 + 计划任务 + 桌面快捷方式
# 用法: powershell -ExecutionPolicy Bypass -File scripts\deploy.ps1
$ErrorActionPreference = 'Stop'
$dataDir = Join-Path $env:LOCALAPPDATA 'AIWallpaper'
$binDir  = Join-Path $dataDir 'bin'
$exePath = Join-Path $binDir 'wallpaper.exe'
$srcExe  = Join-Path $PSScriptRoot '..\dist\wallpaper.exe'

# 1. 复制正式副本(与向导注册任务时一致: 计划任务指向 bin, 不再依赖开发目录)
New-Item -ItemType Directory -Force -Path $binDir | Out-Null
Copy-Item -Force $srcExe $exePath
Write-Host "[1/4] 正式副本: $exePath"

# 2. 生成计划任务 XML(UTF-16LE + BOM, schtasks /XML 要求; 与 internal/scheduler 一致)
$next = (Get-Date).Date.AddHours((Get-Date).Hour + 1)
$boundary = $next.ToString('yyyy-MM-ddTHH:mm:ss')
$xml = @"
<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>AI 壁纸工坊 每小时静默出图换壁纸(tick 秒级进程, 无常驻)</Description>
  </RegistrationInfo>
  <Triggers>
    <TimeTrigger>
      <StartBoundary>$boundary</StartBoundary>
      <Enabled>true</Enabled>
      <Repetition>
        <Interval>PT1H</Interval>
        <StopAtDurationEnd>false</StopAtDurationEnd>
      </Repetition>
    </TimeTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT15M</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>$exePath</Command>
      <Arguments>--tick</Arguments>
    </Exec>
  </Actions>
</Task>
"@
$xmlPath = Join-Path $env:TEMP 'aiwallpaper_task.xml'
[System.IO.File]::WriteAllText($xmlPath, $xml, [System.Text.Encoding]::Unicode)
schtasks /Create /TN 'AIWallpaper' /XML $xmlPath /F | Out-Null
Write-Host "[2/4] 计划任务 AIWallpaper 已注册(每小时, 首个触发 $boundary)"

# 3. 桌面快捷方式
$desktop = [Environment]::GetFolderPath('Desktop')
$ws = New-Object -ComObject WScript.Shell
$lnk = $ws.CreateShortcut((Join-Path $desktop 'AI 壁纸工坊.lnk'))
$lnk.TargetPath = $exePath
$lnk.WorkingDirectory = $binDir
$lnk.IconLocation = "$exePath,0"
$lnk.Description = '双击打开设置面板; 计划任务每小时自动换壁纸'
$lnk.Save()
Write-Host "[3/4] 桌面快捷方式: $desktop\AI 壁纸工坊.lnk"

# 4. 立即触发一次验证静默链路
schtasks /Run /TN 'AIWallpaper' | Out-Null
Write-Host '[4/4] 已触发 AIWallpaper 任务(静默 tick 进行中)'
