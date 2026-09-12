"use client";

import { useCallback, useEffect, useState } from "react";
import {
  getTelegramNotify, putTelegramNotify, postTelegramNotifyTest,
  postTelegramNotifyLink,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Loader2, Send, Bot, ExternalLink, AlertTriangle } from "lucide-react";
import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";

export function TelegramNotifyCard() {
  const { t, tf } = useI18n();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [linking, setLinking] = useState(false);
  const [configured, setConfigured] = useState(false);
  const [chatTitle, setChatTitle] = useState("");
  const [notifyMessages, setNotifyMessages] = useState(true);
  const [notifyHandoff, setNotifyHandoff] = useState(true);
  const [notifyAnnouncements, setNotifyAnnouncements] = useState(true);
  // Whether this deployment has a platform bot at all. Without one there is
  // nothing to link to, and offering the button anyway would just produce a
  // failure the merchant cannot act on.
  const [platformReady, setPlatformReady] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const cfg = await getTelegramNotify();
      setConfigured(cfg.configured);
      setChatTitle(cfg.chat_title || "");
      setNotifyMessages(cfg.notify_messages);
      setNotifyHandoff(cfg.notify_handoff);
      setNotifyAnnouncements(cfg.notify_announcements !== false);
      setPlatformReady(cfg.platform_bot_ready !== false);
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
        notify_messages: notifyMessages,
        notify_handoff: notifyHandoff,
        notify_announcements: notifyAnnouncements,
      });
      toast.success(t("settings.tgSaved"));
      await load();
    } catch (err: unknown) {
      toast.error((err as Error).message || t("settings.tgSaveFailed"));
    } finally {
      setSaving(false);
    }
  };

  // One-tap binding through the platform bot. Opens in a new tab so the
  // dashboard stays put — on mobile t.me hands off to the Telegram app.
  const connectTelegram = async () => {
    setLinking(true);
    try {
      const res = await postTelegramNotifyLink();
      window.open(res.link, "_blank", "noopener,noreferrer");
      toast.success(t("settings.tgLinkOpened"));
    } catch (err: unknown) {
      toast.error((err as Error).message || t("settings.tgSaveFailed"));
    } finally {
      setLinking(false);
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

        {!platformReady ? (
          <div className="flex items-start gap-2 rounded-lg border border-border bg-muted/40 p-3">
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
            <p className="text-xs leading-5 text-muted-foreground">{t("settings.tgUnavailable")}</p>
          </div>
        ) : loading ? (
          <Loader2 className="size-4 animate-spin text-muted-foreground" />
        ) : !configured ? (
          <>
            <Button className="h-9 w-full gap-1.5" onClick={connectTelegram} disabled={linking}>
              {linking ? <Loader2 className="size-3.5 animate-spin" /> : <Send className="size-3.5" />}
              {t("settings.tgConnect")}
            </Button>
            <p className="text-[11px] leading-4 text-muted-foreground">{t("settings.tgConnectHint")}</p>
            <p className="text-xs text-muted-foreground">{t("settings.tgNotConnected")}</p>
          </>
        ) : (
          <>
            <div className="flex items-center justify-between gap-2 rounded-lg border border-border p-2.5">
              <div className="min-w-0">
                <p className="truncate text-xs font-medium">
                  {tf("settings.tgLinkedTo", { title: chatTitle || "Telegram" })}
                </p>
              </div>
              <Button
                size="sm"
                variant="outline"
                className="h-8 shrink-0 gap-1.5"
                onClick={connectTelegram}
                disabled={linking}
              >
                {linking ? <Loader2 className="size-3.5 animate-spin" /> : <ExternalLink className="size-3.5" />}
                {t("settings.tgReconnect")}
              </Button>
            </div>

            <div className="space-y-1.5 text-sm">
              <label className="flex cursor-pointer items-center gap-2">
                <input type="checkbox" checked={notifyMessages} onChange={(e) => setNotifyMessages(e.target.checked)} className="size-4 accent-primary" />
                {t("settings.tgNotifyMessages")}
              </label>
              <label className="flex cursor-pointer items-center gap-2">
                <input type="checkbox" checked={notifyHandoff} onChange={(e) => setNotifyHandoff(e.target.checked)} className="size-4 accent-primary" />
                {t("settings.tgNotifyHandoff")}
              </label>
              <label className="flex cursor-pointer items-center gap-2">
                <input type="checkbox" checked={notifyAnnouncements} onChange={(e) => setNotifyAnnouncements(e.target.checked)} className="size-4 accent-primary" />
                {t("settings.tgNotifyAnnouncements")}
              </label>
            </div>

            <div className="flex gap-2">
              <Button size="sm" onClick={save} disabled={saving} className="gap-1.5">
                {saving ? <Loader2 className="size-3.5 animate-spin" /> : null}
                {t("common.save")}
              </Button>
              <Button size="sm" variant="outline" onClick={sendTest} disabled={testing} className="gap-1.5">
                {testing ? <Loader2 className="size-3.5 animate-spin" /> : <Send className="size-3.5" />}
                {t("settings.tgTest")}
              </Button>
            </div>
            <p className="text-[11px] leading-4 text-muted-foreground">{t("settings.tgUnlinkHint")}</p>
          </>
        )}
      </CardContent>
    </Card>
  );
}
