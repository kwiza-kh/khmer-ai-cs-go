"use client";

import { PageHeader } from "@/components/page-header";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { HelpCircle, MessageSquare, Bot, Webhook, Shield, BookOpen, Zap } from "lucide-react";

export default function HelpPage() {
  return (
    <div className="flex h-full flex-col">
      <PageHeader
        icon={HelpCircle}
        kicker="Documentation"
        title="Help & Integration Guide"
        description="Everything you need to wire up platforms, train the AI, and operate the inbox."
      />

      <div className="flex-1 overflow-auto p-5 sm:p-8">
        <div className="max-w-3xl mx-auto space-y-5">

          {/* Quickstart */}
          <Card>
            <CardHeader><CardTitle className="text-sm flex items-center gap-2"><Zap className="size-4 text-primary" /> Quickstart</CardTitle></CardHeader>
            <CardContent>
              <ol className="space-y-2 text-sm">
                <Step n={1} title="Log in">
                  Use the bootstrap <code className="text-xs bg-muted px-1.5 py-0.5 rounded">admin</code> account with the password set in <code className="text-xs bg-muted px-1.5 py-0.5 rounded">INITIAL_ADMIN_PASSWORD</code>.
                </Step>
                <Step n={2} title="Enable real AI">
                  Go to <b>Dashboard → Models</b>, edit the default config, and paste your Gemini API key. Without it the system runs in mock mode (responses include a <Badge variant="warning" className="h-4 px-1.5">mock</Badge> badge).
                </Step>
                <Step n={3} title="Train your knowledge base">
                  <b>Knowledge → Upload</b> lets you add FAQ docs the AI will cite via RAG retrieval.
                </Step>
                <Step n={4} title="Connect a platform">
                  See the Webhook section below.
                </Step>
              </ol>
            </CardContent>
          </Card>

          {/* Webhook setup */}
          <Card>
            <CardHeader><CardTitle className="text-sm flex items-center gap-2"><Webhook className="size-4 text-info" /> Platform webhooks</CardTitle></CardHeader>
            <CardContent className="space-y-4 text-sm">
              <p className="text-muted-foreground">
                Inbound messages from connected platforms are answered by the AI automatically. Configure these endpoints in each platform&apos;s developer console.
              </p>

              <div className="space-y-3">
                <PlatformGuide
                  name="Meta Business & Instagram"
                  webhookUrl="https://YOUR_DOMAIN/api/v1/webhook/meta"
                  fields={["META_APP_ID", "META_APP_SECRET", "META_OAUTH_REDIRECT_URL", "META_OAUTH_FRONTEND_URL", "META_VERIFY_TOKEN", "Platforms -> Connect Meta"]}
                  docsUrl="https://developers.facebook.com/docs/messenger-platform/instagram"
                />
                <PlatformGuide
                  name="Telegram"
                  webhookUrl="https://YOUR_DOMAIN/api/v1/webhook/telegram"
                  fields={["Bot Token (from @BotFather)", "Webhook secret token (set as secret_token in Telegram setWebhook)"]}
                  docsUrl="https://core.telegram.org/bots/api#setwebhook"
                />
              </div>
            </CardContent>
          </Card>

          {/* Inbox / handoff */}
          <Card>
            <CardHeader><CardTitle className="text-sm flex items-center gap-2"><MessageSquare className="size-4 text-warning" /> Operating the inbox</CardTitle></CardHeader>
            <CardContent className="space-y-2 text-sm">
              <p><b>Inbox</b> aggregates every conversation across all platforms.</p>
              <ul className="list-disc pl-5 space-y-1 text-muted-foreground">
                <li><b>Take over</b> — pause the AI and reply yourself. Status becomes <Badge variant="warning" className="h-4 px-1.5">Agent</Badge>.</li>
                <li><b>Internal notes</b> — visible only to your team, never to the user.</li>
                <li><b>Resolve</b> / <b>Close</b> — moves the conversation out of the active queue.</li>
                <li>The session&apos;s <b>CSAT</b> rolls up thumbs up/down from individual replies.</li>
              </ul>
            </CardContent>
          </Card>

          {/* Feature reference */}
          <Card>
            <CardHeader><CardTitle className="text-sm flex items-center gap-2"><BookOpen className="size-4 text-success" /> Feature reference</CardTitle></CardHeader>
            <CardContent className="grid grid-cols-1 sm:grid-cols-2 gap-3 text-sm">
              <Feature icon={Bot} title="Streaming chat" desc="Token-by-token SSE rendering with Stop / Regenerate / Copy / 👍👎." />
              <Feature icon={BookOpen} title="RAG knowledge base" desc="Document chunking + pgvector retrieval + citations." />
              <Feature icon={Shield} title="Rate limiting & audit" desc="60 rpm / user via Redis. Admin mutations are logged." />
              <Feature icon={Zap} title="Context caching" desc="Large docs cached on Gemini side to cut token spend." />
            </CardContent>
          </Card>

          {/* Security note */}
          <Card>
            <CardHeader><CardTitle className="text-sm flex items-center gap-2"><Shield className="size-4 text-danger" /> Security checklist for production</CardTitle></CardHeader>
            <CardContent>
              <ul className="space-y-1.5 text-sm list-disc pl-5 text-muted-foreground">
                <li>Set strong <code className="text-xs bg-muted px-1 rounded">POSTGRES_PASSWORD</code>, <code className="text-xs bg-muted px-1 rounded">JWT_SECRET</code>, and <code className="text-xs bg-muted px-1 rounded">INITIAL_ADMIN_PASSWORD</code> values before first start.</li>
                <li>Set <code className="text-xs bg-muted px-1 rounded">GEMINI_API_KEY</code> or load it via the Models admin tab.</li>
                <li>Set <code className="text-xs bg-muted px-1 rounded">ALLOWED_ORIGINS</code> to your real frontend URL.</li>
                <li>Run behind HTTPS. Webhooks from Meta/Telegram require TLS.</li>
              </ul>
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  );
}

function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <li className="flex gap-3">
      <span className="flex-shrink-0 size-6 rounded-full bg-primary/10 text-primary inline-flex items-center justify-center text-xs font-semibold">
        {n}
      </span>
      <div className="flex-1">
        <p className="font-medium">{title}</p>
        <p className="text-muted-foreground text-xs mt-0.5">{children}</p>
      </div>
    </li>
  );
}

function PlatformGuide({
  name, webhookUrl, verifyToken, fields, docsUrl,
}: {
  name: string;
  webhookUrl: string;
  verifyToken?: string;
  fields: string[];
  docsUrl: string;
}) {
  return (
    <div className="rounded-md border border-border p-3">
      <div className="flex items-center justify-between mb-2">
        <p className="text-xs font-semibold">{name}</p>
        <a href={docsUrl} target="_blank" rel="noreferrer noopener" className="text-xs text-primary hover:underline">
          Docs ↗
        </a>
      </div>
      <dl className="space-y-1 text-xs">
        <div className="flex gap-2">
          <dt className="text-muted-foreground w-28 flex-shrink-0">Callback URL</dt>
          <dd><code className="bg-muted px-1.5 py-0.5 rounded">{webhookUrl}</code></dd>
        </div>
        {verifyToken && (
          <div className="flex gap-2">
            <dt className="text-muted-foreground w-28 flex-shrink-0">Verify Token</dt>
            <dd><code className="bg-muted px-1.5 py-0.5 rounded">{verifyToken}</code></dd>
          </div>
        )}
        <div className="flex gap-2">
          <dt className="text-muted-foreground w-28 flex-shrink-0">Required</dt>
          <dd className="text-muted-foreground">{fields.join(" · ")}</dd>
        </div>
      </dl>
    </div>
  );
}

function Feature({ icon: Icon, title, desc }: { icon: typeof Zap; title: string; desc: string }) {
  return (
    <div className="flex gap-2.5">
      <div className="flex-shrink-0 size-7 rounded-md bg-accent inline-flex items-center justify-center">
        <Icon className="size-3.5 text-primary" />
      </div>
      <div>
        <p className="text-xs font-medium">{title}</p>
        <p className="text-xs text-muted-foreground">{desc}</p>
      </div>
    </div>
  );
}
