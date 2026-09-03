"use client";

import * as React from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeHighlight from "rehype-highlight";
import { CheckIcon, CopyIcon } from "lucide-react";

import "highlight.js/styles/github-dark.css";
import { cn } from "@/lib/utils";

/**
 * Markdown renderer used for AI replies.
 * - GFM (tables, task lists, strikethrough) via remark-gfm
 * - Code blocks with syntax highlighting via rehype-highlight + highlight.js
 * - Per-code-block copy button
 * - Safe by default (react-markdown does not allow raw HTML)
 */
export function Markdown({ children, className }: { children: string; className?: string }) {
  return (
    <div className={cn("prose-chat", className)}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[[rehypeHighlight, { detect: true, ignoreMissing: true }]]}
        components={{
          // Wrap <pre> so we can inject a copy button absolutely positioned.
          pre({ children, ...props }) {
            return <CodeBlock {...props}>{children}</CodeBlock>;
          },
          a({ children, ...props }) {
            return (
              <a {...props} target="_blank" rel="noreferrer noopener" className="text-primary underline underline-offset-2 hover:opacity-80">
                {children}
              </a>
            );
          },
        }}
      >
        {children}
      </ReactMarkdown>
    </div>
  );
}

function CodeBlock({ children, ...props }: React.HTMLAttributes<HTMLPreElement>) {
  const [copied, setCopied] = React.useState(false);

  // Extract the raw code text from the rendered <code> child for the copy button.
  let codeText = "";
  React.Children.forEach(children, (child) => {
    if (React.isValidElement(child) && typeof child.props === "object" && child.props !== null) {
      const maybeChildren = (child.props as { children?: unknown }).children;
      if (typeof maybeChildren === "string") codeText = maybeChildren;
      else if (Array.isArray(maybeChildren)) {
        codeText = maybeChildren.filter((c): c is string => typeof c === "string").join("");
      }
    }
  });

  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(codeText);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard blocked — silently ignore */
    }
  };

  return (
    <div className="relative group/code my-3">
      <button
        type="button"
        onClick={onCopy}
        className="absolute top-2 right-2 z-10 inline-flex items-center gap-1 rounded-md bg-background/80 border border-border px-1.5 py-1 text-[10px] text-muted-foreground opacity-0 group-hover/code:opacity-100 transition-opacity hover:text-foreground"
        aria-label="Copy code"
      >
        {copied ? <CheckIcon className="size-3" /> : <CopyIcon className="size-3" />}
        {copied ? "Copied" : "Copy"}
      </button>
      <pre {...props} className="overflow-x-auto rounded-lg bg-zinc-950 p-3 text-xs leading-relaxed">
        {children}
      </pre>
    </div>
  );
}
