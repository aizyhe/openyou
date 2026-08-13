## 变更描述

请说明这个改动是什么、为什么做：

- [ ] 修复（bug fix）
- [ ] 新功能（feature）
- [ ] 重构 / 文档（refactor / docs）
- [ ] 其他

**改动内容**

**动机 / 背景**

## 关联

- Fixes #（issue 编号，如有）
- 相关讨论：

## 验证

请说明如何验证你的改动：

- [ ] `cd daemon && go test ./...` 通过
- [ ] `python3 web/build.py` 产物与源码一致
- [ ] 本地 daemon 启动 + `curl /api/health` 正常
- [ ] E2E / APK 测试（`scripts/test_e2e.sh` 等）

**测试结果**

## 检查清单

- [ ] 未引入硬编码密钥（LLM 配置走 `userdata/openyou.json` / 设置页）
- [ ] 未修改/提交 `userdata/`（私有 Context）
- [ ] 只改 `web/` 源码，未手改 `index.html` 产物
- [ ] 遵循 [AGENTS.md](../AGENTS.md) 的设计决策与代码约定
- [ ] 已做构建前 code review（CSS/JS/HTML 一致性、回归风险）
