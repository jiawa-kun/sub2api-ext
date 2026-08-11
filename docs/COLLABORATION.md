# 协作说明（sub2api-ext）

## 分支
- `master`：受保护，只接受 PR
- 功能分支：`feature/<简短描述>`
- 修复分支：`fix/<简短描述>`

## 流程
1. 从最新 `master` 拉分支
2. 本地改完并自测
3. `git push -u origin <branch>` 后开 PR
4. 等待 CI（`build-and-push`）通过
5. 至少 1 人 Review Approve 后合并（建议 Squash）
6. 生产部署由维护者执行（默认不自动部署服务器）

## 权限建议
| 角色 | 给谁 |
|------|------|
| Admin | 仓库所有者（少数） |
| Write | 开发协作者 |
| Triage | 只管 Issue/PR 的人 |
| Read | 只读 |

## 注意
- 不要提交 `.env`、`*.db`、密钥、生产配置
- 资金/发放相关改动必须在 PR 标明，并等 owner 审查
- Fork 来的 PR 合并前再发正式镜像
