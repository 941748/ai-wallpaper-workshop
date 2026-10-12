# -*- coding: utf-8 -*-
"""AI 壁纸工坊 服务端一键部署(paramiko 单连接, 内置限速友好重试)。

用法(仓库根目录执行):
    python tools/server-deploy.py --ver 1.0.5 --sha <64位hex>            # 全量: 发布文件 + 云服务
    python tools/server-deploy.py --ver 1.0.5 --sha <64hex> --files-only # 仅发布文件(exe/manifest/官网)
    python tools/server-deploy.py --ver 1.0.5 --sha <64hex> --cloud-only # 仅云服务二进制换装+重启

前置产物(本地):
    dist/AIWallpaper.exe            客户端(发布同款, 本地先 sha256sum 确认)
    dist/manifest-<ver>.json        覆盖下载源的 manifest(含 version/file/sha256/size/notes/url)
    site/index.html                 官网页(版本徽标/下载按钮已升)
    dist/aiwallpaper-cloud          Linux 云服务(--cloud-only/--cloud 需要; GOOS=linux 构建)

服务端(已实测, 2026-10):
    SSH     root@47.237.98.171:22, 密钥 %USERPROFILE%\\.ssh\\id_ed25519_aiwallpaper
    AW_DATA /var/lib/aiwallpaper        (releases/ 子目录放 exe+manifest.json)
    AW_SITE /opt/aiwallpaper/site       (官网 index.html)
    AW_DL   /opt/aiwallpaper/downloads  (主页 /download/ 直连备用)
    云服务  /opt/aiwallpaper/aiwallpaper-cloud  (systemd: aiwallpaper-cloud)

关键坑(踩过, 勿改回):
    * Windows OpenSSH 客户端连这台机会间歇 EOFError/KEX 超时(疑似对端限速), 必须用
      python+paramiko 且脚本内 60s 间隔重试(实测第 2~5 次成功)。frp 不用于 SSH。
    * 单连接完成全部操作, 禁止拆成多次 ssh 反复连(会加剧限速)。
    * discovery 输出绝不整行打印 Environment(会泄露 AW_LLM_KEY/AW_ADMIN_TOKEN)。
    * manifest/官网/health 均按请求读文件, 上传后无需重启; 仅换云服务二进制需 restart。
"""
import argparse
import os
import sys
import time

import paramiko

HOST = "47.237.98.171"
PORT = 22
USER = "root"
KEY = os.path.expandvars(r"%USERPROFILE%\.ssh\id_ed25519_aiwallpaper")
REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BIN_REMOTE = "/opt/aiwallpaper/aiwallpaper-cloud"


def log(msg):
    print(msg, flush=True)


def connect():
    key = paramiko.Ed25519Key.from_private_key_file(KEY)
    c = paramiko.SSHClient()
    c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    c.connect(HOST, port=PORT, username=USER, pkey=key,
              banner_timeout=90, auth_timeout=90, timeout=30)
    return c


def connect_with_retry(max_try=6, backoff=60):
    for i in range(1, max_try + 1):
        try:
            c = connect()
            log("SSH connected (attempt %d/%d)" % (i, max_try))
            return c
        except Exception as e:
            log("connect attempt %d failed: %r" % (i, e))
            if i < max_try:
                time.sleep(backoff)
    sys.exit("FATAL: cannot establish SSH after %d attempts" % max_try)


def run(c, cmd, timeout=180):
    si, so, se = c.exec_command(cmd, timeout=timeout)
    out = so.read().decode("utf-8", "replace")
    err = se.read().decode("utf-8", "replace")
    rc = so.channel.recv_exit_status()
    return rc, out, err


def discover_paths(c):
    """从 systemd unit 解析 AW_DATA/AW_SITE/AW_DL(绝对路径优先, 否则用代码默认)。
    只打印解析后的路径, 不打印原始 Environment 行。"""
    rc, out, err = run(c,
        "systemctl show aiwallpaper-cloud -p Environment -p WorkingDirectory --value")
    if rc != 0:
        sys.exit("FATAL: cannot read unit env: " + err)
    env, wd = {}, ""
    for line in out.splitlines():
        if line.startswith("WorkingDirectory="):
            wd = line.split("=", 1)[1].strip()
        for pair in line.replace("Environment=", "").split():
            if pair.startswith("AW_") and "=" in pair:
                k, v = pair.split("=", 1)
                env[k] = v

    def resolve(name, default):
        v = env.get(name, default)
        if v.startswith("/"):
            return v
        return (wd or "/opt/aiwallpaper").rstrip("/") + "/" + v

    paths = {
        "aw_data": resolve("AW_DATA", "./clouddata"),
        "aw_site": resolve("AW_SITE", "/opt/aiwallpaper/site"),
        "aw_dl": resolve("AW_DL", "/opt/aiwallpaper/downloads"),
    }
    log("AW_DATA=%s" % paths["aw_data"])
    log("AW_SITE=%s" % paths["aw_site"])
    log("AW_DL=%s" % paths["aw_dl"])
    return paths


def health(c):
    rc, out, err = run(c, "curl -sS --max-time 10 http://127.0.0.1:8707/api/v1/health")
    log("health: " + out.strip())
    return out


def deploy_files(c, ver):
    p = discover_paths(c)
    exe = os.path.join(REPO, "dist", "AIWallpaper.exe")
    manifest = os.path.join(REPO, "dist", "manifest-%s.json" % ver)
    site = os.path.join(REPO, "site", "index.html")
    for f in (exe, manifest, site):
        if not os.path.isfile(f):
            sys.exit("FATAL: missing local file " + f)

    rc, out, err = run(c, "mkdir -p %s/releases %s && echo DIRS_OK"
                        % (p["aw_data"], p["aw_dl"]))
    if rc != 0:
        sys.exit("FATAL: mkdir failed: " + err)

    sftp = c.open_sftp()
    uploads = [
        (exe, "%s/releases/AIWallpaper-%s.exe" % (p["aw_data"], ver)),
        (manifest, "%s/releases/manifest.json" % p["aw_data"]),
        (exe, "%s/AIWallpaper.exe" % p["aw_dl"]),
        (site, "%s/index.html" % p["aw_site"]),
    ]
    for src, dst in uploads:
        log("put -> %s" % dst)
        sftp.put(src, dst)
        log("  remote size=%d" % sftp.stat(dst).st_size)
    sftp.close()

    rc, out, err = run(c, "sha256sum %s/releases/AIWallpaper-%s.exe" % (p["aw_data"], ver))
    log("remote exe sha256: " + out.strip())
    rc2, out2, _ = run(c, "cat %s/releases/manifest.json" % p["aw_data"])
    log("manifest on server:\n" + out2)
    health(c)
    return p


def deploy_cloud(c, ver):
    local_bin = os.path.join(REPO, "dist", "aiwallpaper-cloud")
    if not os.path.isfile(local_bin):
        sys.exit("FATAL: missing %s (build: $env:GOOS='linux'; $env:GOARCH='amd64'; "
                 "go build -trimpath -ldflags \"-s -w\" -o dist\\aiwallpaper-cloud ./cloud)" % local_bin)
    bak = BIN_REMOTE + ".bak-v" + ver

    rc, out, err = run(c, "if [ ! -f %s ]; then cp %s %s && echo BACKUP_OK; "
                        "else echo BACKUP_EXISTS; fi" % (bak, BIN_REMOTE, bak))
    if rc != 0:
        sys.exit("FATAL: backup failed: " + err)
    log(out.strip())

    tmp = BIN_REMOTE + ".new"
    sftp = c.open_sftp()
    log("put linux cloud -> %s" % tmp)
    sftp.put(local_bin, tmp)
    sftp.chmod(tmp, 0o755)
    sftp.close()
    rc, out, err = run(c, "mv %s %s && echo SWAPPED" % (tmp, BIN_REMOTE))
    if rc != 0:
        sys.exit("FATAL: swap failed: " + err)
    log(out.strip())

    rc, out, err = run(c, "systemctl restart aiwallpaper-cloud; sleep 3; "
                        "systemctl is-active aiwallpaper-cloud")
    active = out.strip().splitlines()[-1] if out.strip() else ""
    log("service after restart: " + active)
    if active != "active":
        run(c, "cp -f %s %s; systemctl restart aiwallpaper-cloud; sleep 2; "
            "systemctl is-active aiwallpaper-cloud" % (bak, BIN_REMOTE))
        log("ROLLBACK attempted; recent logs:")
        log(run(c, "journalctl -u aiwallpaper-cloud -n 30 --no-pager")[1])
        sys.exit("FATAL: service inactive after restart, rolled back from " + bak)

    out = health(c)
    if '"server_version":"%s"' % ver not in out.replace(" ", ""):
        log("WARN: health server_version != %s (internal/version 未升?)" % ver)
    rc, out, err = run(c,
        "python3 -c \"import sqlite3;print('schema_version=',"
        "sqlite3.connect('/var/lib/aiwallpaper/cloud.db')"
        ".execute('select max(version) from schema_version').fetchone()[0])\"")
    if rc == 0:
        log(out.strip())
    else:
        log("sqlite check skipped: " + err.strip())
    log(run(c, "journalctl -u aiwallpaper-cloud -n 8 --no-pager --since '-2min'")[1])


def main():
    ap = argparse.ArgumentParser(description="AI Wallpaper server deploy")
    ap.add_argument("--ver", required=True, help="release version, e.g. 1.0.5")
    ap.add_argument("--sha", default="", help="expected sha256 of client exe (optional check)")
    ap.add_argument("--files-only", action="store_true")
    ap.add_argument("--cloud-only", action="store_true")
    args = ap.parse_args()

    c = connect_with_retry()
    try:
        if not args.cloud_only:
            p = deploy_files(c, args.ver)
            if args.sha:
                rc, out, err = run(c, "sha256sum %s/releases/AIWallpaper-%s.exe"
                                   % (p["aw_data"], args.ver))
                if args.sha not in out:
                    sys.exit("FATAL: sha256 mismatch, expect " + args.sha)
                log("sha256 verified OK")
        if not args.files_only:
            deploy_cloud(c, args.ver)
        log("DEPLOY DONE (ver=%s)" % args.ver)
    finally:
        c.close()


if __name__ == "__main__":
    main()
