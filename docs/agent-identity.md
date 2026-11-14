# Agent 身份与账号隔离

> 本仓库由两个 GitHub 账号协作：`unzip-zs` 负责提交与推送，`jacob-bits` 负责评审与合并。
> 本文记录如何让自动化 Agent 固定使用贡献者身份，同时不影响维护者的本地配置。

## 1. 目标

- Agent 的每次提交都可追溯到 `unzip-zs`
- Agent 的推送只使用 `unzip-zs` 的凭据
- 维护者本地继续使用 `jacob-bits`，两者互不覆盖
- 权限上无法“自审自合”：贡献者账号对仓库只有读权限

## 2. 三层隔离

| 层 | Agent（unzip-zs） | 维护者（jacob-bits） | 机制 |
| --- | --- | --- | --- |
| 提交作者 | `unzip-zs <97578602+unzip-zs@users.noreply.github.com>` | 全局 git 身份 | 仓库级 `git config --local` |
| 推送凭据 | `.agent/token`（权限 600） | 默认 `~/.config/gh` | 仓库级 credential helper |
| 评审权限 | 只读 → fork + PR | owner → approve / merge | GitHub 权限模型 |

不使用 `GH_CONFIG_DIR` 的原因：`gh` 的 token 存在系统 keyring 中并与账号绑定，多个配置目录会共享同一条记录，登录、登出或切换账号时容易互相影响。显式 token 文件可以与 `gh` 的登录状态完全解耦。

## 3. Agent 侧配置

~~~bash
# 提交身份
git config --local user.name "unzip-zs"
git config --local user.email "97578602+unzip-zs@users.noreply.github.com"

# 推送凭据（token 只落在本机，权限 600）
mkdir -p .agent
umask 077
gh auth token > .agent/token

# 覆盖全局凭据助手：gh auth setup-git 会向 ~/.gitconfig 写入
# credential.https://github.com.helper，且排在仓库配置之前
git config --local credential.https://github.com.helper ""
git config --local --add credential.https://github.com.helper \
  "!$PWD/.agent/git-credential.sh"

# 推送目标
git remote add fork https://github.com/unzip-zs/SimpleAgent.git
~~~

`.agent/git-credential.sh` 只读取同目录的 token：

~~~sh
#!/bin/sh
dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
case "${1:-}" in
get)
	printf 'username=%s\n' "unzip-zs"
	printf 'password=%s\n' "$(cat "$dir/token")"
	;;
esac
~~~

`.agent/` 已在忽略列表中，不会进入版本库。

### 守卫钩子

`.git/hooks/` 下另有两个仅在本机生效的钩子（不随仓库分发）：

- `pre-commit`：提交身份不是 `unzip-zs` 时拒绝提交
- `pre-push`：token 对应的账号不是 `unzip-zs` 时拒绝推送

## 4. 维护者侧

~~~bash
# 浏览器登录 jacob-bits
gh auth login -h github.com -p https -s repo,workflow
gh auth switch -u jacob-bits
gh auth status

# 可选：默认配置中移除贡献者账号（Agent 使用独立 token，不受影响）
gh auth logout -u unzip-zs
~~~

维护者的全局 git 身份保持 `jacob-bits`，不要修改；也不要在 Agent 的仓库目录里提交或推送。

## 5. 校验

~~~bash
git var GIT_AUTHOR_IDENT
GH_TOKEN=$(cat .agent/token) gh api user --jq .login   # unzip-zs
git config --get credential.https://github.com.helper
git push --dry-run fork HEAD:refs/heads/tmp-check
~~~

## 6. 轮换与故障处理

| 现象 | 处理 |
| --- | --- |
| token 失效（401） | 重新导出 `unzip-zs` 的 token 并覆盖 `.agent/token` |
| 提交被拒绝 | `pre-commit` 提示身份不符，检查 `git config --local user.email` |
| 推送被拒绝（账号不符） | `pre-push` 提示账号不符，检查 `.agent/token` 是否属于 `unzip-zs` |
| 推送被拒（permission denied） | 全局 `gh auth setup-git` 的 helper 抢先返回了另一个账号；确认仓库内先用空值重置了 helper 列表（见第 3 节） |
| 换机器或重建工作区 | 重新执行第 3 节的身份配置与 token 导出 |
| 疑似泄露 | 在 GitHub Settings → Applications 撤销 GitHub CLI 授权，再导出新 token |

## 7. 边界

- 历史提交不回写：配置生效前的提交作者保持不变
- 本文件不记录任何 token；`.agent/` 永不提交
- 评审规则见 [review.md](review.md)
