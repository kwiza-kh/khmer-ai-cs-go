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

export function FeedbackTab() {
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
            {data?.total ?? 0} feedback entries
          </p>
          <Select value={filter} onValueChange={(v) => setFilter((v || "all") as "all" | "1" | "-1")}>
            <SelectTrigger className="w-32 h-7 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All</SelectItem>
              <SelectItem value="1">👍 Positive</SelectItem>
              <SelectItem value="-1">👎 Negative</SelectItem>
            </SelectContent>
          </Select>
        </div>

        {isLoading ? (
          <div className="py-10 text-center text-xs text-muted-foreground">Loading…</div>
        ) : messages.length === 0 ? (
          <EmptyState icon={MessageSquare} title="No feedback yet" description="User thumbs-up / thumbs-down on AI replies will appear here." />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="text-xs w-12">Rating</TableHead>
                <TableHead className="text-xs">Reply</TableHead>
                <TableHead className="text-xs">Comment</TableHead>
                <TableHead className="text-xs w-32">When</TableHead>
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
                    {m.feedback_at ? new Date(m.feedback_at).toLocaleString() : "—"}
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
