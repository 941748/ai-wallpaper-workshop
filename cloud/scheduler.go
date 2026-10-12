package main

import (
	"encoding/json"
	"time"
)

// 空闲主动预生成参数: 每用户 ready 目标 / 活跃窗口 / 单次调度上限。
const (
	proactiveReadyTarget  = 2
	proactiveActiveWindow = 24 * time.Hour
	proactiveMaxPerTick   = 4
)

// proactivePregen 空闲调度器: 为近 24h 活跃用户在云端主动保持 proactiveReadyTarget 张
// ready 预生成图(不依赖客户端先下单), 使客户端下次唤醒可"秒提"而无需现场等待。
// 由队列 worker 空闲分支周期调用; 复用客户端下单同一路径, 因此同样计入用户配额。
func (a *API) proactivePregen() {
	users, err := a.st.ListActiveUsers(time.Now().Add(-proactiveActiveWindow))
	if err != nil {
		a.logf("主动预生成: 活跃用户查询失败: %v", err)
		return
	}
	submitted := 0
	for _, uid := range users {
		if submitted >= proactiveMaxPerTick {
			return
		}
		snap, err := a.st.LoadProfileSnapshot(uid)
		if err != nil || snap == nil || snap.ProfileVersion <= 0 {
			continue // 尚无画像(如刚注册未走完向导), 不代下单
		}
		ready, _ := a.st.CountPregenReady(uid, snap.ProfileVersion)
		pending, _ := a.st.CountPregenPending(uid)
		if proactiveReadyTarget-ready-pending <= 0 {
			continue
		}
		u, err := a.st.UserByID(uid)
		if err != nil || u == nil || u.Disabled {
			continue
		}
		if !a.checkQuota(u, "llm") || !a.checkQuota(u, "gen") {
			continue // 配额不足不代下, 避免挤占用户自用额度
		}
		req := nextReq{
			ProfileVersion: snap.ProfileVersion,
			ScreenW:        snap.ScreenW, ScreenH: snap.ScreenH,
		}
		_ = json.Unmarshal([]byte(snap.Profile), &req.Profile)
		_ = json.Unmarshal([]byte(snap.Disliked), &req.Disliked)
		_ = json.Unmarshal([]byte(snap.Custom), &req.CustomKeywords)
		if a.submitPregenOrder(u, req) != "" {
			submitted++
		}
	}
}
