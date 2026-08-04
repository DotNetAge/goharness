package events

// SecurityLevel 表示工具或操作的安全分级。
// 安全级别用于确定授权要求和风险评估，
// 适用于可能产生副作用的操作。
// 更高的安全级别需要更严格的授权检查。
type SecurityLevel int

const (
	// LevelSafe 表示无副作用的纯查询操作。
	// 这些操作是只读的，不会修改任何状态。
	// 例如：读取文件、查询数据库、搜索网页。
	LevelSafe SecurityLevel = iota
	// LevelSensitive 表示具有有限写入影响的操作。
	// 这些操作可能会修改状态，但影响范围可控且可预测。
	// 例如：创建临时文件、更新用户偏好、发送消息。
	LevelSensitive
	// LevelHighRisk 表示具有不可预测或破坏性影响的操作。
	// 这些操作可能造成重大变更，需要显式授权。
	// 例如：删除文件、执行任意代码、修改系统配置。
	LevelHighRisk
)
