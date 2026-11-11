// 历史页: 本地/云端双模式 — 交互两模式对齐(负责人 2026-11-11 定稿)
// ①列表行=心形+时间+规格(无提示词, 选中后预览下方显示全文)
// ②点行右侧预览(Go 侧已修 image.Decode 自动识别)
// ③云端视觉区分: ☁ 前缀+状态条+按钮文案「召回到本地并应用」+切换后自动预览首张
// ④全部召回: {jobID}.png 命名去重, 进度条, 服务端 offset 翻页
// ⑤两模式统一交互: 行内心形点按收藏/取消、只看收藏筛选、删除、刷新有反馈
// ⑥本地行收藏星标: {jobID}.png 反查 + 服务端收藏集合 GetFavs, 旧时间戳文件本地清单兜底
// ⑦云端删除: 归档行+OSS 副本一并清理; 模式切换按钮在页头右侧

window.initHistory = function () {
  const $ = (id) => document.getElementById(id);
  let mode = "local";          // local | cloud
  let items = [];              // 当前列表数据(本地模式 = 视图, 见 localAll/favOnly)
  let selected = -1;
  // 云端列表会话缓存(stale-while-revalidate): 切回/刷新时先秒显上次结果再后台拉新。
  // 与 items 指向同一数组 → 收藏/召回的就地修改自动同步进缓存。
  let cloudCache = null;
  let previewSeq = 0;          // 防过期预览串号
  let recalling = false;
  // 本地收藏: 收藏态存服务端 archive 表, {jobID}.png 反查关联(旧时间戳文件无法关联, 不入筛选);
  // favOnly 两模式通用: 本地按 localFavOf, 云端按 it.fav。
  let favSet = new Set();
  let favOnly = false;
  let localAll = null;         // 本地全量(未筛选), 切筛选时的数据源

  // ---------- 列表渲染 ----------
  function renderList() {
    const root = $("hist-list");
    root.innerHTML = "";
    items.forEach((it, i) => {
      const row = document.createElement("div");
      row.className = "hist-row" + (i === selected ? " active" : "");
      if (mode === "cloud") {
        const heart = it.fav ? "♥" : "♡";
        row.innerHTML =
          '<span class="heart' + (it.fav ? " on" : "") + '">' + heart + "</span>" +
          '<span class="meta">☁ ' + it.createdAt + "</span>" +
          '<span class="spec">' + it.width + "×" + it.height + "</span>" +
          (it.hasLocal ? '<span class="spec" title="本地已有">✓</span>' : "");
        const heartEl = row.querySelector(".heart");
        heartEl.onclick = (ev) => { ev.stopPropagation(); toggleCloudFav(it); };
      } else {
        const label = it.jobId
          ? it.at + "  " + it.jobId.slice(0, 8)
          : it.name.replace(/^wallpaper_/, "").replace(/\.png$/, "") + "  (" + it.at + ")";
        const fav = localFavOf(it);
        row.innerHTML =
          '<span class="heart' + (fav ? " on" : "") + '">' + (fav ? "♥" : "♡") + "</span>" +
          '<span class="meta">' + label + "</span>" +
          (it.jobId ? '<span class="spec">云端可取</span>' : "");
        const heart = row.querySelector(".heart");
        heart.onclick = (ev) => { ev.stopPropagation(); toggleLocalFav(it); };
      }
      row.onclick = () => select(i);
      root.appendChild(row);
    });
    if (items.length === 0) {
      const emptyMsg = favOnly
        ? (mode === "cloud" ? "云端还没有收藏的图片 — 点行内 ♡ 收藏一张"
                            : "还没有收藏的图片 — 点行内 ♡ 收藏一张")
        : "暂无记录";
      root.innerHTML = '<div class="muted" style="padding:20px;text-align:center">' + emptyMsg + "</div>";
    }
  }

  // 本地图收藏判定: jobId 行看云端收藏集合(跨端同步), 旧时间戳行看本地清单(它.fav)。
  const localFavOf = (x) => !!(x.fav || (x.jobId && favSet.has(x.jobId)));

  // 本地行星标(全行可标, 与安卓画廊一致): jobId→云端, 旧时间戳文件→本地清单; 乐观翻心, 失败回滚。
  async function toggleLocalFav(it) {
    const next = !localFavOf(it);
    const prevLocal = it.fav;
    const prevCloud = it.jobId ? favSet.has(it.jobId) : false;
    if (it.jobId) {
      if (next) favSet.add(it.jobId); else favSet.delete(it.jobId);
    } else {
      it.fav = next;
    }
    renderList();
    try {
      if (it.jobId) await B.ToggleFav(it.jobId, next);
      else await B.SetLocalFav(it.name, next);
      setStatus($("hist-status"), next ? "已收藏 ♥" : "已取消收藏", "ok");
      if (favOnly) rebuildLocalView(next ? it : null); // 筛选中: 收藏进入/取消移出即时生效
    } catch (e) {
      if (it.jobId) {
        if (prevCloud) favSet.add(it.jobId); else favSet.delete(it.jobId);
      } else {
        it.fav = prevLocal;
      }
      renderList();
      setStatus($("hist-status"), "" + e, "err");
    }
  }

  // 本地视图重建(favOnly 筛选); keep = 尽量保留当前选中项。
  function rebuildLocalView(keep) {
    const all = localAll || [];
    items = favOnly ? all.filter(localFavOf) : all;
    selected = keep ? items.indexOf(keep) : -1;
    renderList();
  }

  // 云端行内心形(与本地一致): 乐观翻心 → 落云端 fav → 置顶重排(两数组同步) → 筛选中即时移出。
  // 选中项不被心形劫持: 翻心后仍锚定你正在看的那张(与本地行为一致)。
  async function toggleCloudFav(it) {
    const next = !it.fav;
    it.fav = next;
    renderList();
    try {
      const fav = await B.ToggleFav(it.jobId, next);
      it.fav = fav;
      setStatus($("hist-status"), fav ? "已收藏 ♥" : "已取消收藏", "ok");
      // 不做收藏置顶重排(负责人 2026-11-11: 有"只看收藏"即可, 列表恒为最新在前)
      const selItem = selected >= 0 ? items[selected] : null;
      if (favOnly && !it.fav) rebuildCloudView(selItem); // 筛选中取消 → 移出列表
      else {
        selected = selItem ? items.indexOf(selItem) : -1;
        renderList();
      }
    } catch (e) {
      it.fav = !next; // 回滚
      renderList();
      setStatus($("hist-status"), "" + e, "err");
    }
  }

  // 云端视图重建(favOnly 筛选); keep = 尽量保留当前选中项。
  function rebuildCloudView(keep) {
    const all = cloudCache || [];
    items = favOnly ? all.filter((x) => x.fav) : all;
    selected = keep ? items.indexOf(keep) : -1;
    renderList();
  }

  // ---------- 选中 + 预览 ----------
  async function select(i) {
    selected = i;
    renderList();
    const seq = ++previewSeq;
    const it = items[i];
    $("hist-empty").classList.remove("hidden");
    $("hist-empty").textContent = "加载预览…";
    $("hist-img").classList.add("hidden");
    setStatus($("hist-status"), "");
    try {
      let b64;
      if (mode === "cloud") {
        b64 = await B.PreviewCloud(it.jobId);
      } else {
        b64 = await B.PreviewLocal(it.path);
      }
      if (seq !== previewSeq) return; // 已换选中, 过期丢弃
      $("hist-img").src = "data:image/jpeg;base64," + b64;
      $("hist-img").classList.remove("hidden");
      $("hist-empty").classList.add("hidden");
    } catch (e) {
      if (seq !== previewSeq) return;
      $("hist-empty").textContent = "预览加载失败";
      setStatus($("hist-status"), "" + e, "err");
    }
  }

  // ---------- 数据加载 ----------
  async function loadLocal() {
    localAll = await B.GetLocalHistory();
    items = localAll;
    selected = -1;
    renderList();
    $("hist-empty").textContent = "选择左侧图片预览";
    // 星标显示需要收藏集合(先显示列表, 收藏态返回后纠偏; 失败保留已有缓存)
    try {
      const ids = await B.GetFavs();
      favSet = new Set(ids || []);
      if (mode === "local") {
        const keep = selected >= 0 && items[selected] ? items[selected] : null;
        rebuildLocalView(keep);
      }
    } catch (e) {
      if ((localAll || []).some((x) => x.jobId)) {
        setStatus($("hist-status"), "收藏状态未同步: " + e, "hint");
      }
    }
  }

  async function loadCloud(autoPreview) {
    const hadStale = cloudCache != null;
    // 记住当前选中(按 jobId, 刷新重排后仍能对上)
    const keepId = selected >= 0 && items[selected] ? items[selected].jobId : null;

    if (hadStale) {
      // stale: 缓存秒显, 不打断用户, 后台静默拉新
      items = favOnly ? cloudCache.filter((x) => x.fav) : cloudCache;
      selected = keepId ? items.findIndex((x) => x.jobId === keepId) : -1;
      renderList();
      setStatus($("hist-status"), "");
      if (selected < 0 && autoPreview && items.length > 0) select(0);
      else if (items.length === 0) $("hist-empty").textContent = favOnly ? "云端还没有收藏的图片" : "暂无记录";
    } else {
      // 首次进云端: 先清空本地行(防"本地行混在云端模式里"的错行窗口), 再拉取
      items = [];
      selected = -1;
      renderList();
      setStatus($("hist-status"), "加载中…", "hint");
    }

    const r = await B.GetCloudHistory(0);
    if (!r.ok) {
      if (hadStale) {
        // 有缓存: 保留 stale 显示, 只提示刷新失败
        setStatus($("hist-status"), "刷新失败, 仍显示上次结果: " + (r.message || "网络错误"), "err");
        return true;
      }
      // 无缓存失败: 清空列表, 不能让本地行留在云端模式下造成"错行"混淆
      items = [];
      selected = -1;
      renderList();
      $("hist-empty").textContent = "云端历史加载失败";
      $("hist-img").classList.add("hidden");
      $("hist-empty").classList.remove("hidden");
      setStatus($("hist-status"), r.message || "云端历史加载失败", "err");
      return false;
    }

    // revalidate: 新数据原位替换, 保持选中(按 jobId 对上); cloudCache 恒存全量
    cloudCache = r.items;
    items = favOnly ? r.items.filter((x) => x.fav) : r.items;
    selected = keepId ? items.findIndex((x) => x.jobId === keepId) : -1;
    renderList();
    if (!hadStale) {
      // 首次加载: ③ 自动预览首张
      if (autoPreview && items.length > 0) select(0);
      else $("hist-empty").textContent = "选择左侧图片预览";
    } else if (selected < 0 && autoPreview && items.length > 0) {
      select(0); // 原选中已被远端删除等 → 兜底预览首张
    } else if (selected >= 0) {
      setStatus($("hist-status"), ""); // 清掉可能残留的刷新失败提示
    }
    return true;
  }

  function setMode(m) {
    mode = m;
    const cloud = m === "cloud";
    $("hist-toggle").textContent = cloud ? "本地历史" : "云端历史";
    $("hist-modebar").classList.toggle("hidden", !cloud);
    // ③ 按钮文案切换
    $("hist-apply").textContent = cloud ? "召回到本地并应用" : "重新应用这张";
    // 交互两模式对齐: 只看收藏/删除 两模式常显(行为按 mode 分支); 全部召回仅云端
    $("hist-favonly").style.display = "";
    $("hist-del").style.display = "";
    $("hist-recallall").style.display = cloud ? "" : "none";
    // 切模式重置筛选, 避免"列表缺图"式困惑
    favOnly = false;
    $("hist-favonly").textContent = "♡ 只看收藏";
    setStatus($("hist-status"), "");
  }

  // ---------- 按钮 ----------
  $("hist-toggle").onclick = async () => {
    if (mode === "cloud") {
      setMode("local");
      await loadLocal();
    } else {
      setMode("cloud");
      await loadCloud(true); // 缓存秒显+后台刷新; 状态文案由 loadCloud 自管
    }
  };

  $("hist-refresh").onclick = async () => {
    setStatus($("hist-status"), "刷新中…", "hint");
    if (mode === "cloud") {
      // loadCloud 自管错误状态: 失败时它已设 err, 这里不能无条件清空(修复"刷新无效"观感)
      const ok = await loadCloud(false);
      if (ok) setStatus($("hist-status"), "已刷新 ✓ " + items.length + " 张", "ok");
    } else {
      await loadLocal();
      setStatus($("hist-status"), "已刷新 ✓ " + items.length + " 张", "ok");
    }
  };

  $("hist-apply").onclick = async () => {
    if (selected < 0 || !items[selected]) {
      setStatus($("hist-status"), "请先选择一张图片。", "hint");
      return;
    }
    const it = items[selected];
    const btn = $("hist-apply");
    btn.disabled = true;
    setStatus($("hist-status"), mode === "cloud" ? "召回中…" : "应用中…", "hint");
    try {
      let msg;
      if (mode === "cloud") {
        const r = await B.RecallCloud(it.jobId);
        msg = r.message;
        if (r.ok) {
          it.hasLocal = true;
          renderList();
          await loadLocalOnce();
        }
      } else {
        msg = await B.ApplyLocal(it.path);
      }
      setStatus($("hist-status"), msg, "ok");
    } catch (e) {
      setStatus($("hist-status"), "" + e, "err");
    }
    btn.disabled = false;
  };

  async function loadLocalOnce() { /* 召回后仅在切回本地时刷新, 这里保持云端列表不动 */ }

  // ④ 全部召回
  $("hist-recallall").onclick = async () => {
    if (recalling) return;
    const ok = await confirmBox("全部召回",
      "将把云端全部本地图拉回本地(已有 {jobID}.png 的跳过)。\n" +
      "只下载留档, 不更换当前壁纸。\n\n继续吗?");
    if (!ok) return;
    recalling = true;
    const btn = $("hist-recallall");
    btn.disabled = true;
    $("hist-progress").classList.remove("hidden");
    setStatus($("hist-status"), "召回中…", "hint");
    // 进度事件(Wails 事件经 runtime.EventsOn 投递, 详见 settings.js)
    const onProg = (p) => {
      const pct = p.total > 0 ? Math.round(((p.done + p.skipped) * 100) / p.total) : 0;
      $("hist-progress-bar").style.width = Math.min(pct, 95) + "%";
      setStatus($("hist-status"),
        "召回中… 已下载 " + p.done + " · 跳过 " + p.skipped + " · 失败 " + p.failed, "hint");
    };
    const offProg = window.runtime.EventsOn("recall-progress", onProg);
    try {
      const r = await B.RecallAll();
      $("hist-progress-bar").style.width = "100%";
      setStatus($("hist-status"), r.message, r.ok ? "ok" : "hint");
      if (mode === "cloud") await loadCloud(false);
    } catch (e) {
      setStatus($("hist-status"), "" + e, "err");
    } finally {
      offProg();
      setTimeout(() => {
        $("hist-progress").classList.add("hidden");
        $("hist-progress-bar").style.width = "0%";
      }, 800);
      btn.disabled = false;
      recalling = false;
    }
  };

  // ⑦ 删除: 本地=删文件( jobId 行连带清云端副本); 云端=删归档行+OSS 副本(与本地交互一致)
  $("hist-del").onclick = async () => {
    if (selected < 0 || !items[selected]) {
      setStatus($("hist-status"), "请先选择一张图片。", "hint");
      return;
    }
    const it = items[selected];
    if (mode === "cloud") {
      const ok = await confirmBox("删除这张图片？",
        "将从云端历史移除并清理云端副本(OSS), 删除后无法恢复。本地图库中的副本不受影响。");
      if (!ok) return;
      try {
        const msg = await B.DeleteCloud(it.jobId);
        setStatus($("hist-status"), msg + (it.hasLocal ? " (本地图库仍保留这张)" : ""), "ok");
        cloudCache = (cloudCache || []).filter((x) => x.jobId !== it.jobId);
        items = items.filter((x) => x.jobId !== it.jobId);
        selected = items.length ? Math.min(selected, items.length - 1) : -1;
        renderList();
        if (items.length === 0) {
          $("hist-empty").textContent = "暂无记录";
          $("hist-img").classList.add("hidden");
          $("hist-empty").classList.remove("hidden");
        }
      } catch (e) {
        setStatus($("hist-status"), "" + e, "err");
      }
      return;
    }
    const ok = await confirmBox(
      "删除这张图片？",
      it.jobId
        ? "将从「最近生成」中移除并清理云端副本, 删除后无法恢复。已设置的壁纸不受影响。"
        : "将从「最近生成」中移除, 删除后无法恢复。已设置的壁纸不受影响。"
    );
    if (!ok) return;
    try {
      const msg = await B.DeleteLocal(it.name);
      setStatus($("hist-status"), msg, "ok");
      localAll = (localAll || []).filter((x) => x.name !== it.name);
      if (cloudCache && it.jobId) cloudCache = cloudCache.filter((x) => x.jobId !== it.jobId);
      rebuildLocalView();
      if (items.length === 0) $("hist-empty").textContent = "暂无记录";
    } catch (e) {
      setStatus($("hist-status"), "" + e, "err");
    }
  };

  // 只看收藏(两模式通用, 与安卓画廊"收藏中"筛选一致)
  $("hist-favonly").onclick = () => {
    favOnly = !favOnly;
    $("hist-favonly").textContent = favOnly ? "♥ 只看收藏中" : "♡ 只看收藏";
    if (mode === "local") rebuildLocalView();
    else rebuildCloudView(null);
    setStatus($("hist-status"), favOnly ? "只显示已收藏 ♥ 的图片" : "", favOnly ? "hint" : "");
    if (favOnly && items.length > 0) select(0);
  };

  $("hist-open").onclick = () => B.OpenFolder(window.__boot ? window.__boot.dir + "/wallpapers" : "");

  // ---------- 入口 ----------
  window.loadHistory = async function () {
    if (mode === "cloud") await loadCloud(false);
    else await loadLocal();
  };
  setMode("local");
  window.loadHistory();
};
