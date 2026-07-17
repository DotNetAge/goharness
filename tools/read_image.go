package tools

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"path/filepath"
	"strings"

	_ "image/gif"
	_ "image/png"
)

// supportedImageExtensions 是支持读取的图片扩展名。
var supportedImageExtensions = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".bmp":  true,
	".webp": true,
}

// isImageFile 检查文件扩展名是否为支持的图片格式。
func isImageFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return supportedImageExtensions[ext]
}

// isSVGFile 检查文件扩展名是否为 SVG。
func isSVGFile(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".svg"
}

// compressAndEncodeImage 对图片进行压缩并 base64 编码。
//
// 内联设计（见过度设计审计 #8）：不再通过 ReadHook 接口调用，而是直接在
// Read.Execute 的图片分支中调用此函数。
//
// 处理逻辑：
//   - SVG 文件：不解码，不 resize，直接 base64 编码原始内容
//   - 位图文件：解码 → resize（最长边 ≤ maxSide）→ JPEG quality 编码 → base64
func compressAndEncodeImage(content []byte, path string, maxSide int) ImageContent {
	if isSVGFile(path) {
		return ImageContent{
			MediaType:      "image/svg+xml",
			Base64Data:     base64Encode(content),
			Width:          0,
			Height:         0,
			RawSize:        int64(len(content)),
			CompressedSize: len(content),
		}
	}

	// 位图文件
	img, _, err := image.Decode(bytes.NewReader(content))
	if err != nil {
		return ImageContent{
			MediaType: "image/" + strings.TrimPrefix(filepath.Ext(path), "."),
			Base64Data: base64Encode(content),
			RawSize:   int64(len(content)),
		}
	}

	if maxSide <= 0 {
		maxSide = 512
	}

	bounds := img.Bounds()
	origW := bounds.Dx()
	origH := bounds.Dy()

	scale := math.Min(
		float64(maxSide)/float64(origW),
		float64(maxSide)/float64(origH),
	)
	if scale > 1 {
		scale = 1
	}
	newW := int(math.Round(float64(origW) * scale))
	newH := int(math.Round(float64(origH) * scale))
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	resized := resizeImage(img, newW, newH)
	quality := defaultQuality(int64(len(content)))

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, resized, &jpeg.Options{Quality: quality}); err != nil {
		return ImageContent{
			MediaType: "image/jpeg",
			Base64Data: base64Encode(content),
			RawSize:   int64(len(content)),
		}
	}

	compressed := buf.Bytes()
	return ImageContent{
		MediaType:      "image/jpeg",
		Base64Data:     base64Encode(compressed),
		Width:          newW,
		Height:         newH,
		RawSize:        int64(len(content)),
		CompressedSize: len(compressed),
	}
}

// defaultQuality 根据原始文件大小返回 JPEG 编码质量。
func defaultQuality(rawSize int64) int {
	switch {
	case rawSize < 1*1024*1024:
		return 90
	case rawSize > 5*1024*1024:
		return 70
	default:
		return 85
	}
}

// resizeImage 使用最近邻插值对图片进行缩放。
func resizeImage(img image.Image, newW, newH int) image.Image {
	bounds := img.Bounds()
	origW := bounds.Dx()
	origH := bounds.Dy()

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	for y := 0; y < newH; y++ {
		for x := 0; x < newW; x++ {
			srcX := x * origW / newW
			srcY := y * origH / newH
			dst.Set(x, y, img.At(srcX, srcY))
		}
	}
	return dst
}

// base64Encode 对字节数据进行标准 base64 编码。
func base64Encode(data []byte) string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	encoded := make([]byte, ((len(data)+2)/3)*4)
	i := 0
	j := 0
	for i < len(data) {
		var val int
		remaining := len(data) - i
		if remaining >= 3 {
			val = int(data[i])<<16 | int(data[i+1])<<8 | int(data[i+2])
			encoded[j] = charset[(val>>18)&0x3F]
			encoded[j+1] = charset[(val>>12)&0x3F]
			encoded[j+2] = charset[(val>>6)&0x3F]
			encoded[j+3] = charset[val&0x3F]
		} else if remaining == 2 {
			val = int(data[i])<<16 | int(data[i+1])<<8
			encoded[j] = charset[(val>>18)&0x3F]
			encoded[j+1] = charset[(val>>12)&0x3F]
			encoded[j+2] = charset[(val>>6)&0x3F]
			encoded[j+3] = '='
		} else {
			val = int(data[i]) << 16
			encoded[j] = charset[(val>>18)&0x3F]
			encoded[j+1] = charset[(val>>12)&0x3F]
			encoded[j+2] = '='
			encoded[j+3] = '='
		}
		i += 3
		j += 4
	}
	return string(encoded)
}

// isImageContentLarge 判断图片内容是否超过阈值（用于截断提示）。
func isImageContentLarge(ic ImageContent) bool {
	return len(ic.Base64Data) > 100 * 1024 // 100KB
}

// fmtImageSummary 格式化图片摘要信息。
func fmtImageSummary(ic ImageContent, path string) string {
	if ic.Width > 0 && ic.Height > 0 {
		return fmt.Sprintf(
			"[图片已读取并压缩：%s (%d×%d px, 原始 %d 字节, 压缩后 %d 字节)]",
			path, ic.Width, ic.Height, ic.RawSize, ic.CompressedSize,
		)
	}
	return fmt.Sprintf(
		"[图片已读取：%s (原始 %d 字节)]",
		path, ic.RawSize,
	)
}
