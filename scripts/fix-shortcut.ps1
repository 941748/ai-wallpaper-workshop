# 修复乱码: 重建桌面快捷方式 + 重新注册计划任务描述
$ErrorActionPreference = 'Stop'
$desktop = [Environment]::GetFolderPath('Desktop')
$dataDir = Join-Path $env:LOCALAPPDATA 'AIWallpaper'
$binDir  = Join-Path $dataDir 'bin'
$exePath = Join-Path $binDir 'wallpaper.exe'

# 1. 删除乱码快捷方式
Get-ChildItem $desktop -Filter 'AI*.lnk' -ErrorAction SilentlyContinue | Remove-Item -Force

# 2. 重建正确快捷方式
$ws = New-Object -ComObject WScript.Shell
$lnk = $ws.CreateShortcut((Join-Path $desktop 'AI 壁纸工坊.lnk'))
$lnk.TargetPath = $exePath
$lnk.WorkingDirectory = $binDir
$lnk.IconLocation = "$exePath,0"
$lnk.Description = '双击打开设置面板; 计划任务每小时自动换壁纸'
$lnk.Save()

# 3. 重新注册计划任务修复 Description(不触发 tick)
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

# 4. 验证结果写入 UTF-8 文件(绕开控制台编码)
$ok1 = Test-Path (Join-Path $desktop 'AI 壁纸工坊.lnk')
$items = (Get-ChildItem $desktop -Filter 'AI*.lnk').Name -join ' | '
$desc = $lnk.Description
$result = "快捷方式存在: $ok1`n桌面 AI*.lnk: $items`n快捷方式描述: $desc"
[System.IO.File]::WriteAllText((Join-Path $env:TEMP 'aiwallpaper_fix_result.txt'), $result, [System.Text.Encoding]::UTF8)
