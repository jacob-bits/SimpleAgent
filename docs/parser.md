# Parser 详细设计

| 项目 | 内容 |
|---|---|
| 文档版本 | v0.1 |
| 状态 | 已实现，测试通过；Agent Loop 与 CLI 已接入 |
| 日期 | 2026-11-10 |
| 上位设计 | [DESIGN.md](../DESIGN.md) §4、§5.3、§6.1、§11、§14 |
| 关联设计 | [LLM Adapter 详细设计](llm_adapter.md)、[工具定义设计](tool_definition.md) |
| 实现位置 | `internal/agent/parser.go`、`internal/agent/parser_test.go` |
| 输入 | LLM 返回的单步文本 |
| 输出 | `Step` 或 `*ParseError` |

> 本文档定义 ReAct 单步解析器的公开契约、解析流水线、错误模型和测试边界。Parser 只回答“这段文本是不是一个合法的单步协议对象”，不判断工具是否存在、不校验工具参数、不执行工具、不做终止决策。

## 1. 背景与定位

LLM Adapter 返回的是普通字符串。Agent Loop 在拿到这段文本后，必须先把它变成一个结构化步骤，才能决定是调用工具还是结束：

```text
llm.Client.Complete()
        ↓  string
ParseStep(raw)
        ↓  Step / *ParseError
Agent Loop
        ├── final_answer → 检查 min_steps → 结束
        └── action       → Registry.Execute() → observation → 下一轮
```

Parser 是模型输出进入系统的第一道形状校验。它的输入不可信，输出必须满足固定不变量，否则 Agent 不能安全地继续执行。

### 1.1 职责边界

| 问题 | 归属 | 说明 |
|---|---|---|
| 文本中是否存在一个 JSON 对象 | Parser | 纯 JSON、代码块、前后说明文字 |
| 顶层字段是否合法、类型是否正确 | Parser | 未知字段、类型、重复键、互斥关系 |
| `action` 是否非空 | Parser | 不检查工具是否存在 |
| `action` 是否在注册表中 | Registry | 未知工具作为失败 observation 返回 |
| `action_input` 是否符合工具 Schema | Invoker | 必填、类型、范围、未知参数 |
| `final_answer` 是否达到 `min_steps` | Agent | Parser 只保证答案非空 |
| 是否重试或修复格式 | Agent / 未来策略 | v1 明确不重试 |
| 工具是否真正执行成功 | Tool / Invoker | Parser 不接触工具 |
| 终止原因与退出码 | Agent / CLI | Parser 只返回解析错误 |

这个边界有三个直接后果：

1. 模型写了一个不存在的工具名，不会被判为 `invalid_response`，而是得到 `unknown tool: ...` 的 observation，仍有机会修正。
2. 模型给出的工具参数不合法，也不由 Parser 拒绝；Invoker 会把它变成模型可见的失败结果。
3. Parser 不依赖 Registry、Config、LLM 或工具包，可以用纯字符串做完整单测。

## 2. 目标与非目标

### 2.1 目标

1. 从纯 JSON、Markdown 代码块中的 JSON、以及前后带少量说明文字的响应中提取一个 JSON 对象。
2. 严格校验 ReAct 单步协议：`action` 与 `final_answer` 互斥、字段类型正确、必填字段存在。
3. 拒绝会产生歧义的输入：顶层数组、顶层标量、顶层未知字段、重复顶层键、非对象 `action_input`。
4. 所有失败都返回可判断类型的 `*ParseError`，包装 `ErrInvalidOutput`，并附带截断后的原文片段。
5. 对输入大小、错误片段大小和解析复杂度设上限，不 panic、不阻塞、不执行副作用。
6. 纯函数实现，可用表驱动测试和 fuzz 测试覆盖，不依赖真实 LLM 或外网。

### 2.2 非目标

- 不实现“一次修复提示”或自动重试；解析失败立即终止，由 Agent 映射为 `invalid_response`。
- 不实现 JSON5、注释、尾随逗号、单引号、Python 字面量等非标准 JSON 扩展。
- 不实现多个工具调用；一步最多一个 `action`。
- 不校验 `action` 是否存在于 Registry。
- 不校验 `action_input` 的工具级 Schema，这部分属于 Invoker。
- 不解释 thought 的语义，不做链条推理校验。
- 不从自然语言中“理解”意图；前后说明文字的容忍仅用于提取 JSON 对象。
- 不负责 observation 转义、历史裁剪、重复动作检测或 `no_progress` 判定。
- 不记录日志、不输出 trace；调用方决定如何展示错误。
- 不把 `finish_reason` 作为输入；LLM Adapter 只返回 content，截断导致的残缺 JSON 会自然解析失败。

## 3. 包结构与依赖

```text
internal/agent/
├── parser.go       # Step、ParseStep、ParseError、解析辅助函数
└── parser_test.go  # 表驱动测试 + fuzz seed
```

后续 Agent Loop 落地时，同包会增加 `agent.go`、`prompt.go` 等文件；Parser 本身不依赖它们。

依赖方向：

```text
internal/agent/parser.go → 标准库
internal/agent/parser.go ✗→ internal/config
internal/agent/parser.go ✗→ internal/llm
internal/agent/parser.go ✗→ internal/tool
```

`parser.go` 仅使用 `bytes`、`encoding/json`、`errors`、`fmt`、`io`、`sort`、`strings`、`unicode/utf8` 等标准库。Parser 不依赖 `llm.Message`，从而避免为了解析一个字符串而引入整个适配层；Agent 在调用处完成类型衔接。后续同包的 `agent.go` 会依赖 `internal/llm` 与 `internal/tool`，这不改变 `parser.go` 的独立性。

## 4. 公开契约

### 4.1 Step

```go
type Step struct {
    Thought     string          `json:"thought"`
    Action      string          `json:"action,omitempty"`
    ActionInput json.RawMessage `json:"action_input,omitempty"`
    FinalAnswer string          `json:"final_answer,omitempty"`
}
```

字段语义：

| 字段 | 类型 | 何时存在 | 约束 |
|---|---|---|---|
| `Thought` | string | 总是 | 可为空；缺失时视为 `""`；不 trim、不改写 |
| `Action` | string | 工具调用步骤 | 非空（TrimSpace 后）；保留原字符串 |
| `ActionInput` | JSON object | 工具调用步骤 | 非 nil、合法 JSON 对象；已压缩空白 |
| `FinalAnswer` | string | 结束步骤 | 非空（TrimSpace 后）；保留原字符串 |

成功返回的 `Step` 必须满足：

1. `Action` 与 `FinalAnswer` 恰好一个非空。
2. `Action != ""` 时，`ActionInput` 非 nil 且表示一个 JSON 对象。
3. `FinalAnswer != ""` 时，`ActionInput == nil`。
4. `Thought` 可以是空字符串。
5. 不保证 `Action` 存在于工具注册表，也不保证 `ActionInput` 满足工具 Schema。

### 4.2 ParseStep

```go
func ParseStep(raw string) (Step, error)
```

性质：

- **纯函数**：不读取全局状态，不访问文件、网络或时钟。
- **并发安全**：可以被多个 goroutine 同时调用。
- **无 context**：输入有硬上限，解析是 O(n) 内存操作，不存在需要取消的阻塞点。
- **无副作用**：不执行工具、不写日志、不修改输入。
- **确定性**：同一输入总是返回同一结果；错误文本中的字段顺序也保持稳定。
- **不做修复重试**：解析失败不会再次调用 LLM。

### 4.3 错误类型

```go
var ErrInvalidOutput = errors.New("invalid model response")

type ParseErrorKind string

const (
    ParseErrEmpty           ParseErrorKind = "empty_response"
    ParseErrTooLarge        ParseErrorKind = "response_too_large"
    ParseErrInvalidUTF8     ParseErrorKind = "invalid_utf8"
    ParseErrNoObject        ParseErrorKind = "no_json_object"
    ParseErrInvalidJSON     ParseErrorKind = "invalid_json"
    ParseErrNotObject       ParseErrorKind = "not_object"
    ParseErrUnknownField    ParseErrorKind = "unknown_field"
    ParseErrDuplicateField  ParseErrorKind = "duplicate_field"
    ParseErrFieldType       ParseErrorKind = "field_type"
    ParseErrConflict        ParseErrorKind = "conflict"
    ParseErrMissingOutcome  ParseErrorKind = "missing_outcome"
    ParseErrEmptyAction     ParseErrorKind = "empty_action"
    ParseErrMissingInput    ParseErrorKind = "missing_action_input"
    ParseErrInvalidInput    ParseErrorKind = "invalid_action_input"
    ParseErrEmptyFinal      ParseErrorKind = "empty_final_answer"
    ParseErrUnexpectedInput ParseErrorKind = "unexpected_action_input"
)

type ParseError struct {
    Kind    ParseErrorKind
    Detail  string // 面向开发者的说明，最多 1024 个 rune
    Snippet string // 截断后的候选文本，最多 512 个 rune
}

func (e *ParseError) Error() string
func (e *ParseError) Unwrap() error { return ErrInvalidOutput }
```

`ParseError.Error()` 的格式固定为：

```text
invalid model response: conflict: "action" and "final_answer" are mutually exclusive (raw="{\"action\":\"read_file\",\"final_answer\":\"ok\"}")
```

规则：

1. `Kind` 用于测试、日志分类和统计，是稳定 API；`Detail` 的文案可以调整。
2. `Snippet` 来自**触发错误的候选文本**：如果使用了 fenced block，则是 block 内容；否则是整段原文。最多 512 个 rune，超出部分以 `...` 结尾。
3. `Error()` 使用 `%q` 输出 snippet，转义换行、引号、制表符和 ANSI 控制字符，避免终端注入。
4. `errors.Is(err, ErrInvalidOutput)` 必须为 true；`errors.As(err, &parseErr)` 必须成功。
5. 错误信息只包含截断后的模型输出片段和开发者说明，不附加配置、请求头或工具执行结果；模型输出本身可能包含敏感内容，调用方应把 snippet 当作不可信数据。
6. 空输入、超限、非法 UTF-8 等早期错误也返回 `*ParseError`，不返回裸字符串错误。

错误分类：

| Kind | 触发条件 |
|---|---|
| `empty_response` | trim/BOM 处理后为空 |
| `response_too_large` | 原始输入超过 `MaxStepBytes` |
| `invalid_utf8` | 原始输入不是合法 UTF-8 |
| `no_json_object` | 进入扫描阶段，文本中没有任何 `{` |
| `invalid_json` | 进入扫描阶段且出现过 `{`，但没有可解析的完整对象，或候选片段 JSON 语法非法 |
| `not_object` | 顶层是数组、字符串、数字、布尔或 null；或选中的 fence 是合法非对象 JSON |
| `unknown_field` | 顶层出现 `thought`、`action`、`action_input`、`final_answer` 之外的字段 |
| `duplicate_field` | 顶层同一个键出现多次 |
| `field_type` | 字段类型不是协议要求（如 `thought` 为数字、`action` 为对象） |
| `conflict` | `action` 与 `final_answer` 同时存在 |
| `missing_outcome` | 两者都不存在 |
| `empty_action` | `action` 存在但为空白 |
| `missing_action_input` | 有 `action` 但没有 `action_input` |
| `invalid_action_input` | `action_input` 不是 JSON 对象 |
| `empty_final_answer` | `final_answer` 存在但为空白 |
| `unexpected_action_input` | 使用 `final_answer` 时仍带有 `action_input` |

## 5. 输入预处理

### 5.1 上限常量

```go
const (
    MaxStepBytes         = 1 << 20 // 1 MiB
    MaxErrorSnippetRunes = 512
    MaxErrorDetailRunes  = 1024
)
```

- `MaxStepBytes` 按字节计算，在 BOM、trim、fence 扫描之前检查。
- 1 MiB 远大于正常 ReAct 步骤，也小于 LLM Adapter 的 4 MiB 响应上限，形成第二道防线。
- v1 不把该上限做成配置项；如未来需要，必须同时考虑 LLM 的 `max_tokens` 和上下文预算。
- 超限时不解析、不分配候选对象，直接返回 `response_too_large`。

### 5.2 处理顺序

```text
1. len(raw) > MaxStepBytes ? → response_too_large
2. utf8.ValidString(raw) ?   → 否：invalid_utf8
3. TrimSpace → 去除首个 UTF-8 BOM → TrimSpace
4. 为空 ?                     → empty_response
5. 选择 payload 来源（fence 或原文）
6. 解析来源
```

说明：

- 只去除经过首次 trim 后仍位于开头的**首个** BOM（`\ufeff`），不处理正文中间出现的 BOM。
- 使用 Unicode-aware 的 `strings.TrimSpace`；CRLF、全角空格和普通空白均可处理。
- 不转换大小写、不做 Unicode 归一化、不删除零宽字符。
- 内部换行、缩进和字符串内的空白保留；对象内的空白由 `json.Compact` 在 `ActionInput` 上消除。
- 非法 UTF-8 必须在 `utf8.ValidString` 处拒绝，避免后续扫描把多字节序列误当字节边界。

## 6. 解析流水线

### 6.1 选择 payload 来源

模型常见输出有三类：

```json
{"thought":"...","action":"read_file","action_input":{"path":"notes.txt"}}
```

~~~text
好的，我来读取文件：
```json
{"thought":"...","action":"read_file","action_input":{"path":"notes.txt"}}
```
~~~

~~~text
{"thought":"...","final_answer":"项目代号是 Orion。"}
以上是最终答案。
~~~

解析器先找 Markdown fenced block，再决定以哪个文本作为解析来源：

1. 逐行扫描以三个或更多反引号开头的 fenced block；记录 info string 和 block 正文。
2. 优先选择第一个 info string 为 `json`（大小写不敏感）的 block。
3. 如果没有 `json` block，选择第一个**无 info string**且正文 trim 后以 `{` 或 `[` 开头的 block。
4. 如果都没有，使用整段原文。
5. 选中的 block 就是唯一 payload 来源；它解析失败时不再回退到其他 block 或原文。

选择规则的理由：

- fenced block 是模型显式声明的结构化输出，应优先于正文中的示例对象。
- `json` 标签比无标签更明确；有多个 JSON block 时，第一个是预期输出。
- 无标签 block 只有看起来像 JSON 容器时才作为 payload，避免把 bash/text 示例误当成答案。
- 一旦明确选中一个 payload，失败就应报告，而不是继续搜索另一个“可能能过”的对象；后者会掩盖协议违规。
- 其他语言标签（`text`、`bash`、`javascript` 等）不参与 payload 选择；如果最终回退到全文扫描，其中的对象仍可能被扫描到，这是容忍性而非推荐行为。

fence 语法细节：

- 只支持反引号 fence，不支持 `~~~`；tilde fence 作为普通文本处理。
- 开头行允许最多 3 个前导空格；info string 取反引号后的第一个字段。
- fence 正文必须从开头行的下一行开始；同一行反引号后的剩余内容全部视为 info string，不作为 JSON 正文。
- 结束行必须是“仅由至少同样数量的反引号组成”的行。
- 未闭合的 fence：正文一直延伸到输入末尾。
- 空 fence 正文会得到 `empty_response`。

### 6.2 来源分类

选定来源 `source` 后，先 trim，再按以下顺序处理：

```text
1. source 为空 ?                         → empty_response
2. json.Valid(source) ?
   ├── 是，且首字符 == '{'              → 解码对象 → 字段校验
   ├── 是，且首字符 != '{'              → not_object
   └── 否 → 下一步
3. source 首字符 == '[' ?                → not_object
4. 扫描平衡的 {...} 片段
```

为什么先判断 `json.Valid`：

- 纯数组 `[{"action":"read_file",...}]` 是合法 JSON，但顶层不是对象；必须在扫描前拒绝，不能把数组里的对象当作单步。
- 纯字符串、数字、布尔、null 同理。
- 如果整段不是合法 JSON，但有前后说明文字，则进入扫描模式，提取第一个平衡对象。

为什么额外的 `source[0] == '['` 检查：

- 覆盖“数组被截断/带尾随文字，导致 `json.Valid` 为 false”的情况，避免扫描到数组内部的对象。
- 代价是以 `[` 开头的说明文字会被判为 `not_object`；这属于不推荐输出，可接受。

### 6.3 对象扫描

扫描只负责找出候选 span，不做字段校验：

```go
func scanObjects(src string) []span
```

规则：

1. 从左到右寻找 `{`。
2. 从该位置开始维护 `depth`、`inString`、`escaped` 三个状态。
3. 字符串内的 `{`、`}`、`"` 不参与括号计数；反斜杠转义下一个字符。
4. `depth` 回到 0 时，得到一个完整的候选对象 span。
5. 如果到输入末尾仍未回到 0，视为未闭合对象，不再产生候选。
6. 对每个候选调用 `decodeObject`：
   - 语法解码失败：记录错误，跳过整个 span，继续找下一个候选。
   - 顶层重复键：这是协议错误，立即返回 `duplicate_field`，**不再搜索后续对象**。
   - 解码成功：立即进入字段校验，**不再看后续对象**。
7. 字段校验失败：立即返回该错误，**不再尝试后续对象**。
8. 扫描结束仍没有可解析对象：
   - 文中出现过 `{` → `invalid_json`（含未闭合对象、所有候选均语法非法）。
   - 文中没有 `{` → `no_json_object`。

这个顺序解决了三个容易混淆的场景：

| 输入 | 行为 | 原因 |
|---|---|---|
| 说明文字中有 `{not json}`，后面才是真正 JSON | 跳过错片段，解析真正对象 | 语法非法的候选可以被跳过 |
| 第一个对象字段冲突，第二个对象合法 | 返回 `conflict` | 第一个语法合法对象是模型的实际意图；跳过会掩盖错误 |
| 说明文字中有 JSON 示例，后面是另一个 JSON | 返回第一个示例的结果/错误 | 协议要求只输出一个对象；Parser 不猜语义 |

复杂度：

- 扫描是 O(n)，每个候选 span 互不重叠，`decodeObject` 的总输入规模不超过原文长度。
- 不递归遍历 JSON，只做一次括号扫描；嵌套对象是外层 span 的一部分。
- 不使用正则回溯，不存在指数级行为。

### 6.4 解码对象

`decodeObject` 使用 `json.Decoder` 的 token 流，而不是直接 `json.Unmarshal` 到 `map`，以便检测重复顶层键：

```go
func decodeObject(src string) (map[string]json.RawMessage, error) {
    dec := json.NewDecoder(strings.NewReader(src))
    tok, err := dec.Token()
    // 必须是 '{'

    fields := make(map[string]json.RawMessage)
    for dec.More() {
        key, _ := dec.Token()
        // 同一 key 出现第二次 → duplicate_field
        var raw json.RawMessage
        dec.Decode(&raw)
        fields[key] = raw
    }
    // 读掉 '}'，并确认后面只有空白
    return fields, nil
}
```

规则：

- 顶层必须是 JSON 对象；不是对象时返回 `not_object`。
- 顶层重复键拒绝，因为两者可能让“冲突检测”和“实际执行”看到不同值。
- 字段值以 `json.RawMessage` 保存，Parser 不提前把数字转成 `float64`。
- 未知字段的检查在解码之后进行，以便一次性报告所有未知字段。
- 候选 span 在构造上已经完整，但仍检查解码后的尾随 token，防止实现偏差。
- **已知限制**：只检测顶层重复键。`action_input` 内部的重复键由标准库按“后者覆盖前者”处理；当前不在 Parser 做递归重复键检测（见 §12.2）。

### 6.5 字段校验

校验顺序固定如下，保证同一输入得到稳定错误：

```text
1. 未知顶层字段           → unknown_field
2. action 与 final 同时存在 → conflict
3. 两者都不存在            → missing_outcome
4. thought 类型             → field_type
5. 工具调用分支：
   a. action 类型/非空      → field_type / empty_action
   b. action_input 存在?    → missing_action_input
   c. action_input 是对象?  → invalid_action_input
6. 最终答案分支：
   a. final_answer 类型/非空 → field_type / empty_final_answer
   b. action_input 不存在?   → unexpected_action_input
7. 返回 Step
```

字段规则：

| 字段 | 规则 | 错误 |
|---|---|---|
| `thought` | 可选；存在时必须是 JSON string；缺失视为 `""`；空串合法 | `field_type` |
| `action` | 存在时必须是 string；TrimSpace 后非空；保留原值 | `field_type` / `empty_action` |
| `action_input` | 有 action 时必填；必须是 JSON object；`{}` 合法；压缩空白后返回 | `missing_action_input` / `invalid_action_input` |
| `final_answer` | 存在时必须是 string；TrimSpace 后非空；保留原值 | `field_type` / `empty_final_answer` |
| 其他字段 | 不允许 | `unknown_field` |

实现细节：

- **存在性优先于值**：`{"action":"","final_answer":"x"}` 返回 `conflict`，而不是 `empty_action`。
- **不 trim 返回值**：`action` 和 `final_answer` 只用于判断是否为空，实际值保持模型原文，避免静默改写。
- **action 只检查非空**：不检查命名正则，也不查 Registry；未知工具由 Registry 返回 observation。
- **action_input 只检查对象类型**：不检查字段、不填充默认值、不执行工具 Schema 校验；这些全部交给 Invoker。
- **action_input 压缩**：用 `json.Compact` 去除无意义空白，保留键顺序和转义形式。Agent 的签名规范化仍会再次反序列化/序列化。
- **final_answer 允许任意文本**：包括换行、引号、花括号、Markdown、中文和 emoji；Parser 不转义、不摘要、不截断。
- **未知字段严格拒绝**：用于捕获 `finalanswer`、`actionInput` 之类拼写错误，避免字段被静默忽略后模型以为已结束；排序后最多列出 10 个字段名，其余只报数量。
- **错误文本有界**：`Detail` 最多 1024 个 rune，字段名过长时同样截断，避免恶意键名生成超大错误串；字段名和键名一律用 `%q` 引用，防止控制字符进入日志。
- **字段名大小写敏感**：只接受小写蛇形命名，不接受 `Action`、`FinalAnswer`、`action-input`。

### 6.6 成功结果示例

工具调用：

```go
Step{
    Thought:     "需要读取文件",
    Action:      "read_file",
    ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
}
```

最终答案：

```go
Step{
    Thought:     "文件内容已足够回答",
    FinalAnswer: "项目代号是 Orion。",
    // ActionInput == nil
}
```

空 thought：

```json
{"action":"read_file","action_input":{"path":"notes.txt"}}
```

```go
Step{
    Thought:     "",
    Action:      "read_file",
    ActionInput: json.RawMessage(`{"path":"notes.txt"}`),
}
```

## 7. 与 Agent 的集成

### 7.1 调用位置

按 DESIGN.md §6.1 的循环，Parser 在每次 LLM 调用之后、任何工具执行之前被调用：

```go
raw, err := a.llm.Complete(runCtx, messages)
if err != nil {
    // llm 错误映射
}

st, err := ParseStep(raw)
if err != nil {
    return Result{Steps: step, Reason: StopInvalidOutput}, err
}

messages = append(messages, llm.Message{Role: "assistant", Content: raw})
```

- 只有解析成功后才把原始 assistant 文本追加到历史；失败时错误片段已经足够定位问题。
- 原始文本按模型返回原样保存，不把 `Step` 重新序列化后写回历史，避免丢失模型的格式信息。
- `st.FinalAnswer != ""` 时，Agent 才检查 `step >= min_steps`；Parser 不参与最小轮次判断。
- `st.Action != ""` 时，Agent 才检查重复动作签名并调用 `Registry.Execute`。

### 7.2 错误映射

| 来源 | Agent 处理 | StopReason |
|---|---|---|
| `ParseStep` 返回 `*ParseError` | 立即终止，不重试 | `invalid_response` |
| 未知工具 | `ToolResult{Success:false}` → observation | 不终止 |
| 工具参数错误 | Invoker 返回失败结果 → observation | 不终止 |
| 工具预期失败（文件不存在等） | observation | 不终止 |
| 工具内部 Go error（取消除外） | 归一化为失败 observation | 不终止 |
| LLM 网络/HTTP 错误 | 终止 | `llm_error` |

`errors.Is(err, ErrInvalidOutput)` 是 Agent 判断“这是解析失败”的推荐方式。所有 `ParseError` 都满足该条件。

### 7.3 重复动作与签名

Parser 只保证 `ActionInput` 是合法对象并压缩空白。Agent 的 `canonicalSignature(action, input)` 仍需：

1. 反序列化 `ActionInput`；
2. 重新序列化（或递归排序键）得到稳定签名；
3. 与上一步签名比较。

原因：`json.Compact` 只去除空白，不负责键排序和数字规范化；这些属于 Agent 的签名规范化职责，不应塞进 Parser。

### 7.4 追踪

Agent 的 trace 可以记录：

- `step`
- `thought`（截断）
- `action`
- `action_input`（截断）
- `final_answer` 是否存在（不打印全文）
- 解析失败时的 `ParseError.Kind`

Parser 自身不打印任何内容，避免库代码直接写 stderr。

## 8. 安全与健壮性

### 8.1 输入边界

- 1 MiB 硬上限，防止超大响应导致扫描和 JSON 解码占用过多 CPU/内存。
- 错误片段最多 512 个 rune，`Detail` 最多 1024 个 rune，防止把完整模型输出或超长字段名写入日志。
- `Snippet` 用 `%q` 输出，转义换行、控制字符和 ANSI 序列，降低终端注入风险。
- `Snippet` 是模型输出的一部分，可能包含模型引用或编造的文件内容；调用方不得把它当作可信数据，也不应绕过 `%q` 直接打印。
- `Detail` 中出现的字段名/键名使用 `%q` 引用，避免恶意键名中的控制字符或 ANSI 序列进入日志。
- `json.Decoder` 自带嵌套深度限制；Parser 不递归遍历任意深度。
- 扫描复杂度 O(n)，候选 span 不重叠，不存在二次复杂度。

### 8.2 不执行、不解释

- Parser 不执行工具、不读取文件、不访问网络、不调用 LLM。
- Parser 不把 `thought`、`final_answer` 或 `action_input` 的内容当指令。
- 文件内容中的 prompt injection 不由 Parser 处理；Parser 只处理模型输出文本，不处理 observation。
- `action` 的合法性由 Registry 判定；Parser 不因为未知工具而终止，避免把工具配置错误误判为模型格式错误。

### 8.3 歧义处理

- 顶层重复键拒绝，避免“冲突检测”和“执行”读取不同的 `action`。
- 数组/标量根拒绝，避免数组内部的对象被误认为单步。
- 多个对象时只接受第一个语法合法对象；字段错误不跳过，避免“找下一个能过的对象”。
- 未知字段拒绝，避免拼写错误被静默忽略。
- 非对象 `action_input` 拒绝，避免把字符串/数组传给 Invoker 后再报一个更晚、更难定位的错误。

### 8.4 已知限制

1. Parser 不理解自然语言。如果说明文字里先出现一个语法合法但并非本意的 JSON 示例，Parser 会把它当作实际步骤；协议要求模型只输出一个对象，这是可接受的严格性。
2. 只检测顶层重复键；`action_input` 内部重复键不单独拒绝。
3. 不支持 `~~~` fence、JSON5、尾随逗号、注释、单引号。
4. 1 MiB 上限是编译期常量，v1 不可配置。
5. 错误片段是候选文本的前 512 个 rune，不保证包含错误发生的确切位置。
6. Parser 只校验形状；工具参数的业务合法性仍由 Invoker 和工具实现负责。

## 9. 测试设计

### 9.1 测试组织

`internal/agent/parser_test.go` 使用表驱动测试，按组组织：

| 组 | 用例 |
|---|---|
| 基础成功 | 纯 action、纯 final、thought 缺失、thought 为空、`action_input={}`、Unicode、嵌套引号/花括号 |
| 包装形式 | 纯 JSON、`json`/未标记 fence、前后说明文字、多个 fence、未闭合 fence、CRLF、BOM |
| 来源选择 | JSON fence 优先于正文示例、第一个 JSON fence 生效、失败不回退、未标记 fence 的容器判断 |
| 提取规则 | 前后花括号、字符串内花括号、转义引号、嵌套对象、第一个对象、跳过错片段、字段错误不跳过后面的对象 |
| 协议错误 | action/final 冲突、两者都缺失、空 action、空 final、缺 action_input、非对象 action_input、final 带 action_input |
| 字段错误 | 未知字段、重复顶层键、thought 为数字/null、action 为对象、final 为数组、大小写错误字段 |
| 根类型错误 | 纯数组、数组包裹 action、纯字符串、数字、布尔、null |
| 边界条件 | 空串、纯空白、仅 BOM、1 MiB 边界、超限、非法 UTF-8、snippet 截断 |
| 错误契约 | `errors.Is`、`errors.As`、Kind、Detail、Snippet 长度与转义 |

### 9.2 关键断言

成功用例：

```go
st, err := ParseStep(raw)
if err != nil { t.Fatal(err) }
if st.Action != "" && st.FinalAnswer != "" { t.Fatal("both outcomes set") }
if st.Action != "" && len(st.ActionInput) == 0 { t.Fatal("missing action input") }
if st.FinalAnswer != "" && st.ActionInput != nil { t.Fatal("final must not have input") }
```

失败用例：

```go
_, err := ParseStep(raw)
var pe *ParseError
if !errors.As(err, &pe) { t.Fatalf("want *ParseError, got %T", err) }
if !errors.Is(err, ErrInvalidOutput) { t.Fatal("must wrap ErrInvalidOutput") }
if pe.Kind != wantKind { t.Fatalf("kind = %q, want %q", pe.Kind, wantKind) }
```

fence 用例应同时断言“选中了哪个对象”，而不只断言“解析成功”，否则无法发现来源优先级回归。

### 9.3 Fuzz

```go
func FuzzParseStep(f *testing.F) {
    // 添加纯 JSON、fence、说明文字、冲突、空输入等 seed
    f.Fuzz(func(t *testing.T, raw string) {
        st, err := ParseStep(raw)
        if err != nil {
            if !errors.Is(err, ErrInvalidOutput) {
                t.Fatalf("error does not wrap ErrInvalidOutput: %v", err)
            }
            return
        }
        if (st.Action == "") == (st.FinalAnswer == "") {
            t.Fatalf("exactly one outcome required: %+v", st)
        }
        if st.Action != "" && (len(st.ActionInput) == 0 || st.ActionInput[0] != '{') {
            t.Fatalf("action without object input: %+v", st)
        }
        if st.FinalAnswer != "" && st.ActionInput != nil {
            t.Fatalf("final with input: %+v", st)
        }
    })
}
```

fuzz 目标：

- 不 panic。
- 成功结果满足 §4.1 的不变量。
- 失败结果始终是 `*ParseError` 且包装 `ErrInvalidOutput`。
- 不出现无限循环或超时。

### 9.4 验证命令

```bash
gofmt -l .
go vet ./...
go test ./internal/agent -run TestParseStep -v
go test ./... -race
go test ./internal/agent -run '^$' -fuzz=FuzzParseStep -fuzztime=30s
```

覆盖率目标：Parser 分支覆盖率不低于 95%；关键错误分支必须全部命中。

当前实现覆盖率为 **95.7%**（`go test ./internal/agent -cover`）。

## 10. 验收标准

- [x] `ParseStep` 是纯函数，只依赖标准库，可并发调用。
- [x] 纯 JSON 的 action / final 均能解析，`thought` 缺失时为空串。
- [x] `json` fence、无标签 fence、前后说明文字、CRLF、BOM 均能正确处理。
- [x] JSON fence 优先于正文对象；选中的 fence 解析失败时不回退。
- [x] 字符串内的花括号、引号、转义和嵌套对象不会破坏扫描。
- [x] `action` 与 `final_answer` 同时存在时返回 `conflict`。
- [x] 两者都缺失、action 为空、final 为空、缺 action_input、非对象 action_input、final 带 action_input 均返回对应错误。
- [x] 顶层数组、标量、null 返回 `not_object`，不会解析数组内部对象。
- [x] 顶层未知字段和重复键被拒绝。
- [x] `action` 不存在于 Registry 时 Parser 仍解析成功（存在性由 Registry 判断）。
- [x] `action_input={}` 解析成功；`action_input` 内字段不合法时 Parser 不报错（由 Invoker 报错）。
- [x] 所有失败错误满足 `errors.Is(err, ErrInvalidOutput)`，且 `errors.As(err, &pe)` 能取出 `*ParseError`。
- [x] 超过 1 MiB、空输入、非法 UTF-8、无对象文本均返回稳定 Kind。
- [x] 错误 snippet 最多 512 个 rune、`Detail` 最多 1024 个 rune，且 snippet 使用 `%q` 转义。
- [x] `go test ./...`、`go test -race ./...`、`go vet ./...`、`gofmt -l .` 全部通过。
- [x] fuzz 30 秒无 panic、无不变量违例。
- [x] 测试不依赖真实 LLM、网络或外部文件。

## 11. 实现顺序

1. **类型与错误**：`Step`、`ErrInvalidOutput`、`ParseErrorKind`、`ParseError`、常量。
2. **输入预处理**：大小、UTF-8、BOM、trim、空输入。
3. **fence 选择**：行扫描、info string、正文提取、优先级。
4. **对象扫描**：括号/字符串/转义状态机，返回候选 span。
5. **对象解码**：token 流、重复顶层键、尾随 token 检查。
6. **字段校验**：未知字段、互斥、类型、action_input 压缩、返回 `Step`。
7. **测试**：先写成功/包装用例，再写协议错误、根类型、边界和错误契约；最后补 fuzz。
8. **Agent 集成**：在 `Run` 中接入，映射为 `StopInvalidOutput`，确认未知工具和参数错误仍走 observation。

每一步以 `go test ./...` 通过为完成标准；Parser 不等待 Agent Loop 就可以独立完成和验收。

截至 v0.1，步骤 1–7（Parser 本身）已实现并通过测试；步骤 8（Agent Loop 集成）和 CLI 集成都已完成。

## 12. 已确认决策与开放问题

### 12.1 已确认决策

1. Parser 放在 `internal/agent`，通过 `ParseStep(raw string)` 暴露，不依赖其他内部包。
2. 只接受顶层 JSON object；数组、标量、null 一律 `not_object`。
3. 采用“第一个语法合法对象”规则；语法错误候选跳过，顶层重复键或字段错误立即失败，不继续搜索后续对象。
4. 有 `json` fence 时优先使用第一个 `json` fence；没有时使用第一个无标签且以 `{`/`[` 开头的 fence；都没有才看全文。
5. 选中的 fence 失败后不回退，避免掩盖协议错误。
6. `action` 与 `final_answer` 以“字段是否存在”判断互斥，不以值是否为空判断。
7. `action_input` 在 action 步骤中必填，且必须是对象；`{}` 合法。
8. `final_answer` 步骤不允许出现 `action_input`。
9. `thought` 可选，缺失为空串，空串合法。
10. 顶层未知字段严格拒绝；顶层重复键拒绝。
11. `action` 只校验非空，不校验工具名格式或 Registry 存在性。
12. 输入上限 1 MiB，错误片段上限 512 rune，均使用常量。
13. 所有错误包装 `ErrInvalidOutput`，不实现自动修复/重试。
14. `ActionInput` 压缩空白后返回；`final_answer` 和 `action` 保留原值。
15. 错误 snippet 在 `Error()` 中用 `%q` 转义。

### 12.2 开放问题

1. **未知字段是否长期严格拒绝？** 当前建议是拒绝，便于发现拼写错误；如果未来模型经常输出 `"reasoning"` 等附加字段，可改为白名单忽略，但需要显式设计。
2. **零参数工具是否允许省略 `action_input`？** 当前要求显式 `{}`。如果希望更宽容，可在 Invoker 也接受 nil 时调整，但这会改变“action_input 必须是对象”的协议。
3. **是否在 Parser 校验 action 命名格式？** 当前不校验，交给 Registry。若未来需要更早失败，可复用工具的命名规则，但要避免两处规则漂移。
4. **1 MiB 是否足够？** 当前按正常 ReAct 输出估算足够；如果未来支持长文最终答案，应改成配置并同步上下文预算。
5. **是否递归拒绝 action_input 内的重复键？** 当前只拒绝顶层重复键，嵌套重复键交给标准库和 Invoker；如果工具参数对重复键敏感，应增加递归检测。
6. **是否支持更多 fence 形式（`~~~`、`jsonc`）？** 当前不支持，保持解析器小而可预测。
7. **是否保留“最后一个对象”策略？** 当前不保留。若模型经常先给示例再给答案，应通过 prompt 约束，而不是让 Parser 猜测。
