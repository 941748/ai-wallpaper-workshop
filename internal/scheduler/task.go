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

// TickHours 计划任务理想节律(小时)。固定每小时触发一次; 换图频率与补池节流均由 tick 内部完成,
// 用户选择的"换图频率"不影响任务节律。安装/迁移/重注册统一使用此值, 避免各处硬编码打架。
const TickHours = 1

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

// VerifyRegistered 校验计划任务确实注册成功且指向 exePath, 返回状态描述(如 "就绪")。
// 注册可能被 360 等杀软静默拦截(schtasks /Create 假成功或事后清除),
// 因此以查询结果为准: XML 内容指向当前 exe 且带 --tick、状态存在且未被禁用, 才算注册成功。
func VerifyRegistered(exePath string) (string, error) {
	out, err := run("schtasks", "/Query", "/TN", config.TaskName, "/XML")
	if err != nil {
		return "", fmt.Errorf("任务查询失败: %v (%s)", err, out)
	}
	// /Query /XML 声明 UTF-16 但实际输出为本地 OEM 编码(中文系统即 GBK), 由 decodeOEM 统一处理。
	text := decodeOEM([]byte(out))
	if !strings.Contains(text, "<Command>"+xmlEscape(exePath)+"</Command>") {
		return "", fmt.Errorf("任务未指向当前程序(%s)", exePath)
	}
	if !strings.Contains(text, "--tick") {
		return "", fmt.Errorf("任务缺少 --tick 参数")
	}
	state, err := QueryState()
	if err != nil {
		return "", fmt.Errorf("任务状态查询失败: %v", err)
	}
	if state == "" {
		return "", fmt.Errorf("任务状态为空, 未确认注册")
	}
	if strings.Contains(state, "禁用") {
		return "", fmt.Errorf("任务已注册但处于禁用状态")
	}
	return state, nil
}

// RunTickNow 直接拉起本机隐藏 tick(--force, 立即换一张), 返回已启动的进程供调用方等待退出。
// 不经 schtasks: 手动触发不依赖计划任务注册(任务被杀软清除时按钮照样可用)。
func RunTickNow(exePath string) (*exec.Cmd, error) {
	cmd := exec.Command(exePath, "--tick", "--force")
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动换图进程失败: %v", err)
	}
	return cmd, nil
}

// QueryState 查询任务是否存在及其状态描述(如 "就绪"/"已禁用"/"正在运行")。
// 用 /V /FO LIST 拿键值行(普通查询是表格, 无前缀可解析);
// 状态列中文系统前缀为"模式:"(英文 "Mode:"), schtasks 输出为本地 OEM 编码(中文系统即 GBK), 需转码。
func QueryState() (string, error) {
	out, err := run("schtasks", "/Query", "/TN", config.TaskName, "/V", "/FO", "LIST")
	if err != nil {
		return "", err
	}
	return parseTaskState([]byte(out)), nil
}

// parseTaskState 从 schtasks /Query /V /FO LIST 输出中提取状态值; 找不到时返回空串。
func parseTaskState(out []byte) string {
	text := decodeOEM(out)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"状态:", "Status:", "模式:", "Mode:"} {
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
