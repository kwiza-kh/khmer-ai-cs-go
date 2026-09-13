"use client";

import * as React from "react";
import useSWR, { mutate as globalMutate } from "swr";
import {
  BusinessHours,
  listBusinessHours, upsertBusinessHours, isBusinessOpen,
  listCannedResponses, createCannedResponse, deleteCannedResponse,
} from "@/lib/api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/empty-state";
import { Clock, Zap, Plus, Trash2, Loader2, CheckCircle2, XCircle } from "lucide-react";
import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";

// Weekday labels resolve through i18n at render time.
const WEEKDAY_KEYS = ["ops.day0", "ops.day1", "ops.day2", "ops.day3", "ops.day4", "ops.day5", "ops.day6"];

/**
 * Admin "Operations" tab: business hours scheduler + canned responses library.
 * Split into a self-contained component so admin/page.tsx stays readable.
 */
export function OperationsTab() {
  return (
    <div className="space-y-4">
      <BusinessHoursCard />
      <CannedResponsesCard />
    </div>
  );
}

// ============================================
// Business hours
// ============================================

export function BusinessHoursCard() {
  const { t } = useI18n();
  const { data: hours } = useSWR("business-hours", listBusinessHours);
  const { data: open } = useSWR("business-open", () => isBusinessOpen());

  // Derive the 7-row schedule straight from the server data with useMemo (no
  // useEffect + setState → no cascading renders). Local edits override the
  // seed row per weekday until save mutates the SWR cache.
  const seed = React.useMemo<BusinessHours[]>(() => {
    const byDay = new Map<number, BusinessHours>();
    (hours ?? []).forEach((h) => byDay.set(h.weekday, h));
    return WEEKDAY_KEYS.map((_, i) => byDay.get(i) ?? {
      weekday: i, open_time: "09:00", close_time: "17:00", is_active: i !== 0, // Sun closed by default
    });
  }, [hours]);

  const [edits, setEdits] = React.useState<Record<number, BusinessHours>>({});
  const setRow = (i: number, patch: Partial<BusinessHours>) =>
    setEdits((prev) => ({ ...prev, [seed[i].weekday]: { ...seed[i], ...(prev[seed[i].weekday] ?? {}), ...patch } }));
  const schedule = seed.map((row) => edits[row.weekday] ?? row);

  const [saving, setSaving] = React.useState(false);
  const handleSave = async () => {
    setSaving(true);
    try {
      await upsertBusinessHours(schedule);
      setEdits({});
      toast.success(t("ops.hoursSaved"));
      void globalMutate("business-hours");
      void globalMutate("business-open");
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card>
      <CardHeader className="pb-2">
        <div className="flex items-center justify-between">
          <CardTitle className="text-sm flex items-center gap-2">
            <Clock className="size-4 text-primary" /> {t("ops.businessHours")}
          </CardTitle>
          <Badge variant={open?.open ? "success" : "secondary"} className="gap-1 text-xs h-5">
            {open?.open ? <><CheckCircle2 className="size-3" />{t("ops.openNow")}</> : <><XCircle className="size-3" />{t("ops.closedNow")}</>}
          </Badge>
        </div>
      </CardHeader>
      <CardContent className="space-y-2">
        <p className="text-xs text-muted-foreground">
          {t("ops.hoursDesc")}
        </p>
        <div className="space-y-1.5">
          {schedule.map((row, i) => (
            <div key={i} className="grid grid-cols-[100px_1fr_1fr_32px] items-center gap-2">
              <span className="text-xs">{t(WEEKDAY_KEYS[row.weekday])}</span>
              <Input
                type="time"
                value={row.open_time || ""}
                onChange={(e) => setRow(i, { open_time: e.target.value })}
                className="h-7 text-xs"
                disabled={!row.is_active}
              />
              <Input
                type="time"
                value={row.close_time || ""}
                onChange={(e) => setRow(i, { close_time: e.target.value })}
                className="h-7 text-xs"
                disabled={!row.is_active}
              />
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => setRow(i, { is_active: !row.is_active })}
                title={row.is_active ? t("ops.openDay") : t("ops.closedDay")}
              >
                {row.is_active ? <CheckCircle2 className="size-3.5 text-success" /> : <XCircle className="size-3.5 text-muted-foreground" />}
              </Button>
            </div>
          ))}
        </div>
        <Button onClick={handleSave} disabled={saving} className="h-8 text-xs gap-1.5">
          {saving ? <Loader2 className="size-3 animate-spin" /> : <Clock className="size-3" />}{t("ops.saveSchedule")}
        </Button>
      </CardContent>
    </Card>
  );
}

// ============================================
// Canned responses
// ============================================

export function CannedResponsesCard() {
  const { t } = useI18n();
  const { data: responses, mutate } = useSWR("canned-responses", () => listCannedResponses());
  const [title, setTitle] = React.useState("");
  const [body, setBody] = React.useState("");
  const [category, setCategory] = React.useState("");
  const [lang, setLang] = React.useState("km");
  const [saving, setSaving] = React.useState(false);

  const handleAdd = async () => {
    if (!title.trim() || !body.trim()) return;
    setSaving(true);
    try {
      await createCannedResponse({ title, body, category, language: lang });
      setTitle(""); setBody(""); setCategory("");
      toast.success(t("ops.added"));
      void mutate();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async (id: number) => {
    try {
      await deleteCannedResponse(id);
      toast.success(t("ops.deleted"));
      void mutate();
    } catch (e) {
      toast.error((e as Error).message);
    }
  };

  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-sm flex items-center gap-2">
          <Zap className="size-4 text-warning" /> {t("ops.quickReplies")}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <p className="text-xs text-muted-foreground">
          {t("ops.quickDesc")}
        </p>

        {/* New entry */}
        <div className="rounded-md border border-border p-3 space-y-2 bg-muted/30">
          <div className="grid grid-cols-1 sm:grid-cols-[1fr_120px_80px] gap-2">
            <Input placeholder={t("ops.titlePh")} value={title} onChange={(e) => setTitle(e.target.value)} className="h-8 text-xs" />
            <Input placeholder={t("ops.categoryPh")} value={category} onChange={(e) => setCategory(e.target.value)} className="h-8 text-xs" />
            <select value={lang} onChange={(e) => setLang(e.target.value)} className="h-8 text-xs rounded-md border border-input bg-transparent px-2">
              <option value="km">km</option>
              <option value="en">en</option>
              <option value="zh">zh</option>
            </select>
          </div>
          <Textarea placeholder={t("ops.bodyPh")} value={body} onChange={(e) => setBody(e.target.value)} rows={2} className="text-xs resize-none" />
          <Button onClick={handleAdd} disabled={saving || !title.trim() || !body.trim()} size="sm" className="h-7 text-xs gap-1">
            <Plus className="size-3" /> {t("ops.add")}
          </Button>
        </div>

        {/* List */}
        {!responses || responses.length === 0 ? (
          <EmptyState icon={Zap} title={t("ops.emptyTitle")} />
        ) : (
          <div className="space-y-1.5">
            {responses.map((r) => (
              <div key={r.id} className="rounded-md border border-border p-2.5 group">
                <div className="flex items-start justify-between gap-2">
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-1.5">
                      <p className="text-xs font-medium truncate">{r.title}</p>
                      {r.category && <Badge variant="outline" className="h-4 px-1 text-[11px]">{r.category}</Badge>}
                      <Badge variant="secondary" className="h-4 px-1 text-[11px] uppercase">{r.language}</Badge>
                    </div>
                    <p className="text-xs text-muted-foreground mt-1 line-clamp-2">{r.body}</p>
                  </div>
                  <button
                    onClick={() => handleDelete(r.id)}
                    className="opacity-100 transition-opacity text-muted-foreground hover:text-danger sm:opacity-0 sm:group-hover:opacity-100 sm:focus-visible:opacity-100 flex-shrink-0"
                    title={t("kb.delete")}
                  >
                    <Trash2 className="size-3.5" />
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
