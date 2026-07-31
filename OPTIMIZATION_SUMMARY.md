# 优化总结文档
# 日期：2026-07-31
# 作者：Codex 优化助手
# 仓库：sub2api-ext

## 总览
本次优化聚焦**性能、可维护性**，完成以下模块：
- ranking
- lottery
- patrol
- tasks
- ledger
- notify / report

所有改动已提交到 master。

## 各模块优化详情

### 1. ranking
- **问题**：每次请求都要全量聚合 + 批量拉名字，limit 时有冗余全量查询
- **优化**：
  - 添加 store 级内存缓存（30s TTL）
  - 移除 limit > 0 时额外调用全量查询
  - 使用 CTE + ROW_NUMBER() 替代 LIMIT
  - 添加复合索引 `idx_checkin_date_user`
- **效果**：高并发场景响应更稳
- **测试**：原 ranking_test 通过

### 2. lottery
- **问题**：上一轮“优化”将包改坏（类型重声明）
- **优化**：恢复原实现 + 保留原有抽奖公平性逻辑
- **效果**：无改动

### 3. patrol
- **问题**：cron 每秒重复 ParseCron + LoadLocation；stats 计数器锁竞争；NextCronAt 空置
- **优化**：
  - 缓存 cron 表达式与时区（同一分钟只匹配一次）
  - stats 计数器改为 atomic.Int64
  - 添加 `CronExpr.Next()` 填充 `next_cron_hint`
- **效果**：cron 调度更稳，日志更少

### 4. tasks
- **问题**：`/api/tasks` N+1 查询；`CountStreakBefore` 逐日查库
- **优化**：
  - 批量加载用户状态（今日签到/抽奖 + 周统计 + claims）
  - `CountStreakBefore` 改为一次区间查询 + 内存计算
  - 添加 `ListTaskClaimsByPeriods`
- **效果**：列表请求减少大量 DB 查询

### 5. ledger
- **问题**：`substr(created_at)` 破坏索引；`HasLedgerIdem` 用 COUNT
- **优化**：
  - 日期过滤改为 `[from, nextDay)` bounds（保持索引友好）
  - `HasLedgerIdem` 改 EXISTS/LIMIT 1
  - 添加 `idx_ledger_status_created`
- **效果**：admin 列表与导出更快

### 6. notify / report
- **问题**：日报多查询；notify 计数器 mutex；投递后去重失败
- **优化**：
  - 批量查询 `StatsByDates` / `LotteryStatsByDates`
  - 巡检查询去掉 `log_json`
  - notify 计数器改为 atomic + 优化 HTTP Transport
  - 投递成功后立即写入 `KeyLastSent`（手动/定时共用）
- **效果**：日报构建更快，去重更稳

## 部署状态
- 镜像 revision：2786dd4a4e2bdd9f46d9510460ea1e9897053a82（与本地 HEAD 一致）
- 容器运行正常，健康检查通过
- 模块已加载：checkin, ranking, tasks, ledger, account-patrol, notify, lottery, daily-report

## 后续建议
- 监控排名查询命中率
- 考虑在 `deploy-remote.ps1` 中添加 `-NoPrune` 标志
- 后续可继续优化 `notify` 的 webhook 重试策略

---

**优化总结已生成并提交**，仓库当前干净状态。
