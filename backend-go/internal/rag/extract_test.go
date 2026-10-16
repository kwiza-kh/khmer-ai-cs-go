package rag

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

// ooxmlZip builds an in-memory zip package for the extractor tests: every
// fixture is a minimal but structurally real OOXML part set. Keys are zip
// member paths ("xl/worksheets/sheet2.xml"), values the part XML.
func ooxmlZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestIsImage(t *testing.T) {
	for _, f := range []string{"a.JPG", "b.jpeg", "c.png", "d.webp"} {
		if IsImage(f) == false {
			t.Fatalf("%s should be an image", f)
		}
	}
	for _, f := range []string{"a.pdf", "b.txt", "c.docx", "d.csv"} {
		if IsImage(f) {
			t.Fatalf("%s should not be an image", f)
		}
	}
}

func TestImageMimeType(t *testing.T) {
	if got := ImageMimeType("x.PNG"); got != "image/png" {
		t.Fatalf("mime = %q, want image/png", got)
	}
}

func TestLooksLikeBadExtraction(t *testing.T) {
	if LooksLikeBadExtraction("") == false {
		t.Fatal("empty text must be flagged")
	}
	if LooksLikeBadExtraction("abc") == false {
		t.Fatal("very short text must be flagged")
	}
	replacementHeavy := ""
	for i := 0; i < 60; i++ {
		replacementHeavy += "\uFFFD"
	}
	if LooksLikeBadExtraction(replacementHeavy) == false {
		t.Fatal("replacement-character soup must be flagged")
	}
	good := "ផលិតផលនេះមានតម្លៃ 189 ដុល្លារ និងមានការធានា 12 ខែ សម្រាប់គ្រួសារ"
	if LooksLikeBadExtraction(good) {
		t.Fatal("healthy Khmer text must not be flagged")
	}
}

func TestAcceptedExtensionsIncludeImages(t *testing.T) {
	acc := AcceptedExtensions()
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
		if _, ok := acc[ext]; ok == false {
			t.Fatalf("%s missing from AcceptedExtensions", ext)
		}
	}
}

func TestAcceptedExtensionsIncludeOfficeAndWPS(t *testing.T) {
	acc := AcceptedExtensions()
	for _, ext := range []string{".xlsx", ".wps", ".et"} {
		if _, ok := acc[ext]; ok == false {
			t.Fatalf("%s missing from AcceptedExtensions", ext)
		}
	}
	for _, ext := range []string{".pptx", ".dps"} {
		if _, ok := acc[ext]; ok {
			t.Fatalf("%s must not be accepted (removed 2026-10-16)", ext)
		}
	}
}

// xlsxFixture: shared strings (including a rich-text run), a literal number, an
// inline string, and two sheets whose part numbers must sort naturally
// (sheet2 before sheet10 — string order would flip them).
func xlsxFixture(t *testing.T) []byte {
	t.Helper()
	return ooxmlZip(t, map[string]string{
		"xl/sharedStrings.xml": `<?xml version="1.0"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <si><t>产品名称</t></si>
  <si><r><t>价格</t></r><r><t>表</t></r></si>
</sst>`,
		"xl/worksheets/sheet2.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
  <row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>
  <row r="2"><c r="A2"><v>42</v></c><c r="B2" t="inlineStr"><is><t>内联文本</t></is></c></row>
</sheetData></worksheet>`,
		"xl/worksheets/sheet10.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
  <row r="1"><c r="A1" t="inlineStr"><is><t>SHEET10</t></is></c></row>
</sheetData></worksheet>`,
	})
}

func TestExtractXlsx(t *testing.T) {
	text, err := ExtractText("价格表.xlsx", xlsxFixture(t))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !strings.Contains(text, "产品名称\t价格表") {
		t.Fatalf("shared strings (incl. rich text) not resolved: %q", text)
	}
	if !strings.Contains(text, "42\t内联文本") {
		t.Fatalf("literal/inline cells missing: %q", text)
	}
	if strings.Index(text, "产品名称") > strings.Index(text, "SHEET10") {
		t.Fatalf("sheet2 must extract before sheet10, got: %q", text)
	}
}

// The .wps/.et containers of modern WPS are the docx/xlsx layouts — the
// shared extractors must read both. A genuinely old binary .wps must fail with
// the re-save hint instead of indexing nothing.
func TestExtractWPSContainers(t *testing.T) {
	docxLayout := ooxmlZip(t, map[string]string{
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:t>WPS文字内容</w:t></w:p></w:body></w:document>`,
	})
	if text, err := ExtractText("协议.wps", docxLayout); err != nil || !strings.Contains(text, "WPS文字内容") {
		t.Fatalf(".wps (docx layout): err=%v text=%q", err, text)
	}
	if text, err := ExtractText("报价单.et", xlsxFixture(t)); err != nil || !strings.Contains(text, "产品名称") {
		t.Fatalf(".et (xlsx layout): err=%v text=%q", err, text)
	}

	_, err := ExtractText("老文件.wps", []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1})
	if err == nil || !strings.Contains(err.Error(), "另存为 .docx") {
		t.Fatalf("legacy binary .wps must carry the re-save hint, got: %v", err)
	}
}
