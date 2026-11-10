# 工具定义设计

| 项目 | 内容 |
|---|---|
| 文档版本 | v0.1 |
| 状态 | 核心已实现（internal/tool）；认证、审批、沙箱执行器待接入 |
| 日期 | 2026-11-10 |
| 上位设计 | [DESIGN.md](../DESIGN.md) [§5.2](../DESIGN.md#L235) |
| 关联设计 | [read_file 详细设计](read_file.md) |
| 实现位置 | `internal/tool/tool.go`、`internal/tool/registry.go`、`internal/tool/invoker.go` |
| 范围 | 工具元数据、参数定义、执行结果、校验与 Schema 生成 |

> 本文档是工具层的规范性设计。它替换 DESIGN.md v0.3 及更早版本中的最小 `ToolSpec`，并作为 `read_file` 等具体工具的共同契约。策略字段如何被强制执行、以及当前 CLI 的能力边界，见第 8 节。

## 1. 目标与范围

### 1.1 目标

1. 用一套结构化定义同时描述：工具身份、执行策略、参数 Schema 和执行结果。
2. 参数定义是唯一事实来源，Prompt 中的 JSON Schema 由参数自动生成，避免两处漂移。
3. 默认值安全：沙箱默认开启，超时和内存有明确默认值。
4. 金额使用整数分（CNY），避免浮点误差。
5. 工具实现只关心业务逻辑；参数校验、默认值、超时、策略和结果归一化由 Registry / Invoker 统一负责。
6. 为未来接入认证、审批、限流、预算和沙箱留出契约，同时不要求在最小 CLI 中实现全部能力。

### 1.2 非目标

- 本文档不实现 Authenticator、Approver、RateLimiter、BudgetLedger 或 SandboxRunner。
- 不支持多币种；当前统一为 CNY。
- 不支持任意复杂 JSON Schema（`oneOf`、`anyOf`、条件 Schema 等）。
- 不支持 ARRAY 元素和 OBJECT 属性的嵌套定义（见 §12.1）。
- 不把策略字段下发给模型。

## 2. 总体结构与调用链

```text
ToolDefinition
├── Metadata: ToolMetadata      # 身份 + 执行策略
└── Parameters: []ToolParameter # 参数定义 → 生成 JSON Schema

Tool
├── Definition() ToolDefinition
└── Execute(ctx, input) (ToolResult, error)

Invoker
  → 查找 Definition
  → 校验参数、填充默认值
  → 执行策略检查（认证/审批/限流/预算）
  → 加超时，选择进程内或沙箱执行
  → 归一化 ToolResult，填充执行时间
  → 交给 Agent 渲染 observation
```

## 3. 工具元数据

```go
package tool

const (
    CurrencyCNY = "CNY"
    FenPerYuan  = int64(100)
)

type ToolCategory string

const (
    CategorySearch      ToolCategory = "search"
    CategoryCalculation ToolCategory = "calculation"
    CategoryFile        ToolCategory = "file"
    CategoryNetwork     ToolCategory = "network"
    CategorySystem      ToolCategory = "system"
)

type ToolMetadata struct {
    Name        string       `json:"name"`
    Version     string       `json:"version"`
    Description string       `json:"description"`
    Category    ToolCategory `json:"category"`

    RequiresAuth   bool    `json:"requires_auth"`
    RateLimit      *int    `json:"rate_limit,omitempty"` // 每分钟上限；nil = 不限
    TimeoutSeconds int     `json:"timeout_seconds"`
    MemoryLimitMB  int     `json:"memory_limit_mb"`
    Sandboxed      bool    `json:"sandboxed"`
    Dangerous      bool    `json:"dangerous"`

    // CostPerUse 单次调用成本，单位：分（CNY）。
    // 250 = ¥2.50；0 = 免费。
    CostPerUse int64 `json:"cost_per_use"`
}
```

### 3.1 字段语义

| 字段 | 类型 | 语义 | 约束 |
|---|---|---|---|
| `name` | string | Registry 唯一键，模型 `action` 的精确值 | 非空、唯一、`^[a-z][a-z0-9_]*$` |
| `version` | string | 工具实现版本，用于审计与兼容性判断 | 非空，约定 SemVer，如 `1.0.0` |
| `description` | string | 给模型看的能力与限制说明 | 非空 |
| `category` | enum | 策略分类与审计聚合 | 非空；使用已定义分类 |
| `requires_auth` | bool | 是否需要已认证主体 | true 且无认证器时注册失败 |
| `rate_limit` | *int | 每分钟最多调用次数 | nil 或 > 0 |
| `timeout_seconds` | int | 单次执行超时 | > 0，默认 30 |
| `memory_limit_mb` | int | 单次调用内存预算 | > 0，默认 512 |
| `sandboxed` | bool | 是否运行在真实隔离环境 | 默认 true；无沙箱执行器时注册失败 |
| `dangerous` | bool | 是否需要人工审批 | true 且无审批器时注册失败 |
| `cost_per_use` | int64 | 单次调用成本，单位分（CNY） | >= 0，默认 0 |

### 3.2 默认值与构造函数

Go 结构体没有字段默认值。元数据必须通过构造函数创建，或由 JSON 解码器在解码前预填默认值：

```go
func DefaultToolMetadata() ToolMetadata {
    return ToolMetadata{
        TimeoutSeconds: 30,
        MemoryLimitMB:  512,
        Sandboxed:      true,
        CostPerUse:     0,
    }
}

func NewToolMetadata(name, version, description string, category ToolCategory) ToolMetadata {
    m := DefaultToolMetadata()
    m.Name = name
    m.Version = version
    m.Description = description
    m.Category = category
    return m
}
```

如果从 JSON manifest 加载，`UnmarshalJSON` 应先构造 `DefaultToolMetadata()` 再解码，确保缺失字段使用默认值；尤其是 `sandboxed` 必须默认 true，不能因为字段缺失退化为 false。

### 3.3 版本与 Registry 键

- Registry 以 `name` 为唯一键；模型只输出 `name`，不输出版本。
- `version` 用于审计和兼容性判断，不参与 action 匹配。
- 默认一个 `name` 只能有一个激活版本。
- 若未来需要多版本并存，内部键改为 `name@version`，Prompt 只暴露唯一的默认别名。

### 3.4 人民币与整数分

- 币种在系统级固定为 `CNY`，不放入每个工具，避免同一预算内混币种。
- `CostPerUse` 单位为分：`1 CNY = 100 分`。
- 示例：`0` = 免费；`250` = ¥2.50；`1` = ¥0.01。
- 审计日志和预算账本记录 `currency=CNY`。
- 预算累计必须使用整数分并防溢出：

```go
func addCost(total, cost int64) (int64, error) {
    if cost < 0 {
        return 0, errors.New("cost must be >= 0")
    }
    if total > math.MaxInt64-cost {
        return 0, errors.New("cost overflow")
    }
    return total + cost, nil
}
```

- 如果未来需要子分精度，统一升级为 `CostPerUseMicroCNY int64`（1 CNY = 1,000,000 微元），不要混用两种单位。
- 若需要多币种，升级为 `Money{Amount int64, Currency string}`，而不是只加一个孤立字段。

## 4. 参数定义

```go
type ToolParameterType string

const (
    ParameterString  ToolParameterType = "string"
    ParameterInteger ToolParameterType = "integer"
    ParameterFloat   ToolParameterType = "number" // FLOAT 对应 JSON Schema 的 number
    ParameterBoolean ToolParameterType = "boolean"
    ParameterArray   ToolParameterType = "array"
    ParameterObject  ToolParameterType = "object"
)

type ToolParameter struct {
    Name        string            `json:"name"`
    Type        ToolParameterType `json:"type"`
    Description string            `json:"description"`
    Required    bool              `json:"required"`
    Default     any               `json:"default,omitempty"`
    Enum        []any             `json:"enum,omitempty"`
    MinValue    *float64          `json:"min_value,omitempty"`
    MaxValue    *float64          `json:"max_value,omitempty"`
    Pattern     *string           `json:"pattern,omitempty"`
}
```

### 4.1 类型映射

| 设计类型 | Go 常量 | JSON 值 | JSON Schema |
|---|---|---|---|
| STRING | `ParameterString` | `string` | `string` |
| INTEGER | `ParameterInteger` | `integer` | `integer` |
| FLOAT | `ParameterFloat` | `number` | `number` |
| BOOLEAN | `ParameterBoolean` | `boolean` | `boolean` |
| ARRAY | `ParameterArray` | `array` | `array` |
| OBJECT | `ParameterObject` | `object` | `object` |

### 4.2 Required 默认 true

`required` 默认 true，但 Go 零值是 false。所有参数必须通过构造函数创建：

```go
func NewToolParameter(name string, typ ToolParameterType, description string) ToolParameter {
    return ToolParameter{
        Name:        name,
        Type:        typ,
        Description: description,
        Required:    true,
    }
}
```

如果从 JSON 加载，在 `UnmarshalJSON` 中预设 `Required = true` 再解码。

### 4.3 字段约束

| 字段 | 约束 |
|---|---|
| `name` | 非空；同一工具内唯一；建议 `^[A-Za-z_][A-Za-z0-9_]*$` |
| `type` | 必须是六种类型之一 |
| `description` | 非空，给模型看 |
| `required` | true 时调用方必须提供，且不允许同时设置 `default` |
| `default` | nil 表示无默认值；非 nil 时必须通过类型/枚举/范围/正则校验 |
| `enum` | 非空时每个值必须与类型匹配；建议只用于标量类型 |
| `min_value` / `max_value` | 仅用于 INTEGER / FLOAT；`min <= max`；整数参数要求边界为整数 |
| `pattern` | 仅用于 STRING；注册时编译；采用 JSON Schema 的包含匹配语义 |

### 4.4 ARRAY / OBJECT 的已知缺口

当前字段只能声明「这是一个数组/对象」，不能约束元素类型或对象字段。生产级扩展建议：

```go
type ToolParameter struct {
    // ...
    Items      *ToolParameter   `json:"items,omitempty"`      // ARRAY 元素
    Properties []ToolParameter  `json:"properties,omitempty"` // OBJECT 字段
}
```

在补充之前：

- 生成的 Schema 只有 `{"type":"array"}` / `{"type":"object"}`；
- 元素和字段校验由工具实现自行负责；
- 不应在这一层声称已经完成深层参数校验。

## 5. 工具定义与 Schema 生成

```go
type ToolDefinition struct {
    Metadata   ToolMetadata    `json:"metadata"`
    Parameters []ToolParameter `json:"parameters"`
}
```

参数定义是唯一事实来源。Prompt 中的 Input Schema 由 `Parameters` 生成，不再手写第二份 JSON：

```go
func (d ToolDefinition) InputSchema() (json.RawMessage, error) {
    props := make(map[string]any, len(d.Parameters))
    var required []string

    for _, p := range d.Parameters {
        prop := map[string]any{
            "type":        string(p.Type),
            "description": p.Description,
        }
        if len(p.Enum) > 0 {
            prop["enum"] = p.Enum
        }
        if p.MinValue != nil {
            prop["minimum"] = *p.MinValue
        }
        if p.MaxValue != nil {
            prop["maximum"] = *p.MaxValue
        }
        if p.Pattern != nil {
            prop["pattern"] = *p.Pattern
        }
        if p.Default != nil {
            prop["default"] = p.Default
        }
        props[p.Name] = prop
        if p.Required {
            required = append(required, p.Name)
        }
    }

    schema := map[string]any{
        "type":                 "object",
        "properties":           props,
        "additionalProperties": false,
    }
    if len(required) > 0 {
        schema["required"] = required
    }
    return json.Marshal(schema)
}
```

约定：

- `properties` 只包含 `Parameters` 中声明的字段；
- `required` 数组由 `Required=true` 的参数组成；
- `additionalProperties` 固定为 false；
- `default` 同时出现在 Schema 和运行期默认值逻辑中，但**运行期是权威**；
- `encoding/json` 对 map 键排序，Prompt 输出稳定；
- Schema 在注册时生成并缓存，工具运行期不需要重复生成。

## 6. 执行结果

```go
type ToolResult struct {
    Success         bool           `json:"success"`
    Output          any            `json:"output,omitempty"`
    Error           string         `json:"error,omitempty"`
    Metadata        map[string]any `json:"metadata,omitempty"`
    ExecutionTimeMS *int64         `json:"execution_time_ms,omitempty"`
    TokensUsed      *int           `json:"tokens_used,omitempty"`
}
```

### 6.1 字段约定

| 字段 | 约定 |
|---|---|
| `success` | 必填；false 时 `error` 必须非空 |
| `output` | 成功结果；必须可 JSON 序列化。字符串由 Agent 直接展示，其他类型 JSON 序列化 |
| `error` | 模型可见的失败原因，例如 `open file: no such file or directory` |
| `metadata` | 工具附加信息，例如 `bytes_read`；不进入 Prompt，只用于追踪和审计 |
| `execution_time_ms` | 由 Invoker 统一测量并填充；工具不要自行填写 |
| `tokens_used` | 工具内部调用 LLM 时填写；nil 表示不适用或未统计 |

`ExecutionTimeMS` 和 `TokensUsed` 使用指针，以区分「没有值」和「值为 0」。

### 6.2 Go error 与 ToolResult.Error 的边界

| 场景 | 返回方式 | Agent 行为 |
|---|---|---|
| 预期业务失败（文件不存在、路径越界等） | `ToolResult{Success:false, Error:"..."}, nil` | 渲染为 `status="error"` 的 observation，循环继续 |
| context 取消/超时 | `ctx.Err()` 作为 Go error | 映射为 `canceled` / `timeout` 终止 |
| 内部编程错误、序列化失败 | Go error | 记录详细日志，对模型返回通用错误 |

### 6.3 结果渲染

- `success=true`：`output` 为 string 时直接作为 observation 正文；其他类型 JSON 序列化。
- `success=false`：使用 `error` 作为 observation 正文。
- `metadata`、`execution_time_ms`、`tokens_used` 默认不进入 Prompt；只在 verbose 追踪、审计或预算中使用。

## 7. 工具接口与调用流水线

```go
type Tool interface {
    Definition() ToolDefinition
    Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
}
```

`input` 不是模型原始参数，而是 Invoker 处理后的规范化 JSON：

```text
模型 action_input
  → 解析为 JSON object
  → 按 ToolParameter 校验类型/必填/枚举/范围/正则
  → 填充 default
  → 拒绝未知字段
  → 重新序列化为规范 JSON
  → 传入 Execute
```

这样每个工具可以安全地解码到自己的 typed struct，不需要重复实现参数校验和默认值逻辑。

Invoker 的完整流程：

1. 通过 `name` 查找 `ToolDefinition`。
2. 校验 `action_input` 是 JSON object。
3. 逐参数校验类型、必填、枚举、范围、正则；拒绝未声明字段。
4. 填充默认值。
5. 执行策略检查：认证、审批、限流、预算。
6. 用 `context.WithTimeout` 包装调用。
7. 根据 `sandboxed` 选择进程内执行或沙箱执行。
8. 调用 `Tool.Execute`。
9. 归一化 `ToolResult`：填充执行时间、校验成功/错误一致性。
10. 返回给 Agent 渲染 observation。

## 8. 策略字段的执行责任

元数据本身不是安全控制。字段必须有消费者：

| 字段 | 执行者 | 行为 |
|---|---|---|
| `requires_auth` | Invoker + Authenticator | 无认证上下文时拒绝调用 |
| `rate_limit` | Invoker + RateLimiter | 超限返回 `rate limit exceeded`，不进入工具 |
| `timeout_seconds` | Invoker | `context.WithTimeout` 包裹 `Execute` |
| `memory_limit_mb` | Sandbox / 调度器 | 沙箱用 cgroup/rlimit 硬限制；进程内只能做准入和工具自限 |
| `sandboxed` | Invoker + SandboxRunner | 选择隔离执行器；不可用时注册失败 |
| `dangerous` | Invoker + Approver | 执行前人工确认；无审批器时 fail closed |
| `cost_per_use` | BudgetLedger | 调用前检查/预扣预算，调用后记账 |
| `category` | 策略/审计 | 分组、审计、按类别限流 |
| `version` | Registry/审计 | 记录实际执行版本；不参与 action 匹配 |

注册时的 fail-closed 检查：

- `sandboxed=true` 但无 SandboxRunner → 注册失败；
- `dangerous=true` 但无 Approver → 注册失败；
- `requires_auth=true` 但无 Authenticator → 注册失败；
- 不允许「声明了策略但没有执行能力」的工具静默降级执行。

已知边界：

- `memory_limit_mb` 在普通 Go 进程内不是硬限制。`sandboxed=false` 时它只是调度预算；硬限制需要独立进程或沙箱。
- `timeout_seconds` 依赖工具检查 context；不可中断的阻塞 I/O 无法被纯 Go context 强制终止，硬超时需要沙箱/工作进程。
- `rate_limit` 在单进程中用本地限流器即可；多实例部署需要共享存储（当前不在范围）。
- `requires_auth`、`dangerous` 的 bool 零值是 false，存在 fail-open 风险。若要求严格 fail-closed，应改为 `*bool` 或三态类型，并在校验中拒绝未设置。

## 9. 校验与默认值

### 9.1 ToolMetadata

- `name` 非空且符合命名规则，注册时全局唯一；
- `version`、`description`、`category` 非空；
- `rate_limit` 为 nil 或 > 0；
- `timeout_seconds > 0`；
- `memory_limit_mb > 0`；
- `cost_per_use >= 0`；
- 策略能力检查见 §8。

### 9.2 ToolParameter

- `name` 非空，同一工具内唯一；
- `type` 必须是六种类型之一；
- `description` 非空；
- `required=false` 才允许设置 `default`；
- `default` 必须匹配类型、枚举、范围和正则；
- `enum` 每个值与类型一致；
- `min_value <= max_value`，且只用于数值类型；
- `pattern` 能编译，且只用于 STRING；
- ARRAY / OBJECT 的深层校验暂由工具负责（见 §4.4）。

### 9.3 ToolDefinition

- `metadata` 通过校验；
- 参数名唯一；
- 生成的 Input Schema 是合法 JSON。

### 9.4 ToolResult

- `success=true` 时 `error` 必须为空；
- `success=false` 时 `error` 必须非空；
- `execution_time_ms >= 0`；
- `tokens_used >= 0`；
- `output` 和 `metadata` 必须可 JSON 序列化。

## 10. read_file 示例

### 10.1 定义

```go
func readFileDefinition() tool.ToolDefinition {
    meta := tool.NewToolMetadata(
        "read_file",
        "1.0.0",
        "Read a UTF-8 text file and return its complete contents. The path must be relative to the allowed root or an absolute path inside it. The tool is read-only and rejects binary, oversized, or out-of-root files.",
        tool.CategoryFile,
    )
    rateLimit := 60
    meta.RateLimit = &rateLimit
    meta.TimeoutSeconds = 10
    meta.MemoryLimitMB = 16
    meta.Sandboxed = false // 只有应用层路径约束，不是 OS 沙箱
    meta.CostPerUse = 0    // 本地只读操作，免费

    return tool.ToolDefinition{
        Metadata: meta,
        Parameters: []tool.ToolParameter{
            tool.NewToolParameter(
                "path",
                tool.ParameterString,
                "相对于 allowed_root 的路径，或 allowed_root 内的绝对路径",
            ),
        },
    }
}
```

### 10.2 JSON 形态

```json
{
  "metadata": {
    "name": "read_file",
    "version": "1.0.0",
    "description": "Read a UTF-8 text file and return its complete contents. ...",
    "category": "file",
    "requires_auth": false,
    "rate_limit": 60,
    "timeout_seconds": 10,
    "memory_limit_mb": 16,
    "sandboxed": false,
    "dangerous": false,
    "cost_per_use": 0
  },
  "parameters": [
    {
      "name": "path",
      "type": "string",
      "description": "相对于 allowed_root 的路径，或 allowed_root 内的绝对路径",
      "required": true
    }
  ]
}
```

### 10.3 生成的 Schema

```json
{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "相对于 allowed_root 的路径，或 allowed_root 内的绝对路径"
    }
  },
  "required": ["path"],
  "additionalProperties": false
}
```

### 10.4 结果示例

```go
// 成功
return tool.ToolResult{
    Success: true,
    Output:  string(data),
    Metadata: map[string]any{
        "bytes_read": len(data),
    },
}, nil

// 预期失败
return tool.ToolResult{
    Success: false,
    Error:   "open file: no such file or directory",
}, nil
```

`ExecutionTimeMS` 由 Invoker 填充，`CostPerUse=0` 不进入预算扣减。

## 11. 旧结构迁移

| 旧设计（DESIGN.md v0.3 及更早） | 新设计 |
|---|---|
| `ToolSpec` | `ToolDefinition`（`ToolMetadata` + `[]ToolParameter`） |
| `Spec()` | `Definition()` |
| 手写 `InputSchema` | 由 `Parameters` 生成 |
| `Execute(...) (string, error)` | `Execute(...) (ToolResult, error)` |
| 工具错误统一返回 `error` | 预期失败放 `ToolResult.Error`；Go error 用于取消/内部错误 |
| 无策略元数据 | 新增认证、限流、超时、内存、沙箱、危险、成本字段 |
| 无成本 | `CostPerUse int64`，单位分（CNY） |

受影响文档：

- [DESIGN.md](../DESIGN.md) §5.2、§6.1、§8.1、§9.2、§11.1、§14；
- [read_file 详细设计](read_file.md) 的接口、输入、结果和测试章节。

## 12. 开放问题

1. **ARRAY / OBJECT 深层定义**：是否补充 `items` 与 `properties`，以及是否支持递归 Schema。
2. **bool 的 fail-open**：`requires_auth`、`dangerous` 是否改为 `*bool` 或三态类型，拒绝未设置。
3. **内存硬限制**：当前 `memory_limit_mb` 在进程内只是预算；是否需要独立进程/cgroup 执行器。
4. **超时硬限制**：阻塞 I/O 需要沙箱/工作进程才能真正中断。
5. **子分成本**：若单次成本低于 1 分，统一升级为微元并迁移预算账本。
6. **多币种**：当前固定 CNY；未来是否需要 `Money{Amount, Currency}`。
7. **多版本并存**：Registry 是否需要 `name@version` 键与默认版本别名。
8. **复杂 Schema**：`oneOf`、`anyOf`、条件校验是否需要在参数模型中表达。
9. **结果渲染**：observation 中的正文边界转义策略（属于 Agent 层）。

## 13. 验收标准

- [x] `ToolMetadata` 默认值为 timeout=30、memory=512、sandboxed=true、cost=0。
- [x] 从 JSON 加载时缺失字段仍应用默认值。
- [x] `CostPerUse` 为 int64，单位分（CNY），JSON 示例为整数。
- [x] 参数 `required` 默认 true，`required=false` 才允许 `default`。
- [x] Input Schema 由参数生成，`required`、`enum`、范围、正则正确映射。
- [x] Invoker 能拒绝缺失/类型错误/未知字段，并填充默认值。
- [x] `ToolResult` 成功/失败约束可校验，执行时间由 Invoker 填充。
- [x] `sandboxed`、`dangerous`、`requires_auth` 在能力缺失时注册失败。
- [ ] Prompt 只包含 name、description 和生成的 Input Schema，不包含策略字段（Agent 层待集成）。
- [x] `read_file` 定义使用 `category=file`、`sandboxed=false`、`cost_per_use=0`。
- [x] `go test ./...`、`go vet ./...` 通过。

## 14. 实现状态（v0.1）

已实现：

- `ToolMetadata`、`ToolParameter`、`ToolDefinition`、`ToolResult` 及默认值、校验、Schema 生成；
- `MapRegistry`：注册、重名检查、能力 fail-closed、Schema 缓存；
- `Invoker`：参数校验、默认值、超时、每分钟限流、CNY 分预算、结果归一化和 `ExecutionTimeMS`；
- `read_file` 工具与完整安全测试。

尚未实现：

- Prompt 投影（Agent 层）；
- Authenticator、Approver、SandboxRunner 的实际服务；当前只有注册期能力门控；
- ARRAY / OBJECT 的嵌套 Schema。

