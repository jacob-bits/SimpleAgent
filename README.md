# SimpleAgent

一个用 Go 实现的极简 ReAct Agent：通过可配置的轮次范围和终止条件控制执行，并使用只读 `read_file` 工具，基于文件内容回答问题。

- 语言：Go 1.22+
- 依赖：仅标准库
- LLM：OpenAI 兼容的 Chat Completions 接口，可按配置接入 DeepSeek
- 工具：内置只读 `read_file`
- 设计：见 [DESIGN.md](DESIGN.md) 和 [docs/](docs/)

## 快速开始

~~~bash
export OPENAI_API_KEY=...

go run ./cmd/agent \
  -config config.example.json \
  -verbose \
  "根据 examples/notes.txt，项目代号是什么？主要风险是什么？"
~~~

预期：

- stdout 输出最终答案。
- stderr 在 `-verbose` 时输出 step/tool/stop trace。
- 退出码 0。

也可以从 stdin 提供问题：

~~~bash
go run ./cmd/agent -config config.example.json < question.txt
~~~

使用本地 OpenAI 兼容服务时，修改 `config.example.json` 中的 `llm.base_url`、`llm.model` 或通过 flag 覆盖：

~~~bash
go run ./cmd/agent \
  -base-url http://127.0.0.1:8080/v1 \
  -model local-model \
  -allowed-root examples \
  "项目代号是什么？"
~~~

## 配置

完整示例见 [config.example.json](config.example.json)。API Key 只从以下位置读取：

1. `llm.api_key`（不推荐写入文件）
2. `llm.api_key_env` 指向的环境变量，默认 `OPENAI_API_KEY`

CLI 不提供 `-api-key`，不会打印 Key。Key 为空时允许启动，但会输出一次 warning；真实服务通常返回 401。

## CLI

~~~text
agent [flags] [question]
~~~

常用 flag：

| Flag | 说明 |
|---|---|
| `-config` | JSON 配置文件 |
| `-base-url` / `-model` | LLM 地址与模型 |
| `-temperature` / `-max-tokens` / `-llm-timeout` | LLM 参数 |
| `-reasoning-effort` | 推理强度；DeepSeek 可用 `none` 关闭 thinking |
| `-min-steps` / `-max-steps` | 最小/最大 ReAct 轮数 |
| `-max-repeated-actions` | 连续相同动作上限 |
| `-no-progress-threshold` / `-no-progress-min-chars` | 无进展检测 |
| `-max-history-chars` | 历史 UTF-8 字节预算 |
| `-timeout` | Agent 总超时（秒） |
| `-allowed-root` / `-max-file-bytes` | 文件工具边界 |
| `-verbose` | 输出结构化 trace 到 stderr |

问题来源：

1. 位置参数优先。
2. 没有位置参数且 stdin 不是终端时，读取 stdin，最多 64 KiB。
3. 两者都没有时输出 usage 错误，退出 2。

完整 flag、退出码和 trace 格式见 [docs/cli.md](docs/cli.md)。

## 退出码

| 码 | 含义 |
|---:|---|
| 0 | 成功，或显示 help |
| 1 | 运行失败：LLM、解析、超时、取消、输出写入失败 |
| 2 | 用法或启动期失败：flag、空问题、配置、根目录、构造失败 |

## 开发

~~~bash
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
go test ./internal/cli -cover
go build ./cmd/agent
~~~

当前测试包含：

- 配置、工具、read_file、LLM Adapter、Parser、Agent Loop 的单元测试；
- CLI 的 `httptest` 端到端测试：mock OpenAI + 临时文件，验证 `read_file` observation 和最终答案；
- 不依赖真实 LLM 或外网。

## 目录

~~~text
cmd/agent/          CLI 入口
internal/agent/     ReAct Loop、Parser、Prompt、Observation、Trace
internal/cli/       flag、配置覆盖、stdin、退出码、trace 格式化
internal/config/    配置默认值、JSON 加载与校验
internal/llm/       OpenAI 兼容客户端
internal/tool/      工具定义、Registry、Invoker
internal/tool/readfile/
docs/               详细设计
examples/           演示文本
~~~

## 已知限制

- 只支持只读工具；不做文件写入、Shell、浏览器访问。
- 不支持流式输出、断点恢复、上下文自动压缩。
- 单步只支持一个工具调用。
- 历史预算按 UTF-8 字节计，不是精确 token 预算；不记录 token usage。
- 工具超时依赖 context 协作，无法强杀忽略 context 的阻塞实现。
- 认证、审批、沙箱执行器目前只有注册期 fail-closed 门控，尚未提供真实运行时服务。
- LLM 失败不自动重试；解析失败不自动修复。

## 设计文档

- [总体设计](DESIGN.md)
- [Parser 详细设计](docs/parser.md)
- [LLM Adapter 详细设计](docs/llm_adapter.md)
- [工具定义设计](docs/tool_definition.md)
- [read_file 详细设计](docs/read_file.md)
- [Agent Loop 详细设计](docs/agent_loop.md)
- [CLI 详细设计](docs/cli.md)

## 贡献

分支命名、提交信息、本地门禁与评审流程见 [CONTRIBUTING.md](CONTRIBUTING.md)。提交 PR 前请先运行：

~~~bash
make check
~~~
