"use client";

// PasswordInput — a password field with a show/hide toggle. Typing a long
// password on a phone with no way to reveal it is a common source of
// mistyped credentials and failed logins.
import * as React from "react";
import { Input } from "@/components/ui/input";
import { Eye, EyeOff } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

type Props = React.ComponentProps<typeof Input>;

export function PasswordInput({ className, ...props }: Props) {
  const { t } = useI18n();
  const [visible, setVisible] = React.useState(false);
  return (
    <div className="relative">
      <Input
        {...props}
        type={visible ? "text" : "password"}
        className={cn("pr-10", className)}
      />
      <button
        type="button"
        onClick={() => setVisible((v) => !v)}
        aria-label={visible ? t("login.hidePassword") : t("login.showPassword")}
        className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground transition-colors hover:text-foreground"
      >
        {visible ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
      </button>
    </div>
  );
}
