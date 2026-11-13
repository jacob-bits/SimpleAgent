# 评审流程

> 维护者视角：一个 PR 从提交到合并或关闭，需要经过哪些判断和动作。
> 提交侧的分支、提交信息与 PR 规范见 [CONTRIBUTING.md](../CONTRIBUTING.md)。

## 1. 总览

~~~text
收到 PR ─► 分诊 ─► 机器门禁 ─► 本地复现 ─► 代码评审 ─► 决策 ─► 合并/关闭 ─► 收尾
           10 分钟    自动       30–60 分钟     approve / request changes
~~~

原则：先判断“要不要合”，再判断“写得好不好”。方向不对的 PR 不值得逐行评审。

## 2. 分诊

| 检查项 | 判断依据 | 处理 |
| --- | --- | --- |
| 是否 Draft | 作者尚未完成 | 只给方向性意见 |
| 是否关联 issue | feat/fix 应有背景 | 先问背景，再评审 |
| 规模 | 超过 400 行或 10 个文件 | 要求拆分，或让作者说明 |
| 方向 | 是否偏离 DESIGN.md 的目标与非目标 | 暂不评审，先讨论 |
| 贡献者身份 | fork 的首次贡献者 | 先批准 workflow，否则 CI 不会运行 |
| 重复或过时 | 已有替代实现 | 关闭并指向替代方案 |

~~~bash
gh pr list --state open
gh pr view 12 --comments
gh pr diff 12 --stat
~~~

## 3. 机器门禁

- 必过检查：`Test`、`Build (macos-latest)`、`Build (windows-latest)`
- 合并状态为 `MERGEABLE`，无冲突
- PR 标题符合 Conventional Commits（squash 后即最终提交信息）
- 无密钥、大文件、二进制；fork PR 额外确认 workflow 已批准

~~~bash
gh pr checks 12 --watch
~~~

CI 红灯时不进入逐行评审。

## 4. 本地复现

~~~bash
gh pr checkout 12
make check
~~~

需要时运行真实场景（CLI + 示例配置），核对 PR 描述里的验证证据。

安全前提：外部 PR 可能修改 Makefile、hook 或测试脚本。不要在有凭据或生产配置的环境中直接执行；可疑 PR 只读 diff，不 checkout。

## 5. 评审维度

阅读顺序：测试 → 实现 → 文档 → 提交历史。先看测试能最快判断作者对需求的理解。

| 维度 | 关注点 |
| --- | --- |
| 正确性 | 边界、空值、错误路径、返回值语义 |
| 并发与资源 | 竞态、goroutine 泄漏、context 取消、超时是否生效 |
| 安全 | `read_file` 的 `allowed_root` 逃逸（`..`、symlink、FIFO、绝对路径）、大小上限、密钥不落日志 |
| 兼容性 | CLI flag、配置字段、退出码、stderr 格式 |
| 依赖 | 仅标准库；新增依赖需单独讨论 |
| 测试 | 是否覆盖失败路径，断言是否验证行为 |
| 文档 | README、DESIGN.md、docs/ 是否同步 |

## 6. 评论规范

| 前缀 | 含义 |
| --- | --- |
| `blocking:` | 必须修改，否则不合并 |
| `suggestion:` | 建议修改，作者可决定 |
| `question:` | 需要澄清 |
| `nit:` | 风格细节，不改也行 |
| `praise:` | 值得肯定的点 |

~~~text
blocking: 前缀比较会把 /root-evil 当成 /root 的子路径，建议按路径段比较。
nit: 变量名建议用 allowedRoot，和配置字段保持一致。
question: 这个超时复用 llm.timeout，还是新增配置？文档里没有说明。
~~~

要求：

- 一次给全，避免多轮补充
- `blocking` 必须给出可执行的修改方向
- 对代码不对人；设计取舍引用 DESIGN.md 的具体章节
- 首次响应不超过 24 小时

## 7. 决策与合并

| 决策 | 命令 | 场景 |
| --- | --- | --- |
| 批准 | `gh pr review 12 --approve` | 可以合并 |
| 请求修改 | `gh pr review 12 --request-changes --body "..."` | 存在 blocking 项 |
| 仅评论 | `gh pr review 12 --comment --body "..."` | 方向性讨论 |
| 关闭 | `gh pr close 12 --comment "..."` | 不做，或已有替代 |

合并只允许 squash，且合并前确认 PR 标题就是最终提交信息：

~~~bash
gh pr merge 12 --squash --delete-branch
~~~

合并后检查 `main` 的 CI 是否通过，确认关联 issue 已关闭。

## 8. Fork PR 的特殊点

1. 首次贡献者的 workflow 需要维护者批准（PR 页面的 “Approve and run workflows”）
2. 维护者无法直接向对方分支追加提交，只能提 `suggestion` 或请作者修改
3. `--delete-branch` 删除的是本地分支，fork 侧分支需要作者自行清理
4. 合并后 fork 仍然存在，不影响后续贡献

## 9. 权限与保护

- 单人维护：0 approval + 必过 CI，但仍必须通过 PR
- 多人协作：1 approval；核心路径（agent loop、llm、tool 权限）2 approval 或 CODEOWNERS
- 开启“新提交后 dismiss 旧批准”，避免批准后被追加提交绕过
- CODEOWNERS 只有在分支保护开启 “Require review from Code Owners” 后才生效
- 维护者不能批准自己的 PR；紧急情况下用管理员合并并记录原因

## 10. 例外处理

| 情况 | 处理 |
| --- | --- |
| 安全漏洞 | 停止公开讨论，改用 Security Advisory 或私下联系 |
| 恶意或可疑 PR | 不 checkout、不执行，只读 diff 并关闭 |
| 过大 PR | 要求拆分，先合基础部分 |
| 长期无响应 | 30 天标记 stale，60 天关闭 |
| 合并后出问题 | 用 revert PR 回滚，不对 `main` 强制推送 |
