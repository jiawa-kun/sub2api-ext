# 贡献指南

感谢参与 **sub2api-ext** 开发。本仓库通过 Pull Request 协作，`master` 受保护。

完整流程、分支约定与权限说明见：

- [docs/COLLABORATION.md](docs/COLLABORATION.md)

## 快速开始

1. Fork 本仓库（或已有 Write 权限时直接建分支）
2. 从最新 `master` 创建分支：
   - 功能：`feature/<简短描述>`
   - 修复：`fix/<简短描述>`
3. 完成本地改动与自测
4. 推送并打开 PR（会套用 PR 模板）
5. 等待 CI（`build-and-push`）通过，并获得至少 1 人 Approve
6. 合并方式建议 **Squash and merge**

## 提交前请确认

- 不提交 `.env`、数据库文件、密钥、生产私密配置
- 涉及余额 / 发放 / ledger / 回流等资金逻辑：在 PR 中明确勾选，并等待 owner 审查
- 改动范围尽量小，说明「改了什么 / 为什么改 / 如何自测」
- 生产部署默认由维护者执行，合并 PR 不会自动上服务器

## 相关文件

| 文件 | 用途 |
|------|------|
| [docs/COLLABORATION.md](docs/COLLABORATION.md) | 协作流程与权限 |
| [.github/pull_request_template.md](.github/pull_request_template.md) | PR 模板 |
| [.github/CODEOWNERS](.github/CODEOWNERS) | 代码负责人 |
| [LICENSE](LICENSE) | MIT 许可证 |
