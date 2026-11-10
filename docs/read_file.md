# read_file 详细设计

| 项目 | 内容 |
|---|---|
| 文档版本 | v0.2 |
| 状态 | 已实现，测试通过 |
| 日期 | 2026-11-10 |
| 上位设计 | [DESIGN.md](../DESIGN.md) §8 |
| 工具定义 | [工具定义设计](tool_definition.md) |
| 关联配置 | [internal/config/config.go](../internal/config/config.go#L60) |
| 实现位置 | `internal/tool/readfile/readfile.go` |
| 协议 | 只读文本文件读取工具 |

> v0.2 按生产级工具定义同步：元数据、参数定义和执行结果改为 `ToolMetadata` / `ToolParameter` / `ToolResult`；成本使用 int64 分（CNY）。路径安全、读取限流和错误脱敏设计保持不变。

## 1. 背景与定位

`read_file` 是 SimpleAgent 内置的只读文件工具，也是 Agent 基于文件内容回答问题的 grounding 手段：

```text
模型输出 action=read_file
        ↓
Invoker 校验参数 → 策略检查 → 超时控制
        ↓
read_file 路径校验 → 大小/类型/编码校验 → 读取
        ↓
返回 ToolResult
        ↓
Agent 渲染 observation，模型基于内容输出 final_answer
```

它的存在是为了满足三条约束：

1. 问题涉及文件内容时，模型必须先读取，不能编造。
2. 文件读取必须限制在 `allowed_root` 内，不能变成任意文件读取接口。
3. 任何读取失败都应作为可观察结果交还给模型，而不是让进程崩溃。

`read_file` 只负责“安全地读出文本”。参数校验、策略、observation 包装和终止条件分别属于 Invoker 与 Agent 层。

## 2. 目标与非目标

### 2.1 目标

1. 读取 `allowed_root` 内的 UTF-8 文本文件，通过 `ToolResult.Output` 返回完整原文。
2. 阻止路径穿越、符号链接逃逸、目录/设备/管道读取、超大文件、二进制内容。
3. 预期失败以 `ToolResult{Success:false, Error:"..."}` 返回，由 Agent 包装成 observation，不终止循环。
4. 不依赖真实 LLM 或外网，可完全用临时目录做单元测试。
5. 单次读取的内存占用受 `tools.max_file_bytes` 约束。

### 2.2 非目标

- 不写入、不删除、不重命名、不列目录、不做 glob。
- 不支持行号范围、分页读取、编码转换或 base64。
- 不静默截断、不缓存、不重试。
- 不提供 OS 级沙箱；`allowed_root` 是应用层访问控制，元数据中 `sandboxed=false`。
- 不负责 prompt 注入防护，只保证文件内容原样返回。
- 不实现参数 Schema 的通用生成与校验；这部分由 [工具定义设计](tool_definition.md) 和 Invoker 负责。

## 3. 包结构与依赖

```text
internal/
├── tool/
│   ├── tool.go                 # Tool 接口、ToolMetadata、ToolParameter、ToolResult
│   ├── registry.go             # 定义注册、Schema 缓存、重名检查
│   ├── invoker.go              # 参数校验、默认值、策略、超时、结果归一化
│   └── readfile/
│       ├── readfile.go         # read_file 实现
│       └── readfile_test.go
```

依赖方向：

```text
cmd → tool/readfile → tool（接口与数据类型）
cmd → tool/invoker  → tool
tool/readfile → 标准库
tool/readfile ✗→ agent
```

`readfile` 只依赖 `tool` 包暴露的接口和数据类型，不反向依赖 Agent 或 Loop。构造参数使用基础类型，不直接依赖 `config` 包，便于单测和复用。

## 4. 工具定义与接口

### 4.1 工具接口

```go
// internal/tool/tool.go
type Tool interface {
    Definition() ToolDefinition
    Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
}
```

完整的元数据、参数、Schema 生成和结果语义见 [工具定义设计](tool_definition.md)。本节只列 `read_file` 的取值。

### 4.2 read_file 实现形态

```go
// internal/tool/readfile/readfile.go
type Tool struct {
    root     string // 绝对路径，已 EvalSymlinks 规范化
    maxBytes int64
}

// New 在启动期完成根目录规范化与校验，失败即启动失败。
func New(allowedRoot string, maxFileBytes int64) (*Tool, error)

func (t *Tool) Definition() tool.ToolDefinition
func (t *Tool) Execute(ctx context.Context, input json.RawMessage) (tool.ToolResult, error)
```

设计决策：

- **构造期解析根目录**：相对 `allowed_root` 基于进程启动时的工作目录解析一次，之后 `chdir` 不影响已创建的 Tool。
- **不可变**：Tool 创建后只读，天然并发安全；每次调用独立 `os.File`。
- **零值不可用**：`Execute` 检查 `root == "" || maxBytes <= 0`，返回 Go error `read_file tool is not initialized`。这是内部编程错误，不是模型可修正的业务失败。
- **构造失败即启动失败**：根目录不存在、不是目录、大小配置非法都属于配置/用法错误，CLI 映射为退出码 2。

### 4.3 Definition

元数据：

| 字段 | 值 |
|---|---|
| `name` | `read_file` |
| `version` | `1.0.0` |
| `description` | Read a UTF-8 text file and return its complete contents. The path must be relative to the allowed root or an absolute path inside it. The tool is read-only and rejects binary, oversized, or out-of-root files. |
| `category` | `file` |
| `requires_auth` | `false` |
| `rate_limit` | `60`（每分钟） |
| `timeout_seconds` | `10` |
| `memory_limit_mb` | `16` |
| `sandboxed` | `false` |
| `dangerous` | `false` |
| `cost_per_use` | `0`（分，CNY） |

参数：

| 名称 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `path` | `string` | 是 | 相对于 `allowed_root` 的路径，或 `allowed_root` 内的绝对路径 |

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

## 5. 输入协议

参数定义（不是手写 Schema）：

```json
{
  "name": "path",
  "type": "string",
  "description": "相对于 allowed_root 的路径，或 allowed_root 内的绝对路径",
  "required": true
}
```

由参数生成的 Input Schema：

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

调用示例：

```json
{"path": "examples/notes.txt"}
```

### 5.1 Invoker 负责的校验

以下检查发生在 `Execute` 之前，由 Invoker 统一完成：

1. `action_input` 必须是 JSON object。
2. 拒绝未声明的参数（`additionalProperties: false`）。
3. `path` 必填；缺失时报 `invalid arguments: missing required parameter "path"`。
4. `path` 必须是字符串；类型错误时报 `invalid arguments: path must be a string`。
5. 填充默认值；`read_file` 没有默认值。
6. 重新序列化为规范 JSON，再传给 `Execute`。

### 5.2 工具内的防御性解码

`Execute` 收到的是已校验的 JSON，但仍做防御性解码：

```go
type input struct {
    Path string `json:"path"`
}

var errPathRequired = errors.New("path is required")

func decodeInput(raw json.RawMessage) (input, error) {
    dec := json.NewDecoder(bytes.NewReader(raw))
    dec.DisallowUnknownFields()

    var in input
    if err := dec.Decode(&in); err != nil {
        return input{}, fmt.Errorf("decode input: %w", err) // 内部错误
    }
    if strings.TrimSpace(in.Path) == "" {
        return input{}, errPathRequired // 模型可修正的业务失败
    }
    return in, nil
}
```

只对“是否空白”做判断，**不使用 trim 后的值去打开文件**，避免改变合法文件名。`decode input: ...` 说明 Invoker 与工具之间的契约被破坏，应按 Go error 处理并记录内部日志，而不是把内部细节暴露给模型。

## 6. 执行流程

```text
Invoker
  1. 查找 ToolDefinition
  2. 校验 action_input 并填充默认值
  3. 策略检查：认证/审批/限流/预算
  4. context.WithTimeout(timeout_seconds)
  5. 选择进程内或沙箱执行

read_file.Execute
  6. 检查 ctx.Err() 和 Tool 是否初始化
  7. decodeInput：解析规范化的 {"path": "..."}
  8. 构造候选绝对路径并 Clean
  9. 第一层：词法包含性检查（不访问文件系统）
 10. EvalSymlinks 解析全部符号链接
 11. 第二层：真实路径包含性检查
 12. os.Lstat 预检查：普通文件、大小上限
 13. os.Open + fstat 复核
 14. LimitReader 读取 maxBytes+1，超限报错
 15. NUL 检查 → UTF-8 校验
 16. 返回 ToolResult{Success:true, Output:string(data)}

Invoker
 17. 填充 ExecutionTimeMS，校验 Success/Error 一致性
```

流程中的两个关键补充：

- **两阶段包含性检查**：第 9 步在 `EvalSymlinks` 之前拦截绝对路径越界和 `../`，避免对 `allowed_root` 外部路径做存在性探测；第 11 步对解析后的真实路径再判一次，防止软链逃逸。
- **预检查**：第 12 步在 `os.Open` 前拒绝 FIFO 和设备文件，避免打开管道时阻塞或触发设备副作用；第 13 步的 `fstat` 仍不可省略，用于复核打开瞬间的文件类型和大小。

## 7. 路径安全设计

### 7.1 构造期规范化根目录

```go
func New(allowedRoot string, maxFileBytes int64) (*Tool, error) {
    if strings.TrimSpace(allowedRoot) == "" {
        return nil, errors.New("allowed root is required")
    }
    if maxFileBytes <= 0 {
        return nil, errors.New("max file bytes must be > 0")
    }

    abs, err := filepath.Abs(allowedRoot)
    if err != nil {
        return nil, fmt.Errorf("resolve allowed root: %w", err)
    }
    root, err := filepath.EvalSymlinks(abs)
    if err != nil {
        return nil, fmt.Errorf("resolve allowed root: %w", err)
    }
    info, err := os.Stat(root)
    if err != nil {
        return nil, fmt.Errorf("stat allowed root: %w", err)
    }
    if !info.IsDir() {
        return nil, errors.New("allowed root is not a directory")
    }
    return &Tool{root: root, maxBytes: maxFileBytes}, nil
}
```

根目录本身允许是符号链接，但存储的是解析后的真实目录。

### 7.2 目标路径解析

```go
func (t *Tool) resolve(raw string) (string, error) {
    if strings.TrimSpace(raw) == "" {
        return "", errors.New("path is required")
    }

    candidate := raw
    if !filepath.IsAbs(candidate) {
        candidate = filepath.Join(t.root, candidate)
    }
    candidate = filepath.Clean(candidate)

    // 第一层：纯词法检查，阻止明显越界，且不触碰外部路径。
    if !withinRoot(t.root, candidate) {
        return "", errors.New("path escapes allowed_root")
    }

    // 第二层：解析符号链接后再次检查。
    real, err := filepath.EvalSymlinks(candidate)
    if err != nil {
        return "", openErr(err)
    }
    if !withinRoot(t.root, real) {
        return "", errors.New("path escapes allowed_root")
    }
    return real, nil
}

func withinRoot(root, path string) bool {
    rel, err := filepath.Rel(root, path)
    if err != nil || filepath.IsAbs(rel) {
        return false
    }
    return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
```

使用 `filepath.Rel` 而不是字符串前缀判断，避免 `/data/root-evil` 被误判为 `/data/root` 的子路径。`Rel` 返回 `.` 时允许通过，后续由普通文件检查拒绝目录。

### 7.3 路径行为矩阵

| 输入 | 结果 |
|---|---|
| `notes.txt` | 允许，相对 `allowed_root` |
| `sub/a.txt` | 允许 |
| `allowed_root` 内绝对路径 | 允许 |
| `../secret.txt` | `path escapes allowed_root` |
| `sub/../../secret.txt` | `path escapes allowed_root` |
| 根目录外的绝对路径 | `path escapes allowed_root` |
| 指向根目录内的符号链接 | 允许，读取目标 |
| 指向根目录外的符号链接 | `path escapes allowed_root` |
| 悬空符号链接 | `open file: no such file or directory` |
| `.` / 目录 | `not a regular file` |
| `/data/root-evil/x`（根为 `/data/root`） | `path escapes allowed_root` |
| 不存在的文件 | `open file: no such file or directory` |

符号链接策略：只要求**最终目标**在根内，不禁止链接本身；根内软链接是合法用法，根外逃逸才拒绝。

## 8. 文件读取与结果

### 8.1 Execute 包装

```go
func (t *Tool) Execute(ctx context.Context, input json.RawMessage) (tool.ToolResult, error) {
    if err := ctx.Err(); err != nil {
        return tool.ToolResult{}, err
    }
    if t.root == "" || t.maxBytes <= 0 {
        return tool.ToolResult{}, errors.New("read_file tool is not initialized")
    }

    in, err := decodeInput(input)
    if err != nil {
        if errors.Is(err, errPathRequired) {
            return tool.ToolResult{Success: false, Error: err.Error()}, nil
        }
        return tool.ToolResult{}, err
    }

    data, err := t.readFile(in.Path)
    if err != nil {
        return tool.ToolResult{Success: false, Error: err.Error()}, nil
    }

    return tool.ToolResult{
        Success: true,
        Output:  data,
        Metadata: map[string]any{
            "bytes_read": len(data),
        },
    }, nil
}
```

`ExecutionTimeMS` 由 Invoker 填充；`read_file` 不自行填写。`CostPerUse=0`，不进入预算扣减。

### 8.2 readFile

```go
func (t *Tool) readFile(path string) (string, error) {
    // 预检查：尽早拒绝目录/FIFO/设备，避免 open 阻塞或产生副作用。
    pre, err := os.Lstat(path)
    if err != nil {
        return "", openErr(err)
    }
    if !pre.Mode().IsRegular() {
        return "", errors.New("not a regular file")
    }
    if pre.Size() > t.maxBytes {
        return "", errors.New("file exceeds max_file_bytes")
    }

    f, err := os.Open(path)
    if err != nil {
        return "", openErr(err)
    }
    defer f.Close()

    // 复核：防止 stat 与 open 之间文件被替换。
    fi, err := f.Stat()
    if err != nil {
        return "", errors.New("stat file: cannot access file")
    }
    if !fi.Mode().IsRegular() {
        return "", errors.New("not a regular file")
    }
    if fi.Size() > t.maxBytes {
        return "", errors.New("file exceeds max_file_bytes")
    }

    data, err := readLimited(f, t.maxBytes)
    switch {
    case errors.Is(err, errFileTooLarge):
        return "", errFileTooLarge
    case err != nil:
        return "", errors.New("read file: I/O error")
    }

    // NUL 优先：含 NUL 基本可判定为二进制。
    if bytes.IndexByte(data, 0) >= 0 {
        return "", errors.New("file appears to be binary")
    }
    if !utf8.Valid(data) {
        return "", errors.New("file is not valid UTF-8 text")
    }
    return string(data), nil
}

var errFileTooLarge = errors.New("file exceeds max_file_bytes")

func readLimited(r io.Reader, maxBytes int64) ([]byte, error) {
    limit := maxBytes
    if limit < math.MaxInt64 {
        limit++ // 多读 1 字节用于判断超限
    }
    data, err := io.ReadAll(io.LimitReader(r, limit))
    if err != nil {
        return nil, err
    }
    if int64(len(data)) > maxBytes {
        return nil, errFileTooLarge
    }
    return data, nil
}
```

### 8.3 语义约定

| 场景 | 行为 |
|---|---|
| 大小恰好等于 `max_file_bytes` | 允许 |
| `max_file_bytes + 1` | 拒绝，报超限 |
| 文件在读取过程中增长 | `LimitReader` 兜底，超限报错，不截断返回 |
| 空文件 | 成功，`Output=""` |
| CRLF、BOM、首尾空白 | 原样保留，不规范化 |
| 含 NUL | `ToolResult.Error="file appears to be binary"` |
| 非法 UTF-8 且无 NUL | `ToolResult.Error="file is not valid UTF-8 text"` |
| 含 NUL 且非法 UTF-8 | 先判 NUL，报 binary |
| 上下文取消 | 返回 `ctx.Err()`，不是 ToolResult |

大小检查同时做“预检 + fstat + 限读”三层：

1. 预检快速失败，并避免打开特殊文件；
2. `fstat` 复核打开瞬间的类型和大小；
3. `LimitReader` 防止读取期间增长导致内存失控。

三层都返回同一个超限错误，避免模型看到不一致的文案。

## 9. 结果与错误契约

### 9.1 成功结果

```go
tool.ToolResult{
    Success: true,
    Output:  "文件原文",
    Metadata: map[string]any{
        "bytes_read": 128,
    },
}
```

Agent 渲染：

```text
<observation tool="read_file" path="examples/notes.txt" status="ok">文件原文</observation>
```

### 9.2 失败结果

```go
tool.ToolResult{
    Success: false,
    Error:   "open file: no such file or directory",
}
```

Agent 渲染：

```text
<observation tool="read_file" path="missing.txt" status="error">open file: no such file or directory</observation>
```

### 9.3 错误来源

| 来源 | 触发场景 | 错误文本 |
|---|---|---|
| Invoker | 缺少必填参数 | `invalid arguments: missing required parameter "path"` |
| Invoker | 未知参数 | `invalid arguments: unknown parameter "foo"` |
| Invoker | 类型错误 | `invalid arguments: path must be a string` |
| Invoker | 限流 | `rate limit exceeded` |
| Invoker | 预算不足 | `cost budget exceeded` |
| ToolResult | 路径空白 | `path is required` |
| ToolResult | 路径越界（两阶段任一） | `path escapes allowed_root` |
| ToolResult | 文件不存在/悬空链接 | `open file: no such file or directory` |
| ToolResult | 权限不足 | `open file: permission denied` |
| ToolResult | 其他打开失败 | `open file: cannot access file` |
| ToolResult | `fstat` 失败 | `stat file: cannot access file` |
| ToolResult | 目录、FIFO、设备等 | `not a regular file` |
| ToolResult | 超过大小上限 | `file exceeds max_file_bytes` |
| ToolResult | 含 NUL | `file appears to be binary` |
| ToolResult | 非 UTF-8 | `file is not valid UTF-8 text` |
| ToolResult | 读取 I/O 错误 | `read file: I/O error` |
| Go error | 上下文取消/超时 | `context canceled` / `context deadline exceeded` |
| Go error | Tool 零值 | `read_file tool is not initialized` |
| Go error | 内部解码契约破坏 | `decode input: ...`（只记录日志，不直接暴露给模型） |

错误脱敏原则：

- **运行期 ToolResult.Error** 不包含解析后的绝对路径、目录列表或底层 `syscall` 路径详情。observation 标签中已有模型传入的 `path`，足够定位。
- **构造期**错误面向本地启动用户，可以包含根目录路径。
- **Go error** 只用于取消和内部错误，不直接暴露给模型。
- 底层错误通过分类映射：

```go
func openErr(err error) error {
    switch {
    case errors.Is(err, fs.ErrNotExist):
        return errors.New("open file: no such file or directory")
    case errors.Is(err, fs.ErrPermission):
        return errors.New("open file: permission denied")
    default:
        return errors.New("open file: cannot access file")
    }
}
```

## 10. 与 Registry / Agent 的集成

1. **注册**：启动时创建 Tool，读取 `Definition()`，注册进 Registry；重名工具由 Registry 层报错。

   ```go
   rf, err := readfile.New(cfg.Tools.AllowedRoot, cfg.Tools.MaxFileBytes)
   if err != nil {
       // 配置/用法错误，退出码 2
   }
   registry := tool.NewRegistry(rf)
   ```

2. **Prompt**：只使用 `name`、`description` 和由 `Parameters` 生成的 Input Schema（[DESIGN.md §9.2](../DESIGN.md#L738)）。`requires_auth`、`rate_limit`、`timeout_seconds`、`memory_limit_mb`、`sandboxed`、`dangerous`、`cost_per_use` 等策略字段不进入 Prompt。

3. **Observation**：Agent 根据 `ToolResult` 渲染：
   - `Success=true`：`Output` 为 string 时直接作为正文，其他类型 JSON 序列化；
   - `Success=false`：使用 `Error`；
   - `Metadata`、`ExecutionTimeMS`、`TokensUsed` 不进入 Prompt，只用于追踪/审计。

4. **工具失败不终止循环**：模型可以修正路径或基于已有信息结束；连续重试由 `repeated_action` / `no_progress` 兜底（[DESIGN.md §4.2](../DESIGN.md#L163)）。

5. **context 错误会终止**：`Execute` 返回的取消/超时错误由 Agent 映射为 `canceled` / `timeout`。

6. **内容注入**：`read_file` 原样返回正文。文件中的 `</observation>`、伪造标签或指令属于 Agent/prompt 层风险，建议在 Agent 组装 observation 时统一做边界转义或固定分隔；本工具不修改文件内容。

## 11. 配置与启动

对应已完成的配置层字段（[internal/config/config.go](../internal/config/config.go#L60)）：

| 配置 | 用途 | 默认值 |
|---|---|---|
| `tools.allowed_root` | 根目录，相对路径基于启动工作目录 | `.` |
| `tools.max_file_bytes` | 单文件字节上限 | `65536` |

启动约束：

- `allowed_root` 必须在启动时存在且是目录，否则启动失败。
- `allowed_root` 默认 `.`，意味着默认可读整个工作区；敏感项目应显式收窄。
- 配置校验已保证 `allowed_root` 非空、`max_file_bytes > 0`；Tool 构造函数再做一次防御性检查，保证被其他入口复用时也安全。
- `read_file` 的 `memory_limit_mb=16` 应与配置交叉校验：`16 MiB >= 2 * max_file_bytes + 基础开销`。若调大 `max_file_bytes` 超过该预算，应同步调大元数据或拒绝启动。
- 工具初始化失败属于配置/用法错误，CLI 退出码为 2（[DESIGN.md §10.2](../DESIGN.md#L773)）。

## 12. 安全分析

| 威胁 | 防护 | 残留风险 |
|---|---|---|
| `../` 路径穿越 | `Join + Clean + Rel` 词法检查 | 无 |
| 绝对路径越界 | 第一层 `withinRoot` 直接拒绝 | 无 |
| 符号链接逃逸 | `EvalSymlinks` 后二次 `Rel` | TOCTOU 窗口 |
| 前缀兄弟目录伪装 | 使用 `filepath.Rel`，不用字符串前缀 | 无 |
| 目录/设备/FIFO | 预 `Lstat` + `fstat` 检查 `Mode().IsRegular()` | 替换竞态下 FIFO 仍可能阻塞 |
| 超大文件 | 预检 + fstat + `LimitReader(max+1)` | 无静默截断 |
| 二进制/非法编码 | NUL + `utf8.Valid` | 无 |
| 敏感信息泄露 | 运行期错误脱敏，不返回绝对路径/目录列表 | 底层 OS 错误被简化 |
| 提示注入 | System prompt 声明 observation 不可信 | 文件内容仍可能诱导模型 |
| 硬链接 | 根内硬链接视为合法文件 | 无法从路径判断来源 |
| TOCTOU | 预检 + 打开后复核缩小窗口 | 完整解决需 `openat2(RESOLVE_BENEATH)` 或 OS 沙箱 |
| 并发调用 | Tool 不可变，每次独立 `os.File` | 无共享状态 |
| 沙箱误解 | 元数据显式 `sandboxed=false` | 只有应用层路径约束，不是 OS 隔离 |
| 内存/超时误解 | `memory_limit_mb` 是预算，超时依赖 context 检查 | 进程内无法硬限制/中断阻塞 I/O |

已知取舍：

- 为兼容 Go 1.22，采用手工路径校验，而不是 Go 1.24 的 `os.Root` 或 Linux `openat2`。若后续提高最低 Go 版本，可替换底层实现，从根本上消除 TOCTOU，对外接口不变。
- 预 `Lstat` 可避免正常场景下打开 FIFO 阻塞，但无法消除“检查后替换为 FIFO”的竞态；高安全场景应使用容器或内核级约束。
- 不禁止根内符号链接和硬链接；只保证最终解析目标在根内。
- 可选加固：在打开后比较预检查与 `fstat` 的 `os.SameFile`，拒绝打开期间被替换的文件；或在 Unix 上使用 `O_NONBLOCK` 打开。当前设计不强制，以保持实现简单和跨平台。
- `read_file` 不进入沙箱，`memory_limit_mb` 和 `timeout_seconds` 不是硬隔离；高安全部署应通过 SandboxRunner 运行。

## 13. 测试设计

### 13.1 测试基础设施

- 使用 `t.TempDir()` 创建根目录和根外兄弟目录。
- 使用 `os.WriteFile` 构造文本、空文件、超限文件和非法编码文件。
- 使用 `os.Symlink` 构造根内/根外/悬空链接；`os.Symlink` 失败时 `t.Skip`。
- FIFO 测试使用 Unix build tag（`//go:build unix`）和 `syscall.Mkfifo`。
- 权限测试在非 root 用户下执行，`os.Geteuid() == 0` 时跳过。
- 所有错误断言只匹配稳定子串，不依赖底层 OS 错误全文。

### 13.2 构造函数

| 用例 | 期望 |
|---|---|
| `allowedRoot=""` | 错误 |
| `maxFileBytes=0` / 负数 | 错误 |
| 根目录不存在 | 错误 |
| 根路径是普通文件 | `allowed root is not a directory` |
| 根路径是符号链接到目录 | 成功，内部使用真实路径 |
| 正常目录 | 成功 |

### 13.3 Definition

- `name=read_file`、`version=1.0.0`、`category=file`。
- `requires_auth=false`、`rate_limit=60`、`timeout_seconds=10`、`memory_limit_mb=16`。
- `sandboxed=false`、`dangerous=false`、`cost_per_use=0`（分，CNY）。
- 参数只有一个 `path`，`string`，`required=true`。
- 生成的 Input Schema 合法，`required=["path"]`，`additionalProperties=false`。

### 13.4 参数校验与默认值

以下属于 Invoker 测试，但需与 read_file 集成验证：

- 合法 `{"path":"notes.txt"}`。
- `{}` → `invalid arguments: missing required parameter "path"`。
- `{"path":123}` → `invalid arguments: path must be a string`。
- `{"path":"notes.txt","extra":1}` → `invalid arguments: unknown parameter "extra"`。
- `{"path":"   "}` → 工具返回 `path is required`。
- `null`、数组、非 object → `invalid arguments: action_input must be a JSON object`。

### 13.5 路径与文件类型

在临时根目录下构造根内文件、子目录文件、根外兄弟文件、根内软链、根外软链、悬空软链、目录、空文件、FIFO。

覆盖：

- 相对路径、嵌套路径、根内绝对路径成功。
- `../`、`sub/../../`、根外绝对路径、根外软链 → `path escapes allowed_root`。
- 前缀兄弟目录（`root-evil`）→ 越界。
- 根内软链 → 成功；悬空软链 → `open file: ...`。
- 目录、FIFO → `not a regular file`，且 FIFO 用例不能挂起。
- 构造后 `chdir`，仍以构造时根目录为准。

### 13.6 大小与编码

- 恰好 `max_file_bytes` → 成功。
- `max_file_bytes + 1` → `file exceeds max_file_bytes`。
- 中文/emoji 文本 → 内容逐字节相等。
- CRLF、BOM、首尾空白 → 原样返回。
- `[]byte{0xff, 0xfe}` → `file is not valid UTF-8 text`。
- `"a\x00b"` → `file appears to be binary`。
- NUL + 非法 UTF-8 → 固定期望为 binary。

### 13.7 结果与错误

- 成功：`ToolResult.Success=true`、`Output` 等于原文、`Metadata["bytes_read"]` 正确。
- 预期失败：`ToolResult.Success=false`、`Error` 命中稳定子串、Go error 为 nil。
- 已取消的 ctx → `context.Canceled`，不是 ToolResult。
- 零值 Tool → Go error `read_file tool is not initialized`。
- 并行调用多个 `Execute` 无竞态（`go test -race`）。
- 工具只读：调用前后文件内容与修改时间不变。
- 不泄漏解析后的绝对路径。

### 13.8 验证命令

```bash
gofmt -l .
go vet ./...
go test ./... -race
```

## 14. 验收标准

- [x] `Definition()` 返回 `category=file`、`sandboxed=false`、`cost_per_use=0`、`rate_limit=60`、`timeout_seconds=10`、`memory_limit_mb=16`。
- [x] 参数定义生成正确的 Input Schema，`path` 必填。
- [x] Invoker 能拒绝缺失、类型错误和未知参数。
- [x] `New` 对非法根目录/大小配置失败。
- [x] 根内文本文件通过 `ToolResult.Output` 完整返回，空文件返回空串。
- [x] 越界路径在打开前被拒绝，`ToolResult.Error` 为 `path escapes allowed_root`。
- [x] 根外符号链接被拒绝，根内符号链接可读。
- [x] 目录、FIFO、设备文件返回 `not a regular file`。
- [x] 超过上限直接报错，不静默截断。
- [x] NUL、非法 UTF-8 被拒绝。
- [x] 所有运行期错误不泄露绝对路径。
- [x] context 取消/超时以 Go error 返回，可映射为 `canceled` / `timeout`。
- [ ] 通过 Agent + CLI 端到端集成后，越界错误作为 `status="error"` 的 observation 返回（Agent Loop 已实现，CLI/端到端待后续，对应 [DESIGN.md §11.2 第 5 条](../DESIGN.md#L797)）。
- [x] `go test ./...`、`go vet ./...` 通过。

## 15. 实现顺序

1. **工具定义类型**：按 [工具定义设计](tool_definition.md) 实现 `ToolMetadata`、`ToolParameter`、`ToolDefinition`、`ToolResult`、默认值、校验和 Schema 生成。
2. **readfile.New + Definition**：根目录规范化、构造测试、元数据和参数定义。
3. **输入解码 + 路径解析**：两阶段包含性检查、路径矩阵测试。
4. **文件读取**：打开、fstat、限读、大小/编码校验 + 内容测试。
5. **Execute 结果包装**：成功/失败 ToolResult、内部错误与 context 错误边界。
6. **Registry / Invoker 集成**：参数校验、默认值、超时、策略 fail-closed。
7. **Agent 集成**：`ToolResult` 渲染为 observation（后续层）。

每一步以 `go test ./...` 通过为完成标准；真实 LLM 不进入本工具的自动化测试。

截至 v0.2，步骤 1–6 已实现并通过测试；步骤 7（Agent 集成）已完成，CLI/端到端集成待后续。

## 16. 已确认决策与开放问题

### 16.1 已确认决策

1. 根目录在构造期解析并规范化，必须存在且为目录。
2. 根内符号链接允许，根外逃逸拒绝。
3. 采用两阶段包含性检查，越界路径尽量在文件系统访问前拒绝。
4. 在 `os.Open` 前做 `Lstat` 预检查，避免正常场景下 FIFO 阻塞。
5. NUL 检查先于 UTF-8 校验。
6. 读取采用 `maxBytes+1` 限流，超限报错，不截断。
7. 运行期错误脱敏，不返回绝对路径。
8. 工具通过 `ToolResult` 返回结果：预期失败放 `Error`，取消/内部错误用 Go error。
9. `read_file` 元数据：`category=file`、`sandboxed=false`、`dangerous=false`、`requires_auth=false`、`cost_per_use=0` 分。
10. 成本币种固定 CNY，整数分；`ExecutionTimeMS` 由 Invoker 填充。

### 16.2 开放问题

1. （已确定）Observation 中的文件正文边界转义见 [Agent Loop 详细设计](agent_loop.md) §12：Agent 用 JSON 对象包装正文，JSON 编码会转义尖括号和 &；本工具仍保持原文。
2. 是否在 Unix 上额外使用 `O_NONBLOCK` 或 `openat2` 彻底关闭 FIFO/TOCTOU 窗口。当前以 Go 1.22 + 标准库为目标，暂不做平台特定实现。
3. 是否比较预检查与打开后文件的 `os.SameFile` 以拒绝打开期间被替换的文件。当前作为可选加固，不纳入 v0.2 必做范围。
4. `memory_limit_mb=16` 与 `max_file_bytes` 的交叉校验是否在构造期强制失败，还是在配置层限制 `max_file_bytes` 上限。当前建议构造期失败。
