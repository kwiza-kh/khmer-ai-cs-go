"use client";

import { Languages, Check } from "lucide-react";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { LANGS, useI18n } from "@/lib/i18n";

/** Sidebar quick switcher for the interface language. */
export function LanguageSwitcher() {
  const { lang, setLang, t } = useI18n();

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        title={t("nav.language")}
        aria-label={t("nav.language")}
        className="inline-flex size-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-foreground/[0.06] hover:text-foreground"
      >
        <Languages className="size-4" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" side="top" className="w-44">
        {LANGS.map((option) => (
          <DropdownMenuItem
            key={option.key}
            onClick={() => setLang(option.key)}
            className="flex items-center justify-between text-xs"
          >
            <span>{option.full}</span>
            {lang === option.key && <Check className="size-3.5 text-primary" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
