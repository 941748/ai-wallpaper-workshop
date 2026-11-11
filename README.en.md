# AI Wallpaper Workshop

[中文](README.md) | [English](README.en.md)

> Every hour, your desktop makes itself beautiful. A Windows tool that generates images with AI — for you alone — and changes your wallpaper automatically.

<p align="center">
  <img src="site/assets/demo-roadtrip.jpg" width="49%" />
  <img src="site/assets/demo-xianxia.jpg" width="49%" />
</p>
<p align="center"><sub>All images above are real outputs of this project: prompts are planned by a cloud LLM from your preference profile and history</sub></p>

**AI Wallpaper Workshop** is not a wallpaper gallery — it is a wallpaper generator that works for you alone:
a Windows scheduled task fires silently every hour → a cloud LLM plans the prompt from your preference profile and recent history →
ComfyUI (Z-Image Turbo) renders the image → it is fitted to your screen's physical resolution → the desktop wallpaper is replaced. **You do nothing.**

No sign-up, no login, no tray icon, no resident process — close the window and there is no process left in the system.

**Current version: v1.2.5** · Website [cybermesh.space](https://www.cybermesh.space) · Client is a ~14 MB single file.

## Download

| Channel | URL | Notes |
| --- | --- | --- |
| Website | https://www.cybermesh.space | Click "Download the Windows client" |
| Gitee Release (recommended in China) | https://gitee.com/QQ941748/ai-wallpaper-workshop/releases | Download the latest `AIWallpaper.exe` |
| GitHub Release (mirror) | https://github.com/941748/ai-wallpaper-workshop/releases | Same file, faster overseas |

- Requirements: Windows 10 / 11 (64-bit) + WebView2 runtime (bundled with Win11; if missing, the app prompts and installs it automatically).
- After downloading, double-click `AIWallpaper.exe` to open the settings panel directly; first-run registration and the default style pack are completed silently in the background.
- **Existing users never upgrade manually**: the client checks for updates silently on each cycle, then downloads, verifies and swaps itself in.
- **Android**: click "Download for Android" on the website, or grab `AIWallpaper-Android-*.apk` from Gitee Release; in-app update check included.

## Features

- **Effortless preference learning**: a five-dimension questionnaire (style / subject / palette / mood / composition, with one-click style packs) builds your profile;
  afterwards it detects preference drift from your natural actions (resetting preferences, re-applying a past wallpaper, satisfaction check-ins) and quietly refines the profile.
- **Hourly automatic refresh**: the scheduled task launches a process that lives for seconds — it renders, swaps, and exits, with nothing running in the background.
- **Cloud LLM planning + silent fallback**: the LLM plans the next image from your profile, the last 10 rounds, and drift events;
  if it is unreachable or times out, it falls back to the local weighted-sampling engine transparently.
- **Festival & solar-term context**: near the 24 solar terms and major festivals, the mood of the image subtly echoes the occasion (e.g. carnation-toned soft light for Mother's Day) — atmosphere only, no text banners; can be turned off in one click.
- **Local backup pool, works offline**: the machine always keeps up to 3 pre-generated wallpapers; once consumed, the pool is **refilled to cover the whole deficit at once** (up to 3 per round); swaps stay instant even offline or when the render queue is busy.
- **Pre-generation pool**: when the render machine is idle it pre-generates the next round, so a hit changes the wallpaper in ~10 seconds (saving the ~30 seconds of on-demand rendering).
- **Never intrusive**: quiet hours, full-screen apps, and the lock screen are all skipped; satisfaction check-ins follow a memory curve (3→7→15→30→60→90 days, capped) — fewer prompts, but they never disappear.
- **Privacy-friendly**: anonymous token, zero sign-up; profile and history stay entirely on your machine; all outbound calls go through the cloud service you self-host.
- **Favorites & delete sync to the cloud**: every local image can be hearted or deleted (timestamp-named archives included), cloud copies are cleaned up alongside, and phone and PC see the same favorites.
- **All paired devices at a glance**: the Settings page lists every paired device — phones show their model, PCs show their machine name.
- **Staged swap progress**: "Swap Now" renders on demand with four playful progress stages — planning → cloud rendering (with seconds) → download → apply.
- **9 preset style packs, updated live from the cloud**: a pack is just a skeleton (a few tags per dimension) — fine-tune the rest in the questionnaire; the dictionary is served from the cloud, so updates take effect instantly with no client upgrade.
- **Lightweight**: the client is a ~14 MB single file (compiled Go, no runtime dependencies).

## Screenshots

Settings panel: current wallpaper (feedback / swap now) → preferences (five-dimension tri-state chips + one-click style packs) → history (local/cloud favorites & recall) → settings (interval / update / device sync).

<p align="center">
  <img src="site/assets/demo-aurora.jpg" width="66%" />
</p>

## How it works

```
┌──────── Clients (Windows single file ~14 MB / Android APK) ────────┐
│ Settings panel · History favorites      Scheduled tick · resume · │
│ Local prompt engine (LLM fallback)      silent self-update        │
│                             Profile / drift signals               │
└──────────────────────────┬────────────────────────────────────────┘
                 HTTPS (anonymous token, outbound only)
┌──────────────────────────▼────────────────────────────────────────┐
│  Cloud service (Go + SQLite single binary, self-hosted)            │
│  Anonymous signup / quota / rate limit   LLM writes positive      │
│  Render task queue (normal / idle)       prompts; rules compose   │
│  Pre-gen pool · history/favorites        negatives                │
│             silent update distribution                            │
└──────────────────────────┬────────────────────────────────────────┘
          Claim & report (render machine aw-agent dials out, outbound only)
┌──────────────────────────▼────────────────────────────────────────┐
│  Render machine aw-agent (single process, replaces frpc tunnel)   │
│  claim → inject local workflow template → ComfyUI (Z-Image Turbo) │
│  → JPEG transcode → upload   ComfyUI watchdog auto-restart ·      │
│  crash self-heal · versioning & auto-update                       │
└──────────────────────────┬────────────────────────────────────────┘
                     PUT direct (domestic link)
┌──────────────────────────▼────────────────────────────────────────┐
│  Alibaba Cloud OSS (private bucket, u/{user}/{W}x{H}/{job}.jpg)   │
│  clients fetch via one-time signed URL 302; the cloud only signs  │
│  URLs and never touches image bytes                               │
└───────────────────────────────────────────────────────────────────┘
```

### How preferences are "learned"

1. **Tri-state questionnaire**: 90 tags across five dimensions (style 22 / subject 42 / palette 6 / mood 10 / composition 10),
   picked as "like / neutral / dislike" and mapped to profile weights (0.9 / 0.3 / 0.05); style packs can be checked in one click.
2. **Drift detection**: only real user actions are adopted (resetting preferences, re-applying a wallpaper, adjusting weights, check-in choices);
   the profile is refined once same-direction evidence accumulates. It never relies on guessed signals like "how long a wallpaper survived", avoiding intrusion and misjudgement.

## Quick start

The rendering pipeline has a single shape: **client → cloud → render machine (aw-agent)** — no other mode.

### Cloud deployment

1. A server (may be the same machine): compile and run the cloud service (see "Cloud service deployment"), configure the LLM key and OSS credentials.
2. A render machine: ComfyUI + a consumer NVIDIA GPU (Z-Image Turbo, ~30 s/image at 1080p), plus `aw-agent.exe` (see "ComfyUI checklist") — it claims tasks, renders and uploads; no inbound ports needed.
3. The cloud service address is preconfigured — just double-click and run.

## Build

Requirements: Go 1.22+ (windows/amd64). Dependencies are only `lxn/walk`, `golang.org/x/sys`, `golang.org/x/image`,
`golang.org/x/text`, `skip2/go-qrcode`, `modernc.org/sqlite` (pure Go, no CGO).

```powershell
# One-shot build: run tests → dist\AIWallpaper.exe (GUI, no console) + dist\aiwallpaper-cloud.exe
powershell -ExecutionPolicy Bypass -File build.ps1

# When deploying the cloud service to a Linux server, build the Linux binary
$env:AW_BUILD_OS = "linux"; powershell -ExecutionPolicy Bypass -File build.ps1

# Render-machine unified agent (claim & render + OSS upload + watchdog + auto-update)
go build -trimpath -ldflags "-s -w" -o dist\aw-agent.exe ./aw-agent
```

Day-to-day development:

```powershell
go test ./...     # all unit/integration tests (incl. cross-module end-to-end: real tick ↔ real cloud ↔ simulated render agent / LLM)
go vet ./...
go build -trimpath -ldflags "-s -w -H=windowsgui" -o dist\AIWallpaper.exe .
```

> Naming note: the build output and the distributed name are unified as `AIWallpaper.exe`
> (the name users download from the website / Release, and the file that lives in `%LOCALAPPDATA%\AIWallpaper\bin\` after install).
> An older install named `wallpaper.exe` is migrated automatically on the next scheduled-task run.

## Project layout

```
wallpaper/
├── main.go                       # no args = GUI (settings panel); --tick = one silent run; --apply-update = swap helper
├── build.ps1                     # one-shot build (tests → client → cloud service)
├── assets/workflow_template.json # Z-Image Turbo workflow template (placeholder injection; aw-agent default.json shares it)
├── aw-agent/                    # render-machine unified agent (claim & render + OSS upload + watchdog + auto-update)
├── internal/
│   ├── config/                   # config & profile read/write (%LOCALAPPDATA%\AIWallpaper)
│   ├── taxonomy/                 # 5-dimension attribute dictionary (style/subject/palette/mood/composition)
│   ├── prompt/                   # local prompt engine (weighted sampling/exploration/dedup/negative)
│   ├── backend/                  # ImageBackend interface (cloud impl / runninghub reserved)
│   ├── desktop/                  # resolution probe/scale-crop/wallpaper swap/quiet detection/low priority
│   ├── scheduler/                # scheduled-task XML generation + schtasks register/delete/run-now
│   ├── store/                    # data dir/file lock/log rotation/wallpaper archive cleanup
│   ├── signals/                  # drift-event collection (reconfig/re-pick/weight change/check-in)
│   ├── cloud/                    # cloud client (signup/get prompt/report/retry queue)
│   ├── update/                   # silent self-update (version check/download verify/helper swap)
│   ├── tick/                     # --tick full orchestration (incl. satisfaction check-in & self-update)
│   └── ui/                       # Wails Bridge bindings + check-in/uninstall dialogs
├── cloud/                        # standalone cloud service (compiled & deployed separately)
│   ├── main.go                   # wiring + admin dashboard /admin?token=
│   ├── api.go                    # all /api/v1 endpoints
│   ├── handlers_agent.go         # render-agent protocol (claim/rendered/version)
│   ├── queue.go                  # coordinator (proactive pre-gen hook + agent stale reclaim)
│   ├── prompts.go                # LLM planning + fault-tolerant parsing + local-engine fallback
│   ├── workflows.go              # render-workflow registry (whitelist)
│   ├── llm/client.go             # OpenAI-compatible LLM client (default Xiaomi MiMo)
│   └── store/                    # SQLite: users/prompt_records/signals/jobs/pregen/archive/...
├── site/                         # website static pages (promotion & client download)
└── docs/screenshots/             # README screenshots
```

## Using the client

| How it runs | Behaviour |
| --- | --- |
| Double-click (no args) | Opens the settings panel directly (first-run registration completes silently in the background). **Closing the window = the process exits immediately** |
| `AIWallpaper.exe --tick` | Fired hourly by the scheduled task: renders and swaps the wallpaper silently with no window, then exits |
| `AIWallpaper.exe --apply-update ...` | Internal swap helper (used by silent self-update; ordinary users need not care) |

Settings panel tabs: Current Wallpaper (feedback / Swap Now) | Preferences (weights / reset) | History (local & cloud favorites, delete, favorites-only) | Settings (interval / task repair / updates / device sync) |
History (thumbnails / re-apply / open local gallery) | Sync (anonymous user_id / QR pairing for a new device) | Update & Check-in (version / check for updates / check-in cycle).

All the manual actions above (resetting preferences, re-applying from History, adjusting weights) are recorded locally as drift events and reported to the cloud,
serving as the basis for the LLM to plan the next prompt.

Data directory `%LOCALAPPDATA%\AIWallpaper\`:

```
config.json          main config (cloud address / phase / quiet hours / satisfaction cycle)
profile.json         preference profile (5-dimension weights + negative fragments)
prompt_history.json  last 200 render combinations (last 30 deduplicated)
cloud_queue.json     offline retry queue (retried for up to 7 days)
signals.json         drift events pending upload
favs.json            local favorite marks (hearts for timestamp-named archives)
wallpapers\         wallpaper archive (kept forever, no cap - disk space is yours to manage)
bin\AIWallpaper.exe  the official copy made during silent first-run setup
logs\runtime.log     runtime log (1 MB × 3, rotated)
```

Uninstall: Settings → Service & Schedule → delete the scheduled task, then delete the directory above.

## ComfyUI checklist

Rendering is performed locally by `aw-agent` on the render machine after it claims tasks from the cloud; on the ComfyUI side you only need:

1. Local reachability is enough: aw-agent probes `127.0.0.1` on ports 8188/18188 —
   **no inbound ports may be opened to the cloud or LAN** (the frpc tunnel has been retired).
2. Install the models required by the Z-Image Turbo workflow:

   - `models/diffusion_models/z_image_turbo_int8_convrot.safetensors` (UNet)
   - `models/text_encoders/qwen_3_4b_fp4_mixed.safetensors` (CLIP, lumina2)
   - `models/vae/ae.safetensors` (VAE)

3. **The workflow template ships with aw-agent**: `default.json` next to the exe is the template, carrying
   `{{POSITIVE}}/{{NEGATIVE}}/{{WIDTH}}/{{HEIGHT}}/{{SEED}}` placeholders — swap models or workflows by editing this one file, no code changes.
   The server-side `clouddata/workflows.json` registry (auto-generated on first start) selects workflow IDs and the output size range (min_side/max_side);
   unknown IDs fall back to `default.json`. Registry example:

   ```json
   [
     {"id":"wf-base","name":"Z-Image Turbo general","checkpoint":"z_image_turbo_int8_convrot.safetensors",
      "steps":8,"cfg":1,"sampler_name":"res_multistep","scheduler":"simple","min_side":512,"max_side":1920},
     {"id":"wf-small","name":"Small-size tier","checkpoint":"z_image_turbo_int8_convrot.safetensors",
      "steps":8,"cfg":1,"sampler_name":"res_multistep","scheduler":"simple","min_side":512,"max_side":1088}
   ]
   ```

   Restart the cloud service after saving; the LLM only picks workflows from the whitelist.
   No client-side change is needed. The default output size is 1920×1080.
4. No ComfyUI node plugins to install: the template uses standard nodes (CLIP encode → AuraFlow sampling → KSampler → VAEDecode → SaveImage);
   only the standard nodes and models above must exist.
5. aw-agent ships a watchdog: after repeated failed heartbeats it relaunches ComfyUI via `comfyui_cmd` in aw-agent.json
   (a same-directory bat name), and periodically checks `agent/latest` to update itself.

## Cloud service deployment

### Environment variables

| Variable | Default | Description |
| --- | --- | --- |
| `AW_ADDR` | `:8707` | Listen address |
| `AW_DATA` | `./clouddata` | Data directory (db/finished images/pre-gen/update packages) |
| `AW_LLM_BASE` | `https://token-plan-cn.xiaomimimo.com/v1` | OpenAI-compatible LLM endpoint (default Xiaomi MiMo; any compatible service works) |
| `AW_LLM_KEY` | empty | LLM key (**server-side only**; if empty, prompts fall back to the local engine and images still render) |
| `AW_LLM_MODEL` | `mimo-v2.6-flash` | Model name |
| `AW_ADMIN_TOKEN` | empty | Admin dashboard password; when empty the dashboard is disabled (404) |
| `AW_SITE` | `/opt/aiwallpaper/site` | Website static-page directory (built-in site hosting) |
| `AW_OSS_AK_ID` / `AW_OSS_AK_SECRET` | empty | Alibaba Cloud OSS credentials (**server-side only**; required in cloud mode - images live in OSS and are served via signed URLs) |
| `AW_OSS_BUCKET` / `AW_OSS_ENDPOINT` | empty | OSS bucket and endpoint (e.g. `oss-cn-hangzhou.aliyuncs.com`) |
| `AW_AGENT_TOKEN` | empty | Render-agent (aw-agent) auth token (same value as in aw-agent.json; agent endpoints return 503 when empty) |
| `AW_QUOTA_GEN_HOUR` / `AW_QUOTA_GEN_DAY` | `10` / `40` | Per-user render quota (429 when exceeded; Android has its own daily quota) |
| `AW_QUOTA_ANDROID_DAY` | `40` | Android-specific daily render quota (counted separately from PC) |
| `AW_QUOTA_LLM_HOUR` / `AW_QUOTA_LLM_DAY` | `10` / `40` | Per-user LLM call quota (separate scopes per platform) |

> Quota values can also be overridden via a systemd `EnvironmentFile` (e.g. a `quota.env` drop-in) - edit the file and restart, no rebuild needed;
> keep secrets in an EnvironmentFile too rather than inline in the unit file.

### systemd deployment

```ini
# /etc/systemd/system/aiwallpaper-cloud.service
[Unit]
Description=AI Wallpaper Cloud
After=network.target

[Service]
User=aiwallpaper
WorkingDirectory=/opt/aiwallpaper
Environment=AW_ADDR=127.0.0.1:8707
Environment=AW_DATA=/var/lib/aiwallpaper
Environment=AW_LLM_KEY=<your LLM key>
Environment=AW_ADMIN_TOKEN=change-me
# Also required: AW_OSS_* (image storage) and AW_AGENT_TOKEN (render agent) - see the table above;
# put secrets in an EnvironmentFile drop-in instead of this file
ExecStart=/opt/aiwallpaper/aiwallpaper-cloud
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload && sudo systemctl enable --now aiwallpaper-cloud
```

### nginx reverse proxy (optional, HTTPS recommended in production)

```nginx
server {
    listen 443 ssl;
    server_name wallpaper.example.com;
    # ssl_certificate ...; ssl_certificate_key ...;

    client_max_body_size 32m;

    location / {
        proxy_pass http://127.0.0.1:8707;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 120s;
    }
}
```

> Note: the cloud service needs no WebSocket; the whole client flow is HTTP polling, so the reverse proxy needs no extra config.
> The pairing relay page and update-package downloads share the same domain (`/d/{code}`, `/releases/`); the website is hosted by the cloud service itself (`AW_SITE`).

### Admin dashboard

Visit `https://<your-domain>/admin?token=<AW_ADMIN_TOKEN>` in a browser:
see all users (today's LLM/render usage, disabled state), recent render records, drift events, task-queue status, the render agent's online status & version, and website traffic stats (PV/UV/downloads).

## Client releases (silent self-update)

1. Bump the version in code (`Version` in `internal/version/version.go`, shared by client and cloud server), build the new client,
   and rename the artifact to `AIWallpaper-<version>.exe`.
2. Compute the sha256 and size, and write them together with the release notes into the server-side `releases/manifest.json`:

   ```json
   {
     "version": "1.2.5",
     "file": "AIWallpaper-1.2.5.exe",
     "sha256": "<64-hex>",
     "size": 14081024,
     "notes": "shorter job IDs, unified history filenames, wider window",
     "url": "https://www.cybermesh.space/go/dl"
   }
   ```

   - `file`: the file name relative to the server's `releases/` directory (the default download source).
   - `url` (optional): **download-source override**. When set, all clients (including old versions) immediately download from this address,
     with no client-code upgrade needed — used to switch to a domestic CDN (e.g. a Gitee direct link), temporarily fall back to a mirror, or A/B test a new source.
     When empty, the default `<scheme>://<host>/releases/<file>` address is used.
3. Put the exe into the server's `releases/` directory. **Nothing else to do**: at the tail of each tick the client queries
   `/api/v1/client/latest`; if there is a new version it silently downloads → verifies sha256 + size → a helper process
   (`--apply-update`) atomically swaps it in after the tick exits, taking effect on the next tick.
   Any failed step keeps the old version, logs, and retries next round; self-update runs after the wallpaper swap and never interrupts the main line.
4. Two more manifests follow the same pattern: `releases/manifest-android.json` (Android APK, served via `/api/v1/android/latest` and `/go/apk`)
   and `releases/manifest-agent.json` (the render agent, served via `/api/v1/agent/latest` and `/go/agent` - the agent updates itself from it).

## Data & privacy

- Anonymous `user_id` + a per-device token, zero sign-up/login; no personally identifiable information is collected.
- Cloud storage: per-user prompt/attributes/seed/success per round, drift events, and usage counters (for preference evolution and quota).
- Generated images are stored in a private, per-user OSS bucket (`u/{user}/{W}x{H}/` resolution directories) and fetched through one-time signed URLs; the machine keeps every wallpaper locally (no cap), while a cloud archive powers history & favorites - deleting an image cleans its cloud copy too.
- Cross-device sync: the old device shows a 6-digit pairing code in Settings (valid 5 minutes, one-time); a new device enters it to reclaim
  `user_id + token + profile`.
- Update packages are downloaded only from the HTTPS address specified in the manifest and verified by sha256 + size; no file from any other source is executed.

## FAQ

- **Does it work offline?** The client needs access to your cloud service; offline the backup pool still swaps wallpapers and uploads resume once reconnected.
- **Render speed and cost?** Z-Image Turbo takes ~30 s/image at 1080p on an RTX 4060;
  at one image per hour, a single consumer GPU can serve dozens of users.
- **Which LLMs are supported?** Any OpenAI-compatible endpoint (default Xiaomi MiMo); with no key configured it uses the local engine automatically, with no loss of function.
- **Why no tray/background process?** This is a design red line: at any moment either a window is open, or it is nowhere in the system.
- **macOS / Linux clients?** Windows only for now (scheduling relies on the Task Scheduler).
- **Content safety?** The workflow and prompts carry built-in negative-prompt constraints; deployers can add moderation on the cloud side as needed.

## Development & acceptance

- `go test ./...` all green (incl. cross-module end-to-end: real tick ↔ real cloud ↔ simulated render agent / mock LLM)
- Double-click `dist\AIWallpaper.exe` to open the settings panel; first-run task registration and shortcuts complete in the background
- After closing the window, `tasklist | findstr /I AIWallpaper` shows no leftover; `schtasks /Run /TN AIWallpaper` swaps silently with no window
- Run offline: the old wallpaper is kept, events enter the retry queue, and are reported automatically once reconnected
- Satisfaction check-in dialog on due: Satisfied → cycle increments; Change style → swaps immediately and resets; Later → postponed by 3 days

## License

[AGPL-3.0](https://www.gnu.org/licenses/agpl-3.0.html) — you may freely use, modify, and self-host it;
if you offer a modified version to others as a network service, you must open-source your changes under the same license.
