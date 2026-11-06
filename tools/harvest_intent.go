package tools

// HarvestIntentRecorder 是收割意图的落盘接缝（应用侧实现，agents 层
// subAgentManager 经 Runtime 注入使用）。
//
// 收割状态必须独立于任何线程/进程的生命周期存在：subAgentManager 只把
// 收割意图交给本接口持久化（如主会话目录内的 session-harvest.json），
// 落定判定、超时兜底与结果回灌全部由应用侧的无状态轮询器从磁盘重新
// 推导执行——daemon 重启后下一轮扫描自然捡起 pending 意图继续收割，
// 进程内存中不再保存任何收割执行体。
type HarvestIntentRecorder interface {
	// Record 登记收割意图并立即返回（非阻塞）。去重键是任务句柄：
	// 同一主会话已有意图时把新增目标按 task_id 合并登记（收割在飞时
	// 新派发的任务不得丢失）；本次传入目标全部已在登记中（无新增）时
	// 返回 ErrHarvestAlreadyActive，工具层将其转为「已在进行中」回执。
	Record(mainSessionID string, targets []HarvestTarget) error
}
