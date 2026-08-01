package tools

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/tealeg/xlsx"
	"github.com/unidoc/unipdf/v3/extractor"
	"github.com/unidoc/unipdf/v3/model"
)

// docResult 是文档转换的通用结果。
type docResult struct {
	content string // Markdown 格式的文本内容
	title   string // 文档标题（如果有）
	author  string // 文档作者（PDF 特有）
	pages   int    // 总页数（PDF 特有）
}

// detectDocFormat 检测文件扩展名是否属于支持的文档格式。
// 返回格式名称（pdf/docx/xlsx/epub），不支持则返回空字符串。
func detectDocFormat(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf":
		return "pdf"
	case ".docx":
		return "docx"
	case ".xlsx":
		return "xlsx"
	case ".epub":
		return "epub"
	}
	return ""
}

// convertDocument 根据格式名称调用对应的文档转换函数。
func convertDocument(format string, r io.Reader) (*docResult, error) {
	switch format {
	case "pdf":
		return pdfToMarkdown(r)
	case "docx":
		return docxToMarkdown(r)
	case "xlsx":
		return xlsxToMarkdown(r)
	case "epub":
		return epubToMarkdown(r)
	}
	return nil, fmt.Errorf("%s", GuideInvalidValue("Read", "filePath", format, "Read 仅支持 pdf/docx/xlsx/epub 等文档格式，可使用 Ls/Glob 确认文件类型后重试"))
}

// ---------------------------------------------------------------------------
// PDF → Markdown
// ---------------------------------------------------------------------------

func pdfToMarkdown(r io.Reader) (*docResult, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("读取", "PDF 文档", err), err)
	}

	pdfReader, err := model.NewPdfReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			"尝试解析 PDF 文档结构",
			WithErrDetail("文档解析器在解析 PDF 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
			"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
		), err)
	}

	// 提取元数据
	title := ""
	author := ""
	pdfInfo, err := pdfReader.GetPdfInfo()
	if err == nil && pdfInfo != nil {
		if pdfInfo.Title != nil {
			title = pdfInfo.Title.Decoded()
		}
		if pdfInfo.Author != nil {
			author = pdfInfo.Author.Decoded()
		}
	}

	pageCount, err := pdfReader.GetNumPages()
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			"尝试获取 PDF 文档的页数",
			WithErrDetail("文档解析器在解析 PDF 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
			"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
		), err)
	}

	var mdBuilder strings.Builder
	if title != "" {
		mdBuilder.WriteString(fmt.Sprintf("# %s\n\n", title))
	}

	for i := 1; i <= pageCount; i++ {
		page, err := pdfReader.GetPage(i)
		if err != nil {
			continue
		}

		mdBuilder.WriteString(fmt.Sprintf("\n---\n\n## Page %d\n\n", i))

		ex, err := extractor.New(page)
		if err != nil {
			continue
		}

		pageText, err := ex.ExtractText()
		if err == nil && pageText != "" {
			mdBuilder.WriteString(textToMarkdown(pageText))
		}
	}

	return &docResult{content: mdBuilder.String(), title: title, author: author, pages: pageCount}, nil
}

// ---------------------------------------------------------------------------
// EPUB → Markdown
// ---------------------------------------------------------------------------

func epubToMarkdown(r io.Reader) (*docResult, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("读取", "EPUB 文档", err), err)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			"尝试解析 EPUB 文档（zip 容器）结构",
			WithErrDetail("文档解析器在解析 EPUB 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
			"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
		), err)
	}

	opfPath, err := findOPFPath(zr)
	if err != nil {
		return nil, err
	}
	opfDir := filepath.Dir(opfPath)

	opf, err := parseOPF(zr, opfPath)
	if err != nil {
		return nil, err
	}

	content, err := readSpineToMarkdown(zr, opf, opfDir)
	if err != nil {
		return nil, err
	}

	result := &docResult{content: strings.TrimSpace(content)}
	if opf.Title != "" {
		result.title = opf.Title
	}
	return result, nil
}

// --- EPUB internal types ---

type opfMetadata struct {
	Title    string
	Creator  string
	Language string
	Spine    []string
	Manifest map[string]manifestItem
}

type manifestItem struct {
	Href      string
	MediaType string
}

func findOPFPath(zr *zip.Reader) (string, error) {
	containerData, err := readZipFile(zr, "META-INF/container.xml")
	if err != nil {
		return "", fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			"尝试读取 EPUB 文档的 META-INF/container.xml",
			WithErrDetail("文档解析器在解析 EPUB 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
			"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
		), err)
	}

	clean := stripXMLNamespaces(containerData)
	var container struct {
		XMLName   xml.Name `xml:"container"`
		RootFiles struct {
			RootFile []struct {
				FullPath  string `xml:"full-path,attr"`
				MediaType string `xml:"media-type,attr"`
			} `xml:"rootfile"`
		} `xml:"rootfiles"`
	}
	if err := xml.Unmarshal(clean, &container); err != nil {
		return "", fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			"尝试解析 EPUB 文档的 container.xml 配置",
			WithErrDetail("文档解析器在解析 EPUB 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
			"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
		), err)
	}
	if len(container.RootFiles.RootFile) == 0 {
		return "", fmt.Errorf("%s", BuildGuide(
			"尝试从 EPUB 文档的 container.xml 中定位 OPF 文件",
			"container.xml 中缺少 rootfile 条目，EPUB 结构无效",
			"确认文件未被损坏、扩展名与实际格式一致（EPUB 为 zip 容器，内含 META-INF/container.xml）；若仍失败，应告知用户文件无法解析",
		))
	}
	return container.RootFiles.RootFile[0].FullPath, nil
}

func parseOPF(zr *zip.Reader, opfPath string) (*opfMetadata, error) {
	opfData, err := readZipFile(zr, opfPath)
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			fmt.Sprintf("尝试读取 EPUB 文档的 OPF 文件 %q", opfPath),
			WithErrDetail("文档解析器在解析 EPUB 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
			"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
		), err)
	}

	clean := stripXMLNamespaces(opfData)
	var opf struct {
		XMLName  xml.Name `xml:"package"`
		Metadata struct {
			Title    string `xml:"title"`
			Creator  string `xml:"creator"`
			Language string `xml:"language"`
		} `xml:"metadata"`
		Manifest struct {
			Items []struct {
				ID        string `xml:"id,attr"`
				Href      string `xml:"href,attr"`
				MediaType string `xml:"media-type,attr"`
			} `xml:"item"`
		} `xml:"manifest"`
		Spine struct {
			ItemRefs []struct {
				IDRef string `xml:"idref,attr"`
			} `xml:"itemref"`
		} `xml:"spine"`
	}

	if err := xml.Unmarshal(clean, &opf); err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			fmt.Sprintf("尝试解析 EPUB 文档的 OPF 文件 %q", opfPath),
			WithErrDetail("文档解析器在解析 EPUB 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
			"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
		), err)
	}

	meta := &opfMetadata{
		Title:    opf.Metadata.Title,
		Creator:  opf.Metadata.Creator,
		Language: opf.Metadata.Language,
		Manifest: make(map[string]manifestItem),
	}
	for _, item := range opf.Manifest.Items {
		meta.Manifest[item.ID] = manifestItem{
			Href:      item.Href,
			MediaType: item.MediaType,
		}
	}
	for _, ref := range opf.Spine.ItemRefs {
		meta.Spine = append(meta.Spine, ref.IDRef)
	}
	return meta, nil
}

func readSpineToMarkdown(zr *zip.Reader, opf *opfMetadata, opfDir string) (string, error) {
	converter := md.NewConverter("", true, &md.Options{HeadingStyle: "atx"})
	var mdBuilder strings.Builder

	for _, id := range opf.Spine {
		item, ok := opf.Manifest[id]
		if !ok {
			continue
		}
		// 跳过非 XHTML/HTML 文件（CSS、图片等）
		if !isContentFile(item.MediaType) {
			continue
		}
		contentPath := filepath.ToSlash(filepath.Join(opfDir, item.Href))
		data, err := readZipFile(zr, contentPath)
		if err != nil {
			continue
		}

		markdown, err := converter.ConvertString(string(data))
		if err != nil {
			markdown = extractPlainText(string(data))
		}

		mdBuilder.WriteString(markdown)
		mdBuilder.WriteString("\n\n")
	}
	return mdBuilder.String(), nil
}

func isContentFile(mediaType string) bool {
	switch mediaType {
	case "application/xhtml+xml",
		"text/html",
		"application/html+xml",
		"text/xml",
		"application/xml":
		return true
	}
	return false
}

func extractPlainText(html string) string {
	re := regexp.MustCompile(`<[^>]*>`)
	text := re.ReplaceAllString(html, "")
	text = strings.ReplaceAll(text, "&nbsp;", " ")
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&quot;", "\"")
	re = regexp.MustCompile(`\s+`)
	text = re.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

func stripXMLNamespaces(data []byte) []byte {
	re := regexp.MustCompile(`\s+xmlns\s*=\s*"[^"]*"`)
	data = re.ReplaceAll(data, nil)
	re = regexp.MustCompile(`\s+xmlns:[a-zA-Z0-9_.-]+\s*=\s*"[^"]*"`)
	data = re.ReplaceAll(data, nil)
	re = regexp.MustCompile(`<([a-zA-Z_][a-zA-Z0-9_.-]*):([a-zA-Z_][a-zA-Z0-9_.-]*)([/>\s])`)
	data = re.ReplaceAll(data, []byte(`<$2$3`))
	re = regexp.MustCompile(`</([a-zA-Z_][a-zA-Z0-9_.-]*):([a-zA-Z_][a-zA-Z0-9_.-]*)\s*>`)
	data = re.ReplaceAll(data, []byte(`</$2>`))
	re = regexp.MustCompile(`\s+[a-zA-Z_][a-zA-Z0-9_.-]*:([a-zA-Z_][a-zA-Z0-9_.-]*)=`)
	data = re.ReplaceAll(data, []byte(` $1=`))
	return data
}

func readZipFile(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, err
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("%s", GuideNotFound("文件", name, "该文件是 EPUB 文档内部的结构文件，无法通过 Glob/Ls 在文件系统中定位；若文档内部缺少该文件，说明文档已损坏或不是有效的 EPUB，应确认文件未被损坏、扩展名与实际格式一致，若仍失败应告知用户文件无法解析"))
}

// ---------------------------------------------------------------------------
// DOCX → Markdown
// ---------------------------------------------------------------------------

type docxFile struct {
	rels xmlRelationships
	num  xmlNumbering
	list map[string]int
}

type xmlRelationships struct {
	XMLName      xml.Name `xml:"Relationships"`
	Relationship []xmlRel `xml:"Relationship"`
}

type xmlRel struct {
	ID     string `xml:"Id,attr"`
	Type   string `xml:"Type,attr"`
	Target string `xml:"Target,attr"`
}

type xmlNode struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:"-"`
	Content []byte     `xml:",innerxml"`
	Nodes   []xmlNode  `xml:",any"`
}

func (n *xmlNode) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	n.Attrs = start.Attr
	type node xmlNode
	return d.DecodeElement((*node)(n), &start)
}

type xmlNumbering struct {
	XMLName     xml.Name `xml:"numbering"`
	AbstractNum []struct {
		AbstractNumID string      `xml:"abstractNumId,attr"`
		Lvl           []xmlNumLvl `xml:"lvl"`
	} `xml:"abstractNum"`
	Num []struct {
		NumID         string `xml:"numId,attr"`
		AbstractNumID struct {
			Val string `xml:"val,attr"`
		} `xml:"abstractNumId"`
	} `xml:"num"`
}

type xmlNumLvl struct {
	Ilvl  string `xml:"ilvl,attr"`
	Start struct {
		Val string `xml:"val,attr"`
	} `xml:"start"`
	NumFmt struct {
		Val string `xml:"val,attr"`
	} `xml:"numFmt"`
	PPr struct {
		Ind struct {
			Left string `xml:"left,attr"`
		} `xml:"ind"`
	} `xml:"pPr"`
}

func xmlAttr(attrs []xml.Attr, name string) (string, bool) {
	for _, a := range attrs {
		if a.Name.Local == name {
			return a.Value, true
		}
	}
	return "", false
}

func xmlEscape(s, set string) string {
	var replacer []string
	for _, r := range []rune(set) {
		replacer = append(replacer, string(r), `\`+string(r))
	}
	return strings.NewReplacer(replacer...).Replace(s)
}

func docxToMarkdown(r io.Reader) (*docResult, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("读取", "DOCX 文档", err), err)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			"尝试解析 DOCX 文档（zip 容器）结构",
			WithErrDetail("文档解析器在解析 DOCX 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
			"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
		), err)
	}

	var rels xmlRelationships
	var num xmlNumbering
	var docFile *zip.File

	for _, f := range zr.File {
		switch f.Name {
		case "word/_rels/document.xml.rels", "word/_rels/document2.xml.rels":
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
					fmt.Sprintf("尝试读取 DOCX 文档的 %q", f.Name),
					WithErrDetail("文档解析器在解析 DOCX 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
					"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
				), err)
			}
			b, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
					fmt.Sprintf("尝试读取 DOCX 文档的 %q", f.Name),
					WithErrDetail("文档解析器在解析 DOCX 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
					"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
				), err)
			}
			if err := xml.Unmarshal(b, &rels); err != nil {
				return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
					fmt.Sprintf("尝试解析 DOCX 文档的 %q", f.Name),
					WithErrDetail("文档解析器在解析 DOCX 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
					"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
				), err)
			}
		case "word/numbering.xml":
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
					fmt.Sprintf("尝试读取 DOCX 文档的 %q", f.Name),
					WithErrDetail("文档解析器在解析 DOCX 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
					"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
				), err)
			}
			b, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
					fmt.Sprintf("尝试读取 DOCX 文档的 %q", f.Name),
					WithErrDetail("文档解析器在解析 DOCX 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
					"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
				), err)
			}
			if err := xml.Unmarshal(b, &num); err != nil {
				return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
					fmt.Sprintf("尝试解析 DOCX 文档的 %q", f.Name),
					WithErrDetail("文档解析器在解析 DOCX 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
					"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
				), err)
			}
		case "word/document.xml", "word/document2.xml":
			docFile = f
		}
	}

	if docFile == nil {
		return nil, fmt.Errorf("%s", BuildGuide(
			"尝试解析 DOCX 文档内容",
			"文档缺少 word/document.xml，DOCX 结构无效",
			"确认文件未被损坏、扩展名与实际格式一致（DOCX 为 zip 容器，内含 word/document.xml）；若仍失败，应告知用户文件无法解析",
		))
	}

	rc, err := docFile.Open()
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			fmt.Sprintf("尝试读取 DOCX 文档的 %q", docFile.Name),
			WithErrDetail("文档解析器在解析 DOCX 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
			"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
		), err)
	}
	b, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			fmt.Sprintf("尝试读取 DOCX 文档的 %q", docFile.Name),
			WithErrDetail("文档解析器在解析 DOCX 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
			"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
		), err)
	}

	var node xmlNode
	if err := xml.Unmarshal(b, &node); err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			fmt.Sprintf("尝试解析 DOCX 文档的 %q", docFile.Name),
			WithErrDetail("文档解析器在解析 DOCX 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
			"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
		), err)
	}

	var buf bytes.Buffer
	zf := &docxFile{
		rels: rels,
		num:  num,
		list: make(map[string]int),
	}
	if err := zf.walk(&node, &buf); err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			"尝试解析 DOCX 文档的正文内容",
			WithErrDetail("文档解析器在解析 DOCX 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
			"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
		), err)
	}

	return &docResult{content: buf.String()}, nil
}

func (zf *docxFile) walk(node *xmlNode, w io.Writer) error {
	switch node.XMLName.Local {
	case "hyperlink":
		fmt.Fprint(w, "[")
		var cbuf bytes.Buffer
		for i := range node.Nodes {
			if err := zf.walk(&node.Nodes[i], &cbuf); err != nil {
				return err
			}
		}
		fmt.Fprint(w, xmlEscape(cbuf.String(), "[]"))
		fmt.Fprint(w, "](")
		if id, ok := xmlAttr(node.Attrs, "id"); ok {
			for _, rel := range zf.rels.Relationship {
				if id == rel.ID {
					fmt.Fprint(w, xmlEscape(rel.Target, "()"))
					break
				}
			}
		}
		fmt.Fprint(w, ")")
	case "t":
		fmt.Fprint(w, string(node.Content))
	case "pPr":
		code := false
		for i := range node.Nodes {
			switch node.Nodes[i].XMLName.Local {
			case "ind":
				if left, ok := xmlAttr(node.Nodes[i].Attrs, "left"); ok {
					if i, err := strconv.Atoi(left); err == nil && i > 0 {
						fmt.Fprint(w, strings.Repeat("  ", i/360))
					}
				}
			case "pStyle":
				if val, ok := xmlAttr(node.Nodes[i].Attrs, "val"); ok {
					if strings.HasPrefix(val, "Heading") {
						if i, err := strconv.Atoi(val[7:]); err == nil && i > 0 {
							fmt.Fprint(w, strings.Repeat("#", i)+" ")
						}
					} else if val == "Code" {
						code = true
					} else {
						if i, err := strconv.Atoi(val); err == nil && i > 0 {
							fmt.Fprint(w, strings.Repeat("#", i)+" ")
						}
					}
				}
			case "numPr":
				numID := ""
				ilvl := ""
				numFmt := ""
				start := 1
				ind := 0
				for j := range node.Nodes[i].Nodes {
					switch node.Nodes[i].Nodes[j].XMLName.Local {
					case "numId":
						if val, ok := xmlAttr(node.Nodes[i].Nodes[j].Attrs, "val"); ok {
							numID = val
						}
					case "ilvl":
						if val, ok := xmlAttr(node.Nodes[i].Nodes[j].Attrs, "val"); ok {
							ilvl = val
						}
					}
				}
				for _, num := range zf.num.Num {
					if numID != num.NumID {
						continue
					}
					for _, abnum := range zf.num.AbstractNum {
						if abnum.AbstractNumID != num.AbstractNumID.Val {
							continue
						}
						for _, ablvl := range abnum.Lvl {
							if ablvl.Ilvl != ilvl {
								continue
							}
							if i, err := strconv.Atoi(ablvl.Start.Val); err == nil {
								start = i
							}
							if i, err := strconv.Atoi(ablvl.PPr.Ind.Left); err == nil {
								ind = i / 360
							}
							numFmt = ablvl.NumFmt.Val
							break
						}
						break
					}
					break
				}
				fmt.Fprint(w, strings.Repeat("  ", ind))
				switch numFmt {
				case "decimal":
					key := fmt.Sprintf("%s:%d", numID, ind)
					cur, ok := zf.list[key]
					if !ok {
						zf.list[key] = start
					} else {
						zf.list[key] = cur + 1
					}
					fmt.Fprintf(w, "%d. ", zf.list[key])
				case "bullet":
					fmt.Fprint(w, "* ")
				}
			}
		}
		if code {
			fmt.Fprint(w, "`")
		}
		for i := range node.Nodes {
			if err := zf.walk(&node.Nodes[i], w); err != nil {
				return err
			}
		}
		if code {
			fmt.Fprint(w, "`")
		}
	case "tbl":
		var rows [][]string
		for i := range node.Nodes {
			if node.Nodes[i].XMLName.Local != "tr" {
				continue
			}
			var cols []string
			for j := range node.Nodes[i].Nodes {
				if node.Nodes[i].Nodes[j].XMLName.Local != "tc" {
					continue
				}
				var cbuf bytes.Buffer
				if err := zf.walk(&node.Nodes[i].Nodes[j], &cbuf); err != nil {
					return err
				}
				cols = append(cols, strings.Replace(cbuf.String(), "\n", "", -1))
			}
			rows = append(rows, cols)
		}
		if len(rows) == 0 {
			break
		}
		maxcol := 0
		for _, cols := range rows {
			if len(cols) > maxcol {
				maxcol = len(cols)
			}
		}
		widths := make([]int, maxcol)
		for _, row := range rows {
			for i, cell := range row {
				if len(cell) > widths[i] {
					widths[i] = len(cell)
				}
			}
		}
		for ri, row := range rows {
			if ri == 0 {
				for j := 0; j < maxcol; j++ {
					fmt.Fprint(w, "|")
					fmt.Fprint(w, strings.Repeat(" ", widths[j]))
				}
				fmt.Fprint(w, "|\n")
				for j := 0; j < maxcol; j++ {
					fmt.Fprint(w, "|")
					fmt.Fprint(w, strings.Repeat("-", widths[j]))
				}
				fmt.Fprint(w, "|\n")
			}
			for j := 0; j < maxcol; j++ {
				fmt.Fprint(w, "|")
				if j < len(row) {
					fmt.Fprint(w, xmlEscape(row[j], "|"))
					fmt.Fprint(w, strings.Repeat(" ", widths[j]-len(row[j])))
				} else {
					fmt.Fprint(w, strings.Repeat(" ", widths[j]))
				}
			}
			fmt.Fprint(w, "|\n")
		}
		fmt.Fprint(w, "\n")
	case "r":
		bold := false
		italic := false
		strike := false
		for i := range node.Nodes {
			if node.Nodes[i].XMLName.Local != "rPr" {
				continue
			}
			for j := range node.Nodes[i].Nodes {
				switch node.Nodes[i].Nodes[j].XMLName.Local {
				case "b":
					bold = true
				case "i":
					italic = true
				case "strike":
					strike = true
				}
			}
		}
		if strike {
			fmt.Fprint(w, "~~")
		}
		if bold {
			fmt.Fprint(w, "**")
		}
		if italic {
			fmt.Fprint(w, "*")
		}
		var cbuf bytes.Buffer
		for i := range node.Nodes {
			if err := zf.walk(&node.Nodes[i], &cbuf); err != nil {
				return err
			}
		}
		fmt.Fprint(w, xmlEscape(cbuf.String(), `*~\`))
		if italic {
			fmt.Fprint(w, "*")
		}
		if bold {
			fmt.Fprint(w, "**")
		}
		if strike {
			fmt.Fprint(w, "~~")
		}
	case "p":
		for i := range node.Nodes {
			if err := zf.walk(&node.Nodes[i], w); err != nil {
				return err
			}
		}
		fmt.Fprintln(w)
	case "blip":
	case "Fallback":
	case "txbxContent":
		var cbuf bytes.Buffer
		for i := range node.Nodes {
			if err := zf.walk(&node.Nodes[i], &cbuf); err != nil {
				return err
			}
		}
		fmt.Fprintln(w, "\n```\n"+cbuf.String()+"```")
	default:
		for i := range node.Nodes {
			if err := zf.walk(&node.Nodes[i], w); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// XLSX → Markdown tables
// ---------------------------------------------------------------------------

func xlsxToMarkdown(r io.Reader) (*docResult, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", GuideFileError("读取", "XLSX 文档", err), err)
	}

	xlFile, err := xlsx.OpenBinary(data)
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			"尝试解析 XLSX 文档结构",
			WithErrDetail("文档解析器在解析 XLSX 文档时失败（文件可能已损坏或格式与扩展名不符）", err),
			"确认文件未被损坏、扩展名与实际格式一致；若仍失败，应告知用户文件无法解析",
		), err)
	}

	var mdBuilder strings.Builder

	for i, sheet := range xlFile.Sheets {
		if i > 0 {
			mdBuilder.WriteString("\n---\n\n")
		}
		mdBuilder.WriteString(fmt.Sprintf("## Sheet %d: %s\n\n", i+1, sheet.Name))

		if len(sheet.Rows) == 0 {
			continue
		}

		maxCol := 0
		for _, row := range sheet.Rows {
			if len(row.Cells) > maxCol {
				maxCol = len(row.Cells)
			}
		}
		if maxCol == 0 {
			continue
		}

		for ri, row := range sheet.Rows {
			rowData := make([]string, maxCol)
			hasContent := false
			for ci := 0; ci < maxCol; ci++ {
				if ci < len(row.Cells) {
					val := strings.TrimSpace(row.Cells[ci].Value)
					rowData[ci] = val
					if val != "" {
						hasContent = true
					}
				}
			}
			if !hasContent {
				continue
			}

			escaped := make([]string, maxCol)
			for ci, cell := range rowData {
				escaped[ci] = xlsxEscapeCell(cell)
			}
			mdBuilder.WriteString("| " + strings.Join(escaped, " | ") + " |\n")

			if ri == 0 {
				seps := make([]string, maxCol)
				for k := range seps {
					seps[k] = "---"
				}
				mdBuilder.WriteString("| " + strings.Join(seps, " | ") + " |\n")
			}
		}
		mdBuilder.WriteString("\n")
	}

	return &docResult{content: mdBuilder.String()}, nil
}

func xlsxEscapeCell(text string) string {
	text = strings.ReplaceAll(text, "|", "\\|")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\r", "")
	return text
}

// ---------------------------------------------------------------------------
// Shared text helpers
// ---------------------------------------------------------------------------

// textToMarkdown 将提取的纯文本整理为更干净的 Markdown 格式。
func textToMarkdown(text string) string {
	lines := strings.Split(text, "\n")
	var result strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			result.WriteString("\n")
		} else {
			result.WriteString(trimmed + "\n")
		}
	}
	return result.String() + "\n"
}
