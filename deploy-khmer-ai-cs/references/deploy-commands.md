# 部署命令手册（完整版）

SKILL.md 工作流的命令级详细版, 按顺序执行。远程命令通过 SSH_ASKPASS 机制 (见 SKILL.md「SSH 连接」), 下文以 `sshrun "命令"` 代指。上传用 `scprun`。

## 0. 前置：封装 ssh / scp（macOS）

> 安全基线（安全审计 2026-09）：服务器 IP、域名、密码等敏感值一律放环境变量或密码管理器，**不得写进本仓库**；SSH 必须校验主机密钥（`accept-new` 首连自动记录、之后任何变化都会失败——`no` 会把部署通道对中间人敞开，二进制却没有任何独立完整性校验）。

```bash
mkdir -p /tmp/khmer-deploy
cat > /tmp/khmer-deploy/askpass.sh <<'EOF'
#!/bin/bash
echo "${KHMER_SSH_PASSWORD:-}"
EOF
chmod +x /tmp/khmer-deploy/askpass.sh
export KHMER_SSH_PASSWORD='<root 密码, 从密码管理器取>'   # 只放环境变量, 不写进文件/仓库
export KHMER_DEPLOY_HOST='<服务器 IP, 从密码管理器/云控制台取>'

cat > /tmp/khmer-deploy/sshrun.sh <<'EOF'
#!/bin/bash
DISPLAY=:0 SSH_ASKPASS=/tmp/khmer-deploy/askpass.sh SSH_ASKPASS_REQUIRE=force \
ssh -o StrictHostKeyChecking=accept-new \
    -o ConnectTimeout=20 -o NumberOfPasswordPrompts=1 -o PubkeyAuthentication=no \
    "root@${KHMER_DEPLOY_HOST:?KHMER_DEPLOY_HOST 未设置}" "$@" 2>&1 | grep -v "Warning: Permanently added"
EOF
cat > /tmp/khmer-deploy/scprun.sh <<'EOF'
#!/bin/bash
DISPLAY=:0 SSH_ASKPASS=/tmp/khmer-deploy/askpass.sh SSH_ASKPASS_REQUIRE=force \
scp -o StrictHostKeyChecking=accept-new \
    -o ConnectTimeout=20 -o NumberOfPasswordPrompts=1 -o PubkeyAuthentication=no \
    "$@" 2>&1 | grep -v "Warning: Permanently added"
EOF
chmod +x /tmp/khmer-deploy/sshrun.sh /tmp/khmer-deploy/scprun.sh
```

复杂远程脚本 (引号/heredoc 嵌套) 用 base64 传输执行, 免转义:

```bash
SCRIPT=$(cat <<'EOS'
echo "远程里跑的一整段脚本"
EOS
)
B64=$(echo "$SCRIPT" | base64)
/tmp/khmer-deploy/sshrun.sh "echo $B64 | base64 -d | bash"
```

## 1. 本地交叉编译后端（服务器上**没有** Go, 不要试图在服务器编译）

依赖全部纯 Go (pgx / go-redis / bcrypt), CGO 无需开启, 交叉编译可靠 (与 Rust 不同):

```bash
cd <仓库>/backend-go
go vet ./... && go test ./...                                   # 发布前质量门

# SQL 引用检查 —— 强烈建议每次发布前跑。它把源码里每条 SQL 拿去 prepare,
# 能挡住「SQL 引用了不存在的列/类型 → 整个接口 100% 失败」这类编译器看不见的
# bug(已发生过两次: /tenants 的 message_quota、createSLA 的 sla_priority)。
# 只解析不执行、每条都在回滚的事务里, 所以可以直接指向生产库。
# 未设 DATABASE_URL 时它会 skip —— 也就是说常规 go test 覆盖不到它。
set -a; . ./.env-go; set +a; go test ./internal/sqlcheck/ -v
# 本机没有库时用隧道(把 DSN 的 5432 换成隧道端口):
#   ssh -f -N -L 15432:127.0.0.1:5432 root@$KHMER_DEPLOY_HOST
#   DATABASE_URL=$(echo "$DATABASE_URL" | sed 's|:5432/|:15432/|') go test ./internal/sqlcheck/ -v

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/khmer-deploy/server-go  ./cmd/server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/khmer-deploy/migrate-go ./cmd/migrate
# 版本戳: 把当前 commit 写进二进制, /ready 能回答"线上跑的是哪个构建"。
VERSION=$(git rev-parse --short HEAD)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-X khmer-ai-cs-go/internal/api.Version=$VERSION" -o /tmp/khmer-deploy/server-go ./cmd/server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-X khmer-ai-cs-go/internal/api.Version=$VERSION-migrate" -o /tmp/khmer-deploy/migrate-go ./cmd/migrate
file /tmp/khmer-deploy/server-go   # 确认 "ELF 64-bit ... x86-64"
# 构建完整性: 记录摘要, 上传后在服务器上核对 (§3)。
shasum -a 256 /tmp/khmer-deploy/server-go /tmp/khmer-deploy/migrate-go /tmp/khmer-deploy/khmer-fe.tgz > /tmp/khmer-deploy/SHA256SUMS
cat /tmp/khmer-deploy/SHA256SUMS
```

> 本机没装 Go 时: `brew install go` (需 ≥1.26, 见 go.mod)。线上现行二进制为 go1.26.5 构建。
> 二进制名固定 `server-go` / `migrate-go` (与 systemd ExecStart 一致)。想压缩可加 `-ldflags='-s -w'`。

## 2. 本地构建前端（Next 16 standalone）

```bash
cd <仓库>/frontend
# ⚠️ 关键: API_BASE 是构建时烘焙进 bundle 的 (src/lib/auth-client.tsx:
#    process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080/api/v1")
#    忘设 = 线上前端打用户本机的 localhost → 接口全挂
NEXT_PUBLIC_API_URL=https://<部署域名>/api/v1 npm run build

# 组装 standalone 运行目录 (与 frontend/Dockerfile 第三阶段同法):
STAGE=/tmp/khmer-deploy/fe; rm -rf $STAGE && mkdir -p $STAGE/.next
SRC_ROOT=.next/standalone
# standalone 可能把 server.js 镜像到嵌套路径下, 先定位顶层目录 (排除 node_modules 内的):
SERVER_DIR=$(dirname "$(find "$SRC_ROOT" -name server.js -type f -not -path '*/node_modules/*' | head -1)")
cp -a "$SERVER_DIR/." $STAGE/
cp -a .next/static $STAGE/.next/static       # standalone 不含 static, 必须补
cp -a public $STAGE/public                   # public/ 也要补
tar czf /tmp/khmer-deploy/khmer-fe.tgz -C $STAGE .
```

服务器上 node v20 即可 (`node -v`); 无需在服务器 npm install。

## 3. 上传

```bash
/tmp/khmer-deploy/sshrun.sh "mkdir -p /root/khmer-deploy"
/tmp/khmer-deploy/scprun.sh /tmp/khmer-deploy/server-go /tmp/khmer-deploy/migrate-go /tmp/khmer-deploy/khmer-fe.tgz root@\$KHMER_DEPLOY_HOST:/root/khmer-deploy/
/tmp/khmer-deploy/scprun.sh /tmp/khmer-deploy/SHA256SUMS root@\$KHMER_DEPLOY_HOST:/root/khmer-deploy/
# scp 退出码可能是管道误报, 以远端 ls 为准:
/tmp/khmer-deploy/sshrun.sh "ls -lh /root/khmer-deploy/"
# 完整性校验: 摘要不匹配就立即停手 (构建→部署通道的替换检测)。
/tmp/khmer-deploy/sshrun.sh "cd /root/khmer-deploy && sha256sum -c SHA256SUMS"
```

## 4. 后端发布（含迁移）

```bash
# 一个 base64 脚本搞定: 停服 → 备份 → 迁移 → 换二进制 → 属主 → 起服 → 自检
SCRIPT=$(cat <<'EOS'
set -e
cd /opt/khmer-ai-cs
TS=$(date +%Y%m%d%H%M%S)
cp -a server-go server-go.bak-$TS
systemctl stop khmer-ai-cs-go

# 迁移: migrate-go 的 godotenv 只读 ./.env(旧配置), 先显式加载 .env-go 且不担心被覆盖
# (godotenv.Load 不覆盖已存在的环境变量)
set -a; . ./.env-go; set +a
./migrate-go --status          # 先看 pending 列表
cp -f /root/khmer-deploy/migrate-go ./migrate-go && chmod +x migrate-go
./migrate-go                   # 应用全部 pending (001~NNN 内嵌, 每个文件单事务)

install -o khmerai -g khmerai -m 755 /root/khmer-deploy/server-go ./server-go
systemctl start khmer-ai-cs-go
sleep 2
systemctl is-active khmer-ai-cs-go
curl -s http://127.0.0.1:8081/ready
EOS
)
B64=$(echo "$SCRIPT" | base64)
/tmp/khmer-deploy/sshrun.sh "echo $B64 | base64 -d | bash"
```

要点:
- **先 stop 再覆盖二进制**, 否则 `Text file busy`
- 迁移失败会自动回滚该文件事务 (单事务), 服务直接不启动; 修复后重跑即可
- `migrate-go` 支持 `--status` / `--mark-applied <version文件名>` / `--dsn <URL>`
- 无新迁移的纯代码发布: 跳过 migrate 三步, 其余照旧
- 旧回滚链: `khmer-ai-cs.service` (Rust, disabled) + `/opt/khmer-ai-cs/server` — Go 版大翻车时可 `systemctl start khmer-ai-cs.service` 顶回 (端口/环境文件都不同: 它用 `.env`)

## 5. 前端发布（备份-换入模式）

```bash
SCRIPT=$(cat <<'EOS'
set -e
cd /opt/khmer-ai-cs
TS=$(date +%Y%m%d%H%M%S)
cp -a frontend frontend-backup-$TS
rm -rf frontend-new && mkdir frontend-new
tar xzf /root/khmer-deploy/khmer-fe.tgz -C frontend-new
test -f frontend-new/server.js                      # 打包完整性哨兵
rm -rf frontend && mv frontend-new frontend
chown -R khmerai:khmerai frontend                   # 备份目录见过 777 权限漂移, 换入后必须 chown
systemctl restart khmer-ai-cs-web
sleep 3
systemctl is-active khmer-ai-cs-web
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:3001/
# 清理: 只保留最近 3 份备份
ls -dt /opt/khmer-ai-cs/frontend-backup-* 2>/dev/null | tail -n +4 | xargs -r rm -rf
EOS
)
B64=$(echo "$SCRIPT" | base64)
/tmp/khmer-deploy/sshrun.sh "echo $B64 | base64 -d | bash"
```

## 6. 验证

```bash
# 服务器 (sshrun 内执行)
curl -s http://127.0.0.1:8081/health && echo
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:3001/
journalctl -u khmer-ai-cs-go -n 15 --no-pager -o cat   # 启动无 ERROR

# 本地 (走 Cloudflare; 域名上 /health 不存在, 别拿它验证)
curl -s -X POST https://<部署域名>/api/v1/auth/login \
     -H "Content-Type: application/json" -d '{"username":"__probe__","password":"***"}' \
     -w "\n%{http_code}\n"    # 期望 JSON + 401/4xx (非 5xx/521)
curl -s -o /dev/null -w "%{http_code}\n" https://<部署域名>/   # 200
```

## 7. 回滚

```bash
# 后端: 停服 → 还原备份二进制 → 起服 (迁移是 forward-only, 回滚代码不回滚 schema;
#       新迁移均为附加式建表/加列, 旧二进制通常仍能跑)
ls -t /opt/khmer-ai-cs/server-go.bak-* | head -1        # 找最新备份
systemctl stop khmer-ai-cs-go
cp -a <备份> /opt/khmer-ai-cs/server-go && chown khmerai:khmerai /opt/khmer-ai-cs/server-go
systemctl start khmer-ai-cs-go

# 前端: 换回最近的 backup 目录
systemctl stop khmer-ai-cs-web
mv frontend frontend-broken && cp -a frontend-backup-<ts> frontend
chown -R khmerai:khmerai frontend && systemctl start khmer-ai-cs-web
```

## 8. nginx 配置全文（首次部署参考 / 现行生效版）

`/etc/nginx/sites-enabled/khmer-ai-cs`:

```nginx
server {
    listen 80; listen [::]:80;
    server_name <部署域名>;
    return 301 https://$host$request_uri;
}
server {
    listen 443 ssl; listen [::]:443 ssl;
    http2 on;
    server_name <部署域名>;

    # 安全头 (CSP 允许 connect.facebook.net = Meta SDK; 页面本体全同源)
    add_header Strict-Transport-Security "max-age=31536000" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-Frame-Options "DENY" always;
    add_header Referrer-Policy "strict-origin-when-cross-origin" always;
    add_header Permissions-Policy "camera=(), geolocation=(), payment=()" always;
    add_header Content-Security-Policy "default-src 'self'; script-src 'self' 'unsafe-inline' https://connect.facebook.net; style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; media-src 'self' blob: https:; font-src 'self' data:; connect-src 'self' https:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'" always;

    ssl_certificate     /etc/nginx/ssl/wms.crt;    # 与 WMS 共用自签证书 (CF Full 模式接受)
    ssl_certificate_key /etc/nginx/ssl/wms.key;
    proxy_hide_header X-Powered-By;
    ssl_protocols TLSv1.2 TLSv1.3;

    location /api/v1/realtime/inbox {          # WebSocket: 必须排在 /api/ 之前
        proxy_pass http://127.0.0.1:8081;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
    location /api/ {                            # REST + SSE
        proxy_pass http://127.0.0.1:8081;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_buffering off;                    # chat/stream 流式必需
    }
    location / {                                # Next standalone
        proxy_pass http://127.0.0.1:3001;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_buffering off;
    }
}
```

改完 `nginx -t && systemctl reload nginx`。

## 9. 首次全量搭建（新机器/重建时）

```bash
SCRIPT=$(cat <<'EOS'
set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq nginx postgresql-17 postgresql-17-pgvector redis-server nodejs

# 运行用户 + 目录
id khmerai || useradd -r -m -s /bin/bash khmerai
mkdir -p /opt/khmer-ai-cs && chown khmerai:khmerai /opt/khmer-ai-cs

# Postgres: 角色 + 库
sudo -u postgres psql -tc "SELECT 1 FROM pg_roles WHERE rolname='khmer'" | grep -q 1 || \
  sudo -u postgres psql -c "CREATE ROLE khmer LOGIN PASSWORD '<生成强密码>'"
sudo -u postgres psql -tc "SELECT 1 FROM pg_database WHERE datname='khmer_ai_cs'" | grep -q 1 || \
  sudo -u postgres psql -c "CREATE DATABASE khmer_ai_cs OWNER khmer"
sudo -u postgres psql -d khmer_ai_cs -c "CREATE EXTENSION IF NOT EXISTS vector; CREATE EXTENSION IF NOT EXISTS pg_trgm;"

# Redis 密码: /etc/redis/redis.conf requirepass <pwd>; systemctl restart redis-server
EOS
)
```

然后: 写 `/opt/khmer-ai-cs/.env-go` (键名清单见 dev-guide「环境变量」; 权限 chown khmerai) →
上传 `server-go`/`migrate-go` → `migrate-go` (全量迁移 + `INITIAL_ADMIN_PASSWORD` 建管理员) →
落 systemd unit (SKILL.md 模板) → `systemctl daemon-reload && systemctl enable --now khmer-ai-cs-go khmer-ai-cs-web` →
nginx 站点 (§8) + 自签证书 (复用 `/etc/nginx/ssl/wms.*` 或 openssl 新签) → Cloudflare 加 `cs` A 记录 + SSL Full (见 cloudflare.md) → 前端发布 (§5)。

## 10. Vertex 切流手册（studio → vertex, 两步 + 发布门禁）

> 写给**凌晨三点被叫起来的人**: 每条命令可直接复制, 每步有预期输出, 每个失败都有判据和回滚动作。
> 背景与全部实测数据见 `docs/GEMINI-RATE-LIMIT.md` §十二 / §十四 (§十三 是消费护栏在 vertex 下的语义, 同一次改 `.env-go` 时一起处理); 变量语义见 SKILL.md「Vertex 切换」与 dev-guide §3。
> 下面命令都在**服务器本机**执行 (三个工具、SA key 都在那台机器上, 密钥不必离开它); 从 mac 跑时按 §0 包一层,
> 多行脚本用 base64 传参 (同 §4 的写法)。

### 10.0 为什么可以分两步

| 步 | 换掉什么 | 生效方式 |
|---|---|---|
| **1** | **DB `model_configs` 里 `is_default = true` 那行的 `model_name`** → `gemini-3.5-flash` | 管理后台保存 = 立刻热加载; `psql` 直接 UPDATE = **必须重启** |
| **2** | `.env-go`: `GEMINI_PROVIDER=vertex` + `GEMINI_VERTEX_PROJECT` / `_REGION` / `_SA_FILE` | 只能重启 (provider 在进程启动时解析一次) |

分两步安全, 是因为 `gemini-3.5-flash` **在 AI Studio 侧也返回 200**: 第 1 步先换模型名, 不存在"新名字没人认"的窗口期,
出问题也能一眼归因到"换模型"; 第 2 步才换传输层 (端点 / 鉴权 / 报文形状)。中间的观察期不是形式 ——
第 1 步之后若有异常, 回滚只需把模型名改回去, 与传输层无关。

**⚠️ 主模型在 DB, 不在 `.env-go`**: 只要 `model_configs` 有 `is_default` 行, 启动日志就是
`Gemini configured from database model config model=…`, `.env-go` 的 `GEMINI_MODEL` 被完全忽略 (dev-guide §3 ⑤)。
改错地方 = 以为切了, 其实没切。

**模型名纪律**: 模型名必须**锁定版本**, **不要用 `-latest` 别名** (平台不认, 且它随上游漂移, 发布无法复现);
平台**没有 lite 档** (`gemini-2.5-flash-lite` / `gemini-3.5-flash-lite` 亚洲各区全 404)。
`gemini-3.6/3.7/3.8-flash` "平台上不存在"是**曾经的错误结论** (它是在 AI Studio / 只在 asia-southeast1 量出来的):
实测 (2026-09-25, 生产 SA) **`gemini-3.8-flash` 在 `global` / `us` / `eu` 返回 200, 在每一个单区域 (含 asia-southeast1) 返回 404** —— 新模型名是**区域作用域**的, 不是缺席; 3.6/3.7 未重新实测, 别再假设它们不存在。
⇒ 留在 `asia-southeast1` 就把主模型与快模型统一 `gemini-3.5-flash`; 换区域上 3.8 之前先看 dev-guide §11.6 的区域矩阵 (3.5 在 `us-central1` / `europe-west4` 也是 404, 连在用模型都要重测)。
两者同名后, "降级到快模型"的路径会被 `fast != model` 守卫跳过 —— 这是设计如此, **不是故障**。

**按区域看目录 (2026-09-25 起)**: 后台「模型」页的区域下拉走
`GET /api/v1/admin/models/{id}/available?region=<region>` (省略 `region` = 服务端配置的区域), 区域候选走
`GET /api/v1/admin/models/vertex-regions`。它**只影响列表, 不影响服务路径** (在用的模型/区域仍看 `.env-go` 的
`GEMINI_VERTEX_REGION` + DB `model_configs.model_name`)。
- 列表走 `{host}/v1beta1/publishers/google/models?pageSize=100`: **`/v1/` 形式 404, 只有 `/v1beta1/` 有**; `global` 的 host 是 `aiplatform.googleapis.com` (没有 `global-` 前缀)。
- 列表**不是可调用性判据**: `asia-southeast1` 只列 9 条且不含 `gemini-3.5-flash`, 而它在该区**可调 (200)** —— 就是生产在用模型。所以接口 `available` 默认 `true`, 唯一权威是「测试」按钮。
- 列表不完整时 (区域 404 / 报错 / 空) 接口仍 **200** + 带上配置中的在用模型, 原因在响应头 `X-Model-List-Warning` 里。
- 完整说明与实测矩阵: dev-guide §11.6。

### 10.1 切流前先决定: 知识库要不要重嵌入

**换 provider 不会触发重嵌入。** 启动时的重嵌入扫描只认**模型名** (`internal/rag/service.go` SpawnIndexWorkers):

```sql
UPDATE knowledge_documents SET index_status = 'pending'
 WHERE index_status = 'ready' AND embedding_model <> '' AND embedding_model <> 'gemini-embedding-001';
```

`gemini-embedding-001` 这个常量在两个 provider 上**同名**, 所以切到 vertex 后: 库里既有的 chunk 仍是 AI Studio 产的向量,
只有**新查询**是 vertex 产的 —— 这是一个**没有实测过**的组合 (embedcmp 的路径 B 是"查询与文档都重算成 vertex",
它明确把跨供应商配对排除在结论之外)。

- 想要"路径 B 那样"的一致语料: 切流后把文档重新索引一次 —— 后台知识库的「重试/重建」(`POST /api/v1/knowledge/{id}/retry`),
  或按上面的 SQL 批量置 `pending`, 由索引 worker 重新分块 + 重嵌入。前提是**原始文件/URL 还取得到**;
  取不到的文档会变成 `index_status='failed'`、`chunk_count=0`, **从密集检索里消失** ⇒ 先拿一篇试, 确认回到 `ready` 再批量。
- 不重建也可以, 但**必须自己测一次**: 切流后立刻跑 10.6 第 5 项 (`rageval`), 用真实数字决定"能接受 / 要重建 / 要回滚"。
  不要假设它等于路径 B。
- 评测集不受影响: `rageval` 的 `expect` 是**文档 id** (内部用 `chunkDocIDs` 比对), 重建 chunk 不会让基线失效。
- 无论重建与否, `embedding_model` 列写的都是同一个字符串 —— **从数据里看不出向量是哪个 provider 产的**,
  所以"谁在什么时候重建过"只能靠值班记录, 记下来。

### 10.2 切流前门禁: vertexprobe（必需项失败 = `exit 1`, 不许进下一步）

```bash
cd /root/khmer-deploy
./vertexprobe -sa /opt/khmer-ai-cs/vertex-sa.json \
              -project <GCP project id> -region asia-southeast1 \
              -models gemini-3.5-flash
echo "EXIT=$?"          # 门禁看这个数字, 不是看输出好不好看
```

**为什么带 `-models`**: 内置候选表把 `gemini-2.5-flash` 与 `gemini-flash-lite-latest` 标成 `[required]`,
而平台上**没有 lite 档也没有这个浮动别名** (全 404) —— 裸跑会在"平台完全健康"的情况下 `exit 1` (假报警)。
用 `-models` 显式列出本次要验的模型, 是为了让**退出码只反映真正决定迁移的项目**: 只列 `gemini-3.5-flash` 时,
唯一的 `[required]` 就是嵌入那条 (**768 维**)。
> 想顺带确认 `gemini-2.5-flash`(备用档) 也可用, 就写成 `-models gemini-3.5-flash,gemini-2.5-flash` ——
> 它在探针的必需名单里, 会变成第二条必需项 (该区实测可用 ✅); 只做门禁的话没必要把它捆进来。

预期输出 (必需项被排到最前):
```
vertexprobe — project=<project-id> region=asia-southeast1
  service account: <name>@<project-id>.iam.gserviceaccount.com

── results ──────────────────────────────────────────────
✓ embedding :predict: gemini-embedding-001      dim=768 (want 768)  [required]
✓ oauth token minting                           bearer token acquired
✓ chat: gemini-3.5-flash                        OK
• embedding :embedContent: gemini-embedding-001 HTTP 400 INVALID_ARGUMENT: …   ← 平台走 :predict; 这条**预期就失败**, 非必需
• chat: gemini-2.5-flash-preview-tts            HTTP 404 NOT_FOUND: …          ← 平台无 TTS 预览模型, 符合预期

All required checks passed. Review the non-fatal rows for TTS/caching coverage.
```
报告里还会有一行 `context caching: cachedContents` —— **非必需**, 成败都不影响门禁, 原样抄进值班记录即可。

退出码语义 (门禁只认这个):

| 码 | 含义 | 动作 |
|---|---|---|
| `0` | 全部必需项通过 | 继续 |
| `1` | 有必需项失败 (或 `-caps` / `-audiodir` 有形状被拒) | **停手**, 修好再谈切流 |
| `2` | 根本没到 API: 缺 `-sa`、SA JSON 读不了或缺 `client_email`/`private_key`、key 里没 `project_id` 又没给 `-project` | 先修文件/参数, 不是平台问题 |

**⚠️ 两个会骗人的地方**:

1. `-tasktype` 探针在本平台上**必然 `exit 1`** —— "没有任何拼写能条件化嵌入"正是它要报的实测结论, 不是故障。
   它属于**诊断工具, 永远不要放进发布门禁** (放进去 = 每次发布都红)。
2. `-audiodir` 指向空目录 (或只有不支持扩展名的目录) 时会打印 `0/0 containers accepted.` 并 **`exit 0`** —— 空目录也算"通过"。
   跑完必须肉眼确认输出了 ≥1 行 `ogg` + ≥1 行 `m4a`。

顺便确认**回滚腿还活着** (见 10.5 的时序陷阱; 回滚与对照全靠这两个变量):

```bash
grep -c '^GEMINI_API_BASE=' /opt/khmer-ai-cs/.env-go   # 必须 = 1（CF AI Gateway 中继, 含 /v1beta 后缀）
grep -c '^GEMINI_API_KEY='  /opt/khmer-ai-cs/.env-go   # 必须 = 1
# SA key 必须是服务用户可读, 否则第 2 步会"启动即退出":
runuser -u khmerai -- head -c 1 /opt/khmer-ai-cs/vertex-sa.json >/dev/null && echo "khmerai 可读" || \
  { echo "✗ 权限不对: chown khmerai:khmerai /opt/khmer-ai-cs/vertex-sa.json && chmod 600 它"; }
```

**这套探针切流前跑一次就是基线**: 10.6 的第 1 / 2 项在切流后**原样再跑一遍**, 两次输出一致才叫"没有回归"
(平台侧能力也可能随区域调整而漂移, 只跑一次说明不了问题)。

### 10.3 第 1 步: 主模型换 `gemini-3.5-flash`（仍在 studio 上观察）

**方式 A (推荐)**: 管理后台 → 模型 → 编辑默认配置 → `model_name` = `gemini-3.5-flash` → 保存。
保存走 `PUT /api/v1/admin/models/{id}` → `reloadGeminiFromDB()` → `HotReload`, **不用重启**, 旁边的「测试」按钮还能当场跑一次真实调用。

**方式 B (psql)**: 直接 UPDATE **不会**热加载, 必须重启:

```bash
SCRIPT=$(cat <<'EOS'
set -e
cd /opt/khmer-ai-cs
set -a; . ./.env-go; set +a
psql "$DATABASE_URL" -c "select config_id, name, model_name, is_default from model_configs order by config_id"
psql "$DATABASE_URL" -c "update model_configs set model_name='gemini-3.5-flash' where is_default = true"
psql "$DATABASE_URL" -c "select config_id, model_name from model_configs where is_default = true"
systemctl restart khmer-ai-cs-go
sleep 2; systemctl is-active khmer-ai-cs-go
journalctl -u khmer-ai-cs-go -n 30 --no-pager -o cat | grep "database model config"
EOS
)
B64=$(echo "$SCRIPT" | base64)
/tmp/khmer-deploy/sshrun.sh "echo $B64 | base64 -d | bash"
```

预期 (两种方式都要核对): `level=INFO msg="Gemini configured from database model config" model=gemini-3.5-flash`。
观察期判据 (跑一段真实流量; 建议隔夜, 至少覆盖一次知识库问答):

```bash
set -a; . /opt/khmer-ai-cs/.env-go; set +a
psql "$DATABASE_URL" -c "select model_name, count(*) from chat_messages
  where created_at > now() - interval '1 hour' group by 1 order by 2 desc"
```

期望: `gemini-3.5-flash` 有行, 且**旧名字没有新行**。旧名还在涨 = `is_default` 那行没改到。
失败判据: 出现 `404 … no longer available` / `NOT_FOUND` 且模型名是新名 ⇒ 名字拼错 (无 lite 档, 也别加 `-latest`)。
回滚: 把模型名改回原值 (方式 A 不用重启 / 方式 B 重启)。

### 10.4 第 2 步: 切 provider（`GEMINI_PROVIDER=vertex`）

```bash
SCRIPT=$(cat <<'EOS'
set -e
cd /opt/khmer-ai-cs
cp -a .env-go .env-go.bak-$(date +%Y%m%d%H%M%S)
sed -i '/^GEMINI_PROVIDER=/d; /^GEMINI_VERTEX_/d' .env-go      # 幂等: 先删同名旧行再追加
cat >> .env-go <<'EOF'
GEMINI_PROVIDER=vertex
GEMINI_VERTEX_PROJECT=<GCP project id>
GEMINI_VERTEX_REGION=asia-southeast1
GEMINI_VERTEX_SA_FILE=/opt/khmer-ai-cs/vertex-sa.json
EOF
chown khmerai:khmerai .env-go
ls -l /opt/khmer-ai-cs/vertex-sa.json                          # 期望 khmerai 属主、模式 600
systemctl restart khmer-ai-cs-go
sleep 2
systemctl is-active khmer-ai-cs-go                             # 期望 active
curl -s http://127.0.0.1:8081/ready; echo
EOS
)
B64=$(echo "$SCRIPT" | base64)
/tmp/khmer-deploy/sshrun.sh "echo $B64 | base64 -d | bash"
```

预期: `active` + `{"service":"relaychat","status":"ok","checks":{"database":true,"redis":true},"version":"<git-sha>"}`。

启动失败 (systemd `failed` / 服务没起来) 时看最后 30 行 `journalctl -u khmer-ai-cs-go -n 30 --no-pager -o cat`, 对照:

| 日志 | 原因 | 处理 |
|---|---|---|
| `invalid Gemini provider configuration` + `requires GEMINI_VERTEX_SA_FILE` | 变量名拼错/漏写 | 改 `.env-go` |
| `… requires GEMINI_VERTEX_PROJECT: it is unset and … carries no project_id` | SA key 不自带 `project_id` | 补 `GEMINI_VERTEX_PROJECT` |
| `read …: no such file` / `parse service-account JSON` / `parse private key` | 路径错 / 文件传坏 / `khmerai` 读不到 | 重传 + `chown khmerai:khmerai` + `chmod 600` |

**⚠️ 别在线上现修**: 这类失败是"**启动即退出**" (`cmd/server/main.go` 的启动校验, 只读本地文件、不联网),
此刻客户侧**完全没有服务** —— 先按 10.7 把服务拉起来再慢慢修。

**⚠️ 别把中继地址抄进 `GEMINI_VERTEX_API_BASE`**: 它一般**留空** (区域已决定端点); 填了反而把 vertex 流量也绕到中继上, 末尾还容易多带 `/`。

**⚠️ 同一次编辑里顺手处理 `GEMINI_SPEND_LIMIT_USD`**: vertex 下它不再是"Google 的墙的本地镜像" —— **删掉它** (未设 = 不设上限, 推荐) 或改成一个明确的预算数字;
**不要留着 studio 的档位 `10 / 50 / 200`** —— 那三个数字在平台上没有对应物, 留着只会让闸门在一堵不存在的墙前面继续甩客户回合 (判据与观测见 `docs/GEMINI-RATE-LIMIT.md` §十三/§十四)。

### 10.5 ⚠️ 时序陷阱: `GEMINI_API_BASE` 什么时候才能清

**必须等完全切到 vertex 之后 (而且确认不再需要回滚与对照) 再清。** 三个理由, 按重要性排:

1. 这台机器到 AI Studio 的**唯一通路**就是 `GEMINI_API_BASE` 指向的 Cloudflare AI Gateway 中继 (Google 按出口 IP 封锁直连)。
2. **vertex 路径根本不读这个变量** ⇒ 清早了**当天完全没有症状**: 它废掉的是**你以后才需要的东西** —— 回滚 (`GEMINI_PROVIDER` 改回 studio)
   和 studio 侧对照工具 (`embedcmp` 的路径 A 需要它才能跑, 实测就是这么踩出来的)。
3. 所以: 清掉它 = **在没有降落伞的情况下继续飞**。要清, 先确认"不再需要回滚"。

正向证据 (切完之后应该看到): Cloudflare 面板 AI → AI Gateway 里那台网关的请求数**归零** —— 这是"确实不再走 studio"最直接的证据
(**日志里没有任何一行会打印当前 provider**)。`journalctl -u khmer-ai-cs-go --since "1 hour ago" --no-pager | grep -c "User location is not supported"` 也应为 `0`。

### 10.6 切流后验证清单

| # | 检查 | 命令 (服务器上) | 通过判据 |
|---|---|---|---|
| 0 | 服务活着 | `systemctl is-active khmer-ai-cs-go; curl -s http://127.0.0.1:8081/ready` | `active` + `"status":"ok"` |
| 1 | 六项能力 | `./vertexprobe -sa … -region asia-southeast1 -caps gemini-3.5-flash; echo EXIT=$?` | `6/6 request shapes accepted.` + `EXIT=0` |
| 2 | 音频容器 ogg/m4a | `./vertexprobe … -caps gemini-3.5-flash -audiodir /root/khmer-deploy/audio-samples; echo EXIT=$?` | ≥1 行 `audio/ogg` + ≥1 行 `audio/mp4`, `N/N accepted`, `EXIT=0` (**`0/0` = 假通过**) |
| 3 | 服务端到端 | 后台「模型」页的**测试**按钮 (`POST /api/v1/admin/models/{id}/test`); 再发一条真实消息 | 返回 `reply` + `model_name=gemini-3.5-flash`; 近 50 行 journal 无 `gemini returned HTTP` |
| 4 | 真实多模态路径 | 用一条**真实高棉语语音**走客户渠道 (或在后台发语音/图/PDF) | 有转写/回答, 无 500 |
| 5 | 检索基线 | 见下 (rageval) | `LEG dense recall@5 ≥ 37/45` 且 `MRR ≥ 0.655` |
| 6 | 相似度闸门复核 | 见下 | dense 腿不被掏空; 有 sweep 证据才准调 |
| 7 | 429 是否消失 | `journalctl -u khmer-ai-cs-go --since "24 hours ago" --no-pager \| grep -c "spend-based rate limit"` | `0` (迁移的主要动机) |
| 8 | 消费计量仍在记 | `curl -s …/api/v1/platform/spend` + `psql "$DATABASE_URL" -c "select count(*) from token_usage where created_at > now() - interval '1 hour'"` | 窗口消费有数 / 有新增行 (护栏没被 provider 改动破坏) |

第 2 项的样本要**自备**: 把真实语音导出到 `/root/khmer-deploy/audio-samples/` ——
`ogg` 取 WhatsApp/Telegram 的语音条, `m4a` 取 LINE/手机录音 (扩展名决定 mime, 不认识的扩展名会被**静默跳过**)。
目录不存在时探针报 `cannot read …` 并 `exit 1` —— 那是**缺样本**, 不是平台不支持。

第 5 项的完整命令 (rageval **继承 `.env-go` 的 provider**, 所以必须 source; 顺手把 provider 打出来留证):

```bash
set -a; . /opt/khmer-ai-cs/.env-go; set +a
echo "provider=${GEMINI_PROVIDER:-studio} region=${GEMINI_VERTEX_REGION:-}"
cd /root/khmer-deploy
./rageval -eval /root/khmer-deploy/rag_eval.json -user 7 -limit 5 -pipeline
# 期望 (dense 腿就是基线那一行):
#   cases=45 limit=5
#   LEG dense    recall@5 37/45 ( 82%)  recall@10 40/45  MRR 0.655
```

判据: dense 腿**不低于**基线 (实测路径 B 为 `38/45`、`MRR 0.705`)。明显偏低 (如 `33/45`) 先按 10.1 分诊
(语料是否重建过 / `chunk_count` / `embedding_model`), 再怀疑闸门。
> 不要跨工具比 MRR: `rageval` 的 dense 腿 SQL 截断在 `4×topK` 行, `embedcmp` 看得更深, 两者的 `0.655` / `0.658`
> 属已知口径差异 (`embedcmp` 报告 NOTES 里写了), 各自与自己的基线比。

第 6 项 (闸门复核 —— **整本手册里唯一需要动配置的地方**):

```bash
grep -E '^RAG_(SIMILARITY_FLOOR|SIMILARITY_RATIO|RERANK_SKIP)=' /opt/khmer-ai-cs/.env-go
```

- 实际切点是 `max(RAG_SIMILARITY_FLOOR, RAG_SIMILARITY_RATIO × 该查询最高相似度)` (生产与评测同一公式)。
  **`ratio` 随尺度自适应, `floor` 是绝对值** ⇒ 这次要复核的是 **floor**: 相似度尺度整体下移 (实测 mean top1 `0.730 → 0.713`)
  会让 `floor` 更频繁地成为真正的切点, dense 腿可能被掏空 —— 即使排序完全没变。
- 另有两处与绝对相似度比较的判据会跟着轻微移动: rerank 触发 (`RAG_RERANK_SKIP` 现值) 与 `service.go` 里写死的 `0.60`
  (`leaderClear` / `signalsAgree`) ⇒ 复核时把 sweep 输出里的 `rerank(skip/run)` 计数一起看。
- **只有在 sweep 里看到"生产现值那一档把 dense 腿掏空了"才动**, 且必须带证据:

```bash
# <skip> 用上面 grep 出的 RAG_RERANK_SKIP 现值; 前面两个是候选的 floor:ratio
./rageval -eval /root/khmer-deploy/rag_eval.json -user 7 -limit 5 \
  -configs "<floor>:<ratio>:<skip>,0.30:0.70:<skip>,0.33:0.75:<skip>"
```

选**能保住 dense 腿 recall 的最高 floor** (最保守的一档) → 写进 `.env-go` → 重启 → 重跑第 5 项。

### 10.7 回滚（一条环境变量 + 重启）

```bash
SCRIPT=$(cat <<'EOS'
set -e
cd /opt/khmer-ai-cs
cp -a .env-go .env-go.rollback-$(date +%Y%m%d%H%M%S)
grep -q '^GEMINI_API_BASE=' .env-go || {
  echo "⚠️ GEMINI_API_BASE 不在 .env-go 里 —— studio 回滚会失败, 先把中继地址补回来"; exit 1; }
sed -i 's/^GEMINI_PROVIDER=.*/GEMINI_PROVIDER=studio/' .env-go   # 直接删掉该行也行: 不设 = studio
systemctl restart khmer-ai-cs-go
sleep 2; systemctl is-active khmer-ai-cs-go
curl -s http://127.0.0.1:8081/ready; echo
EOS
)
B64=$(echo "$SCRIPT" | base64)
/tmp/khmer-deploy/sshrun.sh "echo $B64 | base64 -d | bash"
```

回滚后确认: `/ready` ok + 发一条真实消息拿到回答 + journal 里没有 `User location is not supported`。
> 回滚**不动** DB 的模型名 —— 第 1 步可以保留 (`gemini-3.5-flash` 在 studio 上也 200)。要让模型也回去, 才去改 `model_configs`。

### 10.8 收尾清理

**迁完 (观察够了、决定不再回滚 studio) 才做**:

| 资产 | 处置 | 注意 |
|---|---|---|
| SA 密钥 `/opt/khmer-ai-cs/vertex-sa.json` | **保留** —— 它是生产凭据; 只收紧权限: `chown khmerai:khmerai` + `chmod 600`, 留在 `/opt` 下 | 绝不进仓库 (`.gitignore` 无兜底规则)。轮换 = 控制台新建 key → 落位 → 重启验证 → 删旧 key (**先落新再删旧**) |
| AI Studio key (`GEMINI_API_KEY` / `model_configs.api_key`) | 二选一: 留作回滚余量, 或到 AI Studio 控制台**删除该 key** | ⚠️ 删 key **且** 清了 `GEMINI_API_BASE` 之后, studio 这条腿彻底死了; 而 `.env-go` 里 `GEMINI_PROVIDER` 一旦在后续编辑中被丢掉, **缺省值就是 studio** ⇒ 生产会指向一个已经不通的端点。要么三样 (provider 行 / 中继 / key) 一起留到不再需要回滚, 要么在收尾时记档"studio 已退役"并让监控盯住 `/ready` 与生成失败率 |
| `GEMINI_API_BASE` | **最后**再清 (见 10.5); 清之前再确认一次 `embedcmp` 与基线复现都不再需要 | 清掉后 `embedcmp` 路径 A 与 10.6 第 5 项的 studio 对照都跑不了 |
| `GEMINI_VERTEX_API_BASE` | 保持不设 | 只有中继/测试才覆盖; 值末尾不带 `/` |

**决定不迁 (或试完回退) 时**:

```bash
rm -f /opt/khmer-ai-cs/vertex-sa.json     # 服务器上的密钥文件
# 再到 GCP 控制台 → IAM 和管理 → 服务账号 → 删除刚建的服务账号 (它的 key 随之失效)
# .env-go 里的 GEMINI_PROVIDER / GEMINI_VERTEX_* 一并删掉 (不设 = studio)
```

> 只删文件不删服务账号 = 留一把仍在授权中的钥匙的副本; 只删账号不删文件 = 留一个再也用不上的密钥文件 (下次谁看到都会犹豫)。**两个一起做。**

### 10.9 一分钟速查

| 现象 | 判据 / 动作 |
|---|---|
| 切完 `/ready` 不通、systemd `failed` | 启动校验拦住了 (10.4 的表) → **先回滚** (10.7) 再修, 别让客户侧长时间无服务 |
| 探针 `6/6` 但真实请求仍 500 | 探针证的是平台能力, 不是服务配置 → 看 journal 里 `gemini returned HTTP …` 的原文 (错误已不再被吞掉) |
| `rageval` 掉到基线以下 | 先 10.1 分诊 (是否重嵌入), 再 10.6 第 6 项查 floor |
| 出现 429 (但不是 `spend-based`) | 平台按**区域 QPS** 配额, 与消费速率无关 → 去 GCP 控制台提配额; **不是**回滚信号 |
| 想快速回退 | 10.7: 一条 `GEMINI_PROVIDER` + 重启 (前提: `GEMINI_API_BASE` 还在) |
