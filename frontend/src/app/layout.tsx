import "./globals.css";
import { Inter, Noto_Sans_Khmer } from "next/font/google";
import { AuthProvider } from "@/lib/auth-client";
import { AuthGuard } from "@/components/auth-guard";
import { ThemeProvider } from "@/components/theme-provider";
import { SWRProvider } from "@/lib/swr-provider";
import { I18nProvider } from "@/lib/i18n";
import { Toaster } from "@/components/toaster";

// Inter: 拉丁字符主字体 (清晰、紧凑、专业)
// 通过 variable 暴露为 --font-sans,在 globals.css 中被引用.
const inter = Inter({
  subsets: ["latin"],
  variable: "--font-sans",
  display: "swap",
});

// Noto Sans Khmer: 高棉语专用,Inter 不含高棉字形时浏览器自动 fallback 到这里.
// 暴露为 --font-khmer,与 --font-sans 组合成完整字体栈.
const notoSansKhmer = Noto_Sans_Khmer({
  subsets: ["khmer"],
  variable: "--font-khmer",
  weight: ["400", "500", "600", "700"],
  display: "swap",
});

export const metadata = {
  title: "RelayChat",
  description: "ប្រព័ន្ធ AI បម្រើអតិថិជនភាសាខ្មែរ · 高棉语AI客服系统",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    // suppressHydrationWarning: next-themes 在客户端给 <html> 写 class,会与 SSR 不一致.
    <html lang="en" suppressHydrationWarning className={`${inter.variable} ${notoSansKhmer.variable}`}>
      <body className="antialiased">
        <ThemeProvider
          attribute="class"
          defaultTheme="light"
          disableTransitionOnChange
        >
          <I18nProvider>
            <AuthProvider>
              <SWRProvider>
                <AuthGuard>{children}</AuthGuard>
              </SWRProvider>
            </AuthProvider>
            <Toaster />
          </I18nProvider>
        </ThemeProvider>
      </body>
    </html>
  );
}
