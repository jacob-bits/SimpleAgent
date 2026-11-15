# PR 规范：提交方与接受方

> 本文汇总一次 Pull Request 从提交到合并的全部约束：提交方必须满足什么，接受方必须检查什么，仓库侧用哪些机器规则兜底。
> 快速上手见 [CONTRIBUTING.md](../CONTRIBUTING.md)，评审细节见 [review.md](review.md)，Agent 身份隔离见 [agent-identity.md](agent-identity.md)。

## 1. 角色与权限

| 角色 | 账号 | 权限 | 职责 |
| --- | --- | --- | --- |
| 提交方 | `unzip-zs` | 上游只读，fork 可写 | 切分支、提交、开 PR、响应评审 |
| 接受方 | `jacob-bits` | 上游 admin | 分诊、评审、合并、维护规则 |
| CI | GitHub Actions | 只读 | 格式、构建、测试、跨平台编译 |

- 提交方只通过 fork 提交，不直接向上游推分支
- 接受方不能批准自己提交的 PR，两个账号天然隔离
- 只允许 squash 合并，`main` 保持线性

## 2. 全流程

```text
sync main ─► branch ─► atomic commits ─► make check ─► push fork ─► PR
                                                                     │
                                           CI 3 checks ─► review ─► squash merge
                                                                     │
                                                            delete branch ─► sync
```

状态机：`draft → ready → ci-green → approved → merged`。任一环节失败退回分支，不允许绕过。

## 3. 提交方规范

### 3.1 分支

- 命名 `<type>/<slug>`，需要时带 issue 编号，例如 `fix/12-symlink-escape`
- 从最新 `main` 切出；合并前 `git rebase origin/main`，禁止把 `main` merge 进分支
- 生命周期不超过 3 天；不使用 `develop`、`release/*` 等长期分支
- 一个分支只做一件事，对应一个 PR

### 3.2 提交信息

格式：`<type>(<scope>): <subject>` + 空行 + body + footer

| type | 用途 |
| --- | --- |
| `feat` | 用户可见的新能力 |
| `fix` | 缺陷修复 |
| `perf` | 性能优化 |
| `refactor` | 行为不变的结构调整 |
| `test` | 测试 |
| `docs` | 文档 |
| `ci` | 流水线 |
| `build` | Makefile、构建脚本 |
| `chore` | 杂务 |
| `revert` | 回滚 |

常用 scope：`agent`、`llm`、`tool`、`readfile`、`cli`、`config`、`docs`、`ci`、`build`。

硬性规则：

- subject 英文祈使句、小写开头、无句号、不超过 72 字符
- body 说明“为什么”，每行不超过 72 字符
- 一个提交一个逻辑单元，且能独立编译通过
- 破坏性变更在 footer 写 `BREAKING CHANGE: ...`
- 关联 issue 用 `Closes #12`
- 分支内允许 `fixup!`；合并时 squash，最终历史由 **PR 标题** 决定

### 3.3 PR

- 标题即 squash 后的提交信息，必须符合 3.2
- 描述使用模板：动机、方案、影响面、验证、风险与回滚、自查清单
- 单一目的：净改动不超过 400 行、10 个文件；超出需说明拆分计划
- 未完成或预期 CI 失败时先开 Draft，通过后再 `gh pr ready`
- 行为、配置、CLI 变化必须同步 `README.md`、`DESIGN.md`、`docs/`
- 不提交密钥、本地配置、调试输出

### 3.4 本地门禁

`make check` 与 CI 执行同一套命令：

| 目标 | 内容 |
| --- | --- |
| `make check` | 依次执行以下全部 |
| `make fmt` | `gofmt -l .` 必须为空 |
| `make fix` | 自动格式化 |
| `make vet` | `go vet ./...` |
| `make build` | `go build ./...` |
| `make test` | `-race -shuffle=on -count=1` + 覆盖率 |
| `make cross` | darwin/arm64、windows/amd64 交叉编译 |

### 3.5 身份与推送

- 提交身份固定为 `unzip-zs <97578602+unzip-zs@users.noreply.github.com>`
- 推送目标为 fork：`git push fork <branch>`，不直推上游
- 凭据来自 `.agent/token` 与仓库级 credential helper
- 全局 `gh auth setup-git` 写入的 helper 会排在仓库配置之前，必须在仓库内用空值重置
- 完整配置与轮换见 [agent-identity.md](agent-identity.md)

### 3.6 提交前自查

- [ ] 分支名符合约定，基于最新 `main`
- [ ] 提交信息符合 Conventional Commits，一个提交一件事
- [ ] `make check` 全绿
- [ ] 新增或修改的逻辑有测试，覆盖失败路径
- [ ] 文档已同步
- [ ] 无密钥、无本地配置、无调试输出
- [ ] PR 描述包含动机、方案、验证与回滚

## 4. 接受方约束

### 4.1 分诊

| 检查项 | 判断 | 处理 |
| --- | --- | --- |
| 是否 Draft | 作者尚未完成 | 只给方向性意见 |
| 是否关联 issue | feat/fix 应有背景 | 先问背景再评审 |
| 规模 | 超过 400 行或 10 个文件 | 要求拆分或说明理由 |
| 方向 | 偏离 DESIGN.md 的目标与非目标 | 不进入逐行评审，先讨论 |
| 首次贡献者 | fork 的 workflow 待批准 | 先点 “Approve and run workflows” |
| 重复或过时 | 已有替代实现 | 关闭并指向替代 |

### 4.2 机器门禁

- 必过检查：`Test`、`Build (macos-latest)`、`Build (windows-latest)`
- 合并状态为 `MERGEABLE`，无冲突，且与 `main` 同步（strict 策略）
- PR 标题符合提交规范
- 无密钥、大文件、二进制

### 4.3 本地复现

`gh pr checkout <n>` → `make check` → 跑真实场景核对描述中的证据。

安全前提：外部 PR 可能修改 Makefile、hook 或测试脚本，不要在有凭据或生产配置的环境直接执行；可疑 PR 只读 diff。

### 4.4 评审维度

阅读顺序：测试 → 实现 → 文档 → 提交历史。

| 维度 | 关注点 |
| --- | --- |
| 正确性 | 边界、空值、错误路径、返回值语义 |
| 并发与资源 | 竞态、goroutine 泄漏、context 取消、超时 |
| 安全 | `read_file` 的 `allowed_root` 逃逸（`..`、symlink、FIFO）、大小上限、密钥不落日志 |
| 兼容性 | CLI flag、配置字段、退出码、stderr 格式 |
| 依赖 | 仅标准库；新增依赖需单独讨论 |
| 测试 | 是否覆盖失败路径，断言是否验证行为 |
| 文档 | README、DESIGN.md、docs/ 是否同步 |

### 4.5 评论规范

| 前缀 | 含义 |
| --- | --- |
| `blocking:` | 必须修改，否则不合并 |
| `suggestion:` | 建议修改，作者可决定 |
| `question:` | 需要澄清 |
| `nit:` | 风格细节，不改也行 |
| `praise:` | 值得肯定的点 |

要求：一次给全；`blocking` 给出可执行改法；对代码不对人；首次响应不超过 24 小时。

### 4.6 决策与合并

| 决策 | 命令 | 场景 |
| --- | --- | --- |
| 批准 | `gh pr review <n> --approve` | 可以合并 |
| 请求修改 | `gh pr review <n> --request-changes --body "..."` | 存在 blocking |
| 仅评论 | `gh pr review <n> --comment --body "..."` | 方向性讨论 |
| 关闭 | `gh pr close <n> --comment "..."` | 不做或已有替代 |

合并只允许 squash：

```bash
gh pr merge <n> --squash --delete-branch
```

合并后检查 `main` 的 CI、确认关联 issue 已关闭、同步本地。

### 4.7 Fork PR

1. 首次贡献者的 workflow 需要维护者批准
2. 维护者不能向对方分支追加提交，只能提 `suggestion` 或请作者修改
3. `--delete-branch` 只删除本地分支，fork 侧分支由作者清理
4. 合并后 fork 仍然存在，不影响后续贡献

### 4.8 维护者红线

- 不合并未通过 CI 的 PR；紧急情况需记录原因
- 不接受超出仓库目标的变更（见 DESIGN.md 非目标）
- 不直接改作者代码后强推
- 不批准自己的 PR

## 5. 仓库硬约束

### 5.1 分支保护（ruleset `protect-main`）

| 规则 | 作用 |
| --- | --- |
| `deletion` | 禁止删除 `main` |
| `non_fast_forward` | 禁止强推 |
| `required_linear_history` | 禁止 merge commit |
| `pull_request` | 必须走 PR；0 approval；只允许 squash；线程需解决 |
| `required_status_checks` | 三项检查必过，且分支需基于最新 `main` |

创建命令：

```bash
gh api -X POST repos/jacob-bits/SimpleAgent/rulesets --input - <<'JSON'
{
  "name": "protect-main",
  "target": "branch",
  "enforcement": "active",
  "conditions": { "ref_name": { "include": ["~DEFAULT_BRANCH"], "exclude": [] } },
  "rules": [
    { "type": "deletion" },
    { "type": "non_fast_forward" },
    { "type": "required_linear_history" },
    { "type": "pull_request", "parameters": {
        "required_approving_review_count": 0,
        "dismiss_stale_reviews_on_push": true,
        "require_code_owner_review": false,
        "require_last_push_approval": false,
        "required_review_thread_resolution": true,
        "allowed_merge_methods": ["squash"] } },
    { "type": "required_status_checks", "parameters": {
        "strict_required_status_checks_policy": true,
        "required_status_checks": [
          { "context": "Test" },
          { "context": "Build (macos-latest)" },
          { "context": "Build (windows-latest)" } ] } }
  ]
}
JSON
```

注意：`pull_request` 的五个参数字段全部必填，缺一个会返回 422 `data matches no possible input`。

### 5.2 合并方式

```bash
gh api -X PATCH repos/jacob-bits/SimpleAgent \
  -F allow_squash_merge=true -F allow_merge_commit=false -F allow_rebase_merge=false \
  -F delete_branch_on_merge=true \
  -F squash_merge_commit_title=PR_TITLE -F squash_merge_commit_message=BLANK
```

### 5.3 线性历史

- `main` 当前为线性历史，PR #1、#2 的 merge commit 已通过 rebase 重写
- 重写步骤：备份分支 → `git rebase <第一个 merge 之前的提交>` → 校验树一致 → `git push --force-with-lease`
- 重写后同步所有 clone 与 fork：`git fetch --prune && git reset --hard origin/main`
- 受保护后禁止强推；确需重写时临时调整 ruleset

### 5.4 权限矩阵

| 操作 | unzip-zs | jacob-bits |
| --- | --- | --- |
| 推送 fork 分支 | 允许 | 允许 |
| 推送上游分支 | 拒绝 | 允许（受 ruleset 约束） |
| 创建 PR | 允许 | 允许 |
| 批准或合并 | 拒绝 | 允许 |
| 修改 ruleset 与设置 | 拒绝 | 允许 |

## 6. 异常处理

| 场景 | 处理 |
| --- | --- |
| CI 红 | 本地 `make check` 复现，只修根因 |
| 冲突 | 分支内 `git rebase origin/main`，用 `--force-with-lease` 推送 |
| 首次贡献者 CI 待批 | 维护者点 “Approve and run workflows” |
| 推送被拒（账号不符） | 检查 `.agent/token` 与 credential helper |
| 推送被拒（permission denied） | 全局 `gh auth setup-git` 的 helper 抢占，仓库内重置 helper 列表 |
| ruleset 422 | `pull_request` 的五个参数必须齐全 |
| 误提交密钥 | 立即吊销凭证、清理历史、通知维护者 |
| 合并后出问题 | 用 revert PR 回滚，不强制推送 `main` |
| 历史重写后本地分叉 | `git fetch --prune && git reset --hard origin/main` |

## 7. 命令速查

提交方：

```bash
git switch main && git pull --ff-only
git switch -c fix/12-symlink-escape
make check
git commit -m "fix(readfile): compare path segments instead of prefixes"
git push -u fork HEAD
gh pr create --repo jacob-bits/SimpleAgent --base main \
  --head unzip-zs:fix/12-symlink-escape --fill
```

接受方：

```bash
gh pr list --state open
gh pr checkout 4
make check
gh pr review 4 --approve
gh pr merge 4 --squash --delete-branch
```

## 8. 验收清单

- [ ] 直推 `main` 被 ruleset 拒绝
- [ ] 合并按钮只提供 Squash
- [ ] 三项检查未通过时无法合并
- [ ] `main` 历史线性，无 merge commit
- [ ] 每个 PR 的标题即 `main` 上的提交信息
- [ ] 贡献者与维护者的身份、凭据、权限相互隔离
