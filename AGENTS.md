# openyou — 快速上手

> **第一准则：听用户的。** 用户说先修复再构建，就先修复再构建。用户说不要构建，就不要构建。永远以用户的指令为第一优先级，不要自作主张。
>
> **对外介绍: [README.md](README.md)** · **宪法: [VISION.md](docs/VISION.md)** · **规格: [SPEC.md](docs/SPEC.md)**
>
> Go daemon (.so via cgo/JNI) + Kotlin WebView shell，DeepSeek API。项目根: `openyou/`，Go module: `openyou/daemon`。

> **开源说明**：本仓库已在 GitHub 开源（`userdata/` 私有数据除外）。`README.md` 面向外部读者；本文档面向 agent/开发者。提交到 GitHub 前：① `userdata/` 只保留本地真实数据、不随仓库上传（开源仓库用 `userdata.example/` 骨架）；② 无硬编码密钥；③ 运行 `python3 web/build.py` 保证产物与源码一致；④ 全量 `go test ./...` 通过。API key 一律走 `userdata/openyou.json`（.jsonc）或设置页，禁止写死在代码里。

---

## 0. 核心设计原则（由 VISION 推导）

所有架构决策必须能回答：**这个决定让"他"更像平行版本的你吗？**

| 原则 | 含义 | 检验标准 |
|------|------|---------|
| **数据是身份** | 他的智慧来自你的数据，不是来自 AI 模型 | 抽掉 Context 后，他还剩什么？如果还能对话——他是假的 |
| **智囊团** | 多个技能并行分析，交叉验证，集体智慧 | 复杂问题是单一回答还是多视角合成？ |
| **他是主体，chat 是接口** | Context（Insides/Rules/Skills/Memories）/记忆是核心系统，对话只是他的一种输出方式 | 代码里是 chat 依赖 Context，还是 Context 依赖 chat？ |
| **他主动，用户被动** | 打开 app 看到的是"他发现的"，不是空白输入框 | 主界面是 Home 看板还是 chat？ |
| **他记得，用户不用复述** | 你说一个词，他知道全部上下文 | 提到"A"时他是否自动关联 A 的全部档案？ |
| **他追问，用户反馈** | 给了建议 → 问结果 → 修正认知 | 有没有"这个建议有用吗？"的回环？现在是死的 |
| **他一直在，不是等待召唤** | 后台持续处理、主动推送、不需要打开 app | daemon 是 HTTP server 还是背景 worker？现在是 HTTP server |

---

## 1. Project Structure

```
openyou/
├── android/                   # Kotlin Android app shell
│   └── app/src/main/          # MainActivity, jniLibs/, assets/www/
├── daemon/                    # Go daemon → .so via cgo + JNI
│   ├── cmd/daemon/            # entry: main.go (local go run) + daemon_android.go (JNI export)
│   └── internal/
│       ├── ai/agent/          # skill types (SkillInfo/SkillDef) + SkillMatcher interface (leaf package)
│       ├── ai/llm/            # OpenAI-compatible client: Chat + SSE Stream + MaskingClient
│       ├── ai/thinktank/      # thinktank: parallel sub-agent orchestration + synthesis
│       ├── infra/bridge/      # WebSocket hub (Broadcast to UI clients)
│       ├── infra/config/      # SQLite key-value store (api_key/model/base_url)
│       ├── infra/crypto/      # AES-256-GCM field-level encryption
│       ├── infra/db/          # schema migration helpers
│       ├── infra/notify/      # notification queue (Android foreground service long-poll)
│       ├── infra/privacy/     # Pseudonymizer: person names → codenames
│       ├── infra/tool/        # Tool registry + doom-loop detection + iteration budget
│       ├── server/            # HTTP router (38 routes) + REST/SSE/WS handlers
│       ├── service/background/# Star check (6h) + inside health (24h) + memory decay (24h)
│       ├── service/importer/  # first-boot import of initial context → userdata
│       ├── service/matcher/   # skill matching + on-demand retrieval (signal→chunk) + incremental refresh
│       ├── store/consult/     # sessions+messages+feedback (SQLite)
│       ├── store/memory/      # LLM memory extraction + per-type confidence thresholds + async queue
│       ├── store/task/        # Task/Calendar (SQLite)
│       └── web/               # //go:embed index.html (build artifact, built from web/ sources)
├── docs/                      # VISION.md, SPEC.md
├── scripts/                   # E2E/APK test scripts + automation
├── web/logo.svg               # single logo source (build syncs web symbol + Android vector)
├── userdata/                  # context source data (8 concepts, gitignored, never published)
│   ├── inside/                # sensitive details (any subdirectory, recursive)
│   ├── rule/                  # behavioral rules
│   ├── skill/                 # analysis frameworks
│   ├── star/ task/ calendar/ memory/  # time-based: fixed YYYY-MM.md
│   └── consult/               # consultation records: fixed YYYY-MM.md
├── userdata.example/          # open-source skeleton: empty category dirs + openyou.example.jsonc (build fallback)
├── AGENTS.md                  # this document
├── Makefile                   # Build automation
└── openyou-v*.apk             # Built APKs (gitignored)
```

## 2. Build & Deploy

> ⚠️ **铁律 — 构建前必做 Code Review**：每次代码改动后、构建 APK 之前，**必须做完整的 code review**：
> 1. 检查修改的代码本身逻辑是否正确
> 2. 检查代码的上下游——调用的和被调用的——是否受影响
> 3. 检查 CSS/JS/HTML 三者是否一致（选择器名、ID、class 是否匹配）
> 4. 检查是否引入了硬编码值（颜色、字号、间距应使用 CSS 变量）
> 5. 检查新功能是否影响已有功能（回归风险）
> 6. 确认无问题后，**才执行构建**。不要构建后才发现问题。

> ⚠️ **构建流程**：Web 源码在 `web/` 目录下（CSS/HTML/JS 按功能分文件）。`bash scripts/build.sh` 先运行 `python3 web/build.py` 合并成 `daemon/internal/server/web/index.html`（产物），再通过 `//go:embed` 嵌入 daemon。**只改 `web/` 下的源文件，不要直接编辑产物 index.html**。快速迭代用 `bash web/dev.sh`（只跑合并，无需完整 APK 构建）。
> ⚠️ **CSS 必须内联**：HarmonyOS WebView **不支持外部 `<link>` 样式表**。CSS 写在 `web/css/style.css`，build.py 自动内联进 `<style>` 标签。
> ⚠️ **Logo 单一来源**：`web/logo.svg` 是唯一 logo 文件。build.py 每次构建自动①内联为 `<symbol id="logo-y">`（页面用 `<use href="#logo-y">` 引用）②重新生成 `android/.../ic_launcher_foreground.xml`。**改 logo 只改 logo.svg**；不要手改生成的 vector XML；logo.svg 里保持只用 `<path>` 元素。minSdk=31，无 PNG 兜底图（自适应矢量图标覆盖所有设备）。

> ⚠️ **版本号规则**：`web/js/core/state.js` 的 `APP_VERSION` 是唯一版本源。`bash scripts/build.sh` 自动：读取当前版本 → +1 → 同步写回 state.js、产物、gradle `versionName` 三处 → 生成 `openyou-v<新版本>.apk`。无需手工改版本号；要指定版本时 `bash scripts/build.sh <x.y.z>`。

### Prerequisites

- Go 1.22+, Android NDK 27.0+, Gradle + Android SDK
- NDK path: `$HOME/Android/Sdk/ndk/27.0.12077973`（或设置 `OPENYOU_NDK`/`ANDROID_NDK_HOME`）

### Build daemon .so (arm64 + x86_64)

```bash
cd daemon
NDK_BIN=$HOME/Android/Sdk/ndk/27.0.12077973/toolchains/llvm/prebuilt/linux-x86_64/bin

# arm64
CGO_ENABLED=1 GOOS=android GOARCH=arm64 \
  CC=$NDK_BIN/aarch64-linux-android21-clang \
  go build -buildmode=c-shared -o libagent.so ./cmd/daemon/

# x86_64 (emulator)
CGO_ENABLED=1 GOOS=android GOARCH=amd64 \
  CC=$NDK_BIN/x86_64-linux-android21-clang \
  go build -buildmode=c-shared -o libagent.so ./cmd/daemon/
```

### Build APK (full cycle)

```bash
bash scripts/build.sh
```

Outputs `openyou-v<version>.apk` in project root.

### Install to phone (Huawei HarmonyOS)

```bash
adb -s <DEVICE_SERIAL> install -r app-debug.apk   # see `adb devices` for the serial
```

### Quick test (daemon only, Linux)

```bash
cd daemon && go build -o daemon ./cmd/daemon/ && ./daemon &
curl http://127.0.0.1:8765/api/health   # → {"status":"ok"}
curl http://127.0.0.1:8765/api/config    # → {"api_key":"", ...}
```

### E2E test

```bash
bash scripts/test_e2e.sh           # default: built-in fake LLM, no key required
bash scripts/test_e2e.sh --live    # real DeepSeek (needs DEEPSEEK_API_KEY)
```

### APK automated test

```bash
bash scripts/test_apk.sh   # 6-layer test: build, native shell, daemon API, WebView, robustness, SPEC alignment
```

## 3. Key Design Decisions

| Decision | Reason |
|----------|--------|
| HTML served by daemon at `/:8765/` | HarmonyOS ArkWeb blocks `file://` cross-origin; same-origin HTTP avoids CORS |
| REST SSE for chat, WebSocket for sessions | Chat streams via `POST /api/chat` SSE; WS at `/ws` for session sync & diagnostics |
| Config dual-persist (localStorage + daemon SQLite) | localStorage survives WebView restarts; daemon SQLite is source of truth for API calls |
| API key returned unmasked on GET | Daemon listens 127.0.0.1 only — no security risk from local read |
| JNI startDaemon blocks the calling thread | Runs the serve+retry loop synchronously so Kotlin `daemonThread.isAlive` reflects the real daemon state; the service restarts a dead daemon via `onStartCommand` |
| Mutating APIs require `Content-Type: application/json` | CSRF guard: cross-origin simple requests (text/plain) can't forge writes against 127.0.0.1; curl/scripts must send the header |
| `PUT /api/tasks/{id}` is partial-update | UI toggles send `{title,end_date,done}` only; full-replace wiped description/priority on every toggle |
| Notification channels versioned by ID (`openyou-*-v2`) | Android freezes channel names after creation; renaming requires a new ID + deleting the legacy ones |
| Data dir multi-path fallback | `/data/data/<pkg>/files/` → `/storage/emulated/0/` → `/data/local/tmp/` |
| SQLite via CGO (`mattn/go-sqlite3`) | Required for Android NDK cross-compilation compatibility |
| WebView onstop destroys & recreates | `onPageFinished` re-sets WebView on daemon process restart |
| **按需检索（VISION §2.8）** | Rule 全量加载；inside 用 `RelevantDocs` 两级检索（signal 粗筛 → `##` chunk 精筛）预算内注入相关**章节**；Skill `Match` top-5 懒加载全文；Content 不驻留内存（signal 常驻） |
| **注入粒度：资料要片段，框架要完整** | inside 按 chunk（实测题库每节约 300 字）注入，30 万字题库一次只带相关几题；skill 整篇注入（框架完整性），clip 12000 runes 兜底 |
| **增量刷新（P9）** | 手动增删改走 `RefreshDoc` 单文件 O(1)（2000 文档下 34µs）；导入/恢复走全量 `Reload`；AI 入口 `refreshIfChanged` 按 mtime 指纹自动感知旁路改文件（1s 时间窗节流） |

## 4. Gotchas & Critical Context

### HarmonyOS ArkWeb quirks
- **`file://` cross-origin is blocked** → daemon serves HTML at `http://127.0.0.1:8765/`
- **`r.body.getReader()` (ReadableStream) must work** for SSE fetch — Android SDK 31+ (Chrome 43+ WebView)

### JNI pitfalls
- **Companion object `init` is lazy** → `System.loadLibrary()` must be in class `init` block; catch `Throwable`（loadLibrary 失败抛的是 Error 不是 Exception）
- **JNI naming must match exactly**: `@JvmStatic external fun startDaemon()` → `Java_io_openyou_app_MainActivity_startDaemon`
- **`startDaemon()` 是阻塞调用**（JNI 内同步跑 serve+重试循环）→ 只能在专用线程调用（`DaemonForegroundService.startDaemonOnce`）；绝不能在 UI 线程调用
- **`isDaemonAlive()` 依赖线程存活** → 不要把 JNI 调用包进 `go func()`/立即返回，否则线程秒死、守护进程死了也无法感知和拉起

### SSE / streaming
- **HTTP error (4xx/5xx) from daemon must reset `setStreaming(false)`** — otherwise UI stuck "thinking"
- **`[DONE]` sentinel is plain text** (not JSON) → SSE parser special-cases it
- **`reader.read()` `{done:true}` must trigger `handleDone()`** → clean stream end without `[DONE]`
- **WebSocket URL path**: daemon registers at `/ws`, not root `/`

### Config
- **`saveSettings()` must PUT to daemon** — localStorage-only means api_key never reaches daemon = chat always 400
- **API key 三来源，优先级从高到低**：① App 设置页填写（`PUT /api/config` 写入，持久化在 SQLite，加密存储）→ ② `userdata/openyou.json` 或 `userdata/openyou.jsonc`（daemon 启动时读取，只在配置项为空时生效，支持 `//` 注释）→ ③ 内置默认（base_url/model；api_key 默认空）。参考模板见 `userdata.example/openyou.example.jsonc`
- **api_key 不硬编码、不入库明文、不上传 git**：`userdata/` 整目录在 `.gitignore`，**不随仓库上传**；开源仓库克隆后无真实内容，`scripts/build.sh` 自动回落到 `userdata.example/` 骨架（空分类目录 + 模板），保证 `go:embed` 与构建可用；`daemon/internal/service/importer/userdata/PLACEHOLDER` 被 `git add -f` 强制跟踪以保嵌入编译通过（go:embed 忽略点文件，故用非隐藏占位；rsync 已 `--exclude='PLACEHOLDER'` 保护）
- **`model` default**: `deepseek-v4-pro` (not `deepseek-chat`)
- **`base_url` default**: `https://api.deepseek.com/v1` (not without `/v1`)

## 5. Target Device

| Property | Value |
|----------|-------|
| Model | Huawei BLK-AL80 (HarmonyOS) |
| Android SDK | 31 (Android 12) |
| EMUI | 14.2.0 |
| ADB ID | `<DEVICE_SERIAL>`（`adb devices` 查看；本仓库开发机见 `git log --format='%an'`） |
| CPU | ARM64 (Kirin?) |

## 6. LLM Config

| Key | Value |
|-----|-------|
| Provider | DeepSeek (OpenAI-compatible) |
| Base URL | `https://api.deepseek.com/v1` |
| Model | `deepseek-v4-pro` |
| API Key | 开发者本机（如 `~/.opencode/opencode.json` 或 `userdata/openyou.json`），由用户自行配置 |

## 7. Testing

```bash
# On phone (daemon diagnostics)
adb -s <DEVICE_SERIAL> shell run-as io.openyou.app cat files/daemon-step.txt
adb -s <DEVICE_SERIAL> logcat -d | grep -i "openyou\|chromium"

# Port forward for direct API testing
adb -s <DEVICE_SERIAL> forward tcp:18765 tcp:8765
curl http://127.0.0.1:18765/api/health
curl http://127.0.0.1:18765/api/config
curl -N -X POST http://127.0.0.1:18765/api/chat \
  -H "Content-Type: application/json" \
  -d '{"message":"hi"}'

# Force stop + restart
adb -s <DEVICE_SERIAL> shell am force-stop io.openyou.app
adb -s <DEVICE_SERIAL> shell am start -n io.openyou.app/.MainActivity
```
