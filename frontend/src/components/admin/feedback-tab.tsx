"use client";

import * as React from "react";
import useSWR from "swr";
import { ChatMessageItem, listFeedback } from "@/lib/api";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { EmptyState } from "@/components/empty-state";
import { ThumbsUp, ThumbsDown, MessageSquare } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { fmtDateTime } from "@/lib/format";

export function FeedbackTab() {
  const { t, tf } = useI18n();
  const [filter, setFilter] = React.useState<"all" | "1" | "-1">("all");
  const key = `feedback-${filter}`;
  const { data, isLoading } = useSWR(key, () =>
    listFeedback(1, 100, filter === "all" ? undefined : (Number(filter) as -1 | 1)),
  );
  const messages: ChatMessageItem[] = (data?.data ?? []) as ChatMessageItem[];

  return (
    <Card>
      <CardContent className="pt-5">
        <div className="flex items-center justify-between mb-3">
          <p className="text-xs text-muted-foreground">
            {tf("fb.count", { n: data?.total ?? 0 })}
          </p>
          <Select value={filter} onValueChange={(v) => setFilter((v || "all") as "all" | "1" | "-1")}>
            <SelectTrigger className="w-32 h-7 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("inbox.filterAll")}</SelectItem>
              <SelectItem value="1">{t("fb.positive")}</SelectItem>
              <SelectItem value="-1">{t("fb.negative")}</SelectItem>
            </SelectContent>
          </Select>
        </div>

        {isLoading ? (
          <div className="py-10 text-center text-xs text-muted-foreground">{t("settings.loading")}</div>
        ) : messages.length === 0 ? (
          <EmptyState icon={MessageSquare} title={t("fb.emptyTitle")} description={t("fb.emptyDesc")} />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="text-xs w-12">{t("fb.colRating")}</TableHead>
                <TableHead className="text-xs">{t("fb.colReply")}</TableHead>
                <TableHead className="text-xs">{t("fb.colComment")}</TableHead>
                <TableHead className="text-xs w-32">{t("fb.colWhen")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {messages.map((m) => (
                <TableRow key={m.message_id}>
                  <TableCell>
                    {m.feedback_rating === 1
                      ? <Badge variant="success" className="gap-1"><ThumbsUp className="size-3" /></Badge>
                      : <Badge variant="destructive" className="gap-1"><ThumbsDown className="size-3" /></Badge>}
                  </TableCell>
                  <TableCell className="text-xs max-w-md">
                    <span className="line-clamp-2">{m.content}</span>
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground max-w-xs">
                    <span className="line-clamp-2">{m.feedback_comment || "—"}</span>
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {m.feedback_at ? fmtDateTime(m.feedback_at) : "—"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}
