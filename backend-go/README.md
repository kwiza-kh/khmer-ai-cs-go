# backend-go — Go 重写版（进行中）

Rust 后端的 Go 重写，目标是：编译秒级、部署简单、行为 1:1 对等（含全部近期修复）。

## 状态

- **阶段 1 ✅ 已完成**：配置/校验、Postgres 连接池、迁移运行器（嵌入全部迁移，截至 2026-09-29 为 65 个 SQL；本目录 `../migrations/` 只是供 psql 手查的镜像，真正生效的是 `internal/migrations/migrations/`，两边由测试逐字节比对）、
  JWT(HS256)+bcrypt(cost 12)+TOTP、HTTP 中间件链（request-id / CORS / 日志 / 认证 /
  admin 守卫 / Redis 限流）、auth 端点（登录锁定/2FA/注册开关/改密/偏好设置）、
  health/ready、优雅停机。
- 阶段 2-5 待移植：知识库+RAG、平台管线（语音/头像）、chat/SSE/admin/inbox 等。

## 体验/业务增强（本轮新增）

面向终端客户与坐席体验补齐了一批此前缺失或只做了一半的能力：

- **真流式 chat**：`/chat`、`/chat/stream` 现在落库（sessions + chat_messages +
  sources_json + tokens），SSE 逐 token 下发（`streamGenerateContent?alt=SSE`），
  与前端 `streamChat` 协议对齐；网页/AI 测试会话具备多轮记忆。
- **token 计费链路**：`internal/usage` 在每次模型调用后写 `token_usage`，
  驱动此前恒为 0 的成本/缓存命中 KPI。
- **分析端点补齐**：`analytics/overview`(全量)、`analytics/timeline`、
  `analytics/top-queries`、`analytics/languages`、`admin/tokens/stats`、
  `admin/feedback`、`admin/agent-performance`、`admin/intent-analytics`、
  `admin/integrations/status`、`reports/{kind}`(CSV)、
  `inbox/sessions/{id}/whatsapp-templates`。
- **网站聊天 Widget（全新公开渠道）**：`widget_tokens` 迁移 + 公开端点
  (`/widget/config|messages|chat|feedback`)，token 鉴权、可选来源域名、
  与 SSE 客服流；前端 `widget-embed.js` 悬浮气泡 + `/widget` iframe 页 +
  `/widget-admin` 管理页；`platform_type` 增 `web`。
- **自动转人工引擎**：关键词（客户主动要人工）+ 后台情绪/意图分类（负面情绪、
  分类判定需人工、知识库零命中）三触发，写 `human_handoff_requests`（含
  `no_knowledge_base` trigger）并翻转会话状态。
- **客户侧 CSAT**：Telegram AI 回复附 👍/👎 内联按钮（webhook callback_query 落库，
  👎 自动转人工）；Widget 气泡下方同样提供点赞点踩。
- **体验细节**：打字指示器（Telegram sendChatAction / Messenger sender_action /
  LINE typing）、图片问答（Gemini vision 转述入上下文）、Khmer TTS 语音回复
  （`TTS_ENABLED` + R2 合成 audio/wav 投递）。
- **修复类**：2FA 登录信令（`action=2fa_required`，前端弹验证码输入）、
  设置页生成 TOTP 二维码、inbox 媒体改返回 R2 presigned GET URL、
  handoff request_id 按 UUID 处理、`/inbox` 与标签/意图回显、
  通知写入（转人工 + SLA 违约）、CORS 为 `/widget/*` 放行任意来源。

## 构建与测试

```
go build ./...
go test ./...
```

`cmd/server` 读同一份 `.env`（与 Rust 版完全相同的变量）；`cmd/migrate` 支持
`--status / --mark-applied / --dsn`，行为与 Rust `migrate` 二进制一致。

## 部署切换计划（阶段 5）

Go 二进制监听独立端口（如 8081），与 Rust 版并行；验证对等后把
`/etc/nginx/sites-enabled/khmer-ai-cs` 的 `proxy_pass` 切到 8081，随时可回滚。
