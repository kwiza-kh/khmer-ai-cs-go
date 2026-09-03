package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/rag"
)

// ============================================
// F9 — document quota (upload guard)
// ============================================

const (
	planFree       = "free"
	planPro        = "pro"
	planEnterprise = "enterprise"
)

func consumeDocQuota(ctx context.Context, db *pgxpool.Pool, userID int32) error {
	if _, err := db.Exec(ctx,
		"INSERT INTO tenant_billing (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING", userID); err != nil {
		return ErrInternal("billing init")
	}
	if _, err := db.Exec(ctx,
		"UPDATE tenant_billing SET messages_used = 0, docs_used = 0, cycle_start = NOW(), cycle_end = NOW() + INTERVAL '30 days' WHERE user_id = $1 AND cycle_end <= NOW()",
		userID); err != nil {
		return ErrInternal("billing rollover")
	}
	var plan string
	var used, quota int64
	err := db.QueryRow(ctx,
		"SELECT plan, docs_used::bigint, monthly_doc_quota::bigint FROM tenant_billing WHERE user_id = $1", userID).
		Scan(&plan, &used, &quota)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil
		}
		return ErrInternal("billing lookup")
	}
	if plan != planEnterprise && used >= quota {
		return &ApiError{http.StatusPaymentRequired, "月度文档配额已用尽，请升级套餐"}
	}
	if _, err := db.Exec(ctx, "UPDATE tenant_billing SET docs_used = docs_used + 1 WHERE user_id = $1", userID); err != nil {
		return ErrInternal("billing increment")
	}
	return nil
}

// ============================================
// Handlers
// ============================================

type knowledgeUploadRequest struct {
	Title    string   `json:"title"`
	Content  string   `json:"content"`
	Language *string  `json:"language"`
	Category *string  `json:"category"`
	Tags     []string `json:"tags"`
}

type ingestURLRequest struct {
	URL      string  `json:"url"`
	Title    *string `json:"title"`
	Language *string `json:"language"`
	Category *string `json:"category"`
}

type updateKnowledgeRequest struct {
	Content  *string  `json:"content"`
	Title    *string  `json:"title"`
	Language *string  `json:"language"`
	Category *string  `json:"category"`
	Tags     []string `json:"tags"`
}

type ragQueryRequest struct {
	Query    string  `json:"query"`
	Language *string `json:"language"`
	TopK     *int64  `json:"top_k"`
}

// UploadKnowledge — paste-text document creation (quota enforced).
func (a *App) uploadKnowledge(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req knowledgeUploadRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Content) == "" {
		return nil, ErrBadRequest("请求格式错误")
	}
	if err := consumeDocQuota(r.Context(), a.DB, user.UserID); err != nil {
		return nil, err
	}
	language := "km"
	if req.Language != nil {
		language = *req.Language
	}
	category := ""
	if req.Category != nil {
		category = *req.Category
	}
	tags := req.Tags
	if tags == nil {
		tags = []string{}
	}
	return a.RAG.UploadDocument(r.Context(), user.UserID, strings.TrimSpace(req.Title), req.Content, language, category, tags, nil)
}

// UploadKnowledgeFile — multipart file upload (file/category/language fields).
func (a *App) uploadKnowledgeFile(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return nil, ErrBadRequest(fmt.Sprintf("multipart error: %v", err))
	}
	filename := ""
	var data []byte
	category := ""
	language := ""
	if r.MultipartForm != nil {
		if files, ok := r.MultipartForm.File["file"]; ok && len(files) > 0 {
			fh := files[0]
			filename = fh.Filename
			f, err := fh.Open()
			if err != nil {
				return nil, ErrBadRequest("读取文件失败")
			}
			defer f.Close()
			data = make([]byte, 0, fh.Size)
			buf := make([]byte, 64*1024)
			for {
				n, err := f.Read(buf)
				if n > 0 {
					data = append(data, buf[:n]...)
				}
				if err != nil {
					break
				}
			}
		}
		category = r.FormValue("category")
		language = r.FormValue("language")
	}

	if filename == "" || len(data) == 0 {
		return nil, ErrBadRequest("缺少或无效的文件 (field name: file)")
	}
	if !rag.IsAccepted(filename) {
		return nil, ErrBadRequest(fmt.Sprintf("不支持的文件类型,允许: .txt .md .csv .pdf .docx (got: %s)", filename))
	}
	text, err := rag.ExtractText(filename, data)
	if err != nil {
		return nil, ErrBadRequest("文档处理失败: " + err.Error())
	}
	if strings.TrimSpace(text) == "" {
		return nil, ErrBadRequest("文档处理失败: 无可提取的文本内容")
	}

	if err := consumeDocQuota(r.Context(), a.DB, user.UserID); err != nil {
		return nil, err
	}
	title := strings.TrimSuffix(filename, filepath.Ext(filename))
	if title == "" {
		title = filename
	}
	lang := language
	if lang == "" {
		lang = "km"
	}
	doc, err := a.RAG.UploadDocument(r.Context(), user.UserID, title, text, lang, category, []string{}, nil)
	if err != nil {
		return nil, err
	}
	// Mark source = upload (Go parity).
	if docID, ok := doc["doc_id"].(int32); ok {
		_, _ = a.DB.Exec(r.Context(), "UPDATE knowledge_documents SET source = 'upload' WHERE doc_id = $1", docID)
		doc["source"] = "upload"
	}
	return doc, nil
}

// IngestKnowledgeURL — SSRF-guarded page fetch + text ingest.
func (a *App) ingestKnowledgeURL(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req ingestURLRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	pageTitle, text, err := rag.FetchURLContent(r.Context(), req.URL)
	if err != nil {
		return nil, ErrBadRequest(err.Error())
	}
	if strings.TrimSpace(text) == "" {
		return nil, &ApiError{http.StatusUnprocessableEntity, "页面无可提取的正文文本 (可能是 JS 渲染的单页应用, 请改用粘贴文本)"}
	}
	title := ""
	if req.Title != nil && strings.TrimSpace(*req.Title) != "" {
		title = *req.Title
	} else if strings.TrimSpace(pageTitle) != "" {
		title = strings.TrimSpace(pageTitle)
	} else {
		title = req.URL
	}
	language := "km"
	if req.Language != nil {
		language = *req.Language
	}
	category := ""
	if req.Category != nil {
		category = *req.Category
	}
	if err := consumeDocQuota(r.Context(), a.DB, user.UserID); err != nil {
		return nil, err
	}
	sourceURL := req.URL
	a.Logger.Info("url ingest: success", "url", req.URL)
	return a.RAG.UploadDocument(r.Context(), user.UserID, title, text, language, category, []string{}, &sourceURL)
}

// AcceptedFileTypes — extension list + size cap for the UI.
func (a *App) acceptedFileTypes(w http.ResponseWriter, r *http.Request) (any, error) {
	return map[string]any{
		"extensions":    rag.AcceptedExtensions(),
		"max_bytes":     rag.MaxUploadBytes,
		"max_megabytes": rag.MaxUploadBytes / (1024 * 1024),
	}, nil
}

// ListKnowledge — paginated summaries.
func (a *App) listKnowledge(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	q := r.URL.Query()
	page := int64(1)
	if v := q.Get("page"); v != "" {
		if n, err := parseInt64(v); err == nil {
			page = n
		}
	}
	pageSize := int64(20)
	if v := q.Get("page_size"); v != "" {
		if n, err := parseInt64(v); err == nil {
			pageSize = n
		}
	}
	return a.RAG.ListDocuments(r.Context(), user.UserID, page, pageSize)
}

func (a *App) getKnowledgeDocument(w http.ResponseWriter, r *http.Request, docID int32) (any, error) {
	user, _ := UserFrom(r)
	return a.RAG.GetDocument(r.Context(), user.UserID, docID)
}

func (a *App) updateKnowledgeDocument(w http.ResponseWriter, r *http.Request, docID int32) (any, error) {
	user, _ := UserFrom(r)
	var req updateKnowledgeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	content := ""
	if req.Content != nil {
		content = *req.Content
	}
	return a.RAG.UpdateDocument(r.Context(), user.UserID, docID, content, req.Title, req.Language, req.Category, req.Tags)
}

func (a *App) deleteKnowledge(w http.ResponseWriter, r *http.Request, docID int32) (any, error) {
	user, _ := UserFrom(r)
	if err := a.RAG.DeleteDocument(r.Context(), user.UserID, docID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, ErrNotFound(err.Error())
		}
		return nil, ErrInternal(err.Error())
	}
	return map[string]string{"message": "已删除"}, nil
}

func (a *App) retryKnowledge(w http.ResponseWriter, r *http.Request, docID int32) (any, error) {
	user, _ := UserFrom(r)
	if err := a.RAG.RetryDocument(r.Context(), user.UserID, docID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, ErrNotFound(err.Error())
		}
		return nil, ErrInternal(err.Error())
	}
	return map[string]string{"message": "已重新加入索引队列"}, nil
}

// RagQuery — standalone RAG Q&A over the tenant's knowledge base.
func (a *App) ragQuery(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	var req ragQueryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if strings.TrimSpace(req.Query) == "" {
		return nil, ErrBadRequest("请求格式错误")
	}
	language := "km"
	if req.Language != nil {
		language = *req.Language
	}
	topK := int64(0)
	if req.TopK != nil {
		topK = *req.TopK
	}
	ctx := a.RAG.Ground(r.Context(), user.UserID, nil, req.Query, language, nil, topK)
	if !ctx.HasMatch {
		return map[string]any{
			"query":   req.Query,
			"answer":  rag.NoMatchReply(language),
			"sources": nil,
			"tokens":  0,
		}, nil
	}
	augmented := ctx.ContextStr + "\n---\n📝 User question: " + req.Query +
		"\n\nAnswer based ONLY on the knowledge base above. If the answer is not contained there, " +
		"say you don't know and offer to escalate to a human agent. Reply in the user's language."
	result, err := a.Gemini.Chat(r.Context(), augmented, nil, language)
	if err != nil {
		return nil, ErrInternal("生成回答失败")
	}
	answer := result.Reply
	if !result.UsedMock {
		answer = stripSourceMarkers(answer)
	}
	return map[string]any{
		"query":   req.Query,
		"answer":  answer,
		"sources": ctx.Sources,
		"tokens":  result.PromptTokens + result.OutputTokens,
	}, nil
}

func parseInt64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n, err
}
