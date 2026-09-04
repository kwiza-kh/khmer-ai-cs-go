---
name: deploy-khmer-ai-cs
description: 将「Khmer AI 客服系统 (khmer-ai-cs-go, Go 后端 + Next.js 前端)」部署/更新到远程 Linux 服务器, 经 Cloudflare 子域名 cs.wanfanginsulationmaterial.com 提供 HTTPS。当用户提到 部署 khmer-ai-cs / 高棉语 AI 客服 / server-go / migrate-go / cs.wanfanginsulationmaterial.com / 连接 38.55.192.90 / 更新线上版本 / 跑迁移 / nginx 反代 / systemd 服务 / Cloudflare 521 / HTTPS 异常 时触发。
---

# Khmer AI 客服系统 (khmer-ai-cs-go) 部署与迭代

将「Khmer AI 客服系统」Go 后端 + Next.js 前端部署/更新到远程 Linux 服务器 (经 Cloudflare 子域名 HTTPS), 覆盖首次全量部署与日常迭代发布 (后端二进制、前端 standalone、数据库迁移、回滚)。

## 何时触发

- 部署/更新 khmer-ai-cs 客服系统到服务器、上线、发布新版本
- 提到 38.55.192.90 或 cs.wanfanginsulationmaterial.com
- 跑迁移 / migrate-go / schema_migrations / pgvector
- nginx / systemd / Cloudflare / 521 / HTTPS 异常排查
- 前端 standalone 发布、Telegram/Meta webhook 域名配置

## 架构与环境（先读这个）

```
浏览器 ──HTTPS──> Cloudflare (橙色云朵, zone=wanfanginsulationmaterial.com)
                      │ HTTPS:443 (源站自签证书, SSL 模式必须 Full)
                      ▼
              nginx (cs.wanfanginsulationmaterial.com)
                ├── /api/                    ──> 127.0.0.1:8081  server-go (Go 后端, SSE 关 buffering)
                ├── /api/v1/realtime/inbox   ──> 127.0.0.1:8081  WebSocket 升级 (3600s 超时)
                └── /                        ──> 127.0.0.1:3001  next-server (standalone)
                                                    Postgres 17 (127.0.0.1:5432, db=khmer_ai_cs) + Redis (127.0.0.1:6379)
```

| 项 | 值 |
|---|---|
| 服务器 | 38.55.192.90 (Debian 13 trixie, x86_64) — **与 WMS 系统同机共存, 勿动 wms.service** |
| SSH | root + 密码 (从环境变量 `KHMER_SSH_PASSWORD` 读取, **勿把明文写进文件/仓库**; 密码末尾两个点) |
| 域名 | **cs**.wanfanginsulationmaterial.com (Cloudflare A 记录 → 38.55.192.90, 橙色云朵代理) |
| 后端 | Go (go.mod 声明 go 1.26; 线上二进制 go1.26.5) `server-go` → :8081, 二进制内 `/health` `/ready`; **服务器上没装 Go, 二进制在构建机交叉编译后上传** |
| 前端 | Next.js 16 standalone (`node server.js`) → 127.0.0.1:3001; `API_BASE` 在**构建时**烘焙 (见下) |
| 数据库 | 系统级 Postgres 17 (apt, 非 Docker), 扩展 **pgvector 0.8.0 + pg_trgm**, 迁移表 `schema_migrations` (001~034) |
| 缓存 | 系统级 Redis (apt, requirepass), 会话/缓存/Gemini 结果缓存 |
| 运行用户 | `khmerai` (系统用户), 应用目录 `/opt/khmer-ai-cs` |
| systemd | `khmer-ai-cs-go.service` (现行 Go 后端, env=`.env-go`) / `khmer-ai-cs-web.service` (前端) / `khmer-ai-cs.service` (**旧后端, 已 disable, 留作回滚**) |
| HTTPS | Cloudflare 托管证书 (浏览器侧) + 源站自签 `/etc/nginx/ssl/wms.crt` (与 WMS 共用); zone SSL 模式必须 **Full** |

## 与 WMS 部署的关键差异（不要照搬经验）

| 点 | WMS | 本项目 |
|---|---|---|
| 编译位置 | 服务器上装 Rust 编译 | **本机/构建机交叉编译 Go** (`CGO_ENABLED=0 GOOS=linux GOARCH=amd64`), 服务器无 Go |
| 前端 | Vite SPA dist, 同源相对路径 | **Next standalone**, `NEXT_PUBLIC_API_URL` 构建时烘焙, 忘设 → 线上打 localhost 挂 |
| 数据库 | SQLite 单文件 | Postgres 17 + pgvector + Redis (系统服务, 独立于部署目录) |
| SSL 模式 | Flexible 或 Full 均可 | **必须 Full** (源站 :80 会 301 到 https, Flexible 会形成重定向环) |
| 部署目录 | /opt/wms | /opt/khmer-ai-cs |

## 部署工作流（迭代发布）

1. **构建后端** (本地, 需 go ≥1.26, `brew install go`): `backend-go/` 下交叉编译 `server-go` 与 `migrate-go` (命令见 references/deploy-commands.md §1)
2. **构建前端**: `NEXT_PUBLIC_API_URL=https://cs.wanfanginsulationmaterial.com/api/v1 npm run build`, 组装 standalone + `.next/static` + `public` 打 tar (§2)
3. **上传**: scp 到 `root@38.55.192.90:/root/khmer-deploy/` (§3)
4. **后端发布**: 备份旧二进制 → `systemctl stop khmer-ai-cs-go` → (有迁移则) 跑 `migrate-go` → cp 新二进制 → `chown khmerai` → start (§4; 先 stop 再 cp, 否则 Text file busy)
5. **前端发布**: `cp -a frontend frontend-backup-<ts>` → 解包新目录 → `chown -R khmerai` → `systemctl restart khmer-ai-cs-web` (§5)
6. **验证**: 服务器 `/ready` 双检查 + 本地走域名 `POST /api/v1/auth/login` 有 JSON 响应 (§6)
7. **回滚**: 后端 = 还原备份二进制 + restart; 前端 = 换回 backup 目录 (§7)

## SSH 连接（macOS, 无 sshpass 的机制）

```bash
mkdir -p /tmp/khmer-deploy
cat > /tmp/khmer-deploy/askpass.sh <<'EOF'
#!/bin/bash
echo "${KHMER_SSH_PASSWORD:-}"
EOF
chmod +x /tmp/khmer-deploy/askpass.sh
export KHMER_SSH_PASSWORD='<密码, 末尾两个点>'

# sshrun 封装 (下文所有远程操作用它; 2>&1 后过滤 known_hosts 噪音)
sshrun() {
  DISPLAY=:0 SSH_ASKPASS=/tmp/khmer-deploy/askpass.sh SSH_ASKPASS_REQUIRE=force \
  ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      -o ConnectTimeout=20 -o NumberOfPasswordPrompts=1 -o PubkeyAuthentication=no \
      root@38.55.192.90 "$@" 2>&1 | grep -v "Warning: Permanently added"
}
```

- macOS 的 OpenSSH 直接支持 `SSH_ASKPASS_REQUIRE=force` (**不需要 setsid**, 那是 Linux 特有的)
- 复杂远程命令 (引号嵌套/heredoc) 一律 **base64 传参执行**: `B64=$(echo "<脚本>" | base64); sshrun "echo $B64 | base64 -d | bash"`, 避免转义地狱
- **⚠️ 远程命令里不要用 `pkill -f "<模式>"`** — 会匹配并杀掉当前 ssh 会话自身; 清进程用 `pgrep` 拿 PID 再精确 kill

## 核心配置模板

**systemd** `/etc/systemd/system/khmer-ai-cs-go.service` (现行)：
```ini
[Service]
User=khmerai
WorkingDirectory=/opt/khmer-ai-cs
EnvironmentFile=/opt/khmer-ai-cs/.env-go
ExecStart=/opt/khmer-ai-cs/server-go
Restart=on-failure
RestartSec=3
```
前端 unit 用 `Environment=PORT=3001` `Environment=HOSTNAME=127.0.0.1` + `ExecStart=/usr/bin/node server.js` (Workdir=`/opt/khmer-ai-cs/frontend`)。

**nginx** `/etc/nginx/sites-enabled/khmer-ai-cs` 三个 location (完整文件见 references/deploy-commands.md §8):
- `location /api/` → 8081, **`proxy_buffering off`** (chat/stream SSE 必需)
- `location /api/v1/realtime/inbox` → 8081, WebSocket Upgrade 头 + `proxy_read/send_timeout 3600s` (**必须排在 /api/ 之前**)
- `location /` → 3001 (Next standalone), 安全头含 CSP (`connect.facebook.net` 允许, Meta SDK 用), 80 块 `return 301 https://`

## 验证清单

```bash
# 服务器本机 (绕过 nginx 直连后端)
curl -s http://127.0.0.1:8081/ready    # {"service":"khmer-ai-cs","status":"ok","checks":{"database":true,"redis":true},"version":"2.0.0-go"}
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:3001/    # 200
systemctl is-active khmer-ai-cs-go khmer-ai-cs-web                 # active active

# 本地 (走 Cloudflare 全链路)。⚠️ 域名上没有 /health —— nginx 只转 /api/, 裸路径全是 Next 页面
curl -s -X POST https://cs.wanfanginsulationmaterial.com/api/v1/auth/login \
     -H "Content-Type: application/json" -d '{"username":"x","password":"***"}' -o /dev/null -w "%{http_code}\n"  # 401 = API 链路通
curl -sI https://cs.wanfanginsulationmaterial.com/ | grep -iE "content-security|HTTP"                            # CSP 头
```

journalctl 里周期性 `/api/v1/realtime/inbox 401 WARN` = 未带 token 的 WS 重试探测, 属正常噪音, 不是故障。

## 故障排查速查

| 症状 | 原因 | 解法 |
|---|---|---|
| HTTPS 521 | Cloudflare 连不上源站 | zone SSL 必须 Full; `cs` A 记录必须 38.55.192.90 (不能是 CF 边缘 IP) |
| Gemini 502 / `User location is not supported` | Google 按出口 IP 地域封锁 Gemini API (2026-09-04 起, 服务器区域被拒) | 已用 **CF AI Gateway** 中继: `.env-go` 的 `GEMINI_API_BASE=https://gateway.ai.cloudflare.com/v1/<acc>/gemini-relay-gw/google-ai-studio/**v1beta**` — **`/v1beta` 后缀绝不能丢** (gemini.go 用 `apiBase+"/models"` 直接拼接, 丢了 = 网关 404 空 body)。CF Worker 边缘中继无效 (出口同被识别为受限区域) |
| 模型列表 200 但为空 | gemini.go ListModels 曾按 `data` 字段解析, Google 实际返回 `models` | 已修复 (2026-09-04); 若回归先查此解析 |
| 模型列表 404 `no longer available to new users` | 测试用了退役模型名 | 用 DB `model_configs.model_name` 里配的现役模型 (当前 gemini-3.6-flash) |
| 浏览器无限重定向 | SSL 模式被改成 Flexible (源站 :80 有 301) | CF 改回 Full |
| 页面能开但接口全挂 | 前端构建忘设 `NEXT_PUBLIC_API_URL` (默认烘焙 localhost:8080) | 带正确 env 重新 build + 发布前端 |
| 回答全是模板/无 AI | `GEMINI_API_KEY` 为空 = MOCK 模式 (设计如此) | 需要真 AI 就配 key 重启 |
| `Text file busy` | 服务运行中覆盖二进制 | `systemctl stop khmer-ai-cs-go` 后再 cp |
| migrate 报 DATABASE_URL is required | `migrate-go` 用 godotenv 只读 `./.env` (旧 Rust 配置), 且 CWD 不对 | 先 `set -a; . ./.env-go; set +a` 再跑 (Load 不覆盖已有变量), 见 deploy-commands §4 |
| 迁移后启动崩 | 迁移 SQL 失败但记了 schema_migrations? 不会 (单事务回滚) — 多半是列被代码引用但未迁移 | `migrate-go --status` 对账; 必要时 `--mark-applied` |
| SSH banner exchange 超时 | 连续密码错误被临时封 / 服务器负载 | 等 30~60s 再试, 别反复重连 |
| 密码被拒 Permission denied | 少打了末尾的点 | 密码结尾是**两个点** |
| 大文件上传 413 | 该 server 块没配 `client_max_body_size` (默认 1m) | cs 的 443 块按需加大后 `nginx -s reload` |
| Telegram webhook 注册失败 | `PUBLIC_API_URL` 不对或没走 https | 必须 `https://cs.wanfanginsulationmaterial.com` (不带 /api/v1) |

## 详细参考（按需阅读）

- `references/deploy-commands.md` — 完整命令手册: 交叉编译/standalone 打包/上传/迁移/发布/回滚/首次搭建/nginx 全文
- `references/cloudflare.md` — cs 子域名接入、SSL Full 缘由、521 排查、CF API 操作
- `references/troubleshooting.md` — 踩坑实录 (askpass/迁移 env 陷阱/Flexible 重定向环/备份目录权限等)
- `references/dev-guide.md` — **开发迭代指南**: 迁移机制 (001~034)、环境变量全表、API 面、渠道 webhook、与 WMS 同机共存、回滚点管理

## 交付后提醒用户

1. SSH 密码在本对话出现过明文, 建议尽快更换并改用密钥登录
2. `.env-go` 里 `JWT_SECRET` / `PLATFORM_CREDENTIAL_KEY` **不可随意轮换**: 前者杀光在线会话, 后者使已保存的渠道凭据无法解密
3. 新库首次启动必须设 `INITIAL_ADMIN_PASSWORD` (≥12 位), 001 迁移自带的默认 admin bcrypt 哈希是不安全占位
4. `/opt/khmer-ai-cs/frontend-backup-*` 定期清理, 只留最近 3 份
5. Postgres 备份: `pg_dump -d khmer_ai_cs` 定时跑 (pgvector 索引大, dump 会慢)
6. `server-go` 监听 0.0.0.0:8081 公网可直连 (未走 nginx 的路径也暴露), 建议防火墙限 127.0.0.1 或加 nft 规则
