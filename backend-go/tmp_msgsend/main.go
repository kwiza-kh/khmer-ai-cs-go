package main

// Throwaway Messenger wire-shape check (2026-10-16).
//
// Verifies, without messaging any real person:
//   - the sealed Page token still decrypts and is accepted by Graph,
//   - the Page is reachable and which app/fields are subscribed to it,
//   - the NEW attachment body (message.attachment.type/payload.url) and the OLD
//     top-level type/image body differ in how Graph validates them.
//
// It sends only to a deliberately non-existent recipient id ("1"), so nothing is
// delivered to any user. Secrets arrive as file paths and are never printed.
//
// usage: go run ./tmp_msgsend <key-file> <sealed-token-file> <pageid-file> <image-url>

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/security"
)

func firstLine(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	line := strings.SplitN(string(raw), "\n", 2)[0]
	return strings.TrimSpace(line)
}

func looksLikePlaceholderKey(encoded string) bool {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(encoded))
		if err != nil {
			return false
		}
	}
	k := string(raw)
	for _, pat := range []string{"0123456789abcdef", "0123456789", "changeme", "test", "secret", "password"} {
		if strings.HasPrefix(strings.ToLower(k), pat) {
			return true
		}
	}
	if len(k) > 0 && strings.Count(k, string(k[0])) == len(k) {
		return true // all one character
	}
	return false
}

func redact(body, secret string) string {
	if secret == "" {
		return body
	}
	return strings.ReplaceAll(body, secret, "<redacted>")
}

func main() {
	keyFile, sealedFile, pageFile, imageURL := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	encodedKey := firstLine(keyFile)
	sealed := firstLine(sealedFile)
	pageID := firstLine(pageFile)

	fmt.Printf("key: %d chars, base64 → looks like a PLACEHOLDER/default key: %v\n",
		len(encodedKey), looksLikePlaceholderKey(encodedKey))

	sealer, err := security.NewSealer(encodedKey)
	if err != nil {
		fmt.Println("sealer error:", err)
		return
	}
	token, err := sealer.Decrypt(sealed)
	if err != nil {
		fmt.Println("decrypt error:", err)
		return
	}
	fmt.Printf("token: decrypted, %d chars, prefix-is-EAA: %v\n", len(token), strings.HasPrefix(token, "EAA"))

	client := &http.Client{Timeout: 30 * time.Second}
	get := func(path string) {
		resp, err := client.Get("https://graph.facebook.com/v21.0" + path + "?access_token=" + url.QueryEscape(token))
		if err != nil {
			fmt.Println(path, "→ request error:", err)
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 900))
		fmt.Printf("%s → %d %s\n", path, resp.StatusCode, redact(strings.TrimSpace(string(body)), token))
	}
	post := func(label, path string, payload map[string]any) {
		blob, _ := json.Marshal(payload)
		resp, err := client.Post("https://graph.facebook.com/v21.0"+path+"?access_token="+url.QueryEscape(token),
			"application/json", bytes.NewReader(blob))
		if err != nil {
			fmt.Printf("%s → request error: %v\n", label, err)
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 900))
		fmt.Printf("%s → %d %s\n", label, resp.StatusCode, redact(strings.TrimSpace(string(body)), token))
	}

	get("/me?fields=id,name")
	get("/" + pageID + "?fields=id,name,username")
	get("/" + pageID + "/subscribed_apps")

	// Bogus recipient: nothing is delivered; only parameter validation runs.
	post("NEW attachment shape (recipient 1)", "/"+pageID+"/messages", map[string]any{
		"recipient": map[string]any{"id": "1"},
		"message": map[string]any{
			"attachment": map[string]any{
				"type":    "image",
				"payload": map[string]any{"url": imageURL},
			},
		},
	})
	post("OLD top-level shape  (recipient 1)", "/"+pageID+"/messages", map[string]any{
		"recipient": map[string]any{"id": "1"},
		"type":      "image",
		"image":     map[string]any{"link": imageURL},
	})

	// The attachment-upload endpoint validates the attachment object AND fetches
	// the URL server-side, without delivering any message — the clean way to test
	// the shape and our R2 reachability at once.
	post("ATTACH-UPLOAD new attachment shape", "/"+pageID+"/message_attachments", map[string]any{
		"message": map[string]any{
			"attachment": map[string]any{
				"type":    "image",
				"payload": map[string]any{"url": imageURL, "is_reusable": true},
			},
		},
	})
	post("ATTACH-UPLOAD old top-level shape", "/"+pageID+"/message_attachments", map[string]any{
		"type":  "image",
		"image": map[string]any{"link": imageURL},
	})
}
