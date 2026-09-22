import type { Metadata } from "next";

export const metadata: Metadata = {
  title: "Privacy Policy | RelayChat",
  description: "How RelayChat collects, uses, and deletes data.",
};

function H({ children }: { children: React.ReactNode }) {
  return <h2 className="mt-10 border-b border-border pb-2 text-xl font-semibold text-foreground">{children}</h2>;
}

function H3({ children }: { children: React.ReactNode }) {
  return <h3 className="mt-6 text-base font-semibold text-foreground">{children}</h3>;
}

function P({ children }: { children: React.ReactNode }) {
  return <p className="mt-3 leading-relaxed text-muted-foreground">{children}</p>;
}

function L({ children }: { children: React.ReactNode }) {
  return <li className="mt-2 leading-relaxed text-muted-foreground">{children}</li>;
}

export default function PrivacyPolicyPage() {
  return (
    <main className="mx-auto max-w-3xl px-5 py-12">
      <p className="text-sm font-medium text-muted-foreground">RelayChat</p>
      <h1 className="mt-2 text-3xl font-bold text-foreground">Privacy Policy / គោលការណ៍ភាពឯកជន / 隐私政策</h1>
      <P>
        Last updated: September 6, 2026 · This policy explains how the RelayChat
        platform (&quot;we&quot;, &quot;our service&quot;) processes data when businesses connect their
        Facebook Pages and Instagram professional accounts, and when customers message those
        accounts. / គោលការណ៍នេះពន្យល់ពីរបៀបដែលយើងដំណើរការទិន្នន័យ។ / 本政策说明我们如何处理通过本平台收发的数据。
      </P>

      <H>1. Who we are / អំពីយើង / 关于我们</H>
      <P>
        RelayChat is a customer-service platform that lets businesses answer
        Messenger and Instagram messages with AI assistance and human agents. Contact:
        b1783467220@gmail.com
      </P>

      <H>2. Data we process / ទិន្នន័យដែលយើងដំណើរការ / 我们处理的数据</H>
      <ul className="list-disc pl-6">
        <L>Business connection data: Facebook Page IDs and names, Instagram professional account IDs and names you connect.</L>
        <L>Customer messages: the content, timestamps, and platform-scoped sender IDs of messages customers send to your connected Page or Instagram account, and the replies sent back.</L>
        <L>Account data: the username and preference settings of your platform users (agents).</L>
        <L>Technical logs needed to deliver the service securely.</L>
      </ul>

      <H>3. How data is used / ការប្រើប្រាស់ទិន្នន័យ / 数据用途</H>
      <ul className="list-disc pl-6">
        <L>Delivering the service: receiving, storing, and displaying conversations between your customers and your business.</L>
        <L>AI assistance: customer message text is sent to an AI provider (Google Gemini) to draft suggested replies; a human agent or the automation decides what is sent.</L>
        <L>Service quality: analytics such as response times and conversation outcomes.</L>
      </ul>

      <H>4. Sharing with third parties / ការចែករំលែក / 第三方共享</H>
      <P>
        We share data only with the providers required to run the service, and we do not sell
        personal data. Depending on which channels your business connects, those providers are: /
        我们不会出售个人数据。
      </P>
      <ul className="list-disc pl-6">
        <L>
          <b>Meta Platforms, Inc.</b> — receiving and sending Facebook Page, Instagram and WhatsApp
          messages through their APIs.
        </L>
        <L>
          <b>Google</b> — message text, images, voice notes and uploaded documents are processed by
          Gemini to draft replies and to compute search embeddings.
        </L>
        <L>
          <b>TypeSafe (System One)</b> — the same message text, the drafted reply and the retrieved
          knowledge passages are sent to a judgment model that decides routing, whether a message
          needs a knowledge-base lookup, whether a reply should go to a human, and whether a drafted
          reply is actually supported by the retrieved passages.
        </L>
        <L>
          <b>Cloudflare</b> — content delivery, the AI gateway that relays Google requests, and object
          storage (Cloudflare R2) for chat attachments, agent avatars and voice recordings.
        </L>
        <L>
          <b>Telegram</b> — message delivery when a Telegram bot is connected, and notifications to
          your own staff.
        </L>
        <L>
          <b>LINE and Zalo</b> — message delivery when those channels are connected.
        </L>
        <L>
          <b>Twilio</b> — voice calls, only where the voice feature is enabled.
        </L>
        <L>
          <b>Our email provider</b> — transactional email such as sign-in and notification messages.
        </L>
      </ul>
      <P>
        These providers are located in the United States and the European Union and process data on
        our instructions under their own data-processing terms.
      </P>

      <H>5. Storage and retention / ការរក្សាទុក / 存储与保留</H>
      <P>
        Data is stored on servers we operate in <b>Hong Kong</b>, and is transferred to the providers
        listed above in the United States and the European Union in order to deliver the service.
        Conversations are retained while your business account is active, or as required by law. You
        can disconnect a channel at any time; new messages stop immediately after disconnection.
      </P>

      <H>6. Data deletion / ការលុបទិន្នន័យ / 数据删除</H>
      <H3>For businesses using the platform</H3>
      <ul className="list-disc pl-6">
        <L>Disconnect a channel: in the platform, open 平台集成 (Platforms) and disconnect the Page/Instagram account. Stored credentials for that channel are deleted immediately.</L>
        <L>Delete your account and all associated conversations: email b1783467220@gmail.com with your username. We complete deletion within 30 days and confirm by email.</L>
        <L>Remove the app&apos;s access to your Facebook assets: Facebook Settings → Business Integrations, or Instagram Settings → Website permissions → Apps and websites.</L>
      </ul>
      <H3>For customers who messaged a business through this platform</H3>
      <P>
        Removing this app in Facebook Settings, or submitting a data deletion request through Meta,
        reaches our automated deletion callback. We verify Meta&apos;s signature, remove the records
        we hold for the identifier Meta supplies, and return a confirmation code with a link to a
        status page where you can follow the outcome.
      </P>
      <P>
        Some requests cannot be matched automatically: Meta supplies an app-scoped identifier, while
        conversations are stored under the channel-scoped identifier of the business you messaged.
        Those requests are recorded as needing review rather than reported as deleted. In that case,
        or to delete a conversation directly, contact the business you were chatting with, or email
        b1783467220@gmail.com with the page name and your message handle. We delete the conversation
        data within 30 days.
      </P>

      <H>7. Contact / ទំនាក់ទំនង / 联系我们</H>
      <P>
        Questions or deletion requests: b1783467220@gmail.com
      </P>
    </main>
  );
}
