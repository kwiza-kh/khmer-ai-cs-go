"use client";

import * as React from "react";
import { Bot, Loader2, RotateCcw, Send, Sparkles, User } from "lucide-react";
import { toast } from "sonner";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { RAGSource, sendTestMessage } from "@/lib/api";

type TestMessage = {
  id: number;
  role: "user" | "model";
  content: string;
  sources?: RAGSource[];
  pending?: boolean;
  usedMock?: boolean;
  elapsedMs?: number;
};

export default function AiTestPage() {
  const [sessionId, setSessionId] = React.useState<string>();
  const [messages, setMessages] = React.useState<TestMessage[]>([]);
  const [draft, setDraft] = React.useState("");
  const [sending, setSending] = React.useState(false);
  const nextId = React.useRef(0);
  const scrollRef = React.useRef<HTMLDivElement>(null);

  React.useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [messages]);

  const send = async () => {
    const content = draft.trim();
    if (!content || sending) return;

    const pendingId = nextId.current++;
    const t0 = Date.now();
    setDraft("");
    setSending(true);
    setMessages((cur) => [
      ...cur,
      { id: nextId.current++, role: "user", content },
      { id: pendingId, role: "model", content: "", pending: true },
    ]);

    try {
      const res = await sendTestMessage(content, sessionId);
      setSessionId(res.session_id);
      setMessages((cur) =>
        cur.map((m) =>
          m.id === pendingId
            ? {
                ...m,
                content: res.reply,
                sources: res.sources,
                usedMock: res.used_mock,
                elapsedMs: Date.now() - t0,
                pending: false,
              }
            : m
        )
      );
    } catch (error) {
      setMessages((cur) => cur.filter((m) => m.id !== pendingId));
      toast.error((error as Error).message || "发送失败");
    } finally {
      setSending(false);
    }
  };

  const startNew = () => {
    setSessionId(undefined);
    setMessages([]);
    setDraft("");
  };

  return (
    <div className="flex h-full flex-col">
      <PageHeader
        icon={Sparkles}
        kicker="AI Testing"
        title="AI 测试"
        description="发任意消息, AI 走真实管线 (RAG 知识检索 + Gemini) 直接回复。不会转人工, 也不会触达任何客户。"
        actions={
          <Button variant="outline" onClick={startNew} disabled={sending} className="gap-2">
            <RotateCcw className="size-4" />
            新测试
          </Button>
        }
      />

      <div className="flex min-h-0 flex-1 flex-col">
        <div ref={scrollRef} className="min-h-0 flex-1 overflow-y-auto">
          <div className="mx-auto max-w-3xl space-y-3 p-5 sm:p-6">
            {messages.length === 0 && (
              <Card className="border-dashed">
                <CardContent className="flex flex-col items-center gap-2 py-14 text-center text-muted-foreground">
                  <Sparkles className="size-8 opacity-40" />
                  <p className="text-sm">在下方输入任意消息开始测试 — 客户常问的问题、闲聊、超纲问题都可以。</p>
                </CardContent>
              </Card>
            )}

            {messages.map((m) =>
              m.role === "user" ? (
                <div key={m.id} className="flex items-start justify-end gap-2.5">
                  <div className="max-w-[80%] rounded-lg rounded-br-sm bg-primary/10 px-3.5 py-2.5 text-sm">
                    {m.content}
                  </div>
                  <div className="mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full bg-muted">
                    <User className="size-3.5" />
                  </div>
                </div>
              ) : (
                <div key={m.id} className="flex items-start gap-2.5">
                  <div className="mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full bg-primary/10">
                    <Bot className="size-3.5 text-primary" />
                  </div>
                  <div className="min-w-0 max-w-[85%] space-y-1.5">
                    {m.pending ? (
                      <div className="flex items-center gap-2 rounded-lg rounded-bl-sm bg-muted px-3.5 py-2.5 text-sm text-muted-foreground">
                        <Loader2 className="size-3.5 animate-spin" /> AI 思考中…
                      </div>
                    ) : (
                      <>
                        <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                          <Badge variant="outline" className="h-4 px-1 text-[10px]">
                            AI 回复
                          </Badge>
                          {m.usedMock && (
                            <Badge variant="destructive" className="h-4 px-1 text-[10px]">
                              MOCK (未配置 Gemini Key)
                            </Badge>
                          )}
                          {m.elapsedMs != null && <span>{m.elapsedMs} ms</span>}
                        </div>
                        <div className="whitespace-pre-wrap rounded-lg rounded-bl-sm bg-muted px-3.5 py-2.5 text-sm">
                          {m.content}
                        </div>
                        {m.sources && m.sources.length > 0 && (
                          <details className="text-xs text-muted-foreground">
                            <summary className="cursor-pointer select-none hover:text-foreground">
                              命中 {m.sources.length} 条知识片段
                            </summary>
                            <ul className="mt-1 space-y-1 pl-4">
                              {m.sources.map((s) => (
                                <li key={s.doc_id} className="list-disc">
                                  {s.title} <span className="opacity-60">(score {s.score.toFixed(2)})</span>
                                </li>
                              ))}
                            </ul>
                          </details>
                        )}
                      </>
                    )}
                  </div>
                </div>
              )
            )}
          </div>
        </div>

        <div className="border-t border-border/70 bg-background px-5 py-3 sm:px-6">
          <div className="mx-auto flex max-w-3xl items-center gap-2">
            <Input
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.nativeEvent.isComposing) send();
              }}
              placeholder="输入要测试的消息…"
              disabled={sending}
            />
            <Button size="icon" onClick={send} disabled={sending || !draft.trim()}>
              {sending ? <Loader2 className="size-4 animate-spin" /> : <Send className="size-4" />}
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}
