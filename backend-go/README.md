# backend-go — Go 重写版（进行中）

Rust 后端的 Go 重写，目标是：编译秒级、部署简单、行为 1:1 对等（含全部近期修复）。

## 状态

- **阶段 1 ✅ 已完成**：配置/校验、Postgres 连接池、迁移运行器（嵌入全部 34 个 SQL）、
  JWT(HS256)+bcrypt(cost 12)+TOTP、HTTP 中间件链（request-id / CORS / 日志 / 认证 /
  admin 守卫 / Redis 限流）、auth 端点（登录锁定/2FA/注册开关/改密/偏好设置）、
  health/ready、优雅停机。
- 阶段 2-5 待移植：知识库+RAG、平台管线（语音/头像）、chat/SSE/admin/inbox 等。

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
