# 安全策略

## 支持的版本

安全补丁只针对最新发布版本。请保持更新到最新 release。

## 报告漏洞

发现安全问题时，**请不要公开提交 issue**。请通过以下方式私密报告：

- 创建 GitHub **Security Advisory**（仓库页面 → Security → Report a vulnerability），或
- 直接联系项目维护者（见 Git 提交记录或仓库主页联系方式）

请在报告中包含：

- 漏洞类型与影响范围
- 复现步骤（或最小 PoC）
- 受影响版本
- 建议的修复方案（可选）

我们会在收到报告后尽快回复，并在修复验证通过后统一公开披露。

## 本项目关注的资产

本项目为单用户本地应用，daemon 仅监听 `127.0.0.1`，但仍请留意：

| 资产 | 风险 |
| --- | --- |
| `userdata/` 私有 Context | 个人信息（简历/人物/职场）——务必保持 `.gitignore` 排除，不要提交 |
| API key | `userdata/openyou.json` 或设置页中的 LLM 密钥——字段级 AES-256-GCM 加密存储，`GET /api/config` 只返回掩码 |
| SQLite 数据库 | App 私有目录 + `master.key` 加密，未来接入 Android Keystore |

## 已知缓解

- daemon 绑定 `127.0.0.1`，不对局域网/公网暴露
- 变更类 API 要求 `Content-Type: application/json`（CSRF 防护）
- 人名脱敏（Pseudonymizer）默认开启，`privacy_mode=trust` 时关闭
- `FLAG_SECURE` 阻止截屏；App 锁（指纹 + 九点图案）保护冷启动/后台切回
