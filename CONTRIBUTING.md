# 贡献指南

本仓库采用主干开发（trunk-based）：`main` 始终可发布，所有变更通过短生命周期分支和 Pull Request 进入，并以 squash 方式合并。本文档说明从切分支到合并的完整规范。

## 开发环境

- Go 1.22 及以上（以 `go.mod` 为准）
- 仅使用标准库，不引入第三方依赖
- 可选：`gh` CLI（命令行创建和查看 PR）；修改 `.github/workflows/` 时需要 `workflow` scope

## 工作流程

1. 同步主干并切分支
2. 改动代码与测试
3. 运行本地门禁 `make check`
4. 原子提交，信息符合约定
5. 推送并创建 Draft PR
6. CI 全绿后转正、评审、squash 合并
7. 删除分支并同步主干

```bash
git switch main && git pull --ff-only
git switch -c fix/12-symlink-escape

# ... 改动 ...

make check
git add -A
git commit -m "fix(readfile): compare path segments instead of prefixes"
git push -u origin HEAD
gh pr create --fill --draft

# CI 通过后
gh pr ready
gh pr merge --squash --delete-branch
git switch main && git pull --ff-only && git branch -d fix/12-symlink-escape
```

推荐全局配置，避免本地产生意外的 merge commit：

```bash
git config --global pull.ff only
```

## 分支命名

`<type>/<slug>`，需要时带上 issue 编号：

- `feat/read-file-guard`
- `fix/12-symlink-escape`
- `docs/pr-workflow`

`type` 与提交类型一致（见下）。约定：

- 从最新 `main` 切出，生命周期尽量不超过 3 天
- 合并前 `git rebase origin/main`，不要把 `main` merge 进分支
- 不使用 `develop`、`release/*` 等长期分支，也不直接向 `main` 推送

## 提交信息

采用 [Conventional Commits](https://www.conventionalcommits.org/)：

```text
<type>(<scope>): <subject>

<body>

<footer>
```

支持的 type：

| type | 用途 |
| --- | --- |
| `feat` | 用户可见的新能力 |
| `fix` | 缺陷修复 |
| `perf` | 性能优化 |
| `refactor` | 行为不变的结构调整 |
| `test` | 测试补充或修正 |
| `docs` | 文档 |
| `ci` | GitHub Actions 与流水线 |
| `build` | Makefile、构建与开发脚本 |
| `chore` | 依赖、配置等杂务 |
| `revert` | 回滚既有提交 |

常用 scope：`agent`、`llm`、`tool`、`readfile`、`cli`、`config`、`docs`、`ci`、`build`。

规则：

- subject 使用英文祈使句，小写开头，不加句号，不超过 72 字符
- body 说明“为什么”，而不是复述 diff；每行不超过 72 字符
- 一个提交只做一件事，且能独立通过编译
- 破坏性变更在 footer 写 `BREAKING CHANGE: ...`
- 关联 issue 用 `Closes #12`
- 分支内允许 `fixup!` 提交；由于合并时使用 squash，最终进入 `main` 的信息由 **PR 标题** 决定，标题必须符合上述格式

示例：

```text
fix(readfile): compare path segments instead of prefixes

前缀比较会把 /root-evil 误判为 /root 的子路径。改为按路径段
比较，并补充跨平台测试。

Closes #12
```

不推荐：

```text
update code
fix bug
```

## 本地门禁

`make check` 与 CI 执行同一套检查：

| 目标 | 内容 |
| --- | --- |
| `make check` | 依次执行下列全部检查 |
| `make fmt` | 检查 `gofmt -l .` 是否为空 |
| `make fix` | `gofmt -w .` 自动格式化 |
| `make vet` | `go vet ./...` |
| `make build` | `go build ./...` |
| `make test` | 竞态、随机顺序、覆盖率 |
| `make cross` | darwin/arm64 与 windows/amd64 交叉编译 |

可选启用推送前钩子：

```bash
git config --local core.hooksPath .githooks
```

启用后每次 `git push` 都会先跑 `make check`；紧急情况可用 `SKIP_GATE=1 git push` 跳过，但 CI 仍会拦截。

## Pull Request

- 标题即合并后的提交信息，格式同提交规范，例如 `feat(tool): add read_file size guard`
- 描述按模板填写：动机、方案、影响面、验证、风险与回滚
- 保持单一目的，净改动不超过 400 行、10 个文件；超出时在描述里说明拆分计划
- 未完成或预期 CI 失败时先开 Draft，通过后再 `gh pr ready`
- 行为、配置或 CLI 发生变化时，必须同步 `README.md`、`DESIGN.md` 或 `docs/` 下对应文档
- 不提交密钥、本地配置与调试输出；`config.json`、`.env` 已被忽略
- 自查 `gh pr diff` 后再请求评审

## 评审与合并

- 至少 1 名评审者；单人维护阶段由 CI 与自查清单兜底
- 评审关注正确性与边界、错误处理、测试有效性、文档一致性、安全性
- 只使用 **Squash and merge**；合并后自动删除分支，`main` 保持线性历史
- 合并后发现问题用 `git revert` 开 revert PR，不对 `main` 强制推送
- 紧急修复同样走 PR（`fix/hotfix-*`），合并后 24 小时内补充测试

维护者侧的完整流程（分诊、门禁、评论规范、合并与 fork 处理）见 [docs/review.md](docs/review.md)。

## 安全

- API Key 只通过环境变量注入（`llm.api_key_env`），禁止写入任何被跟踪的文件
- 修改 `.github/workflows/` 需要 token 具备 `workflow` scope，否则推送会被拒绝
- 发现密钥泄露：立即吊销凭证，清理历史，并通知维护者；公开仓库应视为已泄露

## 许可证

本仓库尚未声明开源许可证。补充 LICENSE 之前，外部贡献请先与维护者确认授权方式。
