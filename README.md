# Sub2API 扩展（sub2api-ext）

独立于 Sub2API 官方镜像的 **扩展服务**（Go sidecar）。

> 项目名 / 镜像 / 容器名：**`sub2api-ext`**  
> 产品标识：**`sub2api-ext`**（Sub2API 扩展）  
> 对外 HTTP 路径前缀为 **`/ext`**（不再使用 `/checkin`）。  
> **每日签到**是当前内置的第一个扩展模块；**账号模型巡检**为第二个内置模块（可配置定时任务）。

## 定位

- 通过 Sub2API **自定义菜单 iframe** 挂载扩展能力
- 共享用户 JWT 识别、管理员 API Key、加余额等基础设施
- 按模块扩展：现在是签到，后续可继续加兑换、邀请、公告等
- Sub2API 继续 `docker pull` 官方镜像；本服务单独升级

### 入口

| 入口 | URL | 说明 |
|---|---|---|
| 签到（默认菜单） | `/ext/` | 默认签到页（自定义菜单请改到此路径） |
| 扩展中心 | `/ext/home.html` | 模块总览（新） |
| 管理台 | `/ext/admin.html` | 扩展管理（签到 + 账号巡检） |
| 巡检锚点 | `/ext/admin.html#patrol` | 直达账号模型巡检配置 |
| 健康检查 | `/ext/healthz` | 含 `product` / `modules` 字段 |
| 模块列表 API | `/ext/api/modules` | 公开，供首页渲染 |

自定义菜单 URL 必须带尾斜杠：`https://your-sub2api.example.com/ext/`  
若希望侧栏先看扩展总览，可改为：`https://your-sub2api.example.com/ext/home.html`


## 目录结构

```
cmd/server/                入口（注册平台路由 + 模块 API）
internal/
  modules/                 扩展模块注册表（产品标识 / 模块元数据）
  config/                  配置加载（yaml + 环境变量）
  store/                   SQLite（签到记录 + app_settings + patrol_runs）
  settings/                运行时签到额度（可热更新）
  patrol/                  账号模型巡检（cron + runner + 配置）
  sub2api/                 调 Sub2API（用户识别 / 加余额 / 账号测活）
  handler/                 HTTP API（平台 + 签到 + 巡检）
web/static/
  index.html               用户签到页（默认入口，兼容菜单）
  home.html                扩展中心（模块总览）
  admin.html               扩展管理台（签到 + 账号巡检）
configs/                   配置示例
deploy/                    Nginx 完整配置 / 片段
scripts/deploy-server.ps1  一键部署脚本（参数可改）
docker-compose.yml
Dockerfile                 多阶段构建（GHCR 发布用）
.github/workflows/          GitHub Actions 构建并推送镜像
```

> 架构方向：`modules` 描述能力清单；签到业务仍在现有 handler/store/settings 中。  
> 后续新能力以新模块 id 注册，并挂独立页面 / API，无需改 Sub2API 本体。

## 功能说明

### 扩展平台

1. 打开 `/ext/home.html` 查看已启用模块
2. `GET /api/modules` 返回产品标识与模块列表
3. 默认菜单仍可直达签到页，不强制经过扩展中心

### 用户（签到模块）

1. 登录 Sub2API
2. 侧栏「每日签到」进入 iframe
3. 查看余额 / 今日奖励，点击签到
4. 同一自然日只能成功一次

### 账号模型巡检

把原油猴「账号模型巡检并自动下线」做成服务端可配置定时任务：

1. 管理页 `/ext/admin.html#patrol` 配置：
   - 是否启用定时巡检（默认关闭）
   - Cron（5 段，默认 `0 */6 * * *` 每 6 小时）
   - 分组列表（必填，逗号分隔）
   - 测试模型（默认 `gpt-5.4`）
   - 失败动作：`disable` / `delete` / `none`
   - 并发、超时、仅可调度、成功自动重新启用等
2. 支持「立即巡检 / 停止 / 查看最近运行与日志」
3. 鉴权复用服务端 Admin API Key（`SUB2API_ADMIN_API_KEY`）
4. 运行摘要写入 SQLite `patrol_runs`（默认保留 50 次）

管理 API：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET/PUT | `/api/admin/patrol/settings` | 读取/更新巡检配置 |
| GET | `/api/admin/patrol/status` | 当前状态 + 最近运行 |
| POST | `/api/admin/patrol/run` | 手动触发一次 |
| POST | `/api/admin/patrol/stop` | 请求停止当前任务 |

环境变量示例：

```env
PATROL_ENABLED=false
PATROL_CRON=0 */6 * * *
PATROL_GROUPS=group-a,group-b
PATROL_TEST_MODEL=gpt-5.4
PATROL_ACTION_ON_FAIL=disable
PATROL_CONCURRENCY=8
PATROL_TIMEOUT_MS=45000
```

> 注意：启用前请先配置分组与 Admin API Key。失败动作选 `delete` 会真实删除上游账号，请谨慎。

### 管理员

1. 打开 `/ext/admin.html`（需管理员登录）
2. 配置：
   - 是否启用签到
   - **奖励模式** `reward_mode`：
     - `fixed`：固定额度 `reward_amount`
     - `random`：区间随机 `reward_min` ~ `reward_max`（均匀，精度 0.0001）
   - 两种模式并存，管理页点选即可切换
   - 时区（默认 `Asia/Shanghai`）
   - 调账备注前缀
3. 保存后立即生效（写入 SQLite，无需改 `.env` 重启）

也可用 API 配置（见下文）。

## 环境变量 / 配置

复制示例：

```bash
cp .env.example .env
# 或
cp configs/config.example.yaml configs/config.yaml
```

| 变量 | 说明 |
|---|---|
| `SUB2API_BASE_URL` | 容器内访问 sub2api，生产为 `http://sub2api:8080` |
| `SUB2API_ADMIN_API_KEY` | **推荐** 管理员 API Key（`x-api-key`，长期有效） |
| `SUB2API_ADMIN_TOKEN` | 或管理员登录 JWT（会过期） |
| `CHECKIN_ENABLED` | 启动默认开关（可被管理页覆盖） |
| `CHECKIN_REWARD_AMOUNT` | 启动默认额度（可被管理页覆盖） |
| `CHECKIN_TIMEZONE` | 启动默认时区 |
| `SERVER_ADDR` | 默认 `:8090` |
| `SERVER_BASE_PATH` | 反代前缀，生产为 `/ext` |
| `SQLITE_PATH` | 默认 `/data/checkin.db` 或 `./data/checkin.db` |

> 管理页改过的额度/开关保存在 SQLite `app_settings`，优先于启动时的环境变量默认值。

### 管理员凭证

**推荐：管理员 API Key**

1. Sub2API 后台 → **管理员 API Key / 外部系统集成**
2. 写入 `.env`：

```env
SUB2API_ADMIN_API_KEY=admin-xxxxxxxx
```

本服务会自动用请求头：`x-api-key: <key>`。

**备选：管理员 JWT**

```env
SUB2API_ADMIN_TOKEN=<auth_token>
```

约 24 小时过期，需定期更换。  
普通用户 API Key（`sk-`）**不能**调 Admin 接口。

## API

### 模块列表（平台）

```http
GET /ext/api/modules
```

返回示例字段：`product`、`product_name`、`compat_name`、`base_path`、`modules[]`。

### 健康检查（平台）

```http
GET /ext/healthz
```

除 `ok` 外包含 `product` / `modules`。


基础路径生产为 `/ext`，本地无前缀时为 `/`。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/healthz` | 健康检查 |
| GET | `/api/status` | 签到状态（用户 JWT） |
| POST | `/api/checkin` | 执行签到（用户 JWT） |
| GET | `/api/calendar` | 用户签到月历 |
| GET | `/api/admin/settings` | 读配置（管理员） |
| PUT/POST | `/api/admin/settings` | 改配置（管理员，立刻生效；写审计） |
| GET | `/api/admin/settings/audit` | 最近配置变更审计（默认 10 条） |
| GET | `/api/admin/stats` | 今日签到统计与近 7 日 |
| GET | `/api/admin/checkins` | 当日签到明细 |
| POST | `/api/admin/settings/rollback` | 按审计记录回滚配置 |

用户请求可带：

- `Authorization: Bearer <user_jwt>`
- 和/或 `X-User-Token`
- 和/或 `?token=`

管理员请求可带：

- 管理员 JWT：`Authorization: Bearer ...`
- 或服务端配置的 Admin Key：`x-api-key: ...`

### 修改额度示例

```bash
# 固定模式
curl -X PUT https://your-sub2api.example.com/ext/api/admin/settings \
  -H "x-api-key: 你的管理员API_Key" \
  -H "Content-Type: application/json" \
  -d '{"enabled":true,"reward_mode":"fixed","reward_amount":0.5,"timezone":"Asia/Shanghai"}'

# 随机模式：每次签到 $0.1 ~ $1.0
curl -X PUT https://your-sub2api.example.com/ext/api/admin/settings \
  -H "x-api-key: 你的管理员API_Key" \
  -H "Content-Type: application/json" \
  -d '{"enabled":true,"reward_mode":"random","reward_min":0.1,"reward_max":1.0,"timezone":"Asia/Shanghai"}'
```


## 镜像与部署（GHCR）

本项目通过 GitHub Actions 构建并推送到 GitHub Container Registry：

```text
ghcr.io/jiawa-kun/sub2api-ext:latest
ghcr.io/jiawa-kun/sub2api-ext:sha-<commit>
```

推送到 `master`/`main` 或打 `v*` tag 会自动构建。也可在 Actions 页手动 **Run workflow**。

### 服务器部署（拉镜像）

1. 准备目录与 `.env`（**不要提交真实密钥**）
2. 使用仓库中的 `docker-compose.yml`
3. 拉取并启动：

```bash
cd /opt/sub2api-ext
# 可选：固定版本
# export IMAGE=ghcr.io/jiawa-kun/sub2api-ext:latest
docker compose pull
docker compose up -d
curl -sS http://127.0.0.1:8090/ext/healthz
```

Windows 一键部署（上传 compose 后远程 pull）：

```powershell
.\scripts\deploy-server.ps1 -HostName your-vps -RemoteDir /opt/sub2api-ext
.\scripts\deploy-server.ps1 -Image ghcr.io/jiawa-kun/sub2api-ext:sha-xxxx
```

> 若 GHCR 包为私有，服务器需先 `docker login ghcr.io`。公开仓库建议把 Package 设为 Public。

## 本地运行

```bash
# Go 1.22+
go mod tidy

# Windows PowerShell
$env:SUB2API_ADMIN_API_KEY="admin-xxx"
$env:SUB2API_BASE_URL="https://your-sub2api.example.com"   # 或本机 sub2api
go run ./cmd/server -config configs/config.yaml
```

打开：

- 签到页：`http://127.0.0.1:8090/`
- 管理页：`http://127.0.0.1:8090/admin.html`

（iframe 场景下会自动读同源 `localStorage` 的 `auth_token`。）

## 部署到服务器

### 一键部署（本机 Windows）

```powershell
cd E:\Projects\GoProjects\sub2api-ext
.\scripts\deploy-server.ps1
```

流程：交叉编译 Linux 二进制 → scp → 远端 `docker build` + `compose up`。  
**不会覆盖** 远端已有 `.env`。

常用参数：

```powershell
.\scripts\deploy-server.ps1 -Logs    # 看日志
.\scripts\deploy-server.ps1 -Down    # 停容器
.\scripts\deploy-server.ps1 -NoCache # 无缓存构建
```

### Nginx

生产配置见 `deploy/nginx-full-sub2api.conf`，要点：

- `location /ext/` 在 `location /` 之前
- 转发 `Authorization`、`X-User-Token`
- 隐藏上游可能带来的 `X-Frame-Options: DENY`，并设置 `frame-ancestors`

```bash
sudo cp /opt/sub2api-ext/deploy/nginx-full-sub2api.conf /etc/nginx/sites-available/sub2api
sudo nginx -t && sudo systemctl reload nginx
```

### Docker Compose 要点

- 镜像：`sub2api-ext:local`
- 端口：`127.0.0.1:8090:8090`
- 网络：加入 `sub2api-deploy_sub2api-network` 以便访问 `http://sub2api:8080`
- 数据：`./data:/data`

## Sub2API 自定义菜单

Admin → 自定义菜单：

| 项 | 值 |
|---|---|
| 名称 | 每日签到 |
| URL | `https://your-sub2api.example.com/ext/` |
| visibility | user |

## 架构与鉴权说明

```
浏览器 iframe
  → 读 localStorage auth_token / refresh_token
  → 签到服务
       ├─ 尝试 auth/me（并透传真实客户端 IP/UA）
       ├─ 若 SESSION_BINDING_MISMATCH：解析 JWT user_id + Admin 查用户
       └─ 加余额：Admin API Key → POST /admin/users/:id/balance
```

为何需要 Admin 凭证：

- 用户 token 在**容器内**回放时，源 IP 变成 Docker 内网，易触发  
  `SESSION_BINDING_MISMATCH`
- 加余额本身也是 Admin 接口

## 常见问题

| 现象 | 原因 | 处理 |
|---|---|---|
| iframe「内容被屏蔽」 | 请求落到主站 SPA，`X-Frame-Options: DENY` | 确认 Nginx `/ext/` 反代正确 |
| invalid token / 登录失效 | access 过期或会话指纹 | 重新登录；已支持 refresh + Admin 兜底 |
| `IDEMPOTENCY_KEY_INVALID` | Key 含空格 | 已修复（清洗 notes） |
| `server missing SUB2API_ADMIN_API_KEY` | 未配置 Admin 凭证 | 写入 `.env` 并 recreate |
| 普通 `sk-` 不能用 | 那是模型网关 Key | 用「管理员 API Key」 |
| 改了 `.env` 额度没变 | 管理页配置在 SQLite 优先 | 到 `/admin.html` 改，或清 `app_settings` |



## 安全与月历（v4）

| 项 | 默认 |
|----|------|
| CORS / frame-ancestors | `your-sub2api.example.com` / `your-sub2api-alt.example.com` + 本机 loopback |
| 签到限流 | 10/分钟/用户+IP |
| 管理写限流 | 30/分钟 |
| 敏感写 | hard_cap/奖励>10 或改日预算 需服务端 Admin Key |
| 里程碑 | 第3天 +0.05、第7天 +0.2（可配） |
| 月历 | `GET /api/calendar?year=&month=` |

## 运维与进阶（v3）

| 端点 | 说明 |
|------|------|
| `GET /readyz` | SQLite + Sub2API 可达性 |
| `GET /metrics` | 签到计数 JSON；`?format=prom` 或 Accept text/plain 为 Prometheus 文本 |

| 能力 | 说明 |
|------|------|
| 配置模板 | `POST /api/admin/settings/template` `{"name":"daily\|promo\|off"}` |
| clamp 持久化 | 启动时若发现超额配置被收敛，写入 SQLite |
| 幂等加固 | 调账失败重试 1 次；识别幂等命中不删本地记录 |
| 连续签到 | `streak_enabled` / `streak_step` / `streak_max_days`；奖励=基础+min(N-1,max-1)*step，再套硬顶与日预算 |
| 备份 | `scripts/backup-sqlite.ps1` 本地；`-HostName your-vps` 远端 |

## 风控与统计（v2）

| 配置 | 默认 | 说明 |
|------|------|------|
| hard_cap | 5 | 单次奖励硬顶（系统绝对上限 100） |
| daily_budget | 50 | 当日发放总额上限，0=不限 |
| budget_action | block | 用尽时 block 拒绝 / disable 自动关签到 |

- 幂等 Key：`checkin-{userID}-{YYYY-MM-DD}`
- `GET /api/admin/stats` 今日人数/总额/预算剩余/近7日
- `GET /api/admin/checkins` 当日明细
- `POST /api/admin/settings/rollback` body `{"audit_id":N}` 回滚配置

加载时若配置奖励超过硬顶会 **clamp** 并在管理页提示。

## 管理页能力（/admin.html）

- **鉴权（二选一）**
  - 管理员登录 JWT（iframe 同源 localStorage / 本页登录态）
  - 粘贴 **Admin API Key** 或管理员 JWT（仅存 `sessionStorage`，不写 URL query）
- **立刻生效**：保存写入 SQLite `app_settings`，无需重启
- **体验**：当前生效摘要、脏检查、时区下拉、关闭/改时区确认、大额（>$10）二次确认（后端需 `confirm_high_amount=true`）
- **随机试算**：管理页前端模拟 10 次抽奖
- **审计**：每次保存写入 `settings_audit`（操作者 / 来源 ui|api / 改前改后 JSON），页底展示最近 10 条

```bash
# 读审计
curl -sS "https://your-sub2api.example.com/ext/api/admin/settings/audit?limit=10" \
  -H "x-api-key: 你的管理员API_Key"

# 大额修改（需确认）
curl -X PUT "https://your-sub2api.example.com/ext/api/admin/settings" \
  -H "x-api-key: 你的管理员API_Key" \
  -H "Content-Type: application/json" \
  -d "{\"enabled\":true,\"reward_mode\":\"fixed\",\"reward_amount\":12,\"confirm_high_amount\":true,\"source\":\"api\"}"
```


## 安全注意

1. **Admin API Key / JWT 只放服务器 `.env`**，不要提交 git、不要贴到公开聊天
2. 用户 token 可能出现在 iframe URL / 查询参数，注意访问日志脱敏
3. 签到服务只监听 `127.0.0.1:8090`，对外走 HTTPS 反代
4. 若 Admin Key 曾泄露，在 Sub2API 后台轮换后更新 `.env` 并重启

## 更新清单（相对早期版本）

- [x] 管理员 API Key（`x-api-key`）
- [x] 管理页动态配置额度
- [x] 管理页双鉴权（JWT + API Key）与配置审计
- [x] `SESSION_BINDING_MISMATCH` 兜底
- [x] Idempotency-Key 合法化
- [x] iframe 嵌入头 / Nginx 反代
- [x] 一键部署脚本
