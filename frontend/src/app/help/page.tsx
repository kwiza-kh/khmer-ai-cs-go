"use client";

import { PageHeader } from "@/components/page-header";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { HelpCircle, MessageSquare, Bot, Webhook, Shield, BookOpen, Zap } from "lucide-react";
import { useI18n } from "@/lib/i18n";

export default function HelpPage() {
  const { t } = useI18n();
  return (
    <div className="flex h-full flex-col">
      <PageHeader
        icon={HelpCircle}
        kicker={t("help.kicker")}
        title={t("help.title")}
        description={t("help.desc")}
      />

      <div className="flex-1 overflow-auto p-5 sm:p-8">
        <div className="mx-auto w-full max-w-3xl space-y-5">

          {/* Quickstart */}
          <Card>
            <CardHeader><CardTitle className="text-sm flex items-center gap-2"><Zap className="size-4 text-primary" /> {t("help.quickstart")}</CardTitle></CardHeader>
            <CardContent>
              <ol className="space-y-2 text-sm">
                <Step n={1} title={t("help.step1t")}>
                  {t("help.step1a")}<code className="text-xs bg-muted px-1.5 py-0.5 rounded">admin</code>{t("help.step1b")}<code className="text-xs bg-muted px-1.5 py-0.5 rounded">INITIAL_ADMIN_PASSWORD</code>{t("help.step1c")}
                </Step>
                <Step n={2} title={t("help.step2t")}>
                  {t("help.step2a")}<b>{t("nav.dashboard")} → {t("nav.models")}</b>{t("help.step2b")}<Badge variant="warning" className="h-4 px-1.5">mock</Badge>{t("help.step2c")}
                </Step>
                <Step n={3} title={t("help.step3t")}>
                  <b>{t("help.knowledgeUpload")}</b>{t("help.step3a")}
                </Step>
                <Step n={4} title={t("help.step4t")}>
                  {t("help.step4body")}
                </Step>
              </ol>
            </CardContent>
          </Card>

          {/* Webhook setup */}
          <Card>
            <CardHeader><CardTitle className="text-sm flex items-center gap-2"><Webhook className="size-4 text-info" /> {t("help.webhooks")}</CardTitle></CardHeader>
            <CardContent className="space-y-4 text-sm">
              <p className="text-muted-foreground">
                {t("help.webhooksDesc")}
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
            <CardHeader><CardTitle className="text-sm flex items-center gap-2"><MessageSquare className="size-4 text-warning" /> {t("help.inboxTitle")}</CardTitle></CardHeader>
            <CardContent className="space-y-2 text-sm">
              <p>{t("help.inboxIntro")}</p>
              <ul className="list-disc pl-5 space-y-1 text-muted-foreground">
                <li>{t("help.inboxTakeover")}</li>
                <li>{t("help.inboxNotes")}</li>
                <li>{t("help.inboxResolve")}</li>
                <li>{t("help.inboxCsat")}</li>
              </ul>
            </CardContent>
          </Card>

          {/* Feature reference */}
          <Card>
            <CardHeader><CardTitle className="text-sm flex items-center gap-2"><BookOpen className="size-4 text-success" /> {t("help.features")}</CardTitle></CardHeader>
            <CardContent className="grid grid-cols-1 sm:grid-cols-2 gap-3 text-sm">
              <Feature icon={Bot} title={t("help.featStreamT")} desc={t("help.featStreamD")} />
              <Feature icon={BookOpen} title={t("help.featRagT")} desc={t("help.featRagD")} />
              <Feature icon={Shield} title={t("help.featRateT")} desc={t("help.featRateD")} />
              <Feature icon={Zap} title={t("help.featCacheT")} desc={t("help.featCacheD")} />
            </CardContent>
          </Card>

          {/* Security note */}
          <Card>
            <CardHeader><CardTitle className="text-sm flex items-center gap-2"><Shield className="size-4 text-danger" /> {t("help.security")}</CardTitle></CardHeader>
            <CardContent>
              <ul className="space-y-1.5 text-sm list-disc pl-5 text-muted-foreground">
                <li>{t("help.sec1a")}<code className="text-xs bg-muted px-1 rounded">POSTGRES_PASSWORD</code>{t("help.sec1b")}<code className="text-xs bg-muted px-1 rounded">JWT_SECRET</code>{t("help.sec1c")}<code className="text-xs bg-muted px-1 rounded">INITIAL_ADMIN_PASSWORD</code>{t("help.sec1d")}</li>
                <li>{t("help.sec2a")}<code className="text-xs bg-muted px-1 rounded">GEMINI_API_KEY</code>{t("help.sec2b")}</li>
                <li>{t("help.sec3a")}<code className="text-xs bg-muted px-1 rounded">ALLOWED_ORIGINS</code>{t("help.sec3b")}</li>
                <li>{t("help.sec4")}</li>
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
  const { t } = useI18n();
  return (
    <div className="rounded-md border border-border p-3">
      <div className="flex items-center justify-between mb-2">
        <p className="text-xs font-semibold">{name}</p>
        <a href={docsUrl} target="_blank" rel="noreferrer noopener" className="text-xs text-primary hover:underline">
          {t("help.docs")}
        </a>
      </div>
      <dl className="space-y-1 text-xs">
        <div className="flex gap-2">
          <dt className="text-muted-foreground w-28 flex-shrink-0">{t("help.callbackUrl")}</dt>
          <dd><code className="bg-muted px-1.5 py-0.5 rounded">{webhookUrl}</code></dd>
        </div>
        {verifyToken && (
          <div className="flex gap-2">
            <dt className="text-muted-foreground w-28 flex-shrink-0">{t("help.verifyToken")}</dt>
            <dd><code className="bg-muted px-1.5 py-0.5 rounded">{verifyToken}</code></dd>
          </div>
        )}
        <div className="flex gap-2">
          <dt className="text-muted-foreground w-28 flex-shrink-0">{t("help.required")}</dt>
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
