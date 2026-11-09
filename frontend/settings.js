// 设置页: 服务/换图/回访/更新/设备同步/卸载

// 设备名转义(设备名来自各端上报, 拼进 innerHTML 前统一转义)
function escDev(s) {
  return String(s == null ? "" : s)
    .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

window.initSettings = function () {
  const $ = (id) => document.getElementById(id);

  // 已配对设备列表: 手机显示型号、PC 显示机器名, 配对几台显示几台。
  // 跨境慢网单独异步拉取, 不拖住设置页打开。
  async function loadDevices() {
    const title = $("st-devices-title");
    const el = $("st-devices");
    title.textContent = "已配对设备…";
    el.innerHTML = "";
    try {
      const list = await B.GetPairedDevices();
      title.textContent = "已配对设备 (" + list.length + ")";
      if (!list.length) {
        el.innerHTML = '<div class="muted">还没有配对的设备 — 点上方配对码把手机/其他电脑拉进来</div>';
        return;
      }
      el.innerHTML = list
        .map((d) => {
          const icon = d.type === "android" ? "📱" : "💻";
          return (
            "<div>" + icon + " " + escDev(d.name) +
            (d.current ? ' <span class="muted">(本机)</span>' : "") +
            "</div>"
          );
        })
        .join("");
    } catch (e) {
      title.textContent = "已配对设备";
      el.innerHTML = '<div class="muted">设备列表获取失败: ' + escDev(e) + "</div>";
    }
  }

  // 自动换图开关 → 暂停时长可用性
  $("st-auto").onchange = (e) => {
    $("st-pause").disabled = e.target.checked;
    updatePauseHint(e.target.checked);
  };

  function updatePauseHint(autoOn) {
    if (autoOn) {
      $("st-pause-hint").textContent = "";
      return;
    }
    const st = window.__svcState;
    if (st && st.pauseHint) $("st-pause-hint").textContent = st.pauseHint;
  }

  // ---------- 加载 ----------
  window.loadSettings = async () => {
    try {
      const st = await B.GetServiceState();
      window.__svcState = st;
      $("st-auto").checked = st.autoChange;
      $("st-interval").value = String(st.interval);
      // 暂停选项(策略下发)
      const ps = $("st-pause");
      ps.innerHTML = "";
      (st.pauseOpts || []).forEach((h) => {
        const o = document.createElement("option");
        o.value = h;
        o.textContent = pauseLabel(h);
        if (h === st.pauseHours) o.selected = true;
        ps.appendChild(o);
      });
      if (!st.pauseHours && ps.options.length) {
        // 默认"1 天"
        [...ps.options].forEach((o) => { if (+o.value === 24) o.selected = true; });
      }
      ps.disabled = st.autoChange;
      updatePauseHint(st.autoChange);
      $("st-task").textContent = "计划任务: " + st.taskState;
      $("st-ask").checked = st.askEnabled;
      $("st-askdays").value = st.askDays;
      $("st-version").textContent = "当前版本: " + st.version;
      $("st-lastupdate").textContent = "最后更新: " + st.lastUpdate;
      $("st-user").textContent = "匿名用户 ID: " + st.userId + "   (零注册零登录)";
      setStatus($("st-status"), "");
      setStatus($("st-conn-status"), "");
      setStatus($("st-sync-status"), "");
      loadDevices(); // 设备列表异步拉取(结果自渲染, 失败自提示)
    } catch (e) {
      setStatus($("st-status"), "" + e, "err");
    }
  };

  function pauseLabel(h) {
    if (h < 24) return h + " 小时";
    if (h % 24 === 0) return h / 24 + " 天";
    return h + " 小时";
  }

  function collect() {
    const pauseSel = $("st-pause");
    return {
      interval: +$("st-interval").value,
      autoChange: $("st-auto").checked,
      pauseHours: pauseSel.options.length ? +pauseSel.value : 24,
      askEnabled: $("st-ask").checked,
      askDays: Math.min(365, Math.max(1, +$("st-askdays").value || 3)),
    };
  }

  // ---------- 保存 ----------
  $("st-save").onclick = async () => {
    const btn = $("st-save");
    btn.disabled = true;
    setStatus($("st-status"), "保存中…", "hint");
    try {
      await B.SaveService(collect());
      setStatus($("st-status"), "设置已保存。", "ok");
      await window.loadSettings();
    } catch (e) {
      setStatus($("st-status"), "" + e, "err");
    }
    btn.disabled = false;
  };

  // ---------- 测试连接 ----------
  $("st-test").onclick = async () => {
    const btn = $("st-test");
    btn.disabled = true;
    setStatus($("st-conn-status"), "测试中...", "hint");
    try {
      const r = await B.TestConnection({});
      setStatus($("st-conn-status"), r.message, r.ok ? (r.render ? "ok" : "hint") : "err");
    } catch (e) {
      setStatus($("st-conn-status"), "测试失败: " + e, "err");
    }
    btn.disabled = false;
  };

  // ---------- 修复计划任务 ----------
  $("st-repair").onclick = async () => {
    const btn = $("st-repair");
    btn.disabled = true;
    $("st-task").textContent = "计划任务: 修复中...";
    try {
      const msg = await B.RepairTask();
      setStatus($("st-status"), msg, "ok");
      const st = await B.GetServiceState();
      $("st-task").textContent = "计划任务: " + st.taskState;
    } catch (e) {
      $("st-task").textContent = "计划任务: 修复失败";
      setStatus($("st-status"), "" + e, "err");
      await modal({
        title: "计划任务修复失败",
        body: "" + e + "\n\n多半是被 360安全等杀毒软件拦截。请暂时关闭杀软的拦截防护, 再点击\"修复计划任务\"重试。",
      });
    }
    btn.disabled = false;
  };

  // ---------- 更新 ----------
  $("st-notes").onclick = () =>
    B.OpenURL("https://gitee.com/QQ941748/ai-wallpaper-workshop/releases");

  $("st-checkupd").onclick = async () => {
    const btn = $("st-checkupd");
    if (btn.disabled) return;
    btn.disabled = true;
    btn.textContent = "检查中…";
    try {
      const r = await B.CheckUpdate();
      if (r.status === "latest") {
        await modal({ title: "检查更新", body: r.message });
        btn.textContent = "检查更新";
        btn.disabled = false;
        return;
      }
      if (r.status === "error") {
        setStatus($("st-status"), r.message, "err");
        btn.textContent = "检查更新";
        btn.disabled = false;
        return;
      }
      const ok = await confirmBox("发现新版本 " + r.version,
        r.message + "\n\n是 = 立即下载更新(约 10 MB), 下载完成后关闭窗口自动换装, 下次打开即新版\n否 = 取消");
      if (!ok) {
        btn.textContent = "检查更新";
        btn.disabled = false;
        return;
      }
      btn.textContent = "下载中…";
      $("st-upd-progress").classList.remove("hidden");
      const onProg = (p) => {
        if (p.total > 0) {
          $("st-upd-bar").style.width = Math.round((p.got * 100) / p.total) + "%";
          btn.textContent = "下载中 " + (p.got >> 20) + "/" + (p.total >> 20) + " MB";
        } else {
          btn.textContent = "下载中 " + (p.got >> 20) + " MB";
        }
      };
      // Wails 事件经 runtime.EventsOn 投递(window 事件桥在 Wails 下不触发)
      const offProg = window.runtime.EventsOn("update-progress", onProg);
      const ur = await B.DoUpdate();
      offProg();
      if (ur.status === "error") {
        setStatus($("st-status"), ur.message, "err");
        btn.textContent = "检查更新";
        btn.disabled = false;
        $("st-upd-progress").classList.add("hidden");
        return;
      }
      btn.textContent = "更新完成, 正在关闭…";
      // 有未保存修改先询问(walk 版 confirmClose 语义简化: 直接关, 设置未保存会丢)
      setTimeout(() => B.QuitApp(), 400);
    } catch (e) {
      setStatus($("st-status"), "" + e, "err");
      btn.textContent = "检查更新";
      btn.disabled = false;
    }
  };

  // ---------- 设备同步 ----------
  $("st-qr").onclick = async () => {
    setStatus($("st-sync-status"), "正在生成配对码...", "hint");
    try {
      const r = await B.CreateLink();
      if (!r.ok) {
        setStatus($("st-sync-status"), r.message, "err");
        return;
      }
      setStatus($("st-sync-status"), r.message, "ok");
      const html =
        (r.qr ? '<div class="modal-img"><img src="data:image/png;base64,' + r.qr + '"></div>' : "") +
        '<div class="modal-code">' + r.code + "</div>" +
        '<div class="muted" style="text-align:center">5 分钟内有效 · 扫码或在另一台设备输入此码</div>';
      await modal({ title: "配对码", html, buttons: [{ text: "关闭", primary: true }] });
    } catch (e) {
      setStatus($("st-sync-status"), "" + e, "err");
    }
  };

  $("st-redeem").onclick = async () => {
    const code = await modal({
      title: "输入配对码",
      body: "请输入另一台设备上显示的 6 位配对码:",
      input: true,
      buttons: [{ text: "取消", value: null }, { text: "确定", primary: true }],
    });
    if (!code) return;
    setStatus($("st-sync-status"), "正在配对...", "hint");
    try {
      const r = await B.RedeemLink(code);
      setStatus($("st-sync-status"), r.message, r.ok ? "ok" : "err");
      if (r.ok) await window.loadSettings();
    } catch (e) {
      setStatus($("st-sync-status"), "" + e, "err");
    }
  };

  // ---------- 其他 ----------
  $("st-opendir").onclick = () => B.OpenFolder("");
  $("st-uninstall").onclick = async () => {
    const ok = await modal({
      title: "卸载 AI 壁纸工坊",
      body: "将停止每小时自动换图, 并移除桌面/开始菜单快捷方式与卸载项。\n请选择如何处理你的数据:\n\n" +
        "确定 = 保留我的偏好与壁纸(推荐; 日后重装可直接沿用)\n" +
        "「彻底清理」= 连同偏好、壁纸、日志一并删除\n取消 = 放弃卸载",
      buttons: [
        { text: "取消", value: null },
        { text: "彻底清理", danger: true, onClick: (_, close) => { close(true); B.Uninstall(true); } },
        { text: "确认卸载", primary: true, onClick: (_, close) => { close(true); B.Uninstall(false); } },
      ],
    });
    // Uninstall 内部 os.Exit, 此处不会到达
    void ok;
  };
};
