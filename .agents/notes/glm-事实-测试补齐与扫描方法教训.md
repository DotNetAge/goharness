# 事实-goharness 单元测试补齐与扫描方法教训

- 分类：事实 / 经验
- 范围：core/goharness 测试覆盖缺口扫描与补齐（2026-11-15）

## 已补齐（全量 + race 通过）

- tools/diffutil/diff_test.go：7 用例（无变化/单行改/插入/删除/多 hunk/空文件/截断）+ SumChanges。
- session/whitelist_test.go：6 用例（落盘恢复/7 工具名路由/去重/未知工具/无 store/损坏文件重置）。
- tools/tasks_state_test.go：ValidTaskTransition 4×4 全矩阵 + 未知状态。
- config/provider_registry_test.go：Provider CRUD/adapter 只读契约/GetRaw vs Get 继承/ResolveProvider/Save round-trip。
- store/filesystem_test.go：MapFS 绝对路径/相对路径/NotFound/Open/真实 OS 文件系统。
- tools/stealth_config_test.go：LoadStealthConfig 默认值/覆盖/非法回退/越界兜底 + profile 名归一化。
- logging/logger_test.go：四级别输出/Error 属性顺序与 nil 处理/nil 回退/NopLogger。

## 教训（下次我看到将触发）

1. **测试输入的构造顺序陷阱**：diff 截断用例最初把「替换块」追加在保留块之后，实际输入是"保留+追加"而非"就地替换"，算法输出 2 hunk 是对的、我的预期才是错的。下次写 diff 类测试，先逐字检查 new 内容的行序是否与业务意图一致，预期值与实际不符时先用探针打印中间结果（hunk 边界/LCS）再下结论，不猜算法有 bug。
2. **"无测试引用"扫描的误报源**：grep 函数名判断覆盖会漏两种情况——①同文件内其他函数已被测但目标函数确实没测（如 pickUAProfile 已测、LoadStealthConfig 没测，两个函数在同文件易混为一谈）；②测试经间接路径覆盖（With* options、On* handler）。下次报"缺测试"结论前，先 grep 测试文件里相关联的函数族，区分"函数缺直接测试"与"模块缺测试"。
3. **slog TextHandler 断言**：中文 msg 不加引号（`msg=会话已创建`），行首有 time= 前缀；断言勿凭英文习惯假想格式，先看真实输出再写。

## 结论（接受无测，非缺口）

- skill 包：纯类型声明 + 接口契约（53 行），无行为可测。
- store/filestore.go：纯接口定义。
- session/helper.go EstimateTokens：agents 侧已有回归测试（见技术债定案 v0.2.55）。
