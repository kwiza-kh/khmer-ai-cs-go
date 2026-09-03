package rag

import (
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// scanDocumentSummary scans the SUMMARY_COLS row into a JSON-friendly map.
func scanDocumentSummary(rows pgx.Rows) (map[string]any, error) {
	var (
		docID       int32
		title       string
		language    string
		category    *string
		chunkCount  int32
		source      string
		indexStatus string
		indexError  *string
		createdAt   time.Time
		updatedAt   time.Time
	)
	if err := rows.Scan(&docID, &title, &language, &category, &chunkCount, &source, &indexStatus, &indexError, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	return map[string]any{
		"doc_id":       docID,
		"title":        title,
		"language":     language,
		"category":     category,
		"chunk_count":  chunkCount,
		"source":       source,
		"index_status": indexStatus,
		"index_error":  indexError,
		"created_at":   createdAt,
		"updated_at":   updatedAt,
	}, nil
}

// scanDocumentRow scans a single RETURNING summary row.
func scanDocumentRow(row pgx.Row) (map[string]any, error) {
	var (
		docID       int32
		title       string
		language    string
		category    *string
		chunkCount  int32
		source      string
		indexStatus string
		indexError  *string
		createdAt   time.Time
		updatedAt   time.Time
	)
	if err := row.Scan(&docID, &title, &language, &category, &chunkCount, &source, &indexStatus, &indexError, &createdAt, &updatedAt); err != nil {
		return nil, fmt.Errorf("扫描文档行失败: %w", err)
	}
	return map[string]any{
		"doc_id":       docID,
		"title":        title,
		"language":     language,
		"category":     category,
		"chunk_count":  chunkCount,
		"source":       source,
		"index_status": indexStatus,
		"index_error":  indexError,
		"created_at":   createdAt,
		"updated_at":   updatedAt,
	}, nil
}

// scanDocumentFull scans the FULL_COLS row (content + tags for editing).
func scanDocumentFull(row pgx.Row) (map[string]any, error) {
	var (
		docID       int32
		title       string
		content     string
		language    string
		category    *string
		tags        []string
		chunkCount  int32
		source      string
		indexStatus string
		indexError  *string
		createdAt   time.Time
		updatedAt   time.Time
	)
	if err := row.Scan(&docID, &title, &content, &language, &category, &tags, &chunkCount, &source, &indexStatus, &indexError, &createdAt, &updatedAt); err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("document not found")
		}
		return nil, fmt.Errorf("扫描文档行失败: %w", err)
	}
	return map[string]any{
		"doc_id":       docID,
		"title":        title,
		"content":      content,
		"language":     language,
		"category":     category,
		"tags":         tags,
		"chunk_count":  chunkCount,
		"source":       source,
		"index_status": indexStatus,
		"index_error":  indexError,
		"created_at":   createdAt,
		"updated_at":   updatedAt,
	}, nil
}
