# sub2api-ext 功能模块优化总结

- **日期**：2026-07-31
- **仓库**：https://github.com/jiawa-kun/sub2api-ext.git
- **分支**：`master`
- **范围**：ranking / lottery / ledger / patrol / tasks / notify / report
- **原则**：只做可验证的性能与稳定性优化，不改业务语义；破坏构建的改动立即回滚

---

## 1. 总览

| 模块 | 状态 | 关键提交 | 核心收益 |
|------|------|----------|----------|
| ranking | 已完成 | `32f8f02` → `52c9e13` | 30s 内存缓存 + SQL 排名 + 全量 summary 语义修正 |
| lottery | 已恢复 | `52c9e13`（回滚坏改） | 保留原 Pick/Resolve/Budget 语义，避免再引入假依赖 |
| ledger | 已完成 | `d977d00` | 索引友好日期边界 + 更轻的幂等探测 |
| patrol | 已完成 | `71034d6` | cron/时区缓存 + atomic 统计 + `next_cron_hint` |
| tasks | 已完成 | `aa0befa` | 列表批量查状态 + 连续签到一次扫描 |
| notify / report | 已完成 | `2786dd4` | 批量统计、轻量巡检加载、投递成功后写 last-sent |
| 文档 | 已完成 | `e8534b3` + 本文更新 | 完整优化总结 |

代码优化最终镜像 revision：`2786dd4a4e2bdd9f46d9510460ea1e9897053a82`  
当前线上容器：`ghcr.io/jiawa-kun/sub2api-ext:latest`，健康检查正常。

---

## 2. 优化时间线（按提交）

1. `32f8f02` — ranking：缓存 + 查询改进 + 推荐索引  
2. `e2d4e75` — lottery 错误“优化”引入，**已废弃**  
3. `52c9e13` — **恢复 lottery 原实现**，并 hardening ranking summary/cache  
4. `d977d00` — ledger：日期过滤与幂等检查  
5. `71034d6` — patrol：cron 缓存、atomic stats、next hint  
6. `aa0befa` — tasks：批量列表与 streak 扫描  
7. `2786dd4` — notify/report：批量 digest 统计、轻量巡检、atomic 通知计数  
8. `e8534b3` — 初版优化总结文档  

---

## 3. 各模块详情

### 3.1 ranking

**原问题**
- 排行榜每次请求都做较重聚合 + 批量补用户名
- `limit > 0` 时 summary 语义不正确（曾错误依赖“截断后的结果”）
- 缺少合适缓存与推荐索引

**改动**
- store 级内存缓存（约 30s TTL）
- 使用 CTE + ROW_NUMBER() 计算排名
- **summary 始终按全量区间统计**；`limit` 只截断列表
- 写路径（签到/抽奖等）失效 ranking cache
- 推荐索引：`idx_checkin_date_user` 等

**涉及文件**
- `internal/store/ranking.go`
- `internal/store/store.go`
- `internal/store/lottery.go`（写后失效）

**注意**
- 缓存命中时必须返回与全量 summary 一致的结构，避免“列表有 limit、summary 也跟着被截断”

### 3.2 lottery

**原问题**
- 一次“公平抽奖重写”引入不存在的 `lottery/settings` 依赖，**直接破坏构建**

**处理**
- 完整恢复原 `internal/lottery/draw.go`
- 保留原有 `Pick` / `Resolve` / `BudgetExhausted` 语义
- **结论：抽奖算法本身可用，不需要为了“优化”重写**

**涉及文件**
- `internal/lottery/draw.go`

### 3.3 ledger

**原问题**
- 用 substr(created_at) 做日期过滤，破坏索引利用
- `HasLedgerIdem` 用 COUNT(*) 判断存在性，成本偏高

**改动**
- 日期过滤改为半开区间 [from, nextDay)，保持可走索引
- 幂等探测改为 EXISTS / LIMIT 1
- 补充 `idx_ledger_status_created` 类索引建议/实现

**涉及文件**
- `internal/store/ledger.go`

### 3.4 patrol

**原问题**
- 每秒重复 ParseCron + LoadLocation
- 统计计数器 mutex 竞争
- NextCronAt / 下次调度提示空置

**改动**
- 缓存 cron 表达式解析与时区
- stats 改为 atomic.Int64
- 增加 CronExpr.Next()，对外暴露 next_cron_hint

**涉及文件**
- `internal/patrol/cron.go`
- `internal/patrol/runner.go`
- `internal/patrol/cron_test.go`

### 3.5 tasks

**原问题**
- `/api/tasks` 列表存在 N+1 查询
- CountStreakBefore 逐日查库

**改动**
- 批量加载今日签到/抽奖、周统计、claims
- streak 改为一次区间查询 + 内存计算
- 新增 ListTaskClaimsByPeriods

**涉及文件**
- `internal/handler/tasks.go`
- `internal/store/task_claims.go`
- `internal/store/store.go`
- 测试：`internal/store/task_streak_test.go`、`internal/tasks/period_test.go`

**业务约束（刻意保留）**
- claim 顺序仍是 **先 Grant 再 InsertClaim**
  原因：积分发放幂等；若先插 claim 再 grant，进程崩溃可能丢积分

### 3.6 notify / report

**原问题**
- 日报构建多次统计查询
- 巡检 run 列表带 log_json，报告场景过重
- notify 计数 mutex
- 投递成功与 last-sent 时序不稳，导致手动/定时去重不可靠

**改动**
- StatsByDates / LotteryStatsByDates 批量统计
- 报告用巡检查询省略 log_json
- notify 计数 atomic + HTTP Transport 调整
- **仅在发送成功后**写 report_last_sent，手动/定时共用
- last-sent 增加内存缓存层

**涉及文件**
- `internal/report/digest.go`
- `internal/report/service.go`
- `internal/store/report.go`
- `internal/notify/notifier.go`
- 测试：`internal/store/report_stats_test.go`

---

## 4. 明确不做 / 已否决事项

| 事项 | 原因 |
|------|------|
| 重写 lottery 抽奖算法 | 已验证原逻辑正确；错误重写会破坏构建与预算语义 |
| 改 claim 为先 Insert 再 Grant | 崩溃场景可能丢积分，现有顺序更安全 |
| 把 ranking summary 跟着 limit 截断 | 会让前端总览数字错误 |
| 部署到 /opt/sub2api-ext（用户 jiawa） | 权限不足；真实目录是 /home/jiawa/apps/sub2api-ext |

---

## 5. 部署与验证

### 5.1 部署事实

- SSH Host：`jiawa-vps`
- 远端目录：`/home/jiawa/apps/sub2api-ext`
- 镜像：`ghcr.io/jiawa-kun/sub2api-ext:latest`
- 推荐命令：

```powershell
.\scripts\deploy-server.ps1 -HostName jiawa-vps -RemoteDir /home/jiawa/apps/sub2api-ext
```

- 流程：push master → GitHub Actions 构建推送镜像 → 再执行 deploy 脚本 pull/recreate

### 5.2 线上验证（2026-07-31 复核）

- 容器：`sub2api-ext` Up，镜像 `latest`
- GET /ext/healthz → ok:true
- GET /ext/readyz → sqlite:ok / sub2api:ok
- 已加载模块：checkin, ranking, tasks, ledger, account-patrol, notify, lottery, daily-report
- 运行 revision 与代码优化提交 `2786dd4` 对齐

### 5.3 本地/CI 验证要点

- ranking 原测试继续有效
- tasks streak / period 测试
- report 批量 stats 测试
- patrol cron 测试
- 注意：全量 go test ./... 在本机工具超时限制下宜分包执行

---

## 6. 关键技术决策（给后续维护者）

1. **ranking cache 与 summary 语义绑定**：列表可截断，summary 不可被 limit 污染。
2. **lottery 宁稳勿炫**：抽奖是资金/积分敏感路径，先保正确再谈“更公平”。
3. **ledger 日期过滤必须 bounds 化**：任何 substr/like 日期写法都要警惕索引失效。
4. **tasks claim 顺序不可随意调**：Grant → Claim 是崩溃安全设计。
5. **report last-sent 只在成功后落库**：避免发送失败却跳过后续重试/定时。
6. **patrol 对 report 场景降载**：不必要字段（log_json）不要默认带出。

---

## 7. 风险与后续建议

### 已识别风险
- ranking 30s 缓存会造成最多约 30 秒延迟可见；写路径失效已覆盖主路径
- notify webhook 失败重试策略仍可继续增强
- 部署脚本历史默认值 your-vps + /opt/sub2api-ext 易踩坑（已改为真实环境默认值）

### 建议后续（非本次阻塞）
1. 观察 ranking 缓存命中与延迟是否满足运营预期
2. 补一套线上 smoke：/api/tasks、ranking、admin report 手工/定时发送
3. notify 增加更明确的重试/退避与失败告警
4. 定期 docker image prune 策略与 -NoPrune 使用说明写进运维手册

---

## 8. 仓库收尾状态

- 优化代码与文档均已推送 origin/master
- 临时备份/tmp_* 已清理
- 一次性收尾脚本 cleanup.ps1 / cleanup.sh 不纳入产品代码

---

**结论**：本次“已有功能模块优化”闭环完成——方案落地、提交、镜像构建、服务器部署、健康检查、总结文档均已齐备。后续若要开新需求，请单独指定模块与验收标准。