"use client";

import { useCallback, useEffect, useState } from "react";
import {
  getTelegramNotify, putTelegramNotify, postTelegramNotifyTest,
  postTelegramNotifyUpdates, type TelegramChat,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Loader2, Send, Bot } from "lucide-react";
import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";

export function TelegramNotifyCard() {
  const { t } = useI18n();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [pulling, setPulling] = useState(false);
  const [configured, setConfigured] = useState(false);
  const [botToken, setBotToken] = useState("");
  const [chatId, setChatId] = useState("");
  const [chatTitle, setChatTitle] = useState("");
  const [notifyMessages, setNotifyMessages] = useState(true);
  const [notifyHandoff, setNotifyHandoff] = useState(true);
  const [chats, setChats] = useState<TelegramChat[]>([]);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const cfg = await getTelegramNotify();
      setConfigured(cfg.configured);
      setChatId(cfg.chat_id || "");
      setChatTitle(cfg.chat_title || "");
      setNotifyMessages(cfg.notify_messages);
      setNotifyHandoff(cfg.notify_handoff);
    } catch {
      toast.error(t("settings.tgLoadFailed"));
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    const timer = window.setTimeout(() => { void load(); }, 0);
    return () => window.clearTimeout(timer);
  }, [load]);

  const save = async () => {
    setSaving(true);
    try {
      await putTelegramNotify({
        bot_token: botToken || undefined,
        chat_id: chatId || undefined,
        chat_title: chatTitle || undefined,
        notify_messages: notifyMessages,
        notify_handoff: notifyHandoff,
      });
      toast.success(t("settings.tgSaved"));
      setConfigured(true);
      setBotToken("");
      await load();
    } catch (err: unknown) {
      toast.error((err as Error).message || t("settings.tgSaveFailed"));
    } finally {
      setSaving(false);
    }
  };

  const pullChats = async () => {
    setPulling(true);
    try {
      const res = await postTelegramNotifyUpdates(botToken || undefined);
      setChats(res.chats || []);
      if ((res.chats || []).length === 0) toast.error(t("settings.tgNoChats"));
    } catch (err: unknown) {
      toast.error((err as Error).message || t("settings.tgPullFailed"));
    } finally {
      setPulling(false);
    }
  };

  const sendTest = async () => {
    setTesting(true);
    try {
      await postTelegramNotifyTest();
      toast.success(t("settings.tgTestSent"));
    } catch (err: unknown) {
      toast.error((err as Error).message || t("settings.tgTestFailed"));
    } finally {
      setTesting(false);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-sm">
          <Bot className="size-4" /> {t("settings.tgTitle")}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <p className="text-xs leading-5 text-muted-foreground">{t("settings.tgDesc")}</p>
        {loading ? (
          <Loader2 className="size-4 animate-spin text-muted-foreground" />
        ) : (
          <>
            <div className="space-y-1">
              <label className="text-xs font-medium">{t("settings.tgToken")}</label>
              <Input
                value={botToken}
                onChange={(e) => setBotToken(e.target.value)}
                placeholder={configured ? t("settings.tgTokenSaved") : "123456:ABC-DEF..."}
                className="h-9 text-sm"
                autoComplete="off"
              />
            </div>
            <div className="flex items-end gap-2">
              <div className="flex-1 space-y-1">
                <label className="text-xs font-medium">{t("settings.tgChatId")}</label>
                <Input value={chatId} onChange={(e) => setChatId(e.target.value)} placeholder="123456789" inputMode="numeric" className="h-9 text-sm" />
              </div>
              <Button variant="outline" className="h-9 gap-1.5" onClick={pullChats} disabled={pulling || (!botToken && !configured)}>
                {pulling ? <Loader2 className="size-3.5 animate-spin" /> : <Bot className="size-3.5" />}
                {t("settings.tgGetChats")}
              </Button>
            </div>
            {chats.length > 0 && (
              <div className="space-y-1 rounded-lg border border-border p-2">
                {chats.map((chat) => (
                  <button
                    key={chat.id}
                    type="button"
                    onClick={() => { setChatId(chat.id); setChatTitle(chat.title); }}
                    className={`block w-full rounded-md px-2 py-1.5 text-left text-xs hover:bg-muted ${chat.id === chatId ? "bg-primary/10" : ""}`}
                  >
                    <span className="font-medium">{chat.title}</span>
                    <span className="ml-2 text-muted-foreground">{chat.id}</span>
                  </button>
                ))}
              </div>
            )}
            <div className="space-y-1.5 text-sm">
              <label className="flex cursor-pointer items-center gap-2">
                <input type="checkbox" checked={notifyMessages} onChange={(e) => setNotifyMessages(e.target.checked)} className="size-4 accent-primary" />
                {t("settings.tgNotifyMessages")}
              </label>
              <label className="flex cursor-pointer items-center gap-2">
                <input type="checkbox" checked={notifyHandoff} onChange={(e) => setNotifyHandoff(e.target.checked)} className="size-4 accent-primary" />
                {t("settings.tgNotifyHandoff")}
              </label>
            </div>
            <div className="flex gap-2">
              <Button size="sm" onClick={save} disabled={saving || (!botToken && !configured)} className="gap-1.5">
                {saving ? <Loader2 className="size-3.5 animate-spin" /> : null}
                {t("common.save")}
              </Button>
              <Button size="sm" variant="outline" onClick={sendTest} disabled={testing || !configured} className="gap-1.5">
                {testing ? <Loader2 className="size-3.5 animate-spin" /> : <Send className="size-3.5" />}
                {t("settings.tgTest")}
              </Button>
            </div>
            {configured && <p className="text-xs text-success">{t("settings.tgConfigured")} {chatTitle}</p>}
          </>
        )}
      </CardContent>
    </Card>
  );
}
