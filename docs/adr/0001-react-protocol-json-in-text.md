# ReAct 协议用 JSON-in-text，不用 function calling

模型的每次回复是一个 JSON 对象（`thought` / `action` / `action_input` / `final_answer`），由 `internal/agent/parser.go` 自行解析，而不是走 OpenAI 的 `tool_calls` / `function calling`。

**为什么**：只依赖标准库，任何 OpenAI 兼容端点都能用，不把可用模型缩小到支持 tool calling 的那一批；解析器还能把格式错误变成可诊断的 `ParseErrorKind`，而不是被供应商的错误码牵着走。

**代价**：格式得靠 prompt 教会模型，格式错误要靠重试与诊断吸收。

## Considered options

- **`tool_calls` / function calling**：结构更硬，但绑定供应商的工具声明格式，且不同兼容端点对它的支持并不一致。
