package tools

// imageReadingEnabled 控制 Read 工具是否启用图片读取。
// 见 read.go 中 Execute 的内联图片处理分支。
var imageReadingEnabled bool

// EnableImageReading 启用图片读取支持。
// 设置后，Read.Execute 会在遇到图片文件时自动压缩并编码。
func EnableImageReading() {
	imageReadingEnabled = true
}

// DisableImageReading 禁用图片读取支持。
func DisableImageReading() {
	imageReadingEnabled = false
}
