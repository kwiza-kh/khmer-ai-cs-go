---
name: deploy-khmer-ai-cs
description: 将「Khmer AI 客服系统 (khmer-ai-cs-go, Go 后端 + Next.js 前端)」部署/更新到远程 Linux 服务器, 经 Cloudflare 子域名 <部署域名> 提供 HTTPS。当用户提到 部署 khmer-ai-cs / 高棉语 AI 客服 / server-go / migrate-go / <部署域名> / 连接 <部署服务器IP> / 更新线上版本 / 跑迁移 / nginx 反代 / systemd 服务 / Cloudflare 521 / HTTPS 异常 时触发。
---

# Khmer AI 客服系统 (khmer-ai-cs-go) 部署与迭代

将「Khmer AI 客服系统」Go 后端 + Next.js 前端部署/更新到远程 Linux 服务器 (经 Cloudflare 子域名 HTTPS), 覆盖首次全量部署与日常迭代发布 (后端二进制、前端 standalone、数据库迁移、回滚)。

## 何时触发

- 部署/更新 khmer-ai-cs 客服系统到服务器、上线、发布新版本
- 提到 <部署服务器IP> 或 <部署域名>
- 跑迁移 / migrate-go / schema_migrations / pgvector
- nginx / systemd / Cloudflare / 521 / HTTPS 异常排查
- 前端 standalone 发布、Telegram/Meta webhook 域名配置

## 架构与环境（先读这个）

```
浏览器 ──HTTPS──> Cloudflare (橙色云朵, zone=<域名>)
                      │ HTTPS:443 (源站自签证书, SSL 模式必须 Full)
                      ▼
              nginx (<部署域名>)
                ├── /api/                    ──> 127.0.0.1:8081  server-go (Go 后端, SSE 关 buffering)
                ├── /api/v1/realtime/inbox   ──> 127.0.0.1:8081  WebSocket 升级 (3600s 超时)
                └── /                        ──> 127.0.0.1:3001  next-server (standalone)
                                                    Postgres 17 (127.0.0.1:5432, db=khmer_ai_cs) + Redis (127.0.0.1:6379)
```

| 项 | 值 |
|---|---|
| 服务器 | <部署服务器IP> (Debian 13 trixie, x86_64) — **与 WMS 系统同机共存, 勿动 wms.service** |
| SSH | root + 密码 (从环境变量 `KHMER_SSH_PASSWORD` 读取, **勿把明文写进文件/仓库**; 密码值放密码管理器) |
| 域名 | **cs**.<域名> (Cloudflare A 记录 → <部署服务器IP>, 橙色云朵代理) |
| 后端 | Go (go.mod 声明 go 1.26; 线上二进制 go1.26.5) `server-go` → :8081, 二进制内 `/health` `/ready`; **服务器上没装 Go, 二进制在构建机交叉编译后上传** |
| 前端 | Next.js 16 standalone (`node server.js`) → 127.0.0.1:3001; `API_BASE` 在**构建时**烘焙 (见下) |
| 数据库 | 系统级 Postgres 17 (apt, 非 Docker), 扩展 **pgvector 0.8.0 + pg_trgm**, 迁移表 `schema_migrations` (见 `internal/migrations/migrations/` 当前文件数) |
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

1. **构建后端** (本地, 需 go ≥1.26, `brew install go`): `backend-go/` 下交叉编译 `server-go` 与 `migrate-go`; 发布前跑一次 SQL 引用检查 (`go test ./internal/sqlcheck/`, 需 `DATABASE_URL`, 未设会 skip) 质量门 —— 它挡的是编译器看不见的那类 bug (命令见 references/deploy-commands.md §1)
2. **构建前端**: `NEXT_PUBLIC_API_URL=https://<部署域名>/api/v1 npm run build`, 组装 standalone + `.next/static` + `public` 打 tar (§2)
3. **上传**: scp 到 `root@$KHMER_DEPLOY_HOST:/root/khmer-deploy/` (§3)
4. **后端发布**: 备份旧二进制 → `systemctl stop khmer-ai-cs-go` → (有迁移则) 跑 `migrate-go` → cp 新二进制 → `chown khmerai` → start (§4; 先 stop 再 cp, 否则 Text file busy)
   - **迁移跑完、新二进制 start 之前, 强制**跑一次 SQL 引用检查 —— 这是本工作流里唯一一次"库已是新 schema"的窗口:
     老 schema + 老二进制是自洽的, 而新二进制引用新列时, 编译器/单测都看不见 (历史事故正是这类: 列被代码引用但没迁移)。
     ```bash
     cd backend-go
     DATABASE_URL=postgres://khmerai:...@127.0.0.1:5432/khmer_ai_cs \
       SQLCHECK_REQUIRED=1 go test -count=1 ./internal/sqlcheck/
     ```
     `SQLCHECK_REQUIRED=1` 把"没有 DATABASE_URL 就 skip"变成**硬失败**: 不加它, 忘了设 DSN 时这个门禁会静默通过 (`PREPARE` 只解析不执行, 指生产库是安全的)。
     跑不通过就**不要** start 新二进制 —— 回到上一步修 SQL 或补迁移。
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
export KHMER_SSH_PASSWORD='<root 密码, 从密码管理器取>'
export KHMER_DEPLOY_HOST='<部署服务器IP, 从密码管理器取>'

# sshrun 封装 (下文所有远程操作用它; 2>&1 后过滤 known_hosts 噪音)
sshrun() {
  DISPLAY=:0 SSH_ASKPASS=/tmp/khmer-deploy/askpass.sh SSH_ASKPASS_REQUIRE=force \
  ssh -o StrictHostKeyChecking=accept-new \
      -o ConnectTimeout=20 -o NumberOfPasswordPrompts=1 -o PubkeyAuthentication=no \
      root@$KHMER_DEPLOY_HOST "$@" 2>&1 | grep -v "Warning: Permanently added"
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
- `location /` → 3001 (Next standalone), 安全头含 CSP (`connect.facebook.net` 允许 Meta SDK; `media-src https:` 允许 R2 音频回放), 80 块 `return 301 https://`

## Vertex 切换（可选，默认仍走 studio）

不设 `GEMINI_PROVIDER` 就是 studio（现状），所以下面这一整块**只在决定迁移时**才碰。

| 变量 | 值 / 要点 |
|---|---|
| `GEMINI_PROVIDER` | 不设/`studio` = 现状；`vertex` = 平台端点 + OAuth2 服务账号。**默认 studio 是刻意的**: 切换是运维动作, 拼错的值只会当 studio, 不会悄悄把生产改道 |
| `GEMINI_VERTEX_PROJECT` | GCP 项目 id。SA key 自带 `project_id` 时可省（缺了才报错） |
| `GEMINI_VERTEX_REGION` | 默认 `asia-southeast1` —— **实测可用的区域**, 填错 = 每次调用都 404 |
| `GEMINI_VERTEX_SA_FILE` | `/opt/khmer-ai-cs/vertex-sa.json` |
| `GEMINI_VERTEX_API_BASE` | 一般**留空**（区域已决定端点）; 仅中继/测试时才覆盖, 末尾不带 `/` |

- **SA 密钥落位**: 上传到 `/opt/khmer-ai-cs/vertex-sa.json` 后 `chown khmerai:khmerai` + `chmod 600`（systemd 以 `khmerai` 运行, 权限不对就是启动失败）。
- ⚠️ **绝不提交进仓库**: 仓库 `.gitignore` 里**没有**对应规则, 别指望它兜底; 密钥只存在于服务器 `/opt/khmer-ai-cs/`。
- 切到 vertex 后 `GEMINI_API_BASE` **可以留空**: 平台端点在区域内, 当初为绕 Google 地域封锁才加的 **Cloudflare AI Gateway 中继不再需要** —— 摘掉这一跳正是迁 Vertex 的收益之一（回滚到 studio 时再填回来）。
  - ⚠️ **时序**: 必须等**彻底切到 vertex（且不再需要回滚/对照）之后**再清。中继是这台机器到 AI Studio 的**唯一通路**, 提前清 = 把**回滚路径**和 `embedcmp` 路径 A 一起废掉; 而 vertex 路径**根本不读这个变量**, 所以清早了当天毫无症状, 等你真需要它时才发现没有降落伞（deploy-commands §10.5）。
- `GEMINI_PROVIDER=vertex` 而 project/SA 缺失时 **启动即退出**（`main.go` 的启动校验）, 不再是每轮请求才 500; 校验只读本地密钥文件, 不联网。
- 模型名必须**锁定版本**（`gemini-3.5-flash`）: **不要用 `-latest` 别名**（平台不认, 且会随上游漂移）, 平台也**没有 lite 档**。
- ⚠️ 新模型名是**区域作用域**的, 不是"不存在": 实测 (2026-09-25, 生产 SA) `gemini-3.8-flash` 在 `global` / `us` / `eu` 返回 200, 在**每一个单区域（含生产区 asia-southeast1）返回 404**; `gemini-3.5-flash` 在 `us-central1` / `europe-west4` 也是 404。后台「模型」页有**区域下拉**可按区查看目录（只改列表, 不改服务路径）—— 机制、接口与完整实测矩阵见 references/dev-guide.md §11.6。

### 切流顺序（两步, 可分离）

**发布门禁（切到 vertex 之后每次发布都跑）**：在改任何生产配置之前先证明目标区域仍然
提供我们依赖的能力。必需项失败会以 `exit 1` 退出，直接中断发布：

```bash
/root/khmer-deploy/vertexprobe -sa /opt/khmer-ai-cs/vertex-sa.json \
  -project gen-lang-client-0354228918 -region asia-southeast1 \
  -models gemini-3.5-flash -timeout 45s
# 期望: "All required checks passed." + 退出码 0
# 必需项 = OAuth 令牌签发 / 聊天模型可用 / 嵌入返回 768 维
```

两个**假通过陷阱**（都会让门禁看起来是绿的）：
- `-tasktype` 在本平台**必然 exit 1** —— 那是"平台不支持 task 条件化"的诊断结论，
  不是故障；把它放进任何门禁都会常红。
- `-audiodir` 指向空目录会打印 `0/0` 并 **exit 0**。要肉眼看输出里有 ogg 与 m4a 行。


| 步 | 动作 | 为什么能分开 |
|---|---|---|
| **1** | 把 DB `model_configs.is_default` 那行的 `model_name` 改成 `gemini-3.5-flash`（**不是改 `.env-go`** —— 线上主模型来自 DB）, 仍在 studio 上观察 | `gemini-3.5-flash` **在 AI Studio 侧也返回 200**, 所以先换名字**没有"新名字没人认"的窗口**; 这一步出问题可一眼归因到换模型, 回滚只是把名字改回去 |
| **2** | `.env-go` 加 `GEMINI_PROVIDER=vertex` + `GEMINI_VERTEX_PROJECT` / `_REGION=asia-southeast1` / `_SA_FILE`, 重启 | 回滚 = 改回/删掉 `GEMINI_PROVIDER`（**不设就是 studio**）+ 重启, 一条变量 |

- **切流前门禁**: `vertexprobe`（必需项失败即 `exit 1`, 不许进第 2 步）; **切流后验证**: 六项能力 `-caps` / 音频容器 `-audiodir` / `rageval` 复现基线 / 复核 `RAG_SIMILARITY_FLOOR`（相似度尺度整体下移, 而它是绝对值）/ 429 是否消失 —— **确切命令、预期输出与失败判据见 `references/deploy-commands.md` §10**（含收尾清理与"决定不迁"时该删什么）。
- ⚠️ **别拿 `vertexprobe -tasktype` 当门禁**: 平台上"没有任何拼写能条件化嵌入"就是实测结论, 它**必然 `exit 1`** —— 那是诊断工具要报的结果, 不是故障。
- ⚠️ **换 provider 不会自动重嵌入知识库**（重嵌入扫描只认**模型名**, 而 `gemini-embedding-001` 在两个 provider 上同名）⇒ 切流后要么重建一次文档、要么立刻用 `rageval` 实测确认, 别假设等于对照实验里的路径 B（详见 deploy-commands §10.1）。

## 验证清单

```bash
# 服务器本机 (绕过 nginx 直连后端)
curl -s http://127.0.0.1:8081/ready    # {"service":"relaychat","status":"ok","checks":{"database":true,"redis":true},"version":"<git-sha>"}
# ⚠️ service 是 "relaychat", 不是 "khmer-ai-cs" (internal/api/auth_handlers.go 里写死)。
# ⚠️ version 是构建期注入的 git 短哈希 (ldflags -X ...api.Version), 不是固定字符串 ——
#    部署后拿它核对"线上跑的到底是哪个构建", 这是唯一的权威信号。
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:3001/    # 200
systemctl is-active khmer-ai-cs-go khmer-ai-cs-web                 # active active

# 本地 (走 Cloudflare 全链路)。⚠️ 域名上没有 /health —— nginx 只转 /api/, 裸路径全是 Next 页面
curl -s -X POST https://<部署域名>/api/v1/auth/login \
     -H "Content-Type: application/json" -d '{"username":"x","password":"***"}' -o /dev/null -w "%{http_code}\n"  # 401 = API 链路通
curl -sI https://<部署域名>/ | grep -iE "content-security|HTTP"                            # CSP 头
```

journalctl 里周期性 `/api/v1/realtime/inbox 401 WARN` = 未带 token 的 WS 重试探测, 属正常噪音, 不是故障。

## 故障排查速查

| 症状 | 原因 | 解法 |
|---|---|---|
| HTTPS 521 | Cloudflare 连不上源站 | zone SSL 必须 Full; `cs` A 记录必须 <部署服务器IP> (不能是 CF 边缘 IP) |
| Gemini 502 / `User location is not supported` | Google 按出口 IP 地域封锁 Gemini API (2026-09-04 起, 服务器区域被拒) | 现状用 **CF AI Gateway** 中继: `.env-go` 的 `GEMINI_API_BASE=https://gateway.ai.cloudflare.com/v1/<acc>/gemini-relay-gw/google-ai-studio/**v1beta**` — **`/v1beta` 后缀绝不能丢** (gemini.go 用 `apiBase+"/models"` 直接拼接, 丢了 = 网关 404 空 body)。CF Worker 边缘中继无效 (出口同被识别为受限区域)。**根治 = 切 Vertex** (区域内端点, 不再需要中继, 见上文「Vertex 切换」), 切完**且不再需要回滚**后把 `GEMINI_API_BASE` 留空 (⚠️ **清早了会同时废掉回滚路径与 studio 对照工具, 见 §10.5**) |
| 模型列表 200 但为空 | gemini.go ListModels 曾按 `data` 字段解析, Google 实际返回 `models` | 已修复 (2026-09-04); 若回归先查此解析 |
| 模型列表 404 `no longer available to new users` | 测试用了退役模型名 | 用 DB `model_configs.model_name` 里配的现役模型 (当前 `gemini-3.5-flash`); 任何 `-latest` 别名平台都不认, 也没有 lite 档。⚠️ 但"新名字不存在"是错的: `gemini-3.8-flash` **存在**, 只是只在 `global` / `us` / `eu` 可调, 在每个单区域 (含 asia-southeast1) 是 404 (`gemini-3.6/3.7` 未重新实测) —— 按区域查后台「模型」页的区域下拉, 别再用"存不存在"下结论 (dev-guide §11.6) |
| Vertex 切完**启动即退出** (`systemctl` = failed) | `GEMINI_PROVIDER=vertex` 但 project/SA 缺失、路径错或 `khmerai` 读不到 key → `main.go` 启动校验 `os.Exit(1)`（只读本地文件, 不联网） | 看 journal 的 `invalid Gemini provider configuration` 一行; **先回滚 provider 把服务拉起来**再修 (deploy-commands §10.4/§10.7) |
| Vertex 切完业务请求 404 | 模型名与区域不匹配 (`-lite` / `-latest` 平台不认; 3.8 只在 `global` / `us` / `eu` 有 —— **不是不存在**), 或区域填错 | 锁 `gemini-3.5-flash` + `asia-southeast1`; 探针 `-caps` 先验证; 按区域查后台「模型」页的区域下拉 (dev-guide §11.6) |
| Vertex 切完检索命中变少 | 相似度尺度整体下移 (mean top1 `0.730→0.713`), 而 `RAG_SIMILARITY_FLOOR` 是**绝对值**; 也可能知识库没重嵌入 (换 provider 不触发重嵌入) | `rageval` 对基线 → 按 deploy-commands §10.6 第 6 项用 sweep 复核 floor; 重嵌入见 §10.1 |
| 清掉 `GEMINI_API_BASE` 后回滚失败 | 中继是这台机器到 AI Studio 的**唯一通路**, 清早了 = 没有降落伞 (vertex 路径不读它, 所以当天无症状) | 把中继地址填回 `.env-go` 再回滚 (deploy-commands §10.5) |
| 浏览器无限重定向 | SSL 模式被改成 Flexible (源站 :80 有 301) | CF 改回 Full |
| 页面能开但接口全挂 | 前端构建忘设 `NEXT_PUBLIC_API_URL` (默认烘焙 localhost:8080) | 带正确 env 重新 build + 发布前端 |
| 回答全是模板/无 AI | `GEMINI_API_KEY` 为空 = MOCK 模式 (设计如此) | 需要真 AI 就配 key 重启 |
| `Text file busy` | 服务运行中覆盖二进制 | `systemctl stop khmer-ai-cs-go` 后再 cp |
| migrate 报 DATABASE_URL is required | `migrate-go` 用 godotenv 只读 `./.env` (旧 Rust 配置), 且 CWD 不对 | 先 `set -a; . ./.env-go; set +a` 再跑 (Load 不覆盖已有变量), 见 deploy-commands §4 |
| 迁移后启动崩 | 迁移 SQL 失败但记了 schema_migrations? 不会 (单事务回滚) — 多半是列被代码引用但未迁移 | `migrate-go --status` 对账; 必要时 `--mark-applied` |
| SSH banner exchange 超时 | 连续密码错误被临时封 / 服务器负载 | 等 30~60s 再试, 别反复重连 |
| 密码被拒 Permission denied | 少打了末尾的点 | 密码结尾是**两个点** |
| 大文件上传 413 | 该 server 块没配 `client_max_body_size` (默认 1m) | cs 的 443 块按需加大后 `nginx -s reload` |
| Telegram webhook 注册失败 | `PUBLIC_API_URL` 不对或没走 https | 必须 `https://<部署域名>` (不带 /api/v1) |

## 详细参考（按需阅读）

- `references/deploy-commands.md` — 完整命令手册: 交叉编译/standalone 打包/上传/迁移/发布/回滚/首次搭建/nginx 全文/**Vertex 切流 + 发布门禁 (§10)**
- `references/cloudflare.md` — cs 子域名接入、SSL Full 缘由、521 排查、CF API 操作
- `references/troubleshooting.md` — 踩坑实录 (askpass/迁移 env 陷阱/Flexible 重定向环/备份目录权限等)
- `references/dev-guide.md` — **开发迭代指南**: 迁移机制 (NNN_name.sql 递增, 当前见仓库)、环境变量全表、API 面、渠道 webhook、与 WMS 同机共存、回滚点管理

## 交付后提醒用户

1. SSH 密码在本对话出现过明文, 建议尽快更换并改用密钥登录
2. `.env-go` 里 `JWT_SECRET` / `PLATFORM_CREDENTIAL_KEY` **不可随意轮换**: 前者杀光在线会话, 后者使已保存的渠道凭据无法解密
3. 新库首次启动必须设 `INITIAL_ADMIN_PASSWORD` (≥12 位), 001 迁移自带的默认 admin bcrypt 哈希是不安全占位
4. `/opt/khmer-ai-cs/frontend-backup-*` 定期清理, 只留最近 3 份
5. Postgres 备份: `pg_dump -d khmer_ai_cs` 定时跑 (pgvector 索引大, dump 会慢)
6. `server-go` 监听 0.0.0.0:8081 公网可直连 (未走 nginx 的路径也暴露), 建议防火墙限 127.0.0.1 或加 nft 规则
