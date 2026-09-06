import type { Metadata } from "next";

export const metadata: Metadata = {
  title: "Privacy Policy | Khmer AI Customer Service",
  description: "How Khmer AI Customer Service collects, uses, and deletes data.",
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
      <p className="text-sm font-medium text-muted-foreground">Khmer AI Customer Service</p>
      <h1 className="mt-2 text-3xl font-bold text-foreground">Privacy Policy / គោលការណ៍ភាពឯកជន / 隐私政策</h1>
      <P>
        Last updated: September 6, 2026 · This policy explains how the Khmer AI Customer Service
        platform (&quot;we&quot;, &quot;our service&quot;) processes data when businesses connect their
        Facebook Pages and Instagram professional accounts, and when customers message those
        accounts. / គោលការណ៍នេះពន្យល់ពីរបៀបដែលយើងដំណើរការទិន្នន័យ។ / 本政策说明我们如何处理通过本平台收发的数据。
      </P>

      <H>1. Who we are / អំពីយើង / 关于我们</H>
      <P>
        Khmer AI Customer Service is a customer-service platform that lets businesses answer
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
        We share data only with the providers required to run the service: Meta Platforms, Inc.
        (receiving and sending Facebook/Instagram messages via their APIs) and Google (message text
        processed by Gemini to draft replies). We do not sell personal data. / 我们不会出售个人数据。
      </P>

      <H>5. Storage and retention / ការរក្សាទុក / 存储与保留</H>
      <P>
        Data is stored on servers operated by us and retained while your business account is active,
        or as required by law. You can disconnect a channel at any time; new messages stop
        immediately after disconnection.
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
        Contact the business you were chatting with, or email b1783467220@gmail.com with the page
        name and your message handle. We delete the conversation data within 30 days.
      </P>

      <H>7. Contact / ទំនាក់ទំនង / 联系我们</H>
      <P>
        Questions or deletion requests: b1783467220@gmail.com
      </P>
    </main>
  );
}
