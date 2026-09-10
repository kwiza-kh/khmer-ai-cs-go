# 部署命令手册（完整版）

SKILL.md 工作流的命令级详细版, 按顺序执行。远程命令通过 SSH_ASKPASS 机制 (见 SKILL.md「SSH 连接」), 下文以 `sshrun "命令"` 代指。上传用 `scprun`。

## 0. 前置：封装 ssh / scp（macOS）

```bash
mkdir -p /tmp/khmer-deploy
cat > /tmp/khmer-deploy/askpass.sh <<'EOF'
#!/bin/bash
echo "${KHMER_SSH_PASSWORD:-}"
EOF
chmod +x /tmp/khmer-deploy/askpass.sh
export KHMER_SSH_PASSWORD='<密码, 末尾两个点>'   # 只放环境变量, 不写进文件/仓库

cat > /tmp/khmer-deploy/sshrun.sh <<'EOF'
#!/bin/bash
DISPLAY=:0 SSH_ASKPASS=/tmp/khmer-deploy/askpass.sh SSH_ASKPASS_REQUIRE=force \
ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o ConnectTimeout=20 -o NumberOfPasswordPrompts=1 -o PubkeyAuthentication=no \
    root@38.55.192.90 "$@" 2>&1 | grep -v "Warning: Permanently added"
EOF
cat > /tmp/khmer-deploy/scprun.sh <<'EOF'
#!/bin/bash
DISPLAY=:0 SSH_ASKPASS=/tmp/khmer-deploy/askpass.sh SSH_ASKPASS_REQUIRE=force \
scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
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
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/khmer-deploy/server-go  ./cmd/server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/khmer-deploy/migrate-go ./cmd/migrate
file /tmp/khmer-deploy/server-go   # 确认 "ELF 64-bit ... x86-64"
```

> 本机没装 Go 时: `brew install go` (需 ≥1.26, 见 go.mod)。线上现行二进制为 go1.26.5 构建。
> 二进制名固定 `server-go` / `migrate-go` (与 systemd ExecStart 一致)。想压缩可加 `-ldflags='-s -w'`。

## 2. 本地构建前端（Next 16 standalone）

```bash
cd <仓库>/frontend
# ⚠️ 关键: API_BASE 是构建时烘焙进 bundle 的 (src/lib/auth-client.tsx:
#    process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080/api/v1")
#    忘设 = 线上前端打用户本机的 localhost → 接口全挂
NEXT_PUBLIC_API_URL=https://cs.wanfanginsulationmaterial.com/api/v1 npm run build

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
/tmp/khmer-deploy/scprun.sh /tmp/khmer-deploy/server-go /tmp/khmer-deploy/migrate-go /tmp/khmer-deploy/khmer-fe.tgz root@38.55.192.90:/root/khmer-deploy/
# scp 退出码可能是管道误报, 以远端 ls 为准:
/tmp/khmer-deploy/sshrun.sh "ls -lh /root/khmer-deploy/"
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
curl -s -X POST https://cs.wanfanginsulationmaterial.com/api/v1/auth/login \
     -H "Content-Type: application/json" -d '{"username":"__probe__","password":"***"}' \
     -w "\n%{http_code}\n"    # 期望 JSON + 401/4xx (非 5xx/521)
curl -s -o /dev/null -w "%{http_code}\n" https://cs.wanfanginsulationmaterial.com/   # 200
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
    server_name cs.wanfanginsulationmaterial.com;
    return 301 https://$host$request_uri;
}
server {
    listen 443 ssl; listen [::]:443 ssl;
    http2 on;
    server_name cs.wanfanginsulationmaterial.com;

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
上传 `server-go`/`migrate-go` → `migrate-go` (001~034 全量 + `INITIAL_ADMIN_PASSWORD` 建管理员) →
落 systemd unit (SKILL.md 模板) → `systemctl daemon-reload && systemctl enable --now khmer-ai-cs-go khmer-ai-cs-web` →
nginx 站点 (§8) + 自签证书 (复用 `/etc/nginx/ssl/wms.*` 或 openssl 新签) → Cloudflare 加 `cs` A 记录 + SSL Full (见 cloudflare.md) → 前端发布 (§5)。
