# 贡献指南

欢迎贡献！请先阅读 [README.md](README.md) 与核心文档：

- [docs/VISION.md](docs/VISION.md) — 宪法：所有功能受其约束，**功能和定位冲突时，以定位为准**。
- [docs/SPEC.md](docs/SPEC.md) — 功能规格与技术实现。

## 开发环境

- Go 1.22+、Android NDK 27.0+、Gradle + Android SDK
- Python 3（用于 `web/build.py` 前端构建）
- 设备：Android SDK 31（HarmonyOS/Android 12）或本机 Linux 直接跑 daemon

## 常用命令

```bash
# Run the daemon locally (no Android required)
cd daemon && go build -o daemon ./cmd/daemon/ && ./daemon &

# Run all Go tests
cd daemon && go test ./...

# Merge web/ sources → index.html artifact (edit web/ sources only, never the artifact)
python3 web/build.py

# Build the APK
bash scripts/build.sh

# E2E / APK tests
bash scripts/test_e2e.sh
bash scripts/test_apk.sh
```

## 提交流程

1. Fork 本仓库并创建特性分支。
2. 改动前先看 [AGENTS.md](AGENTS.md) 里的设计决策与坑点，保持一致。
3. 改动尽量附带测试：
   - Go：对应包内 `*_test.go`；
   - 前端：`web/` 源码改动后运行 `python3 web/build.py` 并确认产物一致。
4. 构建前做完整 code review（见 AGENTS.md 的铁律）。
5. 提交信息简洁、描述清楚（参考现有历史风格）。
6. 发起 Pull Request，说明改了什么、为什么、如何验证。

## 规则

- **不提交 `userdata/`**：那是用户的私有 Context，已被 `.gitignore` 排除。仓库只应包含 `userdata.example/` 骨架。
- **不引入硬编码密钥**：LLM 配置走 `userdata/openyou.json` / 设置页，不要写死在代码里。
- **不直接编辑产物**：`daemon/internal/server/web/index.html` 由 `web/build.py` 生成，改 `web/` 源码。
- **CSS 内联**：HarmonyOS WebView 不支持外部 `<link>`，CSS 写在 `web/css/style.css`。
- **Logo 单一来源**：`web/logo.svg` 是唯一 logo，勿手改生成物。
