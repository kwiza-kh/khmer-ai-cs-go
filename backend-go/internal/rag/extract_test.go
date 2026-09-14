package rag

import "testing"

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
