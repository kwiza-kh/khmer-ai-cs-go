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
import { useI18n } from "@/lib/i18n";

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
  const { t, tf } = useI18n();
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
      toast.error((error as Error).message || t("aitest.sendFailed"));
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
        kicker={t("aitest.kicker")}
        title={t("aitest.title")}
        description={t("aitest.description")}
        actions={
          <Button variant="outline" onClick={startNew} disabled={sending} className="gap-2">
            <RotateCcw className="size-4" />
            {t("aitest.newTest")}
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
                  <p className="text-sm">{t("aitest.emptyHint")}</p>
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
                        <Loader2 className="size-3.5 animate-spin" /> {t("aitest.thinking")}
                      </div>
                    ) : (
                      <>
                        <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                          <Badge variant="outline" className="h-4 px-1 text-[11px]">
                            {t("aitest.aiReply")}
                          </Badge>
                          {m.usedMock && (
                            <Badge variant="destructive" className="h-4 px-1 text-[11px]">
                              {t("aitest.mockBadge")}
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
                              {tf("aitest.sourcesHit", { n: m.sources.length })}
                            </summary>
                            <ul className="mt-1 space-y-1 pl-4">
                              {m.sources.map((s) => (
                                <li key={s.doc_id} className="list-disc">
                                  {s.title} <span className="opacity-60">({tf("aitest.score", { score: s.score.toFixed(2) })})</span>
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
              placeholder={t("aitest.placeholder")}
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
