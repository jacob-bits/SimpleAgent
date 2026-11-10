# LLM Adapter 详细设计

| 项目 | 内容 |
|---|---|
| 文档版本 | v0.1 |
| 状态 | 已实现，测试通过；Agent Loop 已接入 |
| 日期 | 2026-11-10 |
| 上位设计 | [DESIGN.md](../DESIGN.md) §5.2、§7 |
| 关联配置 | [internal/config/config.go](../internal/config/config.go#L36) |
| 实现位置 | `internal/llm/client.go`、`internal/llm/types.go`、`internal/llm/client_test.go` |
| 协议 | OpenAI 兼容 Chat Completions |
| 供应商验证 | OpenAI 兼容接口；DeepSeek 兼容性见 §10 |

> 本文档描述 LLM 适配层的外部契约与内部实现边界。配置层、工具层、`internal/llm` 和 Agent Loop 已实现；CLI 尚未实现。

## 1. 背景与定位

SimpleAgent 的 Agent Loop 只依赖一个最小接口：

```go
type Completer interface {
    Complete(ctx context.Context, messages []llm.Message) (string, error)
}
```

LLM Adapter 的职责是把这个接口落到 OpenAI 兼容的 `POST /chat/completions`：

```text
Agent
  → llm.Client.Complete(ctx, messages)
    → 校验消息
    → 构造 chatRequest
    → POST {base_url}/chat/completions
    → 解析 choices[0].message.content
  → 返回字符串
```

适配层不解析 ReAct JSON、不构造 prompt、不执行工具、不做 Agent 终止判断。

## 2. 目标与非目标

### 2.1 目标

1. 支持任意 OpenAI Chat Completions 兼容服务，通过 `base_url`、`model`、`api_key_env` 配置接入。
2. 正确实现鉴权、超时、取消、非 2xx、非法 JSON、空 choices、空 content 的错误处理。
3. 默认限制响应体大小，错误信息脱敏，不泄漏 API Key、请求消息和文件内容。
4. 保留 `usage`、`finish_reason` 和 DeepSeek `reasoning_content` 等可观测信息。
5. 只使用标准库，可用 `httptest` 完整单测，不依赖外网。
6. 保持 `Completer` 接口最小；通过扩展方法/结果类型支持 usage 和 reasoning 信息。

### 2.2 非目标

- 不实现流式输出（SSE）。
- 不实现重试、退避、限流。
- 不实现原生 Function Calling / Tools API；当前使用提示词 ReAct。
- 不实现多模态 content（图片、音频）。
- 不统计 token 费用，只透传 `usage`。
- 不负责 prompt 构造、JSON 解析和工具调用。

## 3. 包结构与公开对象

```text
internal/llm/
├── client.go       # Client、New、Complete、CompleteResult
├── types.go        # Message、Result、Usage、HTTPError、哨兵错误
└── client_test.go  # httptest 单测
```

### 3.1 Message

```go
type Message struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}
```

`Role` 只允许 `system`、`user`、`assistant`。工具 observation 由 Agent 以 `user` 消息追加，因此不需要 `tool` 角色。

### 3.2 Usage

```go
type CompletionTokensDetails struct {
    ReasoningTokens int `json:"reasoning_tokens"`
}

type Usage struct {
    PromptTokens            int                      `json:"prompt_tokens"`
    CompletionTokens        int                      `json:"completion_tokens"`
    TotalTokens             int                      `json:"total_tokens"`
    PromptCacheHitTokens    int                      `json:"prompt_cache_hit_tokens,omitempty"`
    PromptCacheMissTokens   int                      `json:"prompt_cache_miss_tokens,omitempty"`
    CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}
```

`Usage` 的扩展字段只用于 trace/预算；基础三项对 OpenAI 和 DeepSeek 都可用。

### 3.3 Result

```go
type Result struct {
    Content          string
    ReasoningContent string // DeepSeek thinking 模式；只用于 trace，不进入 ReAct 历史
    ID               string
    Model            string
    FinishReason     string
    Usage            Usage
}
```

`Complete` 只返回 `Content`；`CompleteResult` 返回完整 `Result`，供后续 token 预算和审计使用。

### 3.4 Client

```go
type Client struct {
    endpoint        string
    apiKey          string
    model           string
    temperature     float64
    maxTokens       int
    reasoningEffort string
    timeout         time.Duration
    http            *http.Client
}
```

`Client` 创建后不可变，可并发使用。

### 3.5 HTTPError

```go
type HTTPError struct {
    StatusCode int
    Status     string
    Message    string // 仅保存 API error.message，已截断
}

func (e *HTTPError) Error() string
```

### 3.6 哨兵错误

```go
var (
    ErrRequest          = errors.New("llm: request failed")
    ErrDecode           = errors.New("llm: decode response failed")
    ErrEmptyChoices     = errors.New("llm: response contains no choices")
    ErrEmptyContent     = errors.New("llm: response content is empty")
    ErrResponseTooLarge = errors.New("llm: response too large")
    ErrTimeout          = errors.New("llm: request timed out")
    ErrInvalidMessages  = errors.New("llm: invalid messages")
)
```

## 4. 构造函数与主要方法

```go
type Option func(*Client)

// WithHTTPClient 替换默认 HTTP Client。
// 默认 Client 禁止跟随重定向；注入的 Client 由调用方负责同等安全策略。
func WithHTTPClient(client *http.Client) Option

// New 校验配置并构造 Client，不发起网络请求。
// API Key 通过 cfg.ResolveAPIKey() 解析；允许为空（本地 mock 服务）。
func New(cfg config.LLMConfig, opts ...Option) (*Client, error)

// CompleteResult 执行一次 Chat Completions 调用，保留 usage/finish_reason。
func (c *Client) CompleteResult(ctx context.Context, messages []Message) (Result, error)

// Complete 实现 Agent 的 Completer 接口。
func (c *Client) Complete(ctx context.Context, messages []Message) (string, error)
```

`Complete` 是 `CompleteResult` 的薄封装：

```go
func (c *Client) Complete(ctx context.Context, messages []Message) (string, error) {
    result, err := c.CompleteResult(ctx, messages)
    if err != nil {
        return "", err
    }
    return result.Content, nil
}
```

Agent 侧接口保持不变：

```go
// internal/agent
type Completer interface {
    Complete(ctx context.Context, messages []llm.Message) (string, error)
}
```

可选的 usage 扩展接口：

```go
type UsageCompleter interface {
    CompleteResult(ctx context.Context, messages []llm.Message) (llm.Result, error)
}
```

Agent v1 使用 `Completer`；token 预算接入时再使用 `UsageCompleter`。

## 5. 配置与构造

### 5.1 配置字段

当前 `config.LLMConfig`（[config.go](../internal/config/config.go#L36)）包含：

| 字段 | 用途 |
|---|---|
| `base_url` | API 根地址，如 `https://api.openai.com/v1` |
| `api_key` | 显式 Key，优先级最高 |
| `api_key_env` | 无显式 Key 时读取的环境变量 |
| `model` | 模型名 |
| `temperature` | 采样温度，0 到 2 |
| `max_tokens` | 单次回复上限；0 表示不发送该字段 |
| `timeout_seconds` | 单次 HTTP 请求超时 |
| `reasoning_effort` | 推理强度；空表示不发送；DeepSeek 设为 `none` 关闭 thinking |

`reasoning_effort` 已作为可选字段实现：

```go
type LLMConfig struct {
    // ...已有字段
    ReasoningEffort string `json:"reasoning_effort"`
}
```

该字段只影响请求体；其他 OpenAI 兼容服务不配置时不会发送。

### 5.2 构造规则

1. `BaseURL` 非空，必须是 `http/https` 绝对 URL。
2. 拒绝 userinfo（`user:pass@host`）、query、fragment。
3. 拒绝已包含 `/chat/completions` 的 base URL，避免重复拼接。
4. `Model` 非空；`TimeoutSeconds > 0`；`Temperature ∈ [0,2]`；`MaxTokens >= 0`。
5. `ReasoningEffort` 为空或 `none` / `low` / `high` / `max` / `minimal` / `medium` / `xhigh`（兼容别名由服务端处理，客户端只做宽松校验）。
6. API Key 通过 `cfg.ResolveAPIKey()` 解析：显式 `api_key` 优先，否则读取 `api_key_env`。
7. 允许 Key 为空：本地 mock/无需鉴权的服务可以不发送 `Authorization`。
8. 默认 HTTP Client 禁止跟随重定向，避免 Authorization 被带到其他主机。

### 5.3 Endpoint 拼接

```go
func buildEndpoint(baseURL string) (string, error)
```

规则：

```text
https://api.openai.com/v1   → https://api.openai.com/v1/chat/completions
https://api.deepseek.com    → https://api.deepseek.com/chat/completions
https://api.deepseek.com/v1 → https://api.deepseek.com/v1/chat/completions（旧版兼容路径，以服务端实际支持为准）
```

实现时去掉 base path 末尾的 `/`，再追加 `/chat/completions`。

## 6. 请求协议

### 6.1 请求体

```go
type chatRequest struct {
    Model           string    `json:"model"`
    Messages        []Message `json:"messages"`
    Temperature     float64   `json:"temperature"`
    MaxTokens       int       `json:"max_tokens,omitempty"`
    ReasoningEffort string    `json:"reasoning_effort,omitempty"`
}
```

示例：

```json
{
  "model": "deepseek-flash",
  "messages": [
    {"role": "system", "content": "..."},
    {"role": "user", "content": "..."}
  ],
  "temperature": 0,
  "max_tokens": 8192,
  "reasoning_effort": "none"
}
```

- `temperature` 始终发送，0 也是有效值；DeepSeek thinking 模式下该字段无效。
- `max_tokens` 使用 `omitempty`；配置为 0 时不发送，交给服务端默认值。
- `reasoning_effort` 为空时不发送。
- 不发送 `stream`，默认非流式。
- 每次调用只发送一个请求，不做重试。

### 6.2 消息校验

```go
func validateMessages(messages []Message) error
```

规则：

1. `messages` 非空。
2. `role` 只允许 `system`、`user`、`assistant`。
3. `content` 非空（全空白也拒绝）。

校验失败返回 `ErrInvalidMessages`，不发起网络请求。

### 6.3 HTTP 请求

```go
reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
defer cancel()

req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.endpoint, bytes.NewReader(body))
req.Header.Set("Content-Type", "application/json")
req.Header.Set("Accept", "application/json")
if c.apiKey != "" {
    req.Header.Set("Authorization", "Bearer "+c.apiKey)
}
```

超时使用请求级 context，而不是只依赖 `http.Client.Timeout`：

- 调用方 `ctx` 更短时，以调用方为准；
- 配置的 `timeout_seconds` 始终生效；
- 注入测试 HTTP Client 时仍保留超时语义。

## 7. 响应解析

### 7.1 响应对象

```go
type responseMessage struct {
    Role             string `json:"role"`
    Content          string `json:"content"`
    ReasoningContent string `json:"reasoning_content,omitempty"`
}

type chatChoice struct {
    Index        int             `json:"index"`
    Message      responseMessage `json:"message"`
    FinishReason string          `json:"finish_reason"`
}

type chatResponse struct {
    ID      string       `json:"id"`
    Model   string       `json:"model"`
    Choices []chatChoice `json:"choices"`
    Usage   *Usage       `json:"usage,omitempty"`
}
```

### 7.2 解析规则

1. 只接受 HTTP 2xx；其他状态走 `HTTPError`。
2. `choices` 为空 → `ErrEmptyChoices`。
3. 取 `choices[0]`。
4. `message.content` 为空白 → `ErrEmptyContent`，错误文本带 `finish_reason`。
5. `reasoning_content` 写入 `Result.ReasoningContent`，不写入 Agent 历史。
6. `usage` 缺失时使用零值，不视为错误。
7. `finish_reason` 原样保留；`length` 由 ReAct Parser 判断 JSON 是否完整。
8. `content` 原样返回，不做 trim 或改写。

响应体上限为 **4 MiB**：

```go
func readCapped(r io.Reader, maxBytes int64) (data []byte, tooLarge bool, err error)
```

超限返回 `ErrResponseTooLarge`，不截断后交给 Agent。

## 8. 超时、取消与重试

### 8.1 超时

- 每次调用创建 `context.WithTimeout(ctx, c.timeout)`。
- 请求、响应头、响应体读取都受该 context 约束。
- Agent 总超时由外层 `runCtx` 控制；两者取更早的 deadline。

### 8.2 取消

- 父 context 取消：直接返回 `ctx.Err()`，Agent 映射为 `canceled`。
- 父 context 超时：直接返回 `ctx.Err()`，Agent 映射为 `timeout`。
- 本层请求超时：返回 `ErrTimeout`，Agent 可映射为 `timeout` 或 `llm_error`。

### 8.3 重试

**v1 不重试**：

- 重试会放大成本，Agent 已有步数预算。
- 非幂等性不由这一层判断。
- 失败直接返回，由 Agent 终止并保留错误摘要。

未来可选策略：

- 仅对网络错误、429、5xx 重试；
- 最多 2–3 次，指数退避 + jitter；
- 尊重 `Retry-After`；
- 不重试 4xx（除 429）和 context 取消。

## 9. 错误模型

### 9.1 错误映射

| 场景 | 返回 |
|---|---|
| 父 context 取消 | `ctx.Err()` → `context.Canceled` |
| 父 context 超时 | `ctx.Err()` → `context.DeadlineExceeded` |
| 本层请求超时 | `ErrTimeout` |
| 网络/连接错误 | `ErrRequest` |
| 非 2xx | `*HTTPError` |
| 响应非法 JSON | `ErrDecode` |
| `choices` 为空 | `ErrEmptyChoices` |
| `content` 为空 | `ErrEmptyContent`（含 finish_reason） |
| 响应超过 4 MiB | `ErrResponseTooLarge` |
| 消息非法 | `ErrInvalidMessages` |

### 9.2 HTTPError 解析

非 2xx 响应优先解析 OpenAI/DeepSeek 错误结构：

```json
{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}
```

只保留 `error.message`，最多 512 字符；无法解析时只返回 HTTP 状态，不回显原始响应体。这样避免把服务端回显的请求内容带进日志。

### 9.3 脱敏

- 不把 `Authorization` 头或 API Key 放入错误文本。
- 不打印请求体（可能包含文件内容）。
- 不打印完整响应体。
- 取消/超时与普通网络错误分开，便于 Agent 映射终止原因。

## 10. DeepSeek 兼容性

DeepSeek 提供 OpenAI 兼容的 Chat Completions 接口，协议层可以直接复用本 Adapter。以下依据 DeepSeek 官方文档（2026-11-10 查阅）：

- [Your First API Call](https://api-docs.deepseek.com/)
- [Chat Completions API](https://api-docs.deepseek.com/api/create-chat-completion)
- [Models & Pricing](https://api-docs.deepseek.com/quick_start/pricing)

### 10.1 推荐配置

```json
{
  "llm": {
    "base_url": "https://api.deepseek.com",
    "api_key_env": "DEEPSEEK_API_KEY",
    "model": "deepseek-flash",
    "temperature": 0,
    "max_tokens": 8192,
    "timeout_seconds": 120,
    "reasoning_effort": "none"
  }
}
```

当前官方模型为 `deepseek-flash` 和 `deepseek-v4-pro`。旧文档中的 `deepseek-chat` / `deepseek-reasoner` 已不在当前模型表中，不建议继续使用。

### 10.2 请求字段差异

DeepSeek 的请求字段与 OpenAI 基本一致，差异点：

| 字段 | 说明 |
|---|---|
| `base_url` | 使用 `https://api.deepseek.com`；本 Adapter 自动追加 `/chat/completions` |
| `model` | `deepseek-flash` 或 `deepseek-v4-pro` |
| `thinking` | DeepSeek 专有对象：`{"type":"enabled"}` / `{"type":"disabled"}` |
| `reasoning_effort` | `none` / `low` / `high` / `max`；`none` 关闭 thinking |
| `temperature` | thinking 模式下无效；非 thinking 模式有效 |
| `max_tokens` | 1–384K；未设置时非 thinking 默认 8K，thinking 默认 64K |
| `response_format` | 支持 `{"type":"json_object"}`，可选 |

**关键点：** DeepSeek 在请求同时省略 `thinking` 和 `reasoning_effort` 时，**thinking 默认开启**。SimpleAgent 的 ReAct 协议建议：

1. 优先设置 `reasoning_effort: "none"`（或 `thinking: {"type":"disabled"}`），降低延迟和成本；
2. 如果保持 thinking，增大 `max_tokens`（建议至少 8192，必要时 32768+）和 `timeout_seconds`（建议 120–180），因为推理 token 会消耗输出预算；
3. thinking 模式下 `temperature: 0` 不保证确定性，因为该参数不生效。

### 10.3 响应差异

DeepSeek thinking 模式的响应：

```json
{
  "choices": [{
    "message": {
      "role": "assistant",
      "content": "{\"thought\":\"...\",\"final_answer\":\"...\"}",
      "reasoning_content": "模型的思考过程..."
    },
    "finish_reason": "stop"
  }]
}
```

本 Adapter：

- 解析 `content` 作为 Agent 可见输出；
- 解析 `reasoning_content` 到 `Result.ReasoningContent`，仅用于 trace；
- `complete` 仍只返回 `content`；
- `content` 为 null/空白时返回 `ErrEmptyContent`。

`finish_reason` 可能为：

`stop`、`length`、`content_filter`、`tool_calls`、`insufficient_system_resource`、`aborted`。

当 `content` 为空且 `finish_reason=length` 时，错误文本应包含 finish_reason，便于判断是截断还是模型未输出。

### 10.4 Usage 差异

DeepSeek usage 额外包含：

- `prompt_cache_hit_tokens`
- `prompt_cache_miss_tokens`
- `completion_tokens_details.reasoning_tokens`

基础 `Usage` 已能解析前三项；扩展字段用于成本审计，不影响 `Complete`。

### 10.5 可选 JSON 模式

DeepSeek 支持：

```json
"response_format": {"type": "json_object"}
```

可以提升 ReAct JSON 的格式遵循，但官方限制：

- system/user 消息中必须明确要求 JSON；
- 如果模型输出无法识别为 JSON，API 会替换为空格，`content` 可能为空。

因此：

- 不默认开启，建议作为可选项；
- 即使开启，Adapter 仍把空 content 视为错误；
- 最终由 Parser 做字段互斥和合法性校验。

### 10.6 兼容性结论

| 项目 | 兼容情况 |
|---|---|
| 请求路径 `/chat/completions` | ✅ |
| Bearer 鉴权 | ✅ |
| `model/messages/temperature/max_tokens` | ✅ |
| `choices[0].message.content` | ✅ |
| 非 2xx OpenAI 错误结构 | ✅ |
| `usage` | ✅，可扩展 |
| thinking 默认开启 | ⚠️ 需配置 `reasoning_effort=none` 或增大 max_tokens |
| `reasoning_content` | ⚠️ 需扩展 Result 保留 |
| 流式 | ❌ 当前不支持 |
| 原生 tool calling | ❌ 当前使用提示词 ReAct |
| 重试/429 | ❌ 当前不重试 |

## 11. Agent 集成

### 11.1 接口

```go
type Completer interface {
    Complete(ctx context.Context, messages []llm.Message) (string, error)
}
```

`*llm.Client` 实现该接口。

### 11.2 错误映射

| LLM 错误 | Agent 终止原因 |
|---|---|
| `ctx.Err() == context.Canceled` | `canceled` |
| `ctx.Err() == context.DeadlineExceeded` | `timeout` |
| `errors.Is(err, llm.ErrTimeout)` | 可映射为 `timeout` 或 `llm_error` |
| `*HTTPError` | `llm_error`，保留状态码摘要 |
| `ErrEmptyChoices` / `ErrEmptyContent` | `llm_error` |
| 其他 | `llm_error` |

### 11.3 Token 使用

- v1 的 Agent 仍调用 `Complete`，不消费 usage。
- 后续 token 预算可改用 `CompleteResult` 或可选 `UsageCompleter`。

## 12. 测试设计

全部使用 `httptest`，不访问外网。

| 用例 | 断言 |
|---|---|
| `New` 非法配置 | 空 base、非 http(s)、userinfo、query、空 model、timeout ≤ 0、temperature 越界 |
| 请求格式 | 方法 POST、路径 `/v1/chat/completions`、Content-Type、Accept、Bearer 头 |
| 请求体 | model、messages、temperature、max_tokens；`max_tokens=0` 时字段省略 |
| 无 Key | 不发送 Authorization |
| 成功响应 | Content、ID、Model、FinishReason、Usage 正确 |
| reasoning 响应 | `reasoning_content` 进入 Result，且不影响 Content |
| 非 2xx | `*HTTPError` 状态码与 message 正确 |
| 空 choices | `ErrEmptyChoices` |
| 空 content | `ErrEmptyContent`，错误含 finish_reason |
| 非法 JSON | `ErrDecode` |
| 超大响应 | `ErrResponseTooLarge` |
| 父 context 取消 | `errors.Is(err, context.Canceled)` |
| 父 context 超时 | `errors.Is(err, context.DeadlineExceeded)` |
| 本层请求超时 | `errors.Is(err, llm.ErrTimeout)` |
| 重定向 | 3xx 不跟随，返回 `HTTPError` |
| 无重试 | 500 场景服务端只收到一次请求 |
| 密钥脱敏 | 错误文本不包含 API Key |
| 消息校验 | 空 messages、非法 role、空 content 被拒绝 |
| DeepSeek 配置 | `reasoning_effort` 设置时发送、为空时省略 |
| `readCapped` | 小上限下正确返回超限标记（内部测试） |

## 13. 验收标准

- [x] 发送正确路径、鉴权头、Content-Type 和请求体。
- [x] 成功解析 content、usage、finish_reason。
- [x] DeepSeek `reasoning_content` 可保留在 Result 中。
- [x] `max_tokens=0` 不发送该字段。
- [x] `reasoning_effort` 为空时不发送。
- [x] 非 2xx、空 choices、空 content、非法 JSON、超大响应都有明确错误。
- [x] 父 context 取消/超时与本层超时可区分。
- [x] 不跟随重定向，不重试。
- [x] 任何错误信息都不包含 API Key。
- [x] `go test ./...` 不依赖外网。
- [x] `*Client` 满足 Agent 的 `Completer` 接口。

## 14. 实现顺序

1. `internal/llm/types.go`：Message、Usage、Result、HTTPError、哨兵错误。
2. 配置层增加 `llm.reasoning_effort`（可选）。
3. `internal/llm/client.go`：New、URL 拼接、配置校验、默认 HTTP Client。
4. `CompleteResult`：请求构造、超时、状态处理、响应解析。
5. `Complete`：对 `Completer` 的薄封装。
6. `client_test.go`：httptest 覆盖请求/响应/错误/超时/脱敏/DeepSeek 字段。
7. Agent 集成：用 `Completer` 注入真实 Client 与脚本化假 LLM。

截至 v0.1，步骤 1–6 已实现并通过测试；步骤 7（Agent 集成）已完成，CLI 集成待后续。

## 15. 关键决策与开放问题

### 15.1 已确认决策

1. 使用 `config.LLMConfig` 构造，避免重复定义配置。
2. API Key 允许为空，兼容本地 mock；真实服务返回 401。
3. 请求级 context 超时，确保注入自定义 HTTP Client 时超时语义不丢失。
4. 默认禁止重定向，避免 Authorization 被带到其他主机。
5. 非 2xx 不回显原始 body，只保留 API error.message。
6. 保留 `CompleteResult` 和 `Result.ReasoningContent`，但 `Completer` 接口不变。
7. v1 不重试；重试作为后续显式策略。
8. DeepSeek 推荐 `reasoning_effort=none`，避免默认 thinking 影响 ReAct。

### 15.2 开放问题

1. 是否在 CLI 启动时强制要求非空 API Key（当前允许空）。
2. 是否在 v1 就把 `ErrTimeout` 映射为 Agent 的 `timeout` 终止原因。
3. 是否支持流式、原生 tool calling、多模态 content、`response_format`。
4. 是否引入重试与 `Retry-After` 解析。
5. 是否用 `CompleteResult.Usage` 接入 token 成本预算。
6. DeepSeek thinking 模式是否长期保留，或只作为可选调试能力。

## 16. 实现状态

当前状态：

- 配置层：已实现，包含可选字段 `llm.reasoning_effort` 及校验。
- 工具层：已实现。
- LLM Adapter：已实现，`internal/llm` 通过 `httptest` 测试与 `-race`。
- Agent Loop：已实现，测试通过。
- CLI：尚未实现。

已同步：

- [DESIGN.md](../DESIGN.md) §5.1、§5.2、§7.2、§7.4、§11、§14 与本文件保持一致；
- `config.example.json` 未强制填写 `reasoning_effort`，这是可选字段；
- 测试不访问真实 DeepSeek/OpenAI 服务。
