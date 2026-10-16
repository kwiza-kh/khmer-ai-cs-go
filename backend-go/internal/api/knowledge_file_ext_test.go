package api

import "testing"

// uploadFileExt feeds knowledge_documents.file_ext, which the document library
// renders as the per-type icon/badge. The normalisation is load-bearing: the
// DB value must be lowercase and dot-free for the frontend switch to match
// ("PDF" and ".pdf" would both fall through to the generic icon).
func TestUploadFileExt(t *testing.T) {
	cases := []struct{ name, want string }{
		{"report.PDF", "pdf"},
		{"sheet.xlsx", "xlsx"},
		{"photo.JPEG", "jpeg"},
		{"archive.tar.gz", "gz"},
		{"no-extension", ""},
		{"dot.", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := uploadFileExt(tc.name); got != tc.want {
			t.Errorf("uploadFileExt(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}
