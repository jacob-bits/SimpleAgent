# SimpleAgent

一个极简 ReAct agent：模型每次回复一个 JSON 对象，要么声明一个动作，要么给出最终答案；工具返回 observation，循环直到命中终止条件。本文件定义这套语言，代码标识符与文档正文向它对齐。

## Language

### 协议

**Step**:
ReAct 循环的一次迭代：一次模型调用、一次解析，以及最多一次工具执行。编号与 `min_steps` / `max_steps` 对齐。
_Avoid_: iteration, turn, round

**Action**:
一步里声明的工具调用，由 `action` 与 `action_input` 组成，与 `final_answer` 互斥。解析产物叫 Action，不叫 Step。
_Avoid_: call, tool call, command

**Observation**:
工具返回给模型的正文，永远是带 `status` 的 JSON 对象，按不可信数据处理。
_Avoid_: result, output, tool output

**Trace**:
结构化的运行事件；`-verbose` 时 stderr 上那几行是它的渲染，不是另一种东西。
_Avoid_: log, debug output

**终止条件**:
让循环结束的判定：`final_answer`、步数上下限、重复动作、无进展、上下文预算、超时与取消。
_Avoid_: stop, exit, break

### 工具

**Tool**:
一件可被模型调用的能力，携带元数据、参数 schema 与执行函数。

**Registry**:
工具的登记处，在注册期决定一个工具能不能被暴露。
_Avoid_: tool layer, tool manager

**Invoker**:
工具调用的唯一执行通道，负责调用期的校验、限流、预算与超时。认证、审批与沙箱属于登记期，不属于它。
_Avoid_: executor, dispatcher, tool layer

**allowed-root**:
工具可读路径的边界根，由工具层执行。
_Avoid_: sandbox, workdir, root
