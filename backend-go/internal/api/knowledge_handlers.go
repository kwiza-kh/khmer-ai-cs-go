package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/rag"
	"khmer-ai-cs-go/internal/usage"
)

// ============================================
// F9 — document quota (upload guard)
// ============================================

// Plan names come from internal/usage: the message gate in the pipeline and this
// document gate must agree on what "enterprise" means.
const (
	planFree       = usage.PlanFree
	planPro        = usage.PlanPro
	planEnterprise = usage.PlanEnterprise
)

// consumeDocQuota moved to consume_doc_quota.go during the security-audit
// remediation (atomic quota gate; enterprise unmetered).

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
	if err := requirePermission(user, PermKnowledgeEdit); err != nil {
		return nil, err
	}
	var req knowledgeUploadRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Content) == "" {
		return nil, ErrBadRequest("请求格式错误")
	}
	if err := consumeDocQuota(r.Context(), a.DB, user.Tenant()); err != nil {
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
	return a.RAG.UploadDocument(r.Context(), user.Tenant(), strings.TrimSpace(req.Title), req.Content, language, category, tags, nil)
}

// UploadKnowledgeFile — multipart file upload (file/category/language fields).
func (a *App) uploadKnowledgeFile(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	if err := requirePermission(user, PermKnowledgeEdit); err != nil {
		return nil, err
	}
	// Hard cap the whole request before any parsing/reading (DoS guard);
	// the effective file limit stays MaxUploadBytes, checked after parse.
	r.Body = http.MaxBytesReader(w, r.Body, 40<<20)
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
		return nil, ErrBadRequest(fmt.Sprintf("不支持的文件类型,允许: .txt .md .csv .pdf .docx .jpg .jpeg .png .webp (got: %s)", filename))
	}
	if len(data) > rag.MaxUploadBytes {
		return nil, ErrBadRequest(fmt.Sprintf("文件超过 %d MB 上限", rag.MaxUploadBytes>>20))
	}
	text, err := a.extractKnowledgeText(r.Context(), filename, data)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, ErrBadRequest("文档处理失败: 无可提取的文本内容")
	}

	if err := consumeDocQuota(r.Context(), a.DB, user.Tenant()); err != nil {
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
	doc, err := a.RAG.UploadDocument(r.Context(), user.Tenant(), title, text, lang, category, []string{}, nil)
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

// extractKnowledgeText picks the extraction path for one KB upload. Images go
// straight to vision OCR. PDFs use the native extractor first and fall back to
// OCR when the result is unusable (Khmer fonts without a ToUnicode map come
// out as replacement characters or nothing at all); if the fallback also
// fails, whatever the native extractor produced is kept.
func (a *App) extractKnowledgeText(ctx context.Context, filename string, data []byte) (string, error) {
	if rag.IsImage(filename) {
		ocr, err := a.Gemini.ExtractDocumentText(ctx, data, rag.ImageMimeType(filename))
		if err != nil {
			return "", ErrServiceUnavailable("图片识别失败: " + err.Error())
		}
		return ocr, nil
	}
	native, err := rag.ExtractText(filename, data)
	isPDF := strings.ToLower(filepath.Ext(filename)) == ".pdf"
	if err != nil {
		// A scanned PDF surfaces as "no extractable text" from the native
		// extractor; that error is the OCR trigger, not a rejection.
		if isPDF {
			ocr, oerr := a.Gemini.ExtractDocumentText(ctx, data, "application/pdf")
			if oerr == nil {
				return ocr, nil
			}
			return "", ErrServiceUnavailable("PDF 提取失败且 OCR 回退失败: " + oerr.Error())
		}
		return "", ErrBadRequest("文档处理失败: " + err.Error())
	}
	if isPDF && rag.LooksLikeBadExtraction(native) {
		ocr, oerr := a.Gemini.ExtractDocumentText(ctx, data, "application/pdf")
		if oerr == nil {
			return ocr, nil
		}
		if strings.TrimSpace(native) == "" {
			return "", ErrServiceUnavailable("PDF 无可提取文本，OCR 回退失败: " + oerr.Error())
		}
		if a.Logger == nil {
		} else {
			a.Logger.Warn("pdf OCR fallback failed; keeping native text", "file", filename, "error", oerr.Error())
		}
	}
	return native, nil
}

// IngestKnowledgeURL — SSRF-guarded page fetch + text ingest.
func (a *App) ingestKnowledgeURL(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	if err := requirePermission(user, PermKnowledgeEdit); err != nil {
		return nil, err
	}
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
	if err := consumeDocQuota(r.Context(), a.DB, user.Tenant()); err != nil {
		return nil, err
	}
	sourceURL := req.URL
	a.Logger.Info("url ingest: success", "url", req.URL)
	return a.RAG.UploadDocument(r.Context(), user.Tenant(), title, text, language, category, []string{}, &sourceURL)
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
	if err := requirePermission(user, PermKnowledgeView); err != nil {
		return nil, err
	}
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
	return a.RAG.ListDocuments(r.Context(), user.Tenant(), page, pageSize)
}

func (a *App) getKnowledgeDocument(w http.ResponseWriter, r *http.Request, docID int32) (any, error) {
	user, _ := UserFrom(r)
	if err := requirePermission(user, PermKnowledgeView); err != nil {
		return nil, err
	}
	return a.RAG.GetDocument(r.Context(), user.Tenant(), docID)
}

func (a *App) updateKnowledgeDocument(w http.ResponseWriter, r *http.Request, docID int32) (any, error) {
	user, _ := UserFrom(r)
	if err := requirePermission(user, PermKnowledgeEdit); err != nil {
		return nil, err
	}
	var req updateKnowledgeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	content := ""
	if req.Content != nil {
		content = *req.Content
	}
	return a.RAG.UpdateDocument(r.Context(), user.Tenant(), docID, content, req.Title, req.Language, req.Category, req.Tags)
}

func (a *App) deleteKnowledge(w http.ResponseWriter, r *http.Request, docID int32) (any, error) {
	user, _ := UserFrom(r)
	if err := requirePermission(user, PermKnowledgeEdit); err != nil {
		return nil, err
	}
	if err := a.RAG.DeleteDocument(r.Context(), user.Tenant(), docID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, ErrNotFound(err.Error())
		}
		return nil, ErrInternal(err.Error())
	}
	return map[string]string{"message": "已删除"}, nil
}

func (a *App) retryKnowledge(w http.ResponseWriter, r *http.Request, docID int32) (any, error) {
	user, _ := UserFrom(r)
	if err := requirePermission(user, PermKnowledgeEdit); err != nil {
		return nil, err
	}
	if err := a.RAG.RetryDocument(r.Context(), user.Tenant(), docID); err != nil {
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
	if err := requirePermission(user, PermKnowledgeView); err != nil {
		return nil, err
	}
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
	ctx := a.RAG.Ground(r.Context(), user.Tenant(), nil, req.Query, language, nil, topK)
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
		a.Logger.Error("rag query generation failed", "tenant_id", user.Tenant(), "actor_user_id", user.UserID, "error", err.Error())
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

// ============================================
// Knowledge quality & gap drafting
// ============================================

// knowledgeDocQuality — per-document usage and 👍/👎 outcome stats, so
// operators can spot documents that get cited often but rated poorly.
func (a *App) knowledgeDocQuality(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	if err := requirePermission(user, PermKnowledgeView); err != nil {
		return nil, err
	}
	rows, err := a.DB.Query(r.Context(), `
		WITH cited AS (
			SELECT (s->>'doc_id')::int AS doc_id, COUNT(*)::bigint AS uses
			FROM chat_messages cm
			JOIN sessions ses ON ses.session_id = cm.session_id
			CROSS JOIN LATERAL jsonb_array_elements(cm.sources_json::jsonb) AS s
			WHERE ses.user_id = $1 AND cm.role = 'model' AND cm.sources_json IS NOT NULL
			  AND cm.created_at >= NOW() - INTERVAL '90 days'
			GROUP BY 1
		), rated AS (
			SELECT (s->>'doc_id')::int AS doc_id,
			       COUNT(*) FILTER (WHERE cm.feedback_rating = 1)::bigint  AS up,
			       COUNT(*) FILTER (WHERE cm.feedback_rating = -1)::bigint AS down
			FROM chat_messages cm
			JOIN sessions ses ON ses.session_id = cm.session_id
			CROSS JOIN LATERAL jsonb_array_elements(cm.sources_json::jsonb) AS s
			WHERE ses.user_id = $1 AND cm.role = 'model' AND cm.sources_json IS NOT NULL
			  AND cm.feedback_rating IS NOT NULL
			  AND cm.created_at >= NOW() - INTERVAL '90 days'
			GROUP BY 1
		)
		SELECT kd.doc_id, kd.title, COALESCE(u.uses, 0), COALESCE(rt.up, 0), COALESCE(rt.down, 0), kd.index_status::text
		FROM knowledge_documents kd
		LEFT JOIN cited u ON u.doc_id = kd.doc_id
		LEFT JOIN rated rt ON rt.doc_id = kd.doc_id
		WHERE kd.uploaded_by = $1
		ORDER BY COALESCE(rt.down, 0) DESC, COALESCE(u.uses, 0) DESC
		LIMIT 50`, user.Tenant())
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var docID int32
		var title string
		var uses, up, down int64
		var status string
		if err := rows.Scan(&docID, &title, &uses, &up, &down, &status); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"doc_id": docID, "title": title, "uses": uses,
			"thumbs_up": up, "thumbs_down": down, "index_status": status,
		})
	}
	return map[string]any{"data": out}, rows.Err()
}

// knowledgeGapDraft — draft a KB article for a customer query the knowledge
// base could not answer. The draft returns to the operator for review and is
// published through the normal editor flow; it is never saved automatically.
func (a *App) knowledgeGapDraft(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	// Drafting costs model spend and exists to feed the knowledge base, so it
	// follows the edit permission rather than the view one.
	if err := requirePermission(user, PermKnowledgeEdit); err != nil {
		return nil, err
	}
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	query := strings.TrimSpace(req.Query)
	if query == "" || len([]rune(query)) > 300 {
		return nil, ErrBadRequest("query 不能为空且不超过 300 字")
	}
	lang := gemini.DetectLanguage(query)
	if lang == "" {
		lang = "en"
	}
	languageName := map[string]string{"km": "Khmer", "zh": "Simplified Chinese", "en": "English"}[lang]
	prompt := "Draft a concise customer-support knowledge-base article that directly answers the " +
		"customer question below. Write in " + languageName + ". Output Markdown: the FIRST line must be " +
		"\"# <short title>\", followed by the article body. State facts only — never invent prices, " +
		"policies or deadlines; where a fact is unknown write a placeholder like [待确认]. Max 200 words.\n\n" +
		"Customer question: " + query
	draft, ok := a.Gemini.GenerateFast(r.Context(), prompt, 20*time.Second)
	if !ok || strings.TrimSpace(draft) == "" {
		return nil, &ApiError{http.StatusBadGateway, "草稿生成失败，请稍后重试"}
	}
	draft = strings.TrimSpace(gemini.StripSourceMarkers(draft))
	title := query
	if body, found := strings.CutPrefix(draft, "# "); found {
		if parts := strings.SplitN(body, "\n", 2); len(parts) == 2 {
			title = strings.TrimSpace(parts[0])
			draft = strings.TrimSpace(parts[1])
		}
	}
	return map[string]any{"title": title, "content": draft, "language": lang}, nil
}

// knowledgeGaps — the tenant's own no-hit queries (KB growth candidates).
func (a *App) knowledgeGaps(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	if err := requirePermission(user, PermKnowledgeView); err != nil {
		return nil, err
	}
	days := parseIntOr(r.URL.Query().Get("days"), 14)
	gaps, err := a.RAG.KnowledgeGaps(r.Context(), user.Tenant(), int64(days))
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	return map[string]any{"data": gaps}, nil
}

// ============================================
// Contradiction review queue + compile toggle
// ============================================

// listContradictions — ingest-time KB conflicts awaiting human review.
func (a *App) listContradictions(w http.ResponseWriter, r *http.Request) (any, error) {
	user, _ := UserFrom(r)
	if err := requirePermission(user, PermKnowledgeView); err != nil {
		return nil, err
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	rows, err := a.DB.Query(r.Context(),
		"SELECT c.contradiction_id, c.new_doc_id, nd.title, c.old_doc_id, od.title, c.items, c.status, c.created_at "+
			"FROM kb_contradictions c "+
			"JOIN knowledge_documents nd ON nd.doc_id = c.new_doc_id "+
			"LEFT JOIN knowledge_documents od ON od.doc_id = c.old_doc_id "+
			"WHERE c.user_id = $1 AND c.status = $2 ORDER BY c.created_at DESC LIMIT 50",
		user.Tenant(), status)
	if err != nil {
		return nil, ErrInternal("查询失败")
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var newDocID int32
		var newTitle string
		var oldDocID *int32
		var oldTitle *string
		var items []byte
		var st string
		var createdAt time.Time
		if err := rows.Scan(&id, &newDocID, &newTitle, &oldDocID, &oldTitle, &items, &st, &createdAt); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id": id, "new_doc_id": newDocID, "new_title": newTitle,
			"old_doc_id": oldDocID, "old_title": oldTitle,
			"items": json.RawMessage(items), "status": st, "created_at": createdAt,
		})
	}
	// knowledgeDocQuality above already returns rows.Err() the same way: the
	// caller cannot distinguish a complete list from one that stopped early.
	return map[string]any{"data": out}, rows.Err()
}

func (a *App) setContradictionStatus(w http.ResponseWriter, r *http.Request, id int32, status string) (any, error) {
	user, _ := UserFrom(r)
	if err := requirePermission(user, PermKnowledgeEdit); err != nil {
		return nil, err
	}
	if status != "resolved" && status != "dismissed" {
		return nil, ErrBadRequest("无效状态")
	}
	// resolved_at is inlined (not a parameter): a bare $1 appearing only in a
	// CASE comparison cannot be type-resolved by Postgres.
	resolvedAt := "resolved_at"
	if status == "resolved" {
		resolvedAt = "NOW()"
	}
	tag, err := a.DB.Exec(r.Context(),
		"UPDATE kb_contradictions SET status = $1::varchar, resolved_at = "+resolvedAt+
			" WHERE contradiction_id = $2 AND user_id = $3",
		status, id, user.Tenant())
	if err != nil {
		return nil, ErrInternal("更新失败")
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound("不存在")
	}
	return map[string]string{"message": "已更新"}, nil
}

func (a *App) resolveContradiction(w http.ResponseWriter, r *http.Request, id int32) (any, error) {
	return a.setContradictionStatus(w, r, id, "resolved")
}

func (a *App) dismissContradiction(w http.ResponseWriter, r *http.Request, id int32) (any, error) {
	return a.setContradictionStatus(w, r, id, "dismissed")
}

// getRagSettings / putRagSettings — admin toggle for the ingest-time compile.
func (a *App) getRagSettings(w http.ResponseWriter, r *http.Request) (any, error) {
	return map[string]any{"compile_enabled": a.RAG.CompileEnabledPublic(r.Context())}, nil
}

func (a *App) putRagSettings(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		CompileEnabled *bool `json:"compile_enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		return nil, ErrBadRequest("请求格式错误")
	}
	if req.CompileEnabled == nil {
		return nil, ErrBadRequest("无更新字段")
	}
	if err := a.RAG.SetCompileEnabled(r.Context(), *req.CompileEnabled); err != nil {
		return nil, ErrInternal("保存失败")
	}
	return map[string]any{"compile_enabled": *req.CompileEnabled}, nil
}
