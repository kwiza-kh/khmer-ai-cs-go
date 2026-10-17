package platform

import (
	"context"
	"fmt"
	"strings"
	"time"

	"khmer-ai-cs-go/internal/textutil"
)

// historyWindow is how many messages travel with every turn (see loadHistory).
// Anything older is invisible to the model unless session_memory carries it.
const historyWindow = 20

// memoryRefreshEvery is how much new material must accumulate before the memory
// note is rewritten. Regenerating on every message would spend a model call per
// turn to add "ok" and "thanks"; waiting for ten messages keeps the note current
// at roughly one call per conversation.
const memoryRefreshEvery = 10

// loadSessionMemory returns the assistant's memory note for this session, or ""
// when the thread is short enough that the history window already holds it. The
// note covers the part of the conversation older than historyWindow.
func (p *Pipeline) loadSessionMemory(ctx context.Context, sessionID string) string {
	if sessionID == "" {
		return ""
	}
	var total int
	if err := p.DB.QueryRow(ctx,
		"SELECT COUNT(*) FROM chat_messages WHERE session_id = $1 AND role IN ('user','model','agent')",
		sessionID).Scan(&total); err != nil || total <= historyWindow {
		return ""
	}
	var summary *string
	if err := p.DB.QueryRow(ctx,
		"SELECT memory_summary FROM sessions WHERE session_id = $1", sessionID).Scan(&summary); err != nil {
		return ""
	}
	return strings.TrimSpace(textutil.DerefString(summary))
}

// refreshSessionMemoryAsync updates the note off the customer's path.
//
// The note only matters to a LATER turn, so nothing here may delay this one. It
// rides the critical background lane (never dropped, bounded) because a lost
// refresh costs a slightly staler note, never an answer.
func (p *Pipeline) refreshSessionMemoryAsync(userID int32, sessionID string) {
	if sessionID == "" {
		return
	}
	SpawnCritical(func() { p.refreshSessionMemory(userID, sessionID) })
}

// refreshSessionMemory folds the messages the note has not seen into it.
//
// Incremental on purpose: re-summarising the whole thread would keep describing
// its beginning and grow without bound, while "existing note + new messages"
// stays one bounded call regardless of conversation length.
func (p *Pipeline) refreshSessionMemory(userID int32, sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var previous *string
	var summaryAt *time.Time
	var covered int
	if err := p.DB.QueryRow(ctx,
		"SELECT memory_summary, memory_summary_at, memory_summary_count FROM sessions WHERE session_id = $1 AND user_id = $2",
		sessionID, userID).Scan(&previous, &summaryAt, &covered); err != nil {
		return
	}
	var total int
	if err := p.DB.QueryRow(ctx,
		"SELECT COUNT(*) FROM chat_messages WHERE session_id = $1 AND role IN ('user','model','agent')",
		sessionID).Scan(&total); err != nil {
		return
	}
	// Nothing to remember yet, or nothing new worth a call.
	if total <= historyWindow || total < covered+memoryRefreshEvery {
		return
	}

	// The messages the note has not covered yet, capped so one long absence does
	// not turn into a 500-message prompt.
	rows, err := p.DB.Query(ctx,
		"SELECT role, content FROM chat_messages WHERE session_id = $1 AND role IN ('user','model','agent') "+
			"AND ($2::timestamptz IS NULL OR created_at > $2) ORDER BY message_id ASC LIMIT 60",
		sessionID, summaryAt)
	if err != nil {
		return
	}
	defer rows.Close()
	var transcript strings.Builder
	n := 0
	for rows.Next() {
		var role, content string
		if rows.Scan(&role, &content) != nil {
			continue
		}
		transcript.WriteString(role)
		transcript.WriteString(": ")
		transcript.WriteString(content)
		transcript.WriteString("\n")
		n++
	}
	if err := rows.Err(); err != nil || n == 0 {
		return
	}

	out, ok := p.Gemini.GenerateFastMax(ctx, memoryPrompt(textutil.DerefString(previous), transcript.String()), 45*time.Second, 700)
	if !ok {
		return
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return
	}
	if _, err := p.DB.Exec(ctx,
		"UPDATE sessions SET memory_summary = $1, memory_summary_at = NOW(), memory_summary_count = $2 "+
			"WHERE session_id = $3 AND user_id = $4",
		out, total, sessionID, userID); err != nil {
		if p.Logger != nil {
			p.Logger.Warn("session memory update failed", "session_id", sessionID, "error", err.Error())
		}
		return
	}
	if p.Logger != nil {
		p.Logger.Info("session memory refreshed", "session_id", sessionID, "covered", total)
	}
}

// memoryPrompt asks for the assistant's own note — deliberately not the
// merchant-facing summary prompt (internal/api/inbox_handlers.go). That one
// answers "what happened in this conversation" for the owner; this one answers
// "what must I not forget while answering", and the two must not overwrite each
// other: the owner's digest is in their language and gets rewritten by their
// panel, while memory follows the customer's language and the thread's growth.
func memoryPrompt(previous, transcript string) string {
	var b strings.Builder
	b.WriteString("Update the memory note for an AI assistant that will continue this customer conversation.\n")
	b.WriteString("Keep only: facts the customer stated about themselves or the order (quantities, dates, addresses, constraints), ")
	b.WriteString("commitments the assistant already made, and questions still open. ")
	b.WriteString("Merge the existing note with the new messages and drop anything the new messages resolve. ")
	b.WriteString("Write in the language of the conversation, at most 6 short bullet points, no preamble.\n\n")
	if prev := strings.TrimSpace(previous); prev != "" {
		b.WriteString("Existing note:\n")
		b.WriteString(prev)
		b.WriteString("\n\n")
	}
	b.WriteString("New messages:\n")
	b.WriteString(transcript)
	return b.String()
}

// sessionMemoryLine renders the note for injection ahead of the customer's turn.
// A labelled block, because the model must not mistake it for the customer
// speaking now.
func sessionMemoryLine(memory string) string {
	return fmt.Sprintf("【记忆：更早的对话要点 / memory of earlier turns】\n%s", strings.TrimSpace(memory))
}
