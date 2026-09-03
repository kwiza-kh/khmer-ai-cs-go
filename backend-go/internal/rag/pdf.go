package rag

import (
	"bytes"
	"strings"

	"github.com/ledongthuc/pdf"
)

// pdfTextFromBytes extracts concatenated plain text from all pages.
func pdfTextFromBytes(data []byte) (string, error) {
	reader := bytes.NewReader(data)
	r, err := pdf.NewReader(reader, int64(len(data)))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		text, err := p.GetPlainText(nil)
		if err != nil {
			continue
		}
		b.WriteString(text)
		b.WriteByte('\n')
	}
	return b.String(), nil
}
