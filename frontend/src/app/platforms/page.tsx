"use client";

import * as React from "react";
import useSWR, { mutate as globalMutate } from "swr";
import { apiFetch, ApiError } from "@/lib/api";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { PageHeader } from "@/components/page-header";
import { Globe, MessageCircle, ExternalLink, CheckCircle2, Loader2, Link2, Camera, Plus, RefreshCw, Unplug, CircleAlert, Radio, Clock3, Inbox, Send, RotateCcw, type LucideIcon } from "lucide-react";
import { toast } from "sonner";

type PlatformKey = "meta" | "instagram" | "telegram" | "whatsapp" | "line";

interface PlatformConfig {
  config_id?: number;
  platform: PlatformKey;
  access_token?: string;
  page_id?: string;
  instagram_business_id?: string;
  whatsapp_business_account_id?: string;
  bot_token?: string;
  webhook_secret?: string;
  is_active?: boolean;
  health?: PlatformHealth;
  pending_inbound_events?: number;
  failed_inbound_events?: number;
  pending_deliveries?: number;
  failed_deliveries?: number;
  cancelled_deliveries?: number;
  accepted_deliveries?: number;
  delivered_deliveries?: number;
  read_deliveries?: number;
  last_inbound_at?: string;
}

interface PlatformHealth {
  status: "unknown" | "connected" | "error";
  account_name?: string;
  detail?: string;
  checked_at?: string;
}

interface PlatformMeta {
  icon: LucideIcon;
  tone: "info" | "warning" | "default" | "success";
  name: string;
  label: PlatformKey;
  desc: string;
  fields: { label: string; key: keyof PlatformConfig; placeholder: string; type?: string }[];
  docsUrl: string;
  webhookInfo: string;
  inbound: string[];
  outbound: string[];
  receipts: string;
  limitation: string;
}

interface MetaOAuthPage {
  page_id: string;
  page_name: string;
  instagram_business_id?: string;
  instagram_name?: string;
}

interface MetaOAuthSession {
  session_id: string;
  pages: MetaOAuthPage[];
  expires_at: string;
}

interface PlatformInboundEvent {
  event_id: number;
  content: string;
  attempts: number;
  last_error?: string;
  created_at: string;
}

interface PlatformDelivery {
  delivery_id: number;
  content: string;
  recipient_id: string;
  attempts: number;
  last_error?: string;
  created_at: string;
}

interface PlatformWorkResponse {
  inbound_events: PlatformInboundEvent[];
  deliveries: PlatformDelivery[];
  cancelled_deliveries: PlatformDelivery[];
}

interface WhatsAppEmbeddedSignupConfig {
  app_id: string;
  config_id: string;
}

interface FacebookSDK {
  init: (options: { appId: string; cookie: boolean; xfbml: boolean; version: string }) => void;
  login: (callback: (response: { authResponse?: { code?: string } }) => void, options: Record<string, unknown>) => void;
}

declare global {
  interface Window {
    FB?: FacebookSDK;
  }
}

const PLATFORMS: PlatformMeta[] = [
  {
    icon: Globe,
    tone: "info",
    name: "Facebook Messenger",
    label: "meta",
    inbound: ["Text", "Quick-reply selections", "Postbacks", "Private media"],
    outbound: ["Text", "Image, video, audio, document", "Quick replies"],
    receipts: "Accepted, delivered, read",
    limitation: "Customer media is copied to private tenant storage. The 24-hour customer-service window applies.",
    desc: "Facebook Messenger via Meta Graph API",
    fields: [
      { label: "Page ID", key: "page_id", placeholder: "123456789" },
      { label: "Access Token", key: "access_token", placeholder: "EAA...", type: "password" },
      { label: "App Secret (manual setup only)", key: "webhook_secret", placeholder: "your_meta_app_secret", type: "password" },
    ],
    docsUrl: "https://developers.facebook.com/docs/messenger-platform",
    webhookInfo: "Use Meta authorization above for automatic Page subscription. Manual setup: /api/v1/webhook/meta",
  },
  {
    icon: Globe,
    tone: "warning",
    name: "Instagram Messaging",
    label: "instagram",
    inbound: ["Text", "Quick-reply selections", "Postbacks", "Private media"],
    outbound: ["Text", "Image and video"],
    receipts: "Accepted, delivered, read",
    limitation: "Outbound buttons are not available. Customer media is copied to private tenant storage.",
    desc: "Instagram Business Direct Messages",
    fields: [
      { label: "Business Account ID", key: "instagram_business_id", placeholder: "178414..." },
      { label: "Access Token", key: "access_token", placeholder: "EAA...", type: "password" },
      { label: "App Secret (manual setup only)", key: "webhook_secret", placeholder: "your_meta_app_secret", type: "password" },
    ],
    docsUrl: "https://developers.facebook.com/docs/messenger-platform",
    webhookInfo: "Use Meta authorization above for automatic Instagram subscription",
  },
  {
    icon: MessageCircle,
    tone: "info",
    name: "Telegram Bot",
    label: "telegram",
    inbound: ["Private chat only", "Button callbacks", "Private media"],
    outbound: ["Text", "Image, video, audio, document", "Inline buttons"],
    receipts: "Bot API accepted",
    limitation: "Telegram Bot API does not provide customer delivery or read receipts. Group chats are ignored.",
    desc: "Automated messaging via Bot API",
    fields: [
      { label: "Bot Token", key: "bot_token", placeholder: "123456:ABC-DEF...", type: "password" },
      { label: "Webhook Secret Token", key: "webhook_secret", placeholder: "telegram_webhook_secret", type: "password" },
    ],
    docsUrl: "https://core.telegram.org/bots/api",
    webhookInfo: "Webhook is registered automatically after save and verify",
  },
  {
    icon: MessageCircle,
    tone: "success" as const,
    name: "WhatsApp Business",
    label: "whatsapp",
    inbound: ["Text", "Button and list selections", "Private media"],
    outbound: ["Text inside the 24-hour window", "Image, video, audio, document", "Reply buttons", "Approved templates outside the window"],
    receipts: "Accepted, delivered, read, failed",
    limitation: "Approved templates are managed in Meta Business Manager. Attachments are retained in private tenant storage.",
    desc: "WhatsApp Cloud API with manual business-number setup",
    fields: [
      // WhatsApp Cloud API: Phone Number ID lives in the page_id slot (overloaded
      // backend-side; see platforms.go registerPlatformClient).
      { label: "Phone Number ID", key: "page_id", placeholder: "108123456789012" },
      { label: "Access Token", key: "access_token", placeholder: "EAAG...", type: "password" },
      { label: "App Secret (manual setup only)", key: "webhook_secret", placeholder: "your_meta_app_secret", type: "password" },
    ],
    docsUrl: "https://developers.facebook.com/docs/whatsapp/cloud-api",
    webhookInfo: "Webhook: /api/v1/webhook/whatsapp  ·  Connection becomes live after a signed event arrives",
  },
  {
    icon: MessageCircle,
    tone: "success",
    name: "LINE Official Account",
    label: "line",
    inbound: ["Private text messages"],
    outbound: ["Text via Push API"],
    receipts: "API accepted",
    limitation: "Only one-to-one text conversations are handled. Group, room, and media events are ignored.",
    desc: "LINE Messaging API for customer conversations",
    fields: [
      { label: "Channel User ID", key: "page_id", placeholder: "U1234567890abcdef..." },
      { label: "Channel Access Token", key: "access_token", placeholder: "eyJ...", type: "password" },
      { label: "Channel Secret", key: "webhook_secret", placeholder: "line_channel_secret", type: "password" },
    ],
    docsUrl: "https://developers.line.biz/en/docs/messaging-api/overview/",
    webhookInfo: "Webhook: /api/v1/webhook/line - connection becomes live after a signed event arrives",
  },
];

const TONE_CLASS: Record<"info" | "warning" | "default" | "success", string> = {
  info: "bg-info/15 text-info",
  warning: "bg-warning/15 text-warning",
  default: "bg-primary/10 text-primary",
  success: "bg-success/15 text-success",
};

export default function PlatformsPage() {
  const { data: configs, isLoading } = useSWR<PlatformConfig[]>(
    "platform-configs",
    () => apiFetch<PlatformConfig[]>("/platforms/configs"),
  );

  const [startingMetaOAuth, setStartingMetaOAuth] = React.useState(false);
  const [metaOAuthSession, setMetaOAuthSession] = React.useState<MetaOAuthSession | null>(null);
  const [refreshing, setRefreshing] = React.useState(false);

  const byPlatform = React.useMemo(() => {
    const groups = new Map<PlatformKey, PlatformConfig[]>();
    for (const platform of PLATFORMS) groups.set(platform.label, []);
    // Only ACTIVE configs render as cards. Disconnect is a soft-deactivate on
    // the server, so without this filter the disabled row would keep showing.
    for (const config of configs ?? []) {
      if (config.is_active) groups.get(config.platform)?.push(config);
    }
    return groups;
  }, [configs]);

  const operationalSummary = React.useMemo(() => {
    const all = configs ?? [];
    return {
      connected: all.filter((config) => config.is_active && config.health?.status === "connected").length,
      inboundPending: all.reduce((total, config) => total + (config.pending_inbound_events ?? 0), 0),
      outboundPending: all.reduce((total, config) => total + (config.pending_deliveries ?? 0), 0),
      failed: all.reduce((total, config) => total + (config.failed_inbound_events ?? 0) + (config.failed_deliveries ?? 0), 0),
      cancelled: all.reduce((total, config) => total + (config.cancelled_deliveries ?? 0), 0),
    };
  }, [configs]);

  // Active accounts per platform, for the connect cards' "already connected" state.
  const metaAccounts = React.useMemo(
    () => (configs ?? []).filter((c) => c.is_active && c.platform === "meta"),
    [configs],
  );
  const instagramAccounts = React.useMemo(
    () => (configs ?? []).filter((c) => c.is_active && c.platform === "instagram"),
    [configs],
  );
  const whatsappAccounts = React.useMemo(
    () => (configs ?? []).filter((c) => c.is_active && c.platform === "whatsapp"),
    [configs],
  );

  React.useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const sessionID = params.get("meta_oauth_session");
    const callbackError = params.get("meta_oauth_error");
    if (!sessionID && !callbackError) return;

    window.history.replaceState({}, "", window.location.pathname);
    if (callbackError) {
      toast.error(callbackError === "cancelled" ? "Meta authorization was cancelled" : "Meta authorization could not be completed");
      return;
    }

    void apiFetch<MetaOAuthSession>(`/platforms/meta/oauth/sessions/${sessionID}`)
      .then(setMetaOAuthSession)
      .catch((err) => toast.error(err instanceof ApiError ? err.message : "Could not load Meta accounts"));
  }, []);

  const startMetaOAuth = async () => {
    setStartingMetaOAuth(true);
    try {
      const response = await apiFetch<{ authorize_url: string }>("/platforms/meta/oauth/start", { method: "POST" });
      window.location.assign(response.authorize_url);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : (err as Error).message);
      setStartingMetaOAuth(false);
    }
  };

  const refresh = async () => {
    setRefreshing(true);
    try {
      const active = (configs ?? []).filter((config) => config.config_id && config.is_active);
      const results = await Promise.allSettled(active.map((config) => apiFetch(`/platforms/configs/${config.config_id}/verify`, { method: "POST" })));
      const failed = results.filter((result) => result.status === "rejected").length;
      await globalMutate("platform-configs");
      if (failed > 0) {
        toast.error(`${failed} connection${failed === 1 ? "" : "s"} need attention`);
      } else if (active.length > 0) {
        toast.success("Connection status refreshed");
      }
    } finally {
      setRefreshing(false);
    }
  };

  return (
    <div className="flex flex-col h-full">
      <PageHeader
        icon={Globe}
        kicker="Integrations"
        title="Platform Integration"
        description="Each account has tenant routing, a durable delivery queue, and visible connection health."
        actions={
          <Button size="sm" variant="outline" onClick={refresh} disabled={refreshing} className="gap-1.5">
            <RefreshCw className={`size-3.5 ${refreshing ? "animate-spin" : ""}`} /> Refresh status
          </Button>
        }
      />

      <div className="flex-1 overflow-auto px-4 py-5 sm:px-6">
        <div className="mx-auto max-w-6xl space-y-6">
          <MetaOAuthConnectCard
            starting={startingMetaOAuth}
            onStart={startMetaOAuth}
            connectedMeta={metaAccounts.map((c) => ({ name: c.health?.account_name || c.page_id || `#${c.config_id}`, healthy: c.health?.status === "connected" }))}
            connectedInstagram={instagramAccounts.map((c) => ({ name: c.health?.account_name || c.instagram_business_id || `#${c.config_id}`, healthy: c.health?.status === "connected" }))}
          />
          <WhatsAppEmbeddedSignupCard
            connected={whatsappAccounts.map((c) => ({ name: c.health?.account_name || c.whatsapp_business_account_id || `#${c.config_id}`, healthy: c.health?.status === "connected" }))}
          />
          <div className="grid overflow-hidden border border-border bg-card sm:grid-cols-2 xl:grid-cols-5">
            <StatusMetric icon={Radio} label="Verified connections" value={operationalSummary.connected} tone="text-success" />
            <StatusMetric icon={Inbox} label="Inbound queue" value={operationalSummary.inboundPending} tone="text-info" />
            <StatusMetric icon={Clock3} label="Outbound queue" value={operationalSummary.outboundPending} tone="text-info" />
            <StatusMetric icon={CircleAlert} label="Failures" value={operationalSummary.failed} tone={operationalSummary.failed > 0 ? "text-destructive" : "text-muted-foreground"} />
            <StatusMetric icon={CheckCircle2} label="AI replies held" value={operationalSummary.cancelled} tone={operationalSummary.cancelled > 0 ? "text-warning" : "text-muted-foreground"} />
          </div>
          {isLoading ? (
            <Card><CardContent className="py-12 flex items-center justify-center">
              <Loader2 className="size-5 animate-spin text-muted-foreground" />
            </CardContent></Card>
          ) : PLATFORMS.map((meta) => {
            // keying on config_id forces a clean remount when the saved config
            // changes (after Save → revalidate), avoiding set-state-in-effect.
            return <PlatformGroup key={meta.label} meta={meta} configs={byPlatform.get(meta.label) ?? []} />;
          })}
        </div>
      </div>

      {metaOAuthSession && (
        <MetaOAuthSelectionDialog
          key={metaOAuthSession.session_id}
          session={metaOAuthSession}
          onClose={() => setMetaOAuthSession(null)}
          onConnected={() => {
            setMetaOAuthSession(null);
            toast.success("Meta subscription is ready. Waiting for the first signed webhook event.");
            void globalMutate("platform-configs");
          }}
        />
      )}
    </div>
  );
}

function StatusMetric({ icon: Icon, label, value, tone }: { icon: LucideIcon; label: string; value: number; tone: string }) {
  return (
    <div className="flex min-w-0 items-center gap-3 border-b border-border px-4 py-3 last:border-b-0 sm:border-b-0 sm:border-r sm:last:border-r-0">
      <Icon className={`size-4 shrink-0 ${tone}`} />
      <div className="min-w-0">
        <p className="text-[11px] font-medium text-muted-foreground">{label}</p>
        <p className="mt-0.5 text-lg font-semibold tabular-nums text-foreground">{value}</p>
      </div>
    </div>
  );
}

function PlatformGroup({ meta, configs }: { meta: PlatformMeta; configs: PlatformConfig[] }) {
  const [adding, setAdding] = React.useState(false);
  return (
    <section className="border-t border-border pt-5 first:border-t-0 first:pt-0">
      <div className="mb-3 flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <div className={`flex size-9 shrink-0 items-center justify-center rounded-lg ${TONE_CLASS[meta.tone]}`}>
            <meta.icon className="size-4" />
          </div>
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="text-sm font-semibold text-foreground">{meta.name}</h2>
              <span className="text-xs tabular-nums text-muted-foreground">{configs.length} account{configs.length === 1 ? "" : "s"}</span>
              <GroupConnectionBadge configs={configs} />
            </div>
            <p className="mt-0.5 text-xs text-muted-foreground">{meta.desc}</p>
            <div className="mt-3 grid gap-2 text-[11px] sm:grid-cols-3">
              <CapabilityGroup label="Inbound" values={meta.inbound} />
              <CapabilityGroup label="Outbound" values={meta.outbound} />
              <CapabilityGroup label="Receipts" values={[meta.receipts]} />
            </div>
            <p className="mt-1 text-[11px] leading-4 text-muted-foreground">{meta.limitation}</p>
          </div>
        </div>
        <Button size="sm" variant="outline" onClick={() => setAdding(true)} disabled={adding} className="h-8 gap-1.5 text-xs">
          <Plus className="size-3.5" /> Add account
        </Button>
      </div>

      {configs.length === 0 && !adding ? (
        <div className="flex items-center justify-between border border-dashed border-border px-4 py-4 text-xs text-muted-foreground">
          <span>No account is connected.</span>
          <span className="hidden text-right sm:block">{meta.webhookInfo}</span>
        </div>
      ) : (
        <div className="grid gap-3 lg:grid-cols-2">
          {configs.map((config) => <PlatformCard key={config.config_id} meta={meta} initial={config} />)}
          {adding && <PlatformCard meta={meta} onClose={() => setAdding(false)} />}
        </div>
      )}
    </section>
  );
}

function GroupConnectionBadge({ configs }: { configs: PlatformConfig[] }) {
  const active = configs.filter((c) => c.is_active);
  if (active.length === 0) {
    return (
      <Badge variant="outline" className="h-5 gap-1.5 px-1.5 text-[10px] font-normal text-muted-foreground">
        <span className="size-1.5 rounded-full bg-muted-foreground/40" /> Not connected
      </Badge>
    );
  }
  const connected = active.filter((c) => c.health?.status === "connected").length;
  const errored = active.filter((c) => c.health?.status === "error").length;
  if (errored > 0) {
    return (
      <Badge variant="outline" className="h-5 gap-1.5 border-destructive/40 px-1.5 text-[10px] font-medium text-destructive">
        <span className="size-1.5 rounded-full bg-destructive animate-pulse" /> Needs attention
      </Badge>
    );
  }
  if (connected === active.length) {
    return (
      <Badge variant="outline" className="h-5 gap-1.5 border-success/40 px-1.5 text-[10px] font-medium text-success">
        <span className="size-1.5 rounded-full bg-success" /> Connected
      </Badge>
    );
  }
  return (
    <Badge variant="outline" className="h-5 gap-1.5 border-warning/40 px-1.5 text-[10px] font-medium text-warning">
      <span className="size-1.5 rounded-full bg-warning animate-pulse" /> Awaiting verification
    </Badge>
  );
}

function CapabilityGroup({ label, values }: { label: string; values: string[] }) {
  return (
    <div className="min-w-0 border-l-2 border-border pl-2">
      <p className="mb-1 text-[10px] font-semibold uppercase text-muted-foreground">{label}</p>
      <div className="flex flex-wrap gap-1">
        {values.map((value) => <Badge key={value} variant="outline" className="h-5 max-w-full px-1.5 text-[10px] font-normal">{value}</Badge>)}
      </div>
    </div>
  );
}

interface ConnectedAccount {
  name: string;
  healthy: boolean;
}

function ConnectionBadge({ healthy }: { healthy?: boolean }) {
  if (healthy === undefined) return null;
  return (
    <span className={`inline-flex items-center gap-1 text-[10px] font-medium ${healthy ? "text-success" : "text-warning"}`}>
      <span className={`size-1.5 rounded-full ${healthy ? "bg-success" : "bg-warning animate-pulse"}`} />
      {healthy ? "Verified" : "Awaiting first webhook"}
    </span>
  );
}

function ConnectedAccountRow({
  icon: Icon,
  label,
  names,
  items,
}: {
  icon: LucideIcon;
  label: string;
  names: string[];
  items: ConnectedAccount[];
}) {
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs">
      <span className="inline-flex shrink-0 items-center gap-1 font-medium text-success">
        <Icon className="size-3" /> {label}
      </span>
      <span className="truncate text-muted-foreground">{names.join(", ")}</span>
      <ConnectionBadge healthy={items.every((i) => i.healthy)} />
    </div>
  );
}

function MetaOAuthConnectCard({
  starting,
  onStart,
  connectedMeta,
  connectedInstagram,
}: {
  starting: boolean;
  onStart: () => void;
  connectedMeta: ConnectedAccount[];
  connectedInstagram: ConnectedAccount[];
}) {
  const hasConnections = connectedMeta.length > 0 || connectedInstagram.length > 0;

  return (
    <Card className={hasConnections ? "border-success/40 bg-success/[0.04]" : "border-info/30"}>
      <CardContent className="flex flex-col gap-4 p-5 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex min-w-0 items-start gap-3">
          <div className={`flex size-9 shrink-0 items-center justify-center rounded-lg ${hasConnections ? TONE_CLASS.success : "bg-info/15 text-info"}`}>
            {hasConnections ? <CheckCircle2 className="size-4" /> : <Link2 className="size-4" />}
          </div>
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <p className="text-sm font-medium">Meta Business &amp; Instagram</p>
              {hasConnections && (
                <Badge variant="outline" className="h-5 border-success/40 px-1.5 text-[10px] font-medium text-success">
                  Connected
                </Badge>
              )}
            </div>
            {hasConnections ? (
              <div className="mt-1 space-y-1">
                {connectedMeta.length > 0 && (
                  <ConnectedAccountRow icon={MessageCircle} label="Messenger" names={connectedMeta.map((a) => a.name)} items={connectedMeta} />
                )}
                {connectedInstagram.length > 0 && (
                  <ConnectedAccountRow icon={Camera} label="Instagram" names={connectedInstagram.map((a) => a.name)} items={connectedInstagram} />
                )}
              </div>
            ) : (
              <p className="mt-0.5 text-xs text-muted-foreground">Connect Messenger and a linked Instagram professional account with one Meta authorization.</p>
            )}
          </div>
        </div>
        <Button size="sm" variant={hasConnections ? "outline" : "default"} onClick={onStart} disabled={starting} className="h-8 shrink-0 gap-1.5 text-xs">
          {starting ? <Loader2 className="size-3 animate-spin" /> : <Plus className="size-3" />}
          {hasConnections ? "Connect another Page" : "Connect Meta"}
        </Button>
      </CardContent>
    </Card>
  );
}

function loadFacebookSDK(appID: string) {
  if (window.FB) {
    window.FB.init({ appId: appID, cookie: true, xfbml: false, version: "v24.0" });
    return Promise.resolve(window.FB);
  }
  return new Promise<FacebookSDK>((resolve, reject) => {
    const scriptID = "meta-facebook-sdk";
    const existing = document.getElementById(scriptID) as HTMLScriptElement | null;
    const script = existing ?? document.createElement("script");
    const onLoad = () => {
      if (!window.FB) {
        reject(new Error("Meta SDK did not initialize"));
        return;
      }
      window.FB.init({ appId: appID, cookie: true, xfbml: false, version: "v24.0" });
      resolve(window.FB);
    };
    script.addEventListener("load", onLoad, { once: true });
    script.addEventListener("error", () => reject(new Error("Meta SDK could not be loaded")), { once: true });
    if (!existing) {
      script.id = scriptID;
      script.async = true;
      script.src = "https://connect.facebook.net/en_US/sdk.js";
      document.head.appendChild(script);
    }
  });
}

function WhatsAppEmbeddedSignupCard({ connected }: { connected: ConnectedAccount[] }) {
  const [starting, setStarting] = React.useState(false);
  const [completing, setCompleting] = React.useState(false);
  const signup = React.useRef({ code: "", phoneNumberID: "", businessAccountID: "" });

  const complete = React.useCallback(async () => {
    const current = signup.current;
    if (!current.code || !current.phoneNumberID || !current.businessAccountID || completing) return;
    setCompleting(true);
    try {
      await apiFetch("/platforms/whatsapp/embedded-signup/complete", {
        method: "POST",
        body: JSON.stringify({
          code: current.code,
          phone_number_id: current.phoneNumberID,
          business_account_id: current.businessAccountID,
        }),
      });
      toast.success("WhatsApp is connected. Waiting for the first signed event.");
      await globalMutate("platform-configs");
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not complete WhatsApp connection");
    } finally {
      setCompleting(false);
      setStarting(false);
    }
  }, [completing]);

  React.useEffect(() => {
    const receiveSignupEvent = (event: MessageEvent<unknown>) => {
      if (event.origin !== "https://www.facebook.com") return;
      let payload: unknown = event.data;
      if (typeof payload === "string") {
        try {
          payload = JSON.parse(payload);
        } catch {
          return;
        }
      }
      if (!payload || typeof payload !== "object") return;
      const data = payload as { type?: string; event?: string; data?: { phone_number_id?: string; waba_id?: string } };
      if (data.type !== "WA_EMBEDDED_SIGNUP" || data.event !== "FINISH") return;
      const phoneNumberID = data.data?.phone_number_id?.trim() ?? "";
      const businessAccountID = data.data?.waba_id?.trim() ?? "";
      if (!phoneNumberID || !businessAccountID) {
        setStarting(false);
        toast.error("Meta did not return the selected WhatsApp business account.");
        return;
      }
      signup.current.phoneNumberID = phoneNumberID;
      signup.current.businessAccountID = businessAccountID;
      void complete();
    };
    window.addEventListener("message", receiveSignupEvent);
    return () => window.removeEventListener("message", receiveSignupEvent);
  }, [complete]);

  const start = async () => {
    setStarting(true);
    signup.current = { code: "", phoneNumberID: "", businessAccountID: "" };
    try {
      const config = await apiFetch<WhatsAppEmbeddedSignupConfig>("/platforms/whatsapp/embedded-signup/config");
      const facebook = await loadFacebookSDK(config.app_id);
      facebook.login((response) => {
        const code = response.authResponse?.code?.trim() ?? "";
        if (!code) {
          setStarting(false);
          toast.error("WhatsApp authorization was cancelled or did not return a code.");
          return;
        }
        signup.current.code = code;
        void complete();
      }, {
        config_id: config.config_id,
        response_type: "code",
        override_default_response_type: true,
        extras: { setup: {} },
      });
    } catch (err) {
      setStarting(false);
      toast.error(err instanceof ApiError ? err.message : "WhatsApp Embedded Signup is unavailable");
    }
  };

  return (
    <Card className={connected.length > 0 ? "border-success/40 bg-success/[0.04]" : "border-success/30"}>
      <CardContent className="flex flex-col gap-4 p-5 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex min-w-0 items-start gap-3">
          <div className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-success/15 text-success">
            <MessageCircle className="size-4" />
          </div>
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <p className="text-sm font-medium">WhatsApp Business</p>
              {connected.length > 0 && (
                <Badge variant="outline" className="h-5 border-success/40 px-1.5 text-[10px] font-medium text-success">
                  Connected
                </Badge>
              )}
            </div>
            {connected.length > 0 ? (
              <ConnectedAccountRow icon={MessageCircle} label="WhatsApp" names={connected.map((a) => a.name)} items={connected} />
            ) : (
              <p className="mt-0.5 text-xs text-muted-foreground">Select a business phone number in Meta. Credentials are exchanged and encrypted on the server.</p>
            )}
          </div>
        </div>
        <Button size="sm" variant={connected.length > 0 ? "outline" : "default"} onClick={() => void start()} disabled={starting || completing} className="h-8 shrink-0 gap-1.5 text-xs">
          {starting || completing ? <Loader2 className="size-3 animate-spin" /> : <Plus className="size-3" />}
          {connected.length > 0 ? "Connect another number" : "Connect WhatsApp"}
        </Button>
      </CardContent>
    </Card>
  );
}

function MetaOAuthSelectionDialog({
  session,
  onClose,
  onConnected,
}: {
  session: MetaOAuthSession;
  onClose: () => void;
  onConnected: () => void;
}) {
  const [selectedPageID, setSelectedPageID] = React.useState(session.pages[0]?.page_id ?? "");
  const [enableMessenger, setEnableMessenger] = React.useState(true);
  const [enableInstagram, setEnableInstagram] = React.useState(Boolean(session.pages[0]?.instagram_business_id));
  const [connecting, setConnecting] = React.useState(false);
  const selectedPage = session.pages.find((page) => page.page_id === selectedPageID);

  const choosePage = (page: MetaOAuthPage) => {
    setSelectedPageID(page.page_id);
    if (!page.instagram_business_id) setEnableInstagram(false);
  };

  const completeConnection = async () => {
    if (!selectedPageID || (!enableMessenger && !enableInstagram)) return;
    setConnecting(true);
    try {
      await apiFetch("/platforms/meta/oauth/complete", {
        method: "POST",
        body: JSON.stringify({
          session_id: session.session_id,
          page_id: selectedPageID,
          enable_messenger: enableMessenger,
          enable_instagram: enableInstagram,
        }),
      });
      onConnected();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not connect Meta");
      setConnecting(false);
    }
  };

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Choose a Meta account</DialogTitle>
          <DialogDescription>Select the Facebook Page to receive customer messages.</DialogDescription>
        </DialogHeader>

        {session.pages.length === 0 ? (
          <div className="border-y border-border py-7 text-center text-sm text-muted-foreground">No manageable Facebook Pages were returned by Meta.</div>
        ) : (
          <div className="max-h-64 space-y-2 overflow-y-auto pr-1">
            {session.pages.map((page) => {
              const selected = page.page_id === selectedPageID;
              return (
                <button
                  key={page.page_id}
                  type="button"
                  onClick={() => choosePage(page)}
                  className={`flex w-full items-center gap-3 rounded-lg border px-3 py-3 text-left transition-colors ${selected ? "border-primary bg-primary/5" : "border-border hover:bg-muted/60"}`}
                >
                  <span className={`flex size-4 shrink-0 items-center justify-center rounded-full border ${selected ? "border-primary" : "border-muted-foreground/50"}`}>
                    {selected && <span className="size-2 rounded-full bg-primary" />}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm font-medium">{page.page_name || page.page_id}</span>
                    <span className="mt-0.5 flex items-center gap-1 truncate text-xs text-muted-foreground">
                      <MessageCircle className="size-3" /> Messenger
                      {page.instagram_business_id && <><span className="mx-1 text-border">|</span><Camera className="size-3" /> {page.instagram_name || "Instagram"}</>}
                    </span>
                  </span>
                </button>
              );
            })}
          </div>
        )}

        {selectedPage && (
          <div className="space-y-2 border-t border-border pt-3">
            <label className="flex cursor-pointer items-center gap-2 text-sm">
              <input type="checkbox" checked={enableMessenger} onChange={(event) => setEnableMessenger(event.target.checked)} className="size-4 accent-primary" />
              <MessageCircle className="size-4 text-info" /> Enable Messenger
            </label>
            <label className={`flex items-center gap-2 text-sm ${selectedPage.instagram_business_id ? "cursor-pointer" : "cursor-not-allowed text-muted-foreground"}`}>
              <input type="checkbox" checked={enableInstagram} disabled={!selectedPage.instagram_business_id} onChange={(event) => setEnableInstagram(event.target.checked)} className="size-4 accent-primary" />
              <Camera className="size-4 text-warning" /> Enable Instagram {selectedPage.instagram_business_id ? "" : "(not linked to this Page)"}
            </label>
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={connecting}>Cancel</Button>
          <Button onClick={completeConnection} disabled={!selectedPageID || (!enableMessenger && !enableInstagram) || connecting} className="gap-1.5">
            {connecting ? <Loader2 className="size-4 animate-spin" /> : <CheckCircle2 className="size-4" />}
            Connect selected
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function PlatformCard({ meta, initial, onClose }: { meta: PlatformMeta; initial?: PlatformConfig; onClose?: () => void }) {
  const [form, setForm] = React.useState<PlatformConfig>(() => ({
    platform: meta.label,
    is_active: initial?.is_active ?? true,
    ...stripPresent(initial),
  }));
  const [saving, setSaving] = React.useState(false);
  const [checking, setChecking] = React.useState(false);
  const [disconnecting, setDisconnecting] = React.useState(false);
  const [workOpen, setWorkOpen] = React.useState(false);
  const health = initial?.health;
  const isActive = Boolean(initial?.config_id && initial.is_active);
  const status = !isActive
    ? "Not configured"
    : health?.status === "connected"
      ? "Connected"
      : health?.status === "error"
        ? "Needs attention"
        : health?.checked_at
          ? "Webhook pending"
          : "Verification needed";
  const statusVariant = !isActive ? "secondary" : health?.status === "connected" ? "success" : health?.status === "error" ? "destructive" : "warning";

  const handleSave = async () => {
    setSaving(true);
    try {
      const saved = await apiFetch<PlatformConfig>("/platforms/configs", {
        method: "PUT",
        body: JSON.stringify({
          config_id: initial?.config_id,
          platform: form.platform,
          access_token: form.access_token,
          page_id: form.page_id,
          instagram_business_id: form.instagram_business_id,
          bot_token: form.bot_token,
          webhook_secret: form.webhook_secret,
          is_active: form.is_active,
        }),
      });
      if (saved.is_active && saved.config_id) {
        try {
          const response = await apiFetch<{ health: PlatformHealth }>(`/platforms/configs/${saved.config_id}/verify`, { method: "POST" });
          if (response.health.status === "connected") {
            toast.success(response.health.account_name ? `Connected as ${response.health.account_name}` : `${meta.name} connection verified`);
          } else {
            toast.success("Credentials verified. Waiting for a signed webhook event.");
          }
        } catch (err) {
          toast.error(err instanceof ApiError ? `Saved, but verification failed: ${err.message}` : "Saved, but verification failed");
        }
      } else {
        toast.success(`${meta.name} config saved`);
      }
      await globalMutate("platform-configs");
      onClose?.();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : (err as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const checkConnection = async () => {
    if (!initial?.config_id) {
      toast.message("Save this account before testing the connection.");
      return;
    }
    setChecking(true);
    try {
      const response = await apiFetch<{ health: PlatformHealth }>(`/platforms/configs/${initial.config_id}/verify`, { method: "POST" });
      if (response.health.status === "connected") {
        toast.success(response.health.account_name ? `Connected as ${response.health.account_name}` : "Connection verified");
      } else {
        toast.success("Credentials verified. Waiting for a signed webhook event.");
      }
      await globalMutate("platform-configs");
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Connection check failed");
      void globalMutate("platform-configs");
    } finally {
      setChecking(false);
    }
  };

  const disconnect = async () => {
    if (!initial?.config_id) {
      onClose?.();
      return;
    }
    setDisconnecting(true);
    try {
      await apiFetch(`/platforms/configs/${initial.config_id}`, { method: "DELETE" });
      toast.success(`${meta.name} account disconnected`);
      await globalMutate("platform-configs");
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not disconnect account");
    } finally {
      setDisconnecting(false);
    }
  };

  return (
    <Card className={health?.status === "error" ? "border-destructive/45" : undefined}>
      <CardContent className="p-5">
        <div className="mb-4 flex items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="truncate text-sm font-semibold text-foreground">{health?.account_name || initial?.page_id || initial?.instagram_business_id || (initial?.config_id ? `Account #${initial.config_id}` : "New account")}</p>
            <p className="mt-0.5 text-xs text-muted-foreground">{health?.checked_at ? `Last checked ${formatTimestamp(health.checked_at)}` : "Credentials have not been verified"}</p>
            {initial?.last_inbound_at && <p className="mt-1 text-[11px] text-muted-foreground">Last customer message {formatTimestamp(initial.last_inbound_at)}</p>}
          </div>
          <Badge variant={statusVariant} className="h-5 shrink-0 px-1.5 text-[10px]">
            {status}
          </Badge>
        </div>

        {health?.detail && (
          <div className={`mb-3 flex items-start gap-2 border px-2.5 py-2 text-xs ${health.status === "error" ? "border-destructive/30 bg-destructive/5 text-destructive" : "border-border text-muted-foreground"}`}>
            <CircleAlert className="mt-0.5 size-3 shrink-0" />
            <span className="min-w-0 break-words">{health.detail}</span>
          </div>
        )}

        {initial?.config_id && initial.is_active && <ConnectionReadiness health={health} />}

        <div className="space-y-3">
          {meta.fields.map((f) => (
            <div key={String(f.key)}>
              <label className="text-xs text-muted-foreground mb-1 block">{f.label}</label>
              <Input
                type={f.type || "text"}
                value={String((form[f.key] as string | undefined) ?? "")}
                onChange={(e) => setForm((prev) => ({ ...prev, [f.key]: e.target.value }))}
                placeholder={f.placeholder}
                className="h-9 text-sm"
              />
            </div>
          ))}
        </div>

        {initial?.config_id && (
          <div className="mt-4 space-y-3">
            <div className="grid grid-cols-2 gap-px overflow-hidden border border-border bg-border text-xs sm:grid-cols-5">
              <WorkMetric label="Inbound queue" value={initial.pending_inbound_events ?? 0} />
              <WorkMetric label="Inbound failed" value={initial.failed_inbound_events ?? 0} destructive />
              <WorkMetric label="Outbound queue" value={initial.pending_deliveries ?? 0} />
              <WorkMetric label="Outbound failed" value={initial.failed_deliveries ?? 0} destructive />
              <WorkMetric label="AI replies held" value={initial.cancelled_deliveries ?? 0} />
            </div>
            <div className="grid grid-cols-3 divide-x divide-border border-y border-border text-xs">
              <WorkMetric label="Accepted" value={initial.accepted_deliveries ?? 0} />
              <WorkMetric label="Delivered" value={initial.delivered_deliveries ?? 0} />
              <WorkMetric label="Read" value={initial.read_deliveries ?? 0} />
            </div>
          </div>
        )}

        <div className="mt-4 flex flex-wrap items-center justify-between gap-2 border-t border-border pt-3">
          <div className="flex flex-wrap gap-2">
            <Button size="sm" onClick={handleSave} disabled={saving} className="h-8 text-xs gap-1.5">
              {saving ? <Loader2 className="size-3 animate-spin" /> : <CheckCircle2 className="size-3" />}
              Save
            </Button>
            <Button size="sm" variant="outline" onClick={checkConnection} disabled={checking || !initial?.config_id || !initial.is_active} className="h-8 gap-1.5 text-xs">
              {checking ? <Loader2 className="size-3 animate-spin" /> : <Radio className="size-3" />}
              Verify
            </Button>
            {((initial?.failed_inbound_events ?? 0) + (initial?.failed_deliveries ?? 0) + (initial?.cancelled_deliveries ?? 0) > 0) && (
              <Button size="sm" variant="outline" onClick={() => setWorkOpen(true)} className="h-8 gap-1.5 text-xs">
                <CircleAlert className="size-3" /> Review delivery work
              </Button>
            )}
            <Button
              variant="ghost"
              size="sm"
              onClick={() => window.open(meta.docsUrl, "_blank")}
              className="h-8 text-xs gap-1 text-muted-foreground"
            >
              <ExternalLink className="size-3" />Docs
            </Button>
            {initial?.config_id && initial.is_active && (
              <Button variant="ghost" size="sm" onClick={disconnect} disabled={disconnecting} className="h-8 gap-1 text-destructive hover:text-destructive">
                {disconnecting ? <Loader2 className="size-3 animate-spin" /> : <Unplug className="size-3" />} Disconnect
              </Button>
            )}
            {!initial?.config_id && onClose && <Button variant="ghost" size="sm" onClick={onClose} className="h-8 text-xs">Cancel</Button>}
          </div>
          <span className="hidden max-w-md text-right text-[11px] text-muted-foreground sm:block">
            {meta.webhookInfo}
          </span>
        </div>
      </CardContent>
      {initial?.config_id && <PlatformFailureDialog configID={initial.config_id} open={workOpen} onOpenChange={setWorkOpen} />}
    </Card>
  );
}

function ConnectionReadiness({ health }: { health?: PlatformHealth }) {
  const credentialsReady = Boolean(health?.checked_at) && health?.status !== "error";
  const webhookReady = health?.status === "connected";
  return (
    <div className="mb-4 grid grid-cols-2 divide-x divide-border border-y border-border text-[11px]">
      <ReadinessStep label="Credentials" ready={credentialsReady} readyText="Verified" pendingText="Not verified" />
      <ReadinessStep label="Webhook" ready={webhookReady} readyText="Receiving" pendingText="Waiting" />
    </div>
  );
}

function ReadinessStep({ label, ready, readyText, pendingText }: { label: string; ready: boolean; readyText: string; pendingText: string }) {
  return (
    <div className="flex items-center gap-2 px-2.5 py-2">
      {ready ? <CheckCircle2 className="size-3.5 shrink-0 text-success" /> : <Clock3 className="size-3.5 shrink-0 text-warning" />}
      <span className="min-w-0"><span className="block text-muted-foreground">{label}</span><span className="font-medium text-foreground">{ready ? readyText : pendingText}</span></span>
    </div>
  );
}

function WorkMetric({ label, value, destructive = false }: { label: string; value: number; destructive?: boolean }) {
  return (
    <div className="bg-card px-2.5 py-2">
      <span className="block text-[10px] text-muted-foreground">{label}</span>
      <span className={`font-semibold tabular-nums ${destructive && value > 0 ? "text-destructive" : ""}`}>{value}</span>
    </div>
  );
}

function PlatformFailureDialog({ configID, open, onOpenChange }: { configID: number; open: boolean; onOpenChange: (open: boolean) => void }) {
  const key = open ? `platform-config-work-${configID}` : null;
  const { data, isLoading, mutate } = useSWR<PlatformWorkResponse>(
    key,
    () => apiFetch<PlatformWorkResponse>(`/platforms/configs/${configID}/work`),
  );
  const [retrying, setRetrying] = React.useState<string | null>(null);

  const retry = async (kind: "inbound-events" | "deliveries", id: number) => {
    const retryKey = `${kind}-${id}`;
    setRetrying(retryKey);
    try {
      await apiFetch(`/platforms/configs/${configID}/${kind}/${id}/retry`, { method: "POST" });
      toast.success("Work item queued for retry");
      await Promise.all([mutate(), globalMutate("platform-configs")]);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not retry this work item");
    } finally {
      setRetrying(null);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Platform delivery work</DialogTitle>
          <DialogDescription>Failures can be retried from their durable checkpoint. Held AI replies were intentionally not sent after a handoff or closure.</DialogDescription>
        </DialogHeader>
        {isLoading ? (
          <div className="flex h-32 items-center justify-center"><Loader2 className="size-4 animate-spin text-muted-foreground" /></div>
        ) : (
          <div className="max-h-[58vh] space-y-5 overflow-y-auto pr-1">
            <FailureSection
              icon={Inbox}
              title="Inbound processing"
              items={data?.inbound_events ?? []}
              getID={(item) => item.event_id}
              getContent={(item) => item.content}
              getError={(item) => item.last_error}
              getAttempts={(item) => item.attempts}
              getCreated={(item) => item.created_at}
              retrying={retrying}
              onRetry={(id) => retry("inbound-events", id)}
            />
            <FailureSection
              icon={Send}
              title="Outbound delivery"
              items={data?.deliveries ?? []}
              getID={(item) => item.delivery_id}
              getContent={(item) => item.content}
              getError={(item) => item.last_error}
              getAttempts={(item) => item.attempts}
              getCreated={(item) => item.created_at}
              retrying={retrying}
              onRetry={(id) => retry("deliveries", id)}
            />
            <CancelledDeliverySection items={data?.cancelled_deliveries ?? []} />
          </div>
        )}
        <DialogFooter><Button variant="outline" onClick={() => onOpenChange(false)}>Close</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function CancelledDeliverySection({ items }: { items: PlatformDelivery[] }) {
  return (
    <section>
      <div className="mb-2 flex items-center gap-2 text-xs font-medium text-foreground"><CheckCircle2 className="size-3.5 text-warning" />Held AI replies<Badge variant="secondary">{items.length}</Badge></div>
      {items.length === 0 ? <p className="border border-dashed border-border px-3 py-3 text-xs text-muted-foreground">No AI replies were held.</p> : (
        <div className="divide-y border border-border">
          {items.map((item) => (
            <div key={item.delivery_id} className="space-y-2 px-3 py-3">
              <p className="break-words text-xs text-foreground">{item.content}</p>
              <p className="break-words text-[11px] text-muted-foreground">{item.last_error || "This automatic reply was not sent."}</p>
              <p className="text-[10px] tabular-nums text-muted-foreground">{formatTimestamp(item.created_at)}</p>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function FailureSection<T extends { attempts: number; created_at: string }>({
  icon: Icon,
  title,
  items,
  getID,
  getContent,
  getError,
  getAttempts,
  getCreated,
  retrying,
  onRetry,
}: {
  icon: LucideIcon;
  title: string;
  items: T[];
  getID: (item: T) => number;
  getContent: (item: T) => string;
  getError: (item: T) => string | undefined;
  getAttempts: (item: T) => number;
  getCreated: (item: T) => string;
  retrying: string | null;
  onRetry: (id: number) => void;
}) {
  return (
    <section>
      <div className="mb-2 flex items-center gap-2 text-xs font-medium text-foreground"><Icon className="size-3.5 text-muted-foreground" />{title}<Badge variant="secondary">{items.length}</Badge></div>
      {items.length === 0 ? <p className="border border-dashed border-border px-3 py-3 text-xs text-muted-foreground">No failed work.</p> : (
        <div className="divide-y border border-border">
          {items.map((item) => {
            const id = getID(item);
            const retryKey = `${title === "Inbound processing" ? "inbound-events" : "deliveries"}-${id}`;
            return <div key={id} className="space-y-2 px-3 py-3">
              <div className="flex items-start justify-between gap-3">
                <p className="min-w-0 flex-1 break-words text-xs text-foreground">{getContent(item)}</p>
                <Button size="sm" variant="outline" onClick={() => onRetry(id)} disabled={retrying !== null} className="h-7 shrink-0 gap-1 text-[11px]">
                  {retrying === retryKey ? <Loader2 className="size-3 animate-spin" /> : <RotateCcw className="size-3" />} Retry
                </Button>
              </div>
              <p className="break-words text-[11px] text-destructive">{getError(item) || "No provider error was recorded"}</p>
              <p className="text-[10px] tabular-nums text-muted-foreground">{getAttempts(item)} attempts · {formatTimestamp(getCreated(item))}</p>
            </div>;
          })}
        </div>
      )}
    </section>
  );
}

function formatTimestamp(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString("en-US");
}

// stripPresent returns only the set scalar fields of a config, ignoring
// undefined values so they don't overwrite user input on the form seed.
function stripPresent(c?: PlatformConfig): Partial<PlatformConfig> {
  if (!c) return {};
  const out: Partial<PlatformConfig> = {};
  const keys = ["access_token", "page_id", "instagram_business_id", "bot_token", "webhook_secret"] as (keyof PlatformConfig)[];
  for (const k of keys) {
    const v = c[k];
    if (v !== undefined && v !== "") out[k] = v as never;
  }
  return out;
}
