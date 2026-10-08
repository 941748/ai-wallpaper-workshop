// Package scheduler 通过 Task Scheduler XML + schtasks 注册每小时换图任务。
package scheduler

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"

	"wallpaper/internal/config"
)

// StartBoundary 计算任务起始边界: 今天(或下一小时)的 HH:phase 时刻。
// 任务随后按 PT<N>H 周期触发, 从而把每个用户的换图时刻固定在其 uid 哈希相位上, 削平整点尖峰。
func StartBoundary(now time.Time, phaseMinutes int) time.Time {
	phaseMinutes %= 60
	if phaseMinutes < 0 {
		phaseMinutes += 60
	}
	t := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), phaseMinutes, 0, 0, now.Location())
	if !t.After(now) {
		t = t.Add(time.Hour)
	}
	return t
}

// BuildXML 生成 Task Scheduler XML(UTF-16LE + BOM, schtasks /XML 要求)。
func BuildXML(exePath string, intervalHours, phaseMinutes int, now time.Time) ([]byte, error) {
	if intervalHours <= 0 {
		intervalHours = config.DefaultIntervalHour
	}
	boundary := StartBoundary(now, phaseMinutes).Format("2006-01-02T15:04:05")
	xmlText := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>%s 每小时静默出图换壁纸(tick 秒级进程, 无常驻)</Description>
  </RegistrationInfo>
  <Triggers>
    <TimeTrigger>
      <StartBoundary>%s</StartBoundary>
      <Enabled>true</Enabled>
      <Repetition>
        <Interval>PT%dH</Interval>
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
      <Command>%s</Command>
      <Arguments>--tick</Arguments>
    </Exec>
  </Actions>
</Task>`, config.AppNameCN, boundary, intervalHours, xmlEscape(exePath))

	enc := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder()
	out, err := enc.Bytes([]byte(xmlText))
	if err != nil {
		return nil, err
	}
	return out, nil
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

// Register 注册(或修复)计划任务。
func Register(exePath string, intervalHours, phaseMinutes int) error {
	data, err := BuildXML(exePath, intervalHours, phaseMinutes, time.Now())
	if err != nil {
		return err
	}
	tmp := filepath.Join(os.TempDir(), "aiwallpaper_task.xml")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	defer os.Remove(tmp)
	out, err := run("schtasks", "/Create", "/TN", config.TaskName, "/XML", tmp, "/F")
	if err != nil {
		return fmt.Errorf("schtasks /Create 失败: %v (%s)", err, out)
	}
	return nil
}

// Delete 删除计划任务。
func Delete() error {
	out, err := run("schtasks", "/Delete", "/TN", config.TaskName, "/F")
	if err != nil {
		return fmt.Errorf("schtasks /Delete 失败: %v (%s)", err, out)
	}
	return nil
}

// RunNow 立即触发一次(tick 会自行处理锁与安静检测)。
func RunNow() error {
	out, err := run("schtasks", "/Run", "/TN", config.TaskName)
	if err != nil {
		return fmt.Errorf("schtasks /Run 失败: %v (%s)", err, out)
	}
	return nil
}

// QueryState 查询任务是否存在及其状态描述(如 "就绪"/"已禁用"/"正在运行")。
// schtasks 输出为本地 OEM 编码(中文系统即 GBK), 需转码后再提取"状态"行。
func QueryState() (string, error) {
	out, err := run("schtasks", "/Query", "/TN", config.TaskName)
	if err != nil {
		return "", err
	}
	return parseTaskState([]byte(out)), nil
}

// parseTaskState 从 schtasks /Query 输出中提取状态值; 找不到时返回空串。
func parseTaskState(out []byte) string {
	text := decodeOEM(out)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"状态:", "Status:"} {
			if strings.HasPrefix(line, prefix) {
				return strings.TrimSpace(strings.TrimPrefix(line, prefix))
			}
		}
	}
	return ""
}

// decodeOEM 将本地编码(GBK)输出转为 UTF-8; 已是合法 UTF-8 时原样返回。
func decodeOEM(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	decoded, err := io.ReadAll(transform.NewReader(bytes.NewReader(b), simplifiedchinese.GBK.NewDecoder()))
	if err != nil {
		return string(b)
	}
	return string(decoded)
}

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
