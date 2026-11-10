# CLI 详细设计

| 项目 | 内容 |
|---|---|
| 文档版本 | v0.1 |
| 状态 | 已实现；mock 端到端与真实 DeepSeek 冒烟通过 |
| 日期 | 2026-11-11 |
| 上位设计 | [DESIGN.md](../DESIGN.md) §2.1、§7.3、§10、§13、§14 |
| 关联设计 | [Agent Loop 详细设计](agent_loop.md)、[LLM Adapter 详细设计](llm_adapter.md)、[read_file 详细设计](read_file.md) |
| 实现位置 | `cmd/agent/main.go`、`internal/cli/cli.go`、`internal/cli/trace.go`、`internal/cli/cli_test.go` |
| 输入 | 命令行参数、stdin、配置文件、环境变量、信号 |
| 输出 | stdout 最终答案；stderr trace/warning/错误；进程退出码 |

> 本文档定义 SimpleAgent 的 CLI 契约：参数、配置覆盖、依赖装配、信号、stdout/stderr、trace 格式和退出码。CLI 只做启动与 IO，不实现 Agent Loop、LLM 协议或工具逻辑。

## 1. 背景与定位

目标调用形态：

~~~bash
go run ./cmd/agent \
  -config config.example.json \
  -verbose \
  "根据 examples/notes.txt，项目代号是什么？主要风险是什么？"
~~~

完整启动链路：

~~~text
os.Args
   ↓
internal/cli.Run(ctx, args, stdin, stdout, stderr)
   ├── flag 解析
   ├── config.Load + 显式 flag 覆盖
   ├── readfile.New
   ├── llm.New
   ├── tool.NewRegistry
   ├── agent.New
   └── agent.Run(ctx, question)
         ↓
      stdout 答案 / stderr trace / 退出码
~~~

CLI 是唯一接触 `os.Args`、`os.Stdout`、`os.Stderr`、配置文件路径和信号的层。

### 1.1 职责边界

| 问题 | 归属 | 说明 |
|---|---|---|
| flag 解析与 help | CLI | 不含业务逻辑 |
| 配置默认值/JSON 校验 | config | CLI 只负责显式 flag 覆盖 |
| 显式 flag 优先级 | CLI | 只覆盖实际出现的 flag |
| API Key 读取 | llm/config | CLI 不打印、不落盘 |
| Prompt 构造 | Agent | CLI 只传入 Registry |
| 工具初始化 | CLI | `readfile.New` |
| ReAct 循环 | Agent | CLI 不干预 |
| trace 事件 | Agent | CLI 负责格式化到 stderr |
| 信号与退出码 | CLI | Agent 只接收 context |
| 最终答案格式 | CLI | stdout 一行/多行答案 |

## 2. 目标与非目标

### 2.1 目标

1. 提供与 [DESIGN.md](../DESIGN.md) §13 一致的命令形态。
2. 只对显式出现的 flag 做配置覆盖，避免零值误覆盖 JSON。
3. 启动期错误与运行期错误使用不同退出码。
4. Ctrl-C/SIGTERM 取消 Agent，不产生部分答案到 stdout。
5. 最终答案只写 stdout；trace 和错误只写 stderr。
6. `agent.verbose` 开启时输出经过截断的结构化 trace。
7. CLI 可以不经子进程、用 `httptest` 和临时目录完成单元/端到端测试。
8. 不引入第三方 CLI 库，保持标准库。

### 2.2 非目标

- 不实现交互式 REPL、多轮对话；stdin 仅用于非交互读取初始问题。
- 不实现守护进程、后台任务、断点恢复、流式输出。
- 不实现写文件、Shell、浏览器等新工具。
- 不实现配置编辑、交互式 API Key 输入或密钥管理。
- 不实现多租户、鉴权、Web UI。
- 不把 LLM 的 temperature、max_tokens、LLM timeout 等全部配置暴露为 flag；这些继续通过 JSON 配置。
- 不改变 Agent Loop 的终止语义；CLI 只做映射。

## 3. 目录与包结构

~~~text
cmd/agent/
└── main.go              # 进程入口：信号 + os.Exit(cli.Run(...))

internal/cli/
├── cli.go               # Run、flag、配置覆盖、依赖装配、输出
├── trace.go             # TraceEvent -> stderr 文本
└── cli_test.go          # 单元测试 + httptest 端到端

examples/
└── notes.txt            # 最小演示文本

README.md                # 构建、配置、运行、退出码、限制
~~~

依赖方向：

~~~text
cmd/agent → internal/cli
internal/cli → internal/agent
             → internal/config
             → internal/llm
             → internal/tool
             → internal/tool/readfile
internal/cli ✗→ cmd
internal/agent ✗→ internal/cli
~~~

### 3.1 为什么把逻辑放在 internal/cli

- `main.go` 只做 `signal.NotifyContext` 和 `os.Exit`，几乎不可能出错。
- `cli.Run` 接收 `context.Context`、`args`、`stdin`、`stdout`、`stderr`，测试时无需启动子进程或依赖真实终端。
- 可以用 `httptest.NewServer` 模拟 OpenAI 兼容服务，用 `t.TempDir()` 作为 `allowed_root`，完整验证 flag、配置、工具、Agent、输出和退出码。
- 后续若增加子命令，可在 `internal/cli` 内扩展，而不改动 Agent。

### 3.2 main.go

~~~go
package main

import (
    "context"
    "os"
    "os/signal"
    "syscall"

    "simpleagent/internal/cli"
)

func main() {
    ctx, stop := signal.NotifyContext(
        context.Background(),
        os.Interrupt,
        syscall.SIGTERM,
    )

    code := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
    stop()
    os.Exit(code)
}
~~~

`syscall.SIGTERM` 面向 Unix/容器环境；若未来需要 Windows 构建，单独用 build tags 拆分信号文件。

## 4. 公开契约

### 4.1 cli.Run

~~~go
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int
~~~

- `args` 不包含程序名（`os.Args[1:]`）。
- `stdin` 仅在位置参数为空时用于读取问题；位置参数存在时忽略 stdin。
- `stdout` 只接收最终答案；`stderr` 接收 trace、错误、warning 和 usage。
- 返回进程退出码，不调用 `os.Exit`。
- `ctx` 由 `main` 的信号 context 传入；测试可传 `context.Background()` 或已取消的 context。
- `Run` 是顺序执行；同一进程不并发调用。

### 4.2 退出码

| 码 | 含义 | 典型场景 |
|---:|---|---|
| 0 | 成功或显示 help | Agent 以 `final_answer` 结束；`-h` |
| 1 | 运行失败 | LLM/解析/超时/取消/输出写失败 |
| 2 | 用法或启动期失败 | flag 错误、空问题、配置错误、根目录错误、构造 Agent 失败 |

CLI 沿用 DESIGN 的 0/1/2 约定，不使用 130 作为 Ctrl-C 退出码。取消和超时属于运行失败（1）。

### 4.3 帮助

- `-h` / `-help`：usage 写 stdout，退出 0。
- 未知 flag、非法参数：错误和 usage 写 stderr，退出 2。
- help 在读取配置、构造 LLM/工具/Agent 之前返回，不访问网络。

## 5. 命令行契约

### 5.1 Usage

~~~text
Usage: agent [flags] [question]

Run one ReAct question through the configured LLM and tools.

If no question argument is provided and stdin is not a terminal, the entire
stdin is read as the question (maximum 64 KiB). Positional arguments take
precedence over stdin.

Flags:
  -allowed-root string
        root directory readable by the file tool (overrides tools.allowed_root)
  -base-url string
        OpenAI-compatible API base URL (overrides llm.base_url)
  -config string
        JSON configuration file; empty uses built-in defaults
  -llm-timeout int
        single LLM request timeout in seconds (overrides llm.timeout_seconds)
  -max-file-bytes int
        maximum file size in bytes (overrides tools.max_file_bytes)
  -max-history-chars int
        history byte budget (overrides agent.max_history_chars)
  -max-repeated-actions int
        consecutive identical action limit (overrides agent.max_repeated_actions)
  -max-steps int
        maximum ReAct decision steps (overrides agent.max_steps)
  -max-tokens int
        completion token limit, 0 means provider default (overrides llm.max_tokens)
  -min-steps int
        minimum steps before final_answer is accepted (overrides agent.min_steps)
  -model string
        model name (overrides llm.model)
  -no-progress-min-chars int
        short-observation exact-equality threshold (overrides agent.no_progress_min_chars)
  -no-progress-threshold float
        observation similarity threshold, 0 disables (overrides agent.no_progress_threshold)
  -reasoning-effort string
        reasoning effort; empty omits the field (overrides llm.reasoning_effort)
  -temperature float
        sampling temperature (overrides llm.temperature)
  -timeout int
        total Agent timeout in seconds (overrides agent.timeout_seconds)
  -verbose
        write structured trace to stderr (overrides agent.verbose)

Examples:
  agent -config config.example.json "根据 examples/notes.txt，项目代号是什么？"
  agent -config config.example.json -verbose "主要风险是什么？"
  cat question.txt | agent -config config.example.json
  agent -- "-question text"
~~~

- 推荐把 flag 放在 question 之前。
- question 以 `-` 开头时使用 `--`：`agent -- "-question text"`。
- 多个位置参数按单个空格拼接：`agent 项目 代号` 等价于 `agent "项目 代号"`。
- 拼接后 `strings.TrimSpace` 为空则报 usage 错误。

### 5.2 Flag 表

| Flag | 类型 | 覆盖字段 | 未出现时 | 说明 |
|---|---|---|---|---|
| `-config` | string | 配置文件路径 | 空，使用默认值 | 不写到 Config |
| `-base-url` | string | `llm.base_url` | 保留配置值 | 必须是 http(s) URL |
| `-model` | string | `llm.model` | 保留配置值 | 非空 |
| `-temperature` | float | `llm.temperature` | 保留配置值 | 0 到 2 |
| `-max-tokens` | int | `llm.max_tokens` | 保留配置值 | 0 表示服务端默认 |
| `-llm-timeout` | int | `llm.timeout_seconds` | 保留配置值 | 单次 LLM 请求超时 |
| `-reasoning-effort` | string | `llm.reasoning_effort` | 保留配置值 | 空/none/low/high/max 等 |
| `-min-steps` | int | `agent.min_steps` | 保留配置值 | >=1 且 <= max_steps |
| `-max-steps` | int | `agent.max_steps` | 保留配置值 | >= min_steps |
| `-max-repeated-actions` | int | `agent.max_repeated_actions` | 保留配置值 | >=1 |
| `-no-progress-threshold` | float | `agent.no_progress_threshold` | 保留配置值 | 0 到 1；0 关闭 |
| `-no-progress-min-chars` | int | `agent.no_progress_min_chars` | 保留配置值 | >=0 |
| `-max-history-chars` | int | `agent.max_history_chars` | 保留配置值 | UTF-8 字节预算，>0 |
| `-timeout` | int | `agent.timeout_seconds` | 保留配置值 | Agent 总超时，不是 LLM 超时 |
| `-verbose` | bool | `agent.verbose` | 保留配置值 | 支持 `-verbose=false` |
| `-allowed-root` | string | `tools.allowed_root` | 保留配置值 | 相对路径基于进程 cwd |
| `-max-file-bytes` | int64 | `tools.max_file_bytes` | 保留配置值 | >0 |

不暴露为 flag 的配置只有密钥相关字段：

- `llm.api_key`
- `llm.api_key_env`

其余非敏感配置都可从命令行覆盖。`-timeout` 只覆盖 Agent 总超时；LLM 请求超时使用 `-llm-timeout`。

所有 flag 默认值为零值；是否覆盖由 `flag.Visit` 决定。

### 5.3 显式 flag 检测

不能直接用 flag 的零值覆盖配置，因为无法区分“未出现”和“显式传 0/false”。

实现方式：

~~~go
visited := make(map[string]bool)
flags.Visit(func(f *flag.Flag) {
    visited[f.Name] = true
})
if visited["model"] {
    cfg.LLM.Model = model
}
if visited["verbose"] {
    cfg.Agent.Verbose = verbose
}
~~~

规则：

- 只有 `Visit` 出现的 flag 才覆盖。
- `-verbose=false` 会被 `Visit` 记录，能覆盖配置文件里的 `true`。
- `-min-steps 0` 会进入配置，然后被 `Validate` 拒绝，不会静默当作“未设置”。
- 未知 flag 由 `flag` 拒绝，退出 2。

### 5.4 配置优先级

~~~text
内置默认值 < JSON 配置文件 < 显式命令行 flag
API Key: llm.api_key 优先，否则 llm.api_key_env 指向的环境变量
~~~

顺序：

1. `config.Load(path)`：默认值 + JSON，并执行完整 `Validate`。
2. 应用 `Visit` 检测到的 flag。
3. 再次 `cfg.Validate()`。
4. 用最终配置构造工具、LLM 和 Agent。

注意：`config.Load` 会先校验 JSON 文件本身；如果文件包含非法值，CLI 不会因为后续 flag 可能修复它而继续。配置文件和 flag 都必须让最终配置合法。

### 5.5 API Key

- CLI 不提供 `-api-key`，避免密钥出现在 shell history/ps。
- `llm.api_key` 优先；否则读取 `llm.api_key_env`；环境变量为空时允许构造 Client。
- 空 Key 不阻止启动，便于本地 mock；真实服务返回 401 时作为运行错误退出 1。
- 在依赖构造成功、Agent.Run 之前，如果 `ResolveAPIKey()` 为空，向 stderr 打印一次 warning：

~~~text
warning: no LLM API key configured (set llm.api_key or OPENAI_API_KEY); requests may fail with 401
~~~

- warning 只提示，不改变退出码，不影响本地 mock。
- 环境变量名来自配置时使用转义，不打印 Key 值。
- help/flag/config 错误发生在 warning 之前，不会打印该 warning。
- CLI 和错误信息不打印 API Key、Authorization 或完整请求。

### 5.6 Flag 解析与 help

使用标准库 `flag.NewFlagSet("agent", flag.ContinueOnError)`，不使用第三方 CLI 库。

解析约定：

- 解析期间 `fs.SetOutput(io.Discard)`，`fs.Usage` 设为空函数，避免 `flag` 自动重复打印。
- 解析成功：进入配置加载。
- `errors.Is(err, flag.ErrHelp)`：调用 `printUsage(stdout, fs)`，退出 0。
- 其他解析错误：先向 stderr 打印 `error: <err>`，再向 stderr 打印 usage，退出 2。
- `printUsage(w, fs)` 临时把 `fs.Output` 指向 w，打印 Usage 和 `fs.PrintDefaults()`，然后恢复为 `io.Discard`。
- `-h` 和 `-help` 都由 `flag` 转为 `ErrHelp`。
- 帮助在读取配置、构造依赖之前返回。

~~~go
fs := flag.NewFlagSet("agent", flag.ContinueOnError)
fs.SetOutput(io.Discard)
fs.Usage = func() {}

err := fs.Parse(args)
switch {
case errors.Is(err, flag.ErrHelp):
    printUsage(stdout, fs)
    return 0
case err != nil:
    fmt.Fprintf(stderr, "error: %v\n", err)
    printUsage(stderr, fs)
    return 2
}
~~~

`flag` 在第一个非 flag 参数处停止；因此推荐所有 flag 放在 question 之前。若 question 以 `-` 开头，使用 `--` 分隔。

### 5.7 问题来源与 stdin

问题来源按固定优先级：

1. 位置参数存在：用单个空格拼接、`strings.TrimSpace`；结果非空则使用。
2. 位置参数存在但拼接后为空：usage 错误，不读取 stdin。
3. 没有位置参数：
   - `stdin` 为 nil，或者 `*os.File` 的 `ModeCharDevice` 表明是终端：usage 错误，不阻塞等待。
   - 否则读取 stdin，最多 `64 KiB`；读取错误、超限或 trim 后为空都返回 usage 错误。
4. 位置参数和 stdin 同时存在：位置参数优先，stdin 忽略。

终端检测只在 stdin 是 `*os.File` 时进行；测试传入 `strings.Reader` 时视为管道。

~~~go
const maxQuestionBytes = 64 << 10

func resolveQuestion(stdin io.Reader, positional []string) (string, error) {
    if len(positional) > 0 {
        question := strings.TrimSpace(strings.Join(positional, " "))
        if question == "" {
            return "", errQuestionRequired
        }
        return question, nil
    }
    if stdin == nil || isTerminal(stdin) {
        return "", errQuestionRequired
    }
    data, err := io.ReadAll(io.LimitReader(stdin, maxQuestionBytes+1))
    if err != nil {
        return "", fmt.Errorf("read question from stdin: %w", err)
    }
    if len(data) > maxQuestionBytes {
        return "", fmt.Errorf("question from stdin exceeds %d bytes", maxQuestionBytes)
    }
    question := strings.TrimSpace(string(data))
    if question == "" {
        return "", errQuestionRequired
    }
    return question, nil
}
~~~

用法：

~~~bash
agent -config config.example.json "问题"
cat question.txt | agent -config config.example.json
printf '问题' | agent -config config.example.json
~~~

不提供交互式 prompt；没有参数且 stdin 是终端时直接报用法错误，避免程序看起来“卡住”。

辅助实现：

~~~go
func isTerminal(r io.Reader) bool {
    f, ok := r.(*os.File)
    if !ok {
        return false
    }
    info, err := f.Stat()
    return err == nil && info.Mode()&os.ModeCharDevice != 0
}
~~~

`errQuestionRequired` 的文案建议为：`question is required (provide an argument or pipe it via stdin)`。

## 6. 启动与装配流程

~~~text
1. parse flags / help
2. config.Load(configPath)
3. apply explicitly visited flags
4. cfg.Validate()
5. question = positional args or non-terminal stdin, trim, non-empty
6. readfile.New(cfg.Tools.AllowedRoot, cfg.Tools.MaxFileBytes)
7. llm.New(cfg.LLM)
8. tool.NewRegistry(reader)
9. agent.New(cfg.Agent, client, registry, trace option)
10. agent.Run(ctx, question)
~~~

失败映射：

| 步骤 | 失败示例 | 退出码 |
|---|---|---:|
| flag 解析 | 未知 flag、非法 int | 2 |
| help | `-h` | 0 |
| config.Load | 文件不存在、非法 JSON、未知字段 | 2 |
| 覆盖后 Validate | min_steps > max_steps | 2 |
| question | 空问题、stdin 为空/超限/读失败、stdin 是终端且无参数 | 2 |
| readfile.New | 根目录不存在/不是目录 | 2 |
| llm.New | 非法 URL/配置 | 2 |
| tool.NewRegistry | 工具定义/能力注册失败 | 2 |
| agent.New | Agent 配置/Prompt 生成失败 | 2 |
| agent.Run | LLM/解析/超时/取消/重复动作等 | 1 |
| stdout 写入 | broken pipe/IO 错误 | 1 |

LLM 构造不做网络请求；API Key 为空只在启动成功后给出 warning，不阻断运行。

## 7. 运行与输出

### 7.1 Context 与信号

- `main` 创建 `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)`。
- Ctrl-C/SIGTERM 取消 context。
- Agent 将取消映射为 `StopCanceled`/`ErrCanceled`，CLI 打印错误并退出 1。
- 若 Agent 总超时，返回 `StopTimeout`/`ErrTimeout`，退出 1。
- 信号取消不会把不完整答案写到 stdout。

### 7.2 stdout

- 仅 `StopFinalAnswer` 且 `err == nil` 时写答案。
- 写入内容为 `result.Answer`。
- 若答案不以 `\n` 结尾，补一个换行；若已经以换行结尾，保持原样。
- 写入失败（例如 broken pipe）时向 stderr 打印错误并退出 1。
- 不写 trace、错误、进度、usage 到 stdout。

### 7.3 stderr

- 运行错误：一行 `error: <err>`。
- 启动期错误：`error: <err>`，必要时附 usage。
- 空 API Key warning：启动成功后、Run 前打印一次。
- 解析错误已包含截断后的原始片段，CLI 不再追加完整响应。
- 不打印 API Key、完整文件内容或完整 system prompt。
- trace 仅在 `agent.verbose=true` 时输出。

### 7.4 结果校验

- `err != nil`：按错误处理，忽略 `result.Answer`，退出 1。
- `err == nil` 且 `result.Reason == StopFinalAnswer`：成功，退出 0。
- `err == nil` 但 Reason 不是 final：属于 Agent 契约异常，打印 `error: unexpected stop reason ...`，退出 1。
- `err != nil` 但 Reason final：仍按错误处理，退出 1。

## 8. Trace 格式

### 8.1 启用条件

- 最终 `cfg.Agent.Verbose == true` 时，构造 `agent.WithTrace(newTraceFunc(stderr))`。
- `-verbose=false` 或配置 false：不传 trace，Agent 没有 trace 开销。
- CLI 不读取环境变量单独开启 trace。

### 8.2 行格式

~~~text
trace step=1 thought="read the file" action="read_file" input="{\"path\":\"examples/notes.txt\"}" final=false
trace tool step=1 action="read_file" status=ok observation="{\"output\":\"...\"}"
trace step=2 thought="done" action="" input="" final=true
trace stop reason=final_answer steps=2
~~~

- 每个 `TraceEvent` 一行。
- 字符串字段用 `%q`，避免换行、控制字符和 ANSI 序列破坏日志。
- `status` 只输出 `ok`/`error`，不加引号。
- `trace stop` 的错误用 `error=%q` 输出。
- Agent 已对 thought/input/observation/control 做截断；CLI 不再展开完整内容。
- trace 写失败不改变 Agent 结果；若 stderr 不可写，CLI 无法保证 trace 完整性。

### 8.3 事件映射

| TraceKind | 输出 |
|---|---|
| `step` | `trace step=... thought=... action=... input=... final=...` |
| `tool` | `trace tool step=... action=... status=... observation=...` |
| `control` | `trace control step=... text=...` |
| `stop` | `trace stop reason=... steps=... [error=...]` |

## 9. 错误与退出码

### 9.1 错误文本

- 统一前缀 `error: `。
- 单行输出：CR/LF/TAB 替换为空格，其他控制字符丢弃，最多 1024 rune；避免服务端错误文本或路径中的控制序列破坏终端。
- 不输出 Go stack trace。
- 不输出完整配置结构。
- 配置错误保留字段名和值摘要；不包含 API Key。
- Agent 错误保留 `errors.Is/As` 的包装文本；CLI 只做显示清洗，不重新解释。

### 9.2 退出码判定

~~~text
help                                  → 0
flag/usage/config/startup error      → 2
agent.Run error                      → 1
agent.Run success + final_answer     → 0
stdout write error                   → 1
unexpected none-final success        → 1
~~~

### 9.3 需要避免的行为

- 不要用 `log.Fatal`；它会绕过显式 `stop()` 和统一输出。
- 不要在 flag 解析失败时继续运行。
- 不要在 stdout 打印 trace。
- 不要在错误文本里打印 API Key 或完整 Prompt。
- 不要把工具错误当成 CLI 启动错误；工具错误由 Agent 转 observation。
- 不要在 Ctrl-C 后返回 0。

## 10. 测试设计

### 10.1 测试替身

CLI 测试使用：

- `httptest.NewServer` 模拟 OpenAI 兼容 `/chat/completions`。
- `t.TempDir()` 作为 `allowed_root`，写 `notes.txt`。
- `bytes.Buffer` 捕获 stdout/stderr。
- `strings.Reader` 或临时文件提供 stdin 问题。
- `config.AgentConfig` 默认值 + 临时 JSON 配置。
- 不启动子进程，不访问真实 LLM/外网。

### 10.2 测试矩阵

| 组 | 用例 |
|---|---|
| Help/usage | `-h` 输出 usage 到 stdout，退出 0；未知 flag 到 stderr，退出 2 |
| 空问题 | 无参数且 stdin 为空/终端、全空白、多个空参数 → 2，不调用 LLM |
| 配置文件 | 文件不存在、非法 JSON、未知字段 → 2 |
| Flag 覆盖 | 只覆盖显式 flag；`-verbose=false` 覆盖 true；扩展 flag 覆盖对应字段；零值不被当作未设置 |
| 启动错误 | 非法 min/max、非法 root、非法 base URL、构造失败 → 2 |
| 成功链路 | mock LLM 返回 read_file → final；stdout 答案；退出 0 |
| read_file | 根内文件成功；根外路径作为 error observation；模型随后 final |
| Trace | verbose 时 stderr 有 step/tool/stop；非 verbose 时无 trace |
| LLM 错误 | 500、空 choices、超时 → 1；stdout 为空 |
| 解析错误 | 非法模型输出 → 1；stdout 为空 |
| stdin | 无参数 + 管道读取；空管道/终端/超限/读错误 → 2；参数优先于 stdin |
| API Key warning | 空 Key 输出一次 warning；设置环境变量后不输出 |
| 取消/超时 | context 取消/超时 → 1；stdout 为空 |
| 输出错误 | stdout writer 返回错误 → 1 |
| 资源位置 | 配置相对路径、allowed_root 相对 cwd、问题多参数拼接 |
| 安全 | stderr 不出现 API Key、完整 Prompt 或完整文件内容；含换行/控制字符的错误仍单行显示 |

### 10.3 端到端用例

~~~text
0. 设置 dummy API Key 环境变量，避免空 Key warning 干扰断言。
1. httptest 服务第一次返回 read_file 动作，第二次返回 final_answer。
2. CLI 参数：-base-url <server> -allowed-root <temp> "项目代号是什么？"
3. 断言：
   - exit code 0
   - stdout 是最终答案
   - 第二次请求的 messages 中有 JSON observation
   - 非 verbose 且 API Key 非空时 stderr 为空
4. 加 -verbose 重跑，断言 stderr 有 trace step/tool/stop。
5. 把 action path 改为 ../secret，断言 observation status=error，CLI 仍成功。
6. 用 stdin 提供问题重跑一次，断言答案仍正确。
~~~

### 10.4 覆盖率与命令

目标：`internal/cli` 语句覆盖率不低于 85%；关键分支（help、usage、config、run、output、trace）全覆盖。

当前覆盖率：**93.7%**（`go test ./internal/cli -cover`）。

~~~bash
gofmt -l .
go vet ./...
go test ./internal/cli -v
go test ./... -race
go test ./internal/cli -cover
~~~

`cmd/agent/main.go` 只做信号和 `os.Exit`，不单独追求覆盖率；通过启动构建和手动一次真实运行验证。

## 11. 验收标准

- [x] `-h` 显示 usage 到 stdout 并退出 0，不读取配置、不访问网络。
- [x] 未知 flag/非法参数显示错误和 usage 到 stderr，退出 2。
- [x] 空问题退出 2，不调用 LLM。
- [x] 配置文件不存在/非法退出 2。
- [x] 只有显式 flag 覆盖配置；`-verbose=false` 生效；扩展 flag 覆盖对应字段。
- [x] 成功链路的 stdout 只有最终答案，退出 0。
- [x] 非 verbose 且 API Key 非空时，成功路径 stderr 为空。
- [x] 空 API Key 时输出一次 warning，但不阻止启动。
- [x] 无位置参数且 stdin 非终端时可读取问题；空 stdin、超限、终端输入 → 2。
- [x] verbose 时 stderr 输出 step/tool/control/stop trace。
- [x] read_file 根外路径作为 error observation，CLI 不崩溃。
- [x] LLM/解析/取消/超时退出 1，stdout 为空。
- [x] 输出写入失败退出 1。
- [x] 错误和 trace 不包含 API Key、完整 Prompt 或完整文件内容。
- [x] 含换行或控制字符的错误文本仍以单行、安全转义的形式显示。
- [x] `go test ./...`、`go test -race ./...`、`go vet ./...`、`gofmt -l .` 全部通过。
- [x] mock OpenAI 端到端测试通过，不访问真实外网。
- [x] 手动真实模型冒烟一次成功（DeepSeek deepseek-flash）。

## 12. 配套文件

### 12.1 examples/notes.txt

~~~text
项目代号：Orion
主要风险：第三方支付接口联调延期。
负责人：张三
~~~

用于 README 和手动演示；不参与单元测试，测试使用临时文件。

### 12.2 README.md

README 至少包含：

- 项目定位与架构一句话
- Go 版本与依赖说明
- 构建/测试命令
- 配置说明与 API Key 环境变量
- 最小运行示例
- flag 表与退出码
- 已知限制（无流式、无恢复、工具只读、UTF-8 字节预算说明）
- 设计文档链接

## 13. 实现顺序

1. **CLI 骨架**：`internal/cli.Run`、`flag.FlagSet`、usage、help、退出码。
2. **输入与覆盖**：位置参数/stdin 解析、`config.Load`、`Visit`、再次 `Validate`。
3. **依赖装配**：readfile、llm、registry、agent；错误映射 2。
4. **运行与输出**：context、Agent.Run、stdout 答案、错误。
5. **Trace 与 warning**：`trace.go`、verbose 开关、事件格式化、空 API Key warning。
6. **main.go**：signal context + `os.Exit`。
7. **测试**：本地单元、httptest 端到端、取消/超时/输出错误。
8. **配套文件**：examples/notes.txt、README。
9. **手动真实模型冒烟**。

每一步以 `go test ./...` 通过为完成标准。

## 14. 已确认决策与实现约束

### 14.1 已确认决策

1. CLI 逻辑放 `internal/cli`，`cmd/agent/main.go` 只做信号与退出。
2. `cli.Run(ctx, args, stdin, stdout, stderr) int` 可测试；无位置参数时从非终端 stdin 读取问题。
3. 退出码 0/1/2，沿用 DESIGN；Ctrl-C/SIGTERM 退出 1，不用 130。
4. help 到 stdout；flag 错误 + usage 到 stderr。
5. 暴露所有非密钥配置 flag：`-llm-timeout`、`-max-repeated-actions`、`-max-history-chars`、`-max-file-bytes`、temperature、max-tokens、reasoning-effort、no-progress 参数等；API Key 字段不暴露。
6. `-timeout` 是 Agent 总超时；`-llm-timeout` 是单次 LLM 请求超时。
7. 不使用 `-api-key`；空 Key 允许启动，但输出一次 warning；401 为运行错误。
8. verbose 由最终 `agent.verbose` 决定；关闭时不安装 trace。
9. 成功答案写 stdout，trace/warning/错误写 stderr。
10. trace 使用现有 `TraceEvent`，不做二次展开。
11. 测试使用 `httptest` + `t.TempDir()`，不依赖外网。
12. 问题来源优先级：位置参数 > 非终端 stdin > usage 错误；stdin 上限 64 KiB；终端 stdin 不阻塞。
13. 不提供 shell completion、man page 或交互式 REPL。

### 14.2 评审结果

| # | 问题 | 结果 |
|---|---|---|
| 1 | help 到 stdout、flag 错误/usage 到 stderr | 接受 |
| 2 | Ctrl-C/SIGTERM 退出 1 | 接受 |
| 3 | 增加更多 flag | 需要；暴露全部非密钥配置 |
| 4 | 空 API Key warning | 需要 |
| 5 | stdin 读取问题 | 需要 |
| 6 | shell completion / man page | 暂不需要 |

已按第 13 节实现；mock OpenAI 端到端测试与真实 DeepSeek 冒烟均已通过。
