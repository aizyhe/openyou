# 功能规格与技术（SPEC）

> 所有功能受 [产品定位 (VISION.md)](./VISION.md) 约束。**功能和定位冲突时，以定位为准。**
>
> 最后更新: 2026-08-03（v1.2.4）

---

## 目录

- [最高设计准则](#最高设计准则)
- [1 系统架构](#1-系统架构)
  - [1.1 系统构成](#11-系统构成) · [1.2 技术选型](#12-技术选型) · [1.3 整体架构](#13-整体架构) · [1.4 启动流程](#14-启动流程) · [1.5 后台任务](#15-后台任务) · [1.6 通知保活](#16-通知保活)
- [2 界面设计](#2-界面设计)
  - [2.1 核心原则](#21-核心原则) · [2.2 设计语言](#22-设计语言) · [2.3 概念架构](#23-概念架构) · [2.4 时间页](#24-时间页) · [2.5 上下文](#25-上下文) · [2.6 咨询页](#26-咨询页)
- [3 数据模型](#3-数据模型)
  - [3.1 命名词汇](#31-命名词汇) · [3.2 目录结构](#32-目录结构) · [3.3 备份恢复](#33-备份恢复) · [3.4 任务数据](#34-任务数据) · [3.5 任务生命](#35-任务生命) · [3.6 三页闭环](#36-三页闭环)
- [4 智能引擎](#4-智能引擎)
  - [4.1 智囊团](#41-智囊团) · [4.2 冲突合成](#42-冲突合成) · [4.3 数据导入](#43-数据导入) · [4.4 记忆提取](#44-记忆提取) · [4.5 记忆生命](#45-记忆生命) · [4.6 追问纠正](#46-追问纠正)
- [5 接口设计](#5-接口设计)
  - [5.1 系统对话](#51-系统对话) · [5.2 分析接口](#52-分析接口) · [5.3 文档技能](#53-文档技能) · [5.4 记忆任务](#54-记忆任务) · [5.5 反馈备份](#55-反馈备份)
- [6 安全质量](#6-安全质量)
  - [6.1 三层防御](#61-三层防御) · [6.2 隐私模式](#62-隐私模式) · [6.3 性能策略](#63-性能策略) · [6.4 测试验证](#64-测试验证)

---

## 最高设计准则

> **用户没有明文规定的，就是不做限制。**

1. **App 与用户数据解耦**：用户只需在 8 个概念目录下放入对应的 `.md` 文档，
   App 自动读取、展示、注入——不需要用户改任何代码，也不需要 App 认识文档
   的具体内容。放什么、叫什么、怎么组织，完全是用户的自由。
2. **骨架固定，内容自由**：只有本 SPEC 明确固化的骨架（8 概念、一级子目录、
   命名规则）是固定的。骨架之外的一切（文件内容、文件名、类型、结构），
   用户可任意增删改，代码不做任何假设、不做任何限制。
3. **通用化优先**：能按"目录读取全部文件"实现的，就不按"特定类型"实现。
   新增类型不需要改代码（加文件即可）。
4. **不造新概念**：只用本 SPEC 明确固化的概念命名，不发明用户没定义的名词
    （如不叫 session 叫 consult，不叫 todo 叫 task）。
5. **单一入口**：所有 LLM 调用走同一脱敏入口（人名脱敏），不散落。
6. **一致性**：App 子页、userdata 目录、store 包/表，三者命名严格一致
   （大类+小类，如 time_task / context_rule / consult_new）。

> 判断标准：设计某功能时问自己——"用户是否明文要求了这个限制？"
> 如果答案是否定的，就不要加这个限制。让用户自由使用数据，代码只做骨架
> 层面的约定。

---

## 1 系统架构

### 1.1 系统构成

openyou采用 Go daemon + Kotlin 壳的混合架构。Go daemon 编译为 .so 共享库，通过 JNI 由 Android 主进程加载，在后台运行 HTTP 服务（127.0.0.1:8765）。Kotlin 层提供 WebView 容器、前台保活、系统通知和 App 锁。

内部按 5 层单向依赖组织：`server → service → ai → store → infra`。

| 层 | 包 | 职责 |
| --- | --- | --- |
| server | `server/` | HTTP 入口：REST/SSE/WebSocket，全部路由与 handler（38 条，含 WS） |
| service | `service/background` `service/matcher` `service/importer` | 后台 worker、技能匹配与文档读取、首次数据导入 |
| ai | `ai/thinktank` `ai/agent` `ai/llm` | 智囊团并行分析、技能类型定义、LLM 客户端（SSE + Masking） |
| store | `store/task` `store/consult` `store/memory` | 各领域 SQLite CRUD（task=待办，consult=会话+消息+反馈，memory=AI 记忆） |
| infra | `infra/db` `infra/config` `infra/crypto` `infra/privacy` `infra/notify` `infra/bridge` `infra/tool` | 迁移、配置、加密、化名、通知队列、WS hub、工具注册 |

架构决策：Go 1.22+ 从头编写，不基于 OpenCode。SQLite 本地存储，DeepSeek API 通过 SSE 流式调用。

### 1.2 技术选型

| 层 | 技术 | 理由 |
| --- | --- | --- |
| 界面 | HTML/CSS/JS SPA（`web/` 多文件源码，`build.py` 合并为单文件产物，CSS 内联） | WebView 内嵌，CSS 内联（ArkWeb 不支持外链） |
| 大脑 | Go 1.22+ + cgo → .so | goroutine 天然并行，单二进制 |
| 存储 | SQLite (mattn/go-sqlite3) | 嵌入式，Android 原生 |
| LLM | DeepSeek SSE streaming | OpenAI-compatible |
| 加密 | AES-256-GCM | 应用层字段加密 |
| 通信 | WebSocket + SSE | WS 双向推送，SSE 流式响应 |

### 1.3 整体架构

```text
┌─ UI Layer (WebView SPA)──────────────────────────────────────┐
│  sources: web/{css,html,js/} → build.py → index.html        │
│  Time (Star+Task+Calendar) · Context (Inside/Rule/Skill)     │
│  Home · Consult (New/History/Memory)                         │
└────────────┬─────────────────────────────────────────────────┘
             │ WebSocket (/ws) + HTTP REST (:8765)
┌────────────▼─────────────────────────────────────────────────┐
│  Go daemon (:8765)                                           │
│  ┌─ Background Workers (ticker-based)─────────────────────┐  │
│  │Star check (6h) · Context health (24h) · Memory decay   │  │
│  └────────────────────────────────────────────────────────┘  │
│  ┌─ ThinkTank (ai/thinktank, ≤5 parallel sub-agents)────┐  │
│  │query → Match skills → parallel sub-agents → conflicts │  │
│  │            → synthesize                                │  │
│  └────────────────────────────────────────────────────────┘  │
│  ┌─ LLM: MaskingClient (anonymize) → DeepSeek SSE─────────┐  │
│  └────────────────────────────────────────────────────────┘  │
│  Storage: SQLite (AES-256-GCM encrypted api_key)             │
└────────────┬─────────────────────────────────────────────────┘
             │ JNI (cgo)
┌────────────▼─────────────────────────────────────────────────┐
│  Kotlin Android Shell                                        │
│  WebView · ForegroundService · NotificationManager · App Lock│
└──────────────────────────────────────────────────────────────┘
```

### 1.4 启动流程

```text
System.loadLibrary("agent")
  → Java_io_openyou_app_MainActivity_startDaemon()
      → 1. init data dir + SQLite (config/task/consult/memory)
      → 2. matcher seeds userdata .md into runtime dir
      → 3. import seed data on first launch (importer)
      → 4. build Pseudonymizer + MaskingClient (controlled by privacy_mode)
      → 5. start memory extraction queue + thinktank + background workers
      → 6. start HTTP server (:8765)
```

**JNI 汇总**：

| JNI Export | 方向 | 用途 |
| --- | --- | --- |
| `startDaemon()` | Kotlin → Go | 启动 daemon |
| `KeystoreManager.provisionMasterKeyFile()` | Kotlin | 预置 Keystore 密钥文件（daemon 启动前） |

### 1.5 后台任务

```go
func Start(ctx context.Context, m *memory.Store, h Broadcaster, n Notifier) {
    go w.starLoop(ctx)          // every 6h: Star check (pending task changes) → broadcast + notify
    go w.insideHealthLoop(ctx)  // every 24h: rule conflicts / past-due tasks → broadcast + notify
    go w.memDecayLoop(ctx)      // every 24h: decay confidence of unused memories, delete <0.1
}
```

后台 worker 通过 `Broadcast` 推送到 WebView，通过 `Notifier.PushFields` 入队系统通知：

```json
{"type":"star-check","pending":N}
{"type":"context-health","rule_conflicts":N,"past_due_tasks":N,"issues":[...]}
```

后台例行任务：Star 综合检测（6h）、Context 健康度（24h）、记忆衰减（24h）。
记忆提取为异步队列（LLM 后台处理，并发 2）。

### 1.6 通知保活

**通知类型**（infra/notify 队列）：

| 类型 | 触发 |
| --- | --- |
| `star-alert` | Star 关注项变化（每 6h 检查） |
| `memory-confirm` | 学到不确定的新信息 → 让用户确认 |
| `feedback-reminder` | 给建议 3 天后未收到反馈 → 追问 |
| `health-warning` | 资料健康警告（陈旧人物/规则冲突/到期关注项，每 24h） |

**保活机制**：`DaemonForegroundService` + `NotificationHelper`（4 渠道 `openyou-*-v3`：警报/发现/提醒/运行）+ `BootReceiver` 开机自启 + 长轮询 `GET /api/notifications/poll`。注意：Android 通知渠道创建后名称不可改，改名必须换新渠道 ID（当前为 `-v3`），并在 `ensureChannels` 中删除旧 ID。所有用户可见文案统一用 `openyou`。

---

## 2 界面设计

### 2.1 核心原则

聊天仅为最末层接口。主界面以看板呈现他的主动发现，替代传统空白输入框。

**信息流方向：系统主动推送，用户反馈驱动。不依赖用户先提问。**

### 2.2 设计语言

GitHub 风格暗色 brutalist：无阴影、透明层叠表达深度、单一紫色 accent、锐利边角。

| 变量 | 色值 | 用途 |
| --- | --- | --- |
| `--bg` | `#0d1117` | 主背景（GitHub dark 基底） |
| `--bg-card` | `#161b22` | 卡片背景 |
| `--bg-input` | `#1c2129` | 输入框背景 |
| `--border` | `#21262d` | 分割线/边框 |
| `--text` | `#e6edf3` | 主文字 |
| `--text-dim` | `#8b949e` | 次要文字 |
| `--accent` | `#7C4DFF` | 主题色（用户消息、链接、按钮、选中态） |
| `--danger` | `#f85149` | 危险/删除 |
| `--success` | `#3fb950` | 成功/确认 |
| `--warning` | `#d29922` | 警告 |

来源标注色：人物 `#3b82f6`、规则 `#22c55e`、技能 `#a855f7`、外部 `#f97316`。

**咨询区**：无气泡、无背景，纯粹文字流。`.msg.user` accent 紫，`.msg.assistant` 灰。流式输出光标 `▍`。

**字体**：`system-ui, 'Noto Sans SC', sans-serif`；基础 14px，标题 15px，小字 11-13px，行高 1.5。

**Logo**：唯一来源 `web/logo.svg`（纯色 `#7C4DFF` 方形底 + 白色 Y 笔画）。构建时 `web/build.py` 自动同步到两处：① 内联为 `<symbol id="logo-y">` 供页面 `<use>` 引用；② 生成 Android 自适应图标前景 `ic_launcher_foreground.xml`（该文件自动生成，禁止手改）。改 logo 只改 `web/logo.svg` 一处。

**Tab 栏**：底部固定 4 标签。

| 标签 | 含义 |
| --- | --- |
| Home | 主看板 — 统计 + 待办提醒（有实际数据时显示） |
| Time | 时间/任务管理 — Star + Task + Calendar |
| Context | 上下文 — Inside + Rule + Skill |
| Consult | 咨询 — New + History + Memory |

### 2.3 概念架构

**Time（时间维度）— 描述"你处于什么状态"**：

| 概念 | 定义 | 存储 |
| --- | --- | --- |
| **Star** | 一键综合报告：读取任务/档案/规则/记忆，输出 ①行动建议 ②关注项（该关注的/待办的）③盲区洞察（重复的模式）。每次点击有历史记录。 | DB + md（YYYY-MM.md） |
| **Task** | 结构化待办，支持两种创建模式（用户自主 + Star 建议采纳） | DB（store/task，表 time_task） |
| **Calendar** | Task 的日历视图，前端按 end_date 分组渲染 | 无独立 store（复用 store/task） |

**Context（静态档案）— 描述"你是谁"**：

| 概念 | 定义 | 存储 |
| --- | --- | --- |
| **Inside** | 私有档案：人物/公司/绩效/org 等任意文档，LLM 注入前脱敏 | 纯文件（userdata/inside/*.md） |
| **Rule** | 行为准则，精要背景知识 | 纯文件（userdata/rule/*.md） |
| **Skill** | 分析框架，触发词匹配后智囊团调用 | 纯文件（userdata/skill/*.md） |

**Consult（咨询维度）— 描述"你们聊了什么"**：

| 概念 | 定义 | 存储 |
| --- | --- | --- |
| **New** | 新咨询对话，可预设提示词（如 trend 关键词 → AI 输出相关资讯），SSE 流式 | DB |
| **History** | 咨询历史，按时间排序 | DB |
| **Memory** | AI 从对话自动提取的记忆，需确认后生效 | DB 为主 + md 视图（按月 YYYY-MM.md） |

> **已删除概念**：Radar（并入 Star 的雷达检测）、Mirror（并入 Star 的盲区洞察）、Trend（通过 New 对话询问，无独立功能）。

**三页对应**：

| 页面 | 子页 | 角色 | 数据来源 |
| --- | --- | --- | --- |
| **Time** | Star / Task / Calendar | 时间维度：一键综合报告 + 待办 + 日历视图 | `/api/star` `/api/tasks`（Calendar=Task 按日期前端分组） |
| **Context** | Inside / Rule / Skill | 静态档案：人物/规则/技能 | matcher 读取 userdata/*.md |
| **Consult** | New / History / Memory | 咨询维度：新对话 + 历史 + AI 记忆 | `/api/chat` `/api/analyze` `/api/sessions` `/api/memory` |

### 2.4 时间页

三大子 tab：Star / Task / Calendar。

- **Star（✦）**：一键按钮，零输入。AI 读取任务/日历/关注项/档案/规则，输出综合报告：
  ① 行动建议 ② 关注项（原 radar：该关注的/待办的）③ 盲区洞察（原 mirror：重复的模式）。
  每次点击有历史记录（DB + md 归档，`YYYY-MM.md`）。Star 回答结束后 AI 提炼精简 Task 建议（标题+行动项+日期+优先级），
  UI 提供"接受 / 编辑 / 忽略"三个动作。每日一问也集成在 Star 中，优先级：
  最久未反馈建议 > 最新洞察 > 待确认记忆 > 兜底。

- **Task**：结构化待办（DB），路由 `GET/POST/PUT/DELETE /api/tasks`。两种创建模式：
  1. 用户自主创建：手动输入标题 + 截止时间（+ 可选描述/优先级），`POST /api/tasks`
  2. Star 建议采纳：Star 流式结束后提炼精简 Task 建议，用户接受/编辑/忽略

  > 原则：**Star 的回答可以十分详细具体，但生成的 Task 建议必须简洁概要**——
  > 精简为一个行动项 + 日期 + 优先级。用户可以接受，也可以编辑后接受。

- **Calendar**：Task 的日历视图——前端按 `end_date` 分组渲染，无独立 store 和 API。完成/未完成 Tasks 按月排列。不提供时间提醒（通知由后台 worker + 系统通知实现）。

### 2.5 上下文

三大子 tab：Inside / Rule / Skill。全部为纯文件（`userdata/inside/` `userdata/rule/` `userdata/skill/` 下的 `*.md`），App 内 Markdown 编辑，保存即时生效，敏感内容发 LLM 前脱敏（见 §6.2）。

**按需加载总览（VISION §2.8）——所有上下文数据源的加载形式**：

| 数据源 | 存储 | 加载方式 | 注入粒度 | 按需程度 |
| --- | --- | --- | --- | --- |
| **Rule** | `userdata/rule/*.md` | 宪法级，永远全量加载 | 整目录合并 | 固定全量 |
| **Inside** | `userdata/inside/*.md` | 两级检索（signal 粗筛 → `##` chunk 精筛），预算内注入相关章节 | `##`/`###` 小节（实测题库每节约 300 字） | ✅ 按需 |
| **Skill** | `userdata/skill/*.md` | `Match` top-5 命中才懒加载全文（框架需完整） | 整文档（clip 12000 runes 兜底） | ✅ 命中才读 |
| **清单** | 全 userdata | `ListDocs` 列出（上限 40 + 总数），需要时按名 `ReadDoc` | 路径列表 | ✅ 不注入内容 |
| **Task** | SQLite `time_task` | `List()` 读全量 → 只注入**未完成**的（标题 + 截止日期） | 单条任务 | 🟡 半按需（读全量、注入子集） |
| **Memory** | SQLite `context_memory` + `userdata/memory/*.md` | `Relevant(query,5)`：`LIKE` 子串 + 置信度降序 `LIMIT 5` | 单条记忆 | ✅ 真按需（DB 层限量） |
| **History** | SQLite consult 会话 | 读**当前会话**历史 + `compactHistory` 压缩长对话 | 单条消息 | ✅ 会话内按需 |
| **Calendar** | `userdata/calendar/YYYY-MM.md` | 不注入对话 | — | 页面按需 |
| **Star** | `userdata/star/YYYY-MM.md` | 不注入对话，后台 6h 检查 + `/api/star` 手动触发 | — | 触发式 |
| **Consult** | `userdata/consult/YYYY-MM.md` | 不注入对话 | — | 页面按需 |

> 说明：Memory 为子串匹配（`LIKE '%query%'`）而非语义检索——手机端无 embedding 的取舍，问题与记忆需有字面重叠。Task 若堆积上千条，可优化为 `WHERE done = 0` 只查未完成。

**变更即时生效（增量刷新）**：app 内增删改文档走 `RefreshDoc` 单文件增量（O(1)）；导入/备份恢复走全量 `Reload`；直接改文件（旁路）由 AI 入口 `refreshIfChanged` 按 mtime 指纹自动检测，只重扫变化的文件。均无需重启。

### 2.6 咨询页

三大子 tab：New / History / Memory。

- **New**：新咨询对话。可预设提示词（如 trend 关键词 → AI 输出相关资讯），SSE 流式输出。
- **History**：咨询历史（DB），按时间排序，支持回放。
- **Memory**：AI 从对话自动提取的记忆（DB 为主 + md 视图，按月 `YYYY-MM.md`），可编辑/删除/确认/dispute。

每次对话前系统自动构建三层上下文：

| 层 | 内容 | 策略 |
| --- | --- | --- |
| **稳定前缀**（命中 prefix cache） | Rules 全量 | 始终注入 |
| **详情层** | inside 按需检索的相关章节（chunk）+ 可用档案清单 + 未完成 Tasks | 按相关性注入 |
| **记忆层** | 与问题语义相关的历史记忆 | 最多 5 条 |

- 每条建议标注来源：`[👤人名] [📋规则#N] [🧠技能名] [🌐外部搜索]`
- UI 顶部 Context Pills 展示本次注入的人物
- 消息流式显示；长按消息：复制 / 编辑 / 删除 / 重新生成

**设置页**（4 组）：

| 分组 | 设置项 |
| --- | --- |
| AI Engine | Provider / Model / API Key / Base URL / Max Tokens / Temperature |
| Backup | Export / Import |
| Diagnostics | Daemon 状态 / LLM 延迟 / 统计 / Test Connection |
| About | Version / Build |

---

## 3 数据模型

### 3.1 命名词汇

| 产品概念 | 代码包 | API 路径 |
| --- | --- | --- |
| Star | server handler（无独立 store） | `POST /api/star`（SSE 流式） |
| Task | `store/task` | `/api/tasks`（CRUD） |
| Calendar | 无独立代码（Task 前端视图） | 无独立 API |
| Inside | matcher（纯文件） | `/api/docs`（按 path） |
| Rule | matcher（纯文件） | `/api/docs/{path...}`（通用 CRUD） |
| Skill | matcher + thinktank | `/api/skills`（CRUD + reload + curate） |
| New | `store/consult` | `/api/chat`（简单对话 SSE）+ `/api/analyze`（智囊团 SSE） |
| History | `store/consult` | `/api/sessions`（列表/详情/删除） |
| Memory | `store/memory` | `/api/memory`（CRUD + confirm/dispute） |
| 全部文档 | matcher | `/api/docs/{path...}`（通用 CRUD） |

> 附加说明：`thinktank` = 智囊团引擎；`matcher` = Skills 匹配 + 文档源；`importer` = 首启导入工具。
> 已删除概念：Radar（并入 Star）、Mirror（并入 Star）、Trend（通过 New 对话询问）。

### 3.2 目录结构

**核心架构原则（防耦合）**：

> 只有 `userdata/` 下的**一级子目录是固定的**（与 App 功能绑定，代码按目录读取）。
> 命名分两类契约：**时间类固定命名**（`YYYY-MM.md`，代码校验文件名，不符忽略）与
> **用户完全自定义**（文件名/子目录/内容自由，代码不做任何假设）。
> 代码只按一级目录读取，**绝不硬编码任何具体文件名**。

```
userdata/                           naming contract
├── inside/       user-organized     ← fully free (any subdirectory, recursive)
├── rule/         user-organized     ← fully free (keep it brief, ≤10)
├── skill/        user-organized     ← fully free (any subdirectory, recursive)
├── star/         fixed YYYY-MM.md   ← time-based
├── task/         fixed YYYY-MM.md   ← time-based
├── calendar/     fixed YYYY-MM.md   ← time-based
├── memory/       fixed YYYY-MM.md   ← time-based (AI-generated monthly, written to disk)
└── consult/      fixed YYYY-MM.md   ← consultation records
```

**文件命名规则（两类契约）**：

| 目录 | 命名规则 | 契约类型 |
|------|---------|---------|
| `inside/` | 完全自定义（任意子目录/文件名/内容，支持递归） | **用户自定义** |
| `rule/` | 完全自定义（建议 ≤10 个，精要背景知识） | **用户自定义** |
| `skill/` | 完全自定义（任意子目录/文件名，支持递归） | **用户自定义** |
| `star/` | `YYYY-MM.md`（按月，固定不可改） | **时间类：命名固定** |
| `task/` | `YYYY-MM.md`（按月，固定不可改） | **时间类：命名固定** |
| `calendar/` | `YYYY-MM.md`（按月，固定不可改） | **时间类：命名固定** |
| `memory/` | `YYYY-MM.md`（按月，固定不可改） | **时间类：命名固定** |
| `consult/` | `YYYY-MM.md`（按月，固定不可改） | **时间类：命名固定** |

> **命名契约总原则**：
> - **用户完全自定义**（inside/rule/skill）：文件名、子目录、内容全自由,代码零假设。
> - **时间类固定**（star/task/calendar/memory/consult）：统一按月命名 `YYYY-MM.md`,
>   **扁平化存储**（直接放在对应目录下,不建子层）;代码校验文件名符合契约才使用,不符忽略。
> - **memory 按月**：`YYYY-MM.md`（由 AI 自动按月份生成落盘,非用户手写,故固定格式）。
> - 一级子目录永远固定（与 App 功能绑定）；代码按一级目录读取,不硬编码子文件名。

**设计动机**：

| 契约 | 原因 |
|------|------|
| inside/rule/skill **用户完全自定义** | 这些是用户的核心数据（档案/规则/技能），组织方式因人而异，且会持续演变。代码不假设，用户自由组织，零代码改动。 |
| 时间类 **统一 YYYY-MM.md** | 时间相关的记录。按月命名让代码可按"目录+月份"直接定位文件，文件数量可控，且天然按时间归档。 |
| memory **YYYY-MM.md** | memory 由 AI 自动按月份生成落盘，非用户手写——AI 侧固定格式，保证生成与读取一致。 |
| **代码校验文件名** | 时间类是明文契约，代码校验文件名符合 `YYYY-MM.md` 才使用，不符忽略——防止用户误放的文件被当作时间类数据解析。 |

> 一句话：**用户自己的东西（inside/rule/skill）不设限（支持递归子目录）；时间相关的记录（star/task/calendar/memory/consult）按月约定，AI 生成的 memory 固定格式。**

**读取规则**：
- 代码通过 `matcher.ReadDir("rule")` 等按**一级目录**读取该目录下**全部** `.md` 文件并合并
- 新增/改名/删除子目录内文件,即时生效,无需改代码或重启
- 一级目录名是唯一的"固定契约",与 App 功能绑定

**内置种子**（`daemon/internal/service/importer/userdata/`）：首次启动自动导入运行目录与 SQLite；matcher 播种 .md 到运行目录，已有文件不覆盖（保留用户编辑）。

### 3.3 备份恢复

- 手动导出/导入（`/api/backup/export` `import`），v3 zip 格式：`userdata/` 全量文件（原样、含相对路径）+ `userdata/manifest.json`（version/timestamp/sha256 + DB 数据：memories / sessions（含 messages）/ tasks / feedback + 前端回传的 star_history / settings）。
- **严格校验（导入）**：文件名必须为 `openyou-backup-yyyymmdd-hhmmss.zip`（前端先验）；zip 结构必须与导出完全一致——根下仅 `userdata/`，其下仅允许 inside / rule / skill / star / task / calendar / memory / consult 概念目录 + `manifest.json`，且 inside / rule / skill 必须存在。任何不合规的归档整体拒绝（400），不写入任何内容。
- **导出与导入对称**：导出同样只打包概念目录内的文件（散落的其它文件跳过并计入 warnings），且恒写 inside/rule/skill 三个目录条目——即使某核心目录被清空，自家备份也不会因结构校验被拒。
- **只增不删（additive import）**：导入绝不删除、绝不覆盖本地已有数据——本地已存在的文件原样保留（用户编辑优先），仅补入备份里有而本地缺失的文件；DB 行按 ID 去重插入（memories/feedback 冲突即跳过），已有行不动；manifest 中缺席的 DB 段（如 PC 端 `make userdata` 产出的 zip 无 DB 段）完全不触碰对应表。空目标导入后再导出与原备份一致（可往返），已有目标导入则为并集合并、零丢失。feedback 保留导出时的行 id；记忆恢复不重写 `userdata/memory/*.md` 月报（文件随归档恢复，本地手写内容优先）。
- **导入记忆衰减宽限**：衰减 worker 首次见到一条记忆时，参考时间最多回溯 7 天——旧备份恢复的记忆不会因整段搁置时长在一个 tick 内被衰减删除。
- **内容校验和**：导出在 manifest 写入全部文件的 `sha256`（按 rel 排序、`rel + \x00 + 内容` 流式求和）；导入时重算比对，传输损坏/篡改整体拒绝（400）。`make userdata` 以相同算法生成校验和。
- **大备份传输（手机端）**：导出/导入的字节流不经过 JS/JNI——导出由 Kotlin 直接请求 daemon 并流式写入 MediaStore（失败即删除孤儿行、返回真实文件名）；导入由 JS 按 ~3MB 分块交给 Kotlin 落临时文件后流式 POST。桌面端保留 fetch 回退路径。
- PC 端同等能力：`make userdata` 在项目根生成同款 `openyou-backup-<yyyymmdd-hhmmss>.zip`。
- 前端导入前弹出确认（说明只增不删）。响应按类别返回明细（files/sessions/memories/tasks/feedback restored）供前端 toast 展示；manifest 回写的 settings 仅接受内置键白名单。部分失败返回 207 + `ok:"false"` + `failures[]`，前端必须按字符串 `"true"/"false"` 判断。
- 仅支持 v3 zip；旧版 v2 单 JSON 备份已不再可导入。
- API Key 不包含在备份中（且 daemon 内置固化，不可经 API 覆盖）。

### 3.4 任务数据

Task 存结构化 `tasks` 表(SQLite),每个任务:

| 字段 | 含义 | 例子 |
|------|------|------|
| id | 唯一标识 | tsk_1a2b3c4d |
| title | 任务标题 | "确认张建国对 Q3 方案的态度" |
| description | 简要行动项描述(AI 总结或用户编辑) | "周五前 1v1 确认他的顾虑" |
| start_date | 开始日期(默认今天) | "2026-08-01" |
| end_date | 截止日期(空 = 同一天) | "2026-08-03" |
| done | 是否完成 | false |
| priority | 优先级:high / normal / low | normal |
| source | 来源:user / conversation | conversation（来自 Star 建议采纳） |
| session_id | 来源会话(conversation 时有) | 会话 ID |

**排序规则**(查询时):未完成优先 → 截止日期近的优先 → 无截止的排后 →
最新创建的优先。让"该做的"永远在顶上。

### 3.5 任务生命

```
Create (Task page "+" / Star suggestion accepted)
  → pending: injected into conversation ("you still have these to do")
  → before due: reminder
  → done (done=true): gone from injection
  → deleted: fully removed
```

**在对话中的注入**：每次对话，系统读取未完成 Tasks 注入上下文——
AI 知道你"手上还有什么事",建议会围绕你的真实待办展开,而不是空泛建议。

**用户管理**（Time 页 `/api/tasks`）：

| 操作 | 做什么 | API |
|------|--------|-----|
| 创建 | 输入标题 + 描述 + 截止 + 优先级 | `POST /api/tasks` |
| 查看 | 按截止排序,未完成置顶 | `GET /api/tasks` |
| 完成 | 标记 done | `PUT /api/tasks/{id}` |
| 编辑 | 改标题/描述/日期/优先级 | `PUT /api/tasks/{id}` |
| 删除 | 移除 | `DELETE /api/tasks/{id}` |

### 3.6 三页闭环

```
[Star] AI one-tap comprehensive report (original radar probe + mirror blind spots)
   │  suggestion accepted
   ▼
[Task] your active commitment, with a deadline
   │
   ▼ （completion record）
[Calendar] calendar view of Tasks, frontend groups by end_date
```

- **Star 的输入**：未完成 Tasks + Calendar 日期分布 + Inside/Rule 档案
- **Star 的输出**：①行动建议 ②关注项（该关注的/待办的）③盲区洞察（重复的模式）
- **Star 的闭环**：你接受建议 → 一键加到 Task → 下次 Star 会基于新 Task 给建议

**这就是 Time 页的价值**：Star 让你"看全局"，Task 让你"做承诺"，Calendar 让你"看时间"。三者构成一个"洞察 → 承诺 → 日程"的闭环。

---

## 4 智能引擎

### 4.1 智囊团

**他不是一个人在想。他是多个专家在同时推演。** 每个技能 = 智囊团的一个成员。

```go
type ThinkTank struct {
    llm     LLMClient       // Chat + StreamChat (SSE)
    skills  SkillProvider   // Match(query) []SkillInfo
    memory  MemoryProvider  // Relevant(query, limit) — optional, recall relevant memories
    docs    DocProvider     // ReadDir/ReadDoc/ListDocs/RelevantDocs — on-demand retrieval doc source
}

func New(llm LLMClient, skills SkillProvider, mem MemoryProvider, docs DocProvider) *ThinkTank

func (bt *ThinkTank) Analyze(ctx context.Context, query string, _ string) (<-chan AnalysisEvent, error)
```

1. **自动匹配技能**：关键词/内容触发 → 激活相关技能
2. **并行分析**：≤5 个 sub-agent 并行推演，每个 60s 超时，ctx 取消立即退出
3. **交叉验证**：不同技能发现冲突时标注
4. **合成建议**：主 agent 流式聚合、去重、排序，标注来源

每个 sub-agent 独立注入上下文：命中技能的完整框架（懒加载，inside 文档命中时不再重复注入整篇，去重）+ Rule 全量 + inside 相关章节（`RelevantDocs` 按需检索，预算内）+ 相关记忆。deps 已废弃（技能不再声明依赖文件，见 VISION §2.7）。

**按需检索全空回退**：若某 inside 文档被 Match 选中为技能，但 `RelevantDocs` 对该 query 未命中任何内容（如 query 以 2 字母英文缩写为主），子代理回退注入该档案全文（clip 12000 runes 兜底）——保证"被匹配的档案"不缺席、prompt 不空洞。命中时保持去重，不重复注入全文。

**触发规则**：
- 激活 ≥ 2 个技能 → 智囊团模式（并行 sub-agent）
- 激活 1 个技能 → 单技能深入分析
- 激活 0 个技能 → 简单分析

### 4.2 冲突合成

不同技能给出矛盾建议时，主 agent 标注冲突点、列出各方依据、让用户选择方向。

合成由主 agent 单次调用 `StreamChat` 流式输出 delta，标注来源 `source-citation`。

- `/api/skills/curate`：技能库整理建议（dry-run，不自动改）
- Star 综合报告：一键读取任务/规则/档案/记忆，流式输出 ①行动建议 ②关注项 ③盲区洞察（`POST /api/star` SSE）

### 4.3 数据导入

```text
daemon/internal/service/importer/userdata/   (//go:embed userdata)
├── inside/
│   ├── people/jd.md          → userdata/inside/ (plain files, read by matcher)
│   ├── events/               → userdata/inside/ (plain files)
│   ├── workplace/            → userdata/inside/ (plain files, referenced by skill docs)
│   └── resume/               → userdata/inside/ (plain files)
├── rules/rules.md            → userdata/rule/ (plain files, read by matcher)
├── skills/agent-install.md   → reserved doc
└── tasks/                    → reference docs
```

导入策略：

- 首次启动 `//go:embed userdata` → 解析 Markdown → 写入 SQLite / 播种到运行目录
- inside / rule / skill 为**纯文件**,matcher 按一级目录读取全部 `.md` 文件
- matcher 将整个 userdata 树的 `.md` 文件播种到运行目录 `<dataDir>/userdata/`，已有文件不覆盖（保留用户编辑）

### 4.4 记忆提取

每次对话后扫描提取关键信息，每 3 次对话提取一次（成本控制）。

| 学到什么 | 自动保存阈值 |
| --- | --- |
| 人物更新 | ≥ 0.80 |
| 你的偏好 | ≥ 0.85 |
| 新规则 | ≥ 0.75 |
| 当前上下文 | ≥ 0.80 |

低于阈值 → 待确认；确认后生效。提取有语义去重（极性感知）。

AI 提取的记忆分四类，每类有自己的置信度门槛:

| 类型 | 含义 | 例子 | 自动保存阈值 |
|------|------|------|-------------|
| `person_fact` | 关于某人的新信息 | "张建国最近开始关注成本控制" | ≥ 0.80 |
| `user_trait` | 你的偏好/习惯/模式 | "你倾向于先收集数据再决策" | ≥ 0.85 |
| `rule` | 从经验发现的行为规律 | "周五下午发的消息回复率低" | ≥ 0.75 |
| `context` | 当前情境/进行中的事 | "正在准备 Q3 技术评审" | ≥ 0.80 |

**置信度处理逻辑**:
```
confidence ≥ type threshold  → auto-save (Suggested=false), takes effect immediately
0.60 ≤ confidence < threshold → mark pending confirmation (Suggested=true), await approval
confidence < 0.60      → discard (not worth remembering)
```

### 4.5 记忆生命

对话时,系统从记忆中检索与当前问题**语义相关**的记忆,最多 5 条注入上下文:

```
User asks a question
  → [Memory layer] memory.Relevant(query, 5) retrieves related memories
  → inject: related memories (max 5)
  → AI answers combining Inside (person profiles) + Rule (rules) + Memories
```

这就是"他记得,你不用复述"(VISION 核心原则)——你提一个词,他自动关联之前学到的相关内容。

```
Extract → classify (confidence)
  ├─ high confidence → take effect automatically → used in retrieval
  ├─ medium confidence → pending confirmation → you confirm → take effect
  └─ low confidence → discard
              ↓
       natural decay (every 24h, exponential decay of unused memories)
              ↓
     confirmed memories never auto-deleted (floor 0.5)
     unconfirmed memories decaying below 0.1 are deleted
```

| 状态 | 含义 | 生命周期 |
|------|------|---------|
| Suggested(待确认) | AI 不确定,等你确认 | 衰减到 <0.1 删除 |
| Confirmed(已确认) | 你认可或 AI 高置信 | 不自动删除,长期保留 |
| Disputed(不准确) | 你标记为错误 | 置信度减半,加速衰减 |

**记忆管理**（Consult 页 → Memory 子 tab `/api/memory`）：

| 操作 | 做什么 | API |
|------|--------|-----|
| 查看 | 按类型/人物/置信度过滤,待确认项置顶 | `GET /api/memory` |
| 确认 | 认可 AI 提取的记忆,让它生效 | `PUT /api/memory/{id}/confirm` |
| 编辑 | AI 生成的内容不对 → 直接改内容 | `PUT /api/memory/{id}` |
| 标记不准确 | 这条记忆是错的 | `PUT /api/memory/{id}/dispute` |
| 删除 | 不再需要 | `DELETE /api/memory/{id}` |

**用户编辑的意义**:AI 提取的 memory 可能有偏差——比如把"最近在准备面试"误记为"已离职"。你直接编辑修正,AI 下次就基于正确记忆分析。这就是"AI 自主生成 + 用户可编辑纠错"的双轨。

**落盘格式**（`userdata/memory/`）：

记忆同时存 SQLite(memories 表,运行时查询)和 Markdown 文档(`userdata/memory/*.md`,可读可编辑):

```markdown
# Memory: <title>

- Type: person_fact | user_trait | rule | context
- Person: (optional)
- Status: confirmed | suggested | disputed
- Source: session <session_id> / 2026-08-01 conversation
- Extracted: 2026-08-01 22:00
```

### 4.6 追问纠正

**主动追问**：给出建议后主动追问结果。系统检测 3 天前有建议但无反馈时，Home 页 Needs You 卡片显示该条目（`computePendingFeedback`），点击直接跳转到对应 session。

**用户纠正**：
- 每条建议旁有反馈按钮（👍 有用 / 👎 没用）
- 反馈 API 支持 `useful` / `not_useful` / `inaccurate` 三种 rating：`useful` 增加确认；`inaccurate` + 纠正文本 → 追加为新编号规则（去重）；`not_useful` + 纠正文本 → 校验规则引用

---

## 5 接口设计

> 以下路由列表与 router.go 对齐（2026-08-13），共 38 条（含 WS）。

### 5.1 系统对话

```bash
GET  /                            → Web UI (embedded SPA)
GET  /api/health                  → health check
GET  /api/config                  → current settings (api_key field-encrypted)
PUT  /api/config                  → update settings (api_key/base_url/model/privacy_mode etc.; api_key encrypted at rest, empty clears it — falls back to userdata/openyou.json)

POST /api/chat               → SSE: context-loaded | session | text-delta | tool-call | tool-result | error | [DONE]
GET  /api/sessions           → list sessions
GET  /api/sessions/{id}      → session detail (with message history)
DELETE /api/sessions/{id}    → delete session
```

**SSE 事件类型 (POST /api/chat):**

| Event | Payload | 说明 |
| --- | --- | --- |
| `context-loaded` | `{"type":"context-loaded","people_count":N,"rules_count":N,"memory_count":N,"people_names":[...]}` | 系统 prompt 构建完成，附带上文统计 |
| `session` | `{"session_id":"..."}` | 会话 ID |
| `text-delta` | `{"text":"..."}` | LLM 文本增量 |
| `tool-call` | `{"tool":"...","args":{...}}` | 工具调用 |
| `tool-result` | `{"tool":"...","output":"..."}` | 工具返回 |
| `error` | `{"error":"..."}` | 错误 |
| `[DONE]` | (纯文本) | 流结束 |

**LLM 配置来源（优先级从高到低）：**

1. App 设置页填写（`PUT /api/config` 写入，SQLite 字段级加密持久化）
2. `userdata/openyou.json` 或 `userdata/openyou.jsonc`（daemon 启动时读取，仅在对应配置项为空时生效，支持 `//` 注释；参考模板 `userdata.example/openyou.example.jsonc`）
3. 内置默认：`base_url=https://api.deepseek.com/v1`、`model=deepseek-v4-pro`、`api_key` 为空

api_key 的读写安全：`GET /api/config` 只返回掩码（末 4 位）；`PUT /api/config` 写入空字符串即清除（下次启动回落到 openyou.json）；api_key 不在任何日志、备份、localStorage 中明文出现。

### 5.2 分析接口

```bash
POST /api/analyze → SSE:
    {"type":"thinktank-start","skills":[{"name":"...","description":"..."}]}
    {"type":"skill-progress","skill":"...","status":"analyzing|complete|failed"}
    {"type":"skill-result","skill":"...","result":"..."}
    {"type":"conflict","skill_a":"...","skill_b":"...","description":"..."}
    {"type":"synthesis-delta","text":"..."}
    {"type":"source-citation","source":"...","source_type":"profile|rule|skill|external"}
    {"type":"done"}
```

### 5.3 文档技能

```bash
GET  /api/docs                   → list docs (userdata/ tree)
GET  /api/docs/{path...}         → read doc
PUT  /api/docs/{path...}         → write doc (incremental RefreshDoc O(1) + prefix-cache invalidation + pseudonymizer reseed)
DELETE /api/docs/{path...}       → delete doc

GET  /api/skills              → list skills
POST /api/skills              → create skill (incremental refresh)
POST /api/skills/reload       → full reload from disk
GET  /api/skills/{name}       → skill detail
PUT  /api/skills/{name}       → update skill (incremental refresh)
DELETE /api/skills/{name}     → delete skill (incremental refresh)
POST /api/skills/curate       → curation suggestions (dry-run)
```

### 5.4 记忆任务

```bash
GET  /api/memory               → list memories
PUT  /api/memory/{id}          → update memory
PUT  /api/memory/{id}/confirm  → confirm memory
PUT  /api/memory/{id}/dispute  → mark as inaccurate
DELETE /api/memory/{id}        → delete memory

GET  /api/tasks              → list tasks
POST /api/tasks              → create task
PUT  /api/tasks/{id}         → update task
DELETE /api/tasks/{id}       → delete task

POST /api/star → SSE: one-tap comprehensive report (reads Tasks + Inside/Rule + Memory, streams ① action suggestions ② watch items ③ blind-spot insights)

| Event | Payload | Description |
| --- | --- | --- |
| `text-delta` | `{"text":"..."}` | streaming text delta |
| `done` | `{"type":"done"}` | text complete |
| `suggest-task` | `{"title":"...","description":"...","end_date":"...","priority":"..."}` | task suggestion (sent after `[DONE]`) |
| `error` | `{"error":"..."}` | error |
| `[DONE]` | (plain text) | stream end marker |
```

### 5.5 反馈备份

```bash
GET  /api/home                → dashboard (people/rules/skills/memories/tasks/sessions stats + recent_sessions + pending_feedback + memory_pending + memory_count)
POST /api/feedback            → save feedback
GET  /api/feedback/pending    → pending feedback list

GET  /api/backup/export        → export backup (v3 zip: userdata/ verbatim + manifest.json)
POST /api/backup/import        → import backup (strict validation + mirror restore)
GET  /api/notifications/poll   → notification long-poll
WS   /ws                       → bidirectional push (star-check, context-health, memory-confirm)
```

---

## 6 安全质量

### 6.1 三层防御

```text
┌─ Layer 3: App Lock (biometric + pattern)───────────────────┐
│Unlock before entering; daemon keeps running while locked   │
├─ Layer 2: Android Keystore (hardware key management)───────┤
│API key and DB encryption keys live in TEE/StrongBox        │
├─ Layer 1: SQLite AES-256-GCM (app-level field encryption)──┤
│api_key encrypted (enc:v1: version prefix, plaintext        │
│migration compatible)                                       │
└────────────────────────────────────────────────────────────┘
```

三层防御已落地：`FLAG_SECURE` 阻止截屏；api_key 字段级 AES-256-GCM 加密（`infra/crypto` + `infra/config`）；Keystore 密钥通过文件交接（`KeystoreManager.kt`）；App 锁指纹+九点图案（`LockManager.kt` + `LockActivity.kt`）。inside、memory、session 的字段加密为后续工作。

**App 锁**：
- 9 点图案 + 指纹/面容（BiometricPrompt + PatternLockView）
- 触发：冷启动、后台超时切回、息屏、手动锁定
- SHA-256 加盐图案哈希 + 5 次失败锁定 + 常数时间比较
- 锁屏期间 daemon 继续运行

**数据加密**：
- api_key 字段级 AES-256-GCM 加密存储（`enc:v1:` 版本前缀兼容明文迁移）
- Keystore 生成 Master Key → 文件交接 `<filesDir>/openyou/master.key` → daemon `loadOrCreateKey()` 读取
- `FLAG_SECURE` 阻止截屏/录屏/投屏
- inside / memory / session 字段加密为后续工作

### 6.2 隐私模式

| 模式 | 行为 |
| --- | --- |
| 平衡（默认） | 人物名替换为代号，规则/事件保留原文 |
| 信任 | 全透明发送 |

实现：`Pseudonymizer` 化名 + `MaskingClient` 包裹 LLM 出口（chat / 智囊团 / 记忆提取单点），由 config `privacy_mode` 控制。

### 6.3 性能策略

已实现：

- **稳定前缀缓存**：Rule + Inside 摘要命中 DeepSeek prefix cache；文档写入后 `InvalidateStablePrefix()` 强制重建
- **上下文压缩**：超阈值自动摘要最旧轮次，token 有界（`compaction.go`）
- **执行保护**：doom-loop 检测（3 次重复调用中止）+ 迭代预算（`infra/tool`）
- **流式重试**：429/5xx 指数退避 + 错误分类（`ai/llm/retry.go`）
- **LLM 并发上限**：`LLMSem` 信号量，全局并发 LLM 请求 ≤3
- **记忆去重**：语义去重 + 极性感知（`saveDeduped`）

后续方向：完整 ReAct tool-call 循环（当前 tool registry 为 stub）。

### 6.4 测试验证

**`go build/vet/test ./...` 全部通过**（2026-08-03）。

| 包 | 测试数 | 说明 |
| --- | --- | --- |
| server | 91 | handler 单元 + E2E 集成 + 压缩/引用/scrubber |
| memory | 53 | 提取/分类/衰减/去重/存储/队列 |
| llm | 41 | SSE 客户端 + 退避重试 + Masking |
| matcher | 29 | 匹配/内容/触发词/文档 |
| tool | 22 | doom-loop + 迭代预算 + registry |
| background | 24 | 三个 ticker + 健康/Star/衰减 |
| thinktank | 21 | 智囊团编排/冲突/合成 |
| consult | 16 | 会话+消息+反馈持久化 |
| privacy | 19 | 化名唯一性/子串/round-trip |
| bridge | 18 | WebSocket hub |
| config | 14 | 键值 + 字段加密 |
| crypto | 13 | AES-256-GCM round-trip/nonce/错误密钥 |
| importer | 2 | 首启导入幂等 |
| notify | 4 | 长轮询/唤醒/丢弃 |
| db | 3 | 迁移工具 |
| task | 1 | store 层 CRUD |
| agent | 0 | 纯类型定义包 |

**E2E 集成测试（走真实 router + 假 LLM）**：连续对话+隔离、context-loaded、scrubber、citations、备份 round-trip+幂等、config 加密、记忆 dispute、通知轮询 — 全部 PASS。

**竞态检测**：`go test -race ./...` 未发现 DATA RACE。

**已知未自动化**：Android 原生（App 锁 / Keystore / 保活 / 开机自启）需设备手动验证；真实 LLM 端到端需 API Key。

---

> 受 [产品定位 (VISION.md)](./VISION.md) 约束。功能与定位冲突时，以定位为准。
