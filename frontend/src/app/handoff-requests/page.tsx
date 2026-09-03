"use client";

import * as React from "react";
import useSWR from "swr";
import { useRouter } from "next/navigation";
import {
  ArrowUpRight, CheckCircle2, CircleAlert, Clock3, Headset, Loader2, Plus, RefreshCw, UserRound,
} from "lucide-react";
import {
  createHumanHandoffRequest, HumanHandoffPriority, HumanHandoffRequest, HumanHandoffRequestStatus,
  InboxItem, listHumanHandoffRequests, listInbox, takeoverSession, updateSessionStatus,
} from "@/lib/api";
import { useAuth } from "@/lib/auth-client";
import { useInboxRealtime } from "@/lib/realtime";
import { PageHeader } from "@/components/page-header";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/empty-state";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Textarea } from "@/components/ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { toast } from "sonner";

type RequestFilter = "all" | HumanHandoffRequestStatus;

const FILTERS: { value: RequestFilter; label: string }[] = [
  { value: "all", label: "All" },
  { value: "pending", label: "Waiting" },
  { value: "assigned", label: "Assigned" },
  { value: "resolved", label: "Resolved" },
];

const TRIGGER_LABELS: Record<HumanHandoffRequest["trigger"], string> = {
  customer_request: "Customer request",
  negative_feedback: "Negative feedback",
  ai_decision: "AI decision",
  manual: "Manual request",
};

const STATUS_LABELS: Record<HumanHandoffRequestStatus, string> = {
  pending: "Waiting",
  assigned: "Assigned",
  resolved: "Resolved",
};

function formatTime(value?: string | null) {
  if (!value) return "-";
  return new Intl.DateTimeFormat("en-US", { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}

function customerName(request: HumanHandoffRequest) {
  return request.user_display_name || request.session_title || request.platform_user_id || "Customer";
}

function conversationName(item: InboxItem) {
  return item.user_display_name || item.title || item.platform_user_id || "Customer";
}

export default function HandoffRequestsPage() {
  const router = useRouter();
  const { token } = useAuth();
  const [filter, setFilter] = React.useState<RequestFilter>("pending");
  const [workingID, setWorkingID] = React.useState<string | null>(null);
  const [createOpen, setCreateOpen] = React.useState(false);
  const { data, mutate, isLoading } = useSWR(
    `handoff-requests-${filter}`,
    () => listHumanHandoffRequests({ status: filter === "all" ? undefined : filter, pageSize: 100 }),
  );
  const requests = data?.data ?? [];

  useInboxRealtime(token, React.useCallback(() => { void mutate(); }, [mutate]));

  const takeOver = async (request: HumanHandoffRequest) => {
    setWorkingID(request.request_id);
    try {
      await takeoverSession(request.session_id);
      toast.success("Conversation assigned to you");
      await mutate();
    } catch (error) {
      toast.error((error as Error).message || "Could not take over conversation");
    } finally {
      setWorkingID(null);
    }
  };

  const resolve = async (request: HumanHandoffRequest) => {
    setWorkingID(request.request_id);
    try {
      await updateSessionStatus(request.session_id, "resolved");
      toast.success("Human request resolved");
      await mutate();
    } catch (error) {
      toast.error((error as Error).message || "Could not resolve request");
    } finally {
      setWorkingID(null);
    }
  };

  return (
    <div className="flex h-full flex-col">
      <PageHeader
        icon={Headset}
        kicker="Operations"
        title="Human requests"
        description="AI and customer requests that need a live agent."
        actions={
          <div className="flex items-center gap-2">
            <Button size="sm" onClick={() => setCreateOpen(true)} className="gap-1.5">
              <Plus className="size-3.5" /> Create request
            </Button>
            <Button size="sm" variant="outline" onClick={() => void mutate()} className="gap-1.5" disabled={isLoading}>
              <RefreshCw className={isLoading ? "size-3.5 animate-spin" : "size-3.5"} /> Refresh
            </Button>
          </div>
        }
      />

      <main className="flex-1 overflow-hidden">
        <div className="mx-auto flex h-full w-full max-w-7xl flex-col p-5 sm:p-8">
          <div className="mb-5 flex flex-wrap items-center justify-between gap-3 border-b border-border pb-4">
            <div className="flex flex-wrap gap-1" role="tablist" aria-label="Human request status">
              {FILTERS.map((item) => (
                <Button
                  key={item.value}
                  type="button"
                  size="sm"
                  variant={filter === item.value ? "default" : "ghost"}
                  onClick={() => setFilter(item.value)}
                  aria-selected={filter === item.value}
                  className="h-8 px-3 text-xs"
                >
                  {item.label}
                </Button>
              ))}
            </div>
            <p className="text-xs text-muted-foreground">{data?.total ?? 0} requests</p>
          </div>

          {isLoading ? (
            <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground"><Loader2 className="mr-2 size-4 animate-spin" /> Loading requests</div>
          ) : requests.length === 0 ? (
            <div className="flex flex-1 items-center justify-center border border-dashed border-border">
              <EmptyState icon={Headset} title="No human requests" description="Requests created by customers or the AI will appear here." />
            </div>
          ) : (
            <ScrollArea className="min-h-0 flex-1 border border-border">
              <div className="divide-y divide-border">
                {requests.map((request) => {
                  const isWorking = workingID === request.request_id;
                  const openInbox = () => router.push(`/inbox?session=${encodeURIComponent(request.session_id)}`);
                  return (
                    <article key={request.request_id} className="grid gap-4 p-4 sm:grid-cols-[minmax(0,1fr)_13rem] sm:items-center sm:p-5">
                      <div className="min-w-0">
                        <div className="flex flex-wrap items-center gap-2">
                          <p className="truncate text-sm font-semibold text-foreground">{customerName(request)}</p>
                          <Badge variant={request.status === "resolved" ? "success" : request.status === "assigned" ? "info" : "warning"} className="h-5 px-1.5 text-[10px]">
                            {STATUS_LABELS[request.status]}
                          </Badge>
                          {request.priority === "high" && <Badge variant="destructive" className="h-5 gap-1 px-1.5 text-[10px]"><CircleAlert className="size-3" /> High priority</Badge>}
                        </div>
                        <p className="mt-1.5 text-sm leading-6 text-foreground">{request.reason}</p>
                        {request.last_message && <p className="mt-1.5 line-clamp-1 text-xs text-muted-foreground">Latest: {request.last_message}</p>}
                        <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
                          <span className="inline-flex items-center gap-1"><Clock3 className="size-3" /> {formatTime(request.created_at)}</span>
                          <span>{TRIGGER_LABELS[request.trigger]}</span>
                          {request.platform && <span className="capitalize">{request.platform === "meta" ? "Messenger" : request.platform}</span>}
                          {request.assigned_agent_name && <span className="inline-flex items-center gap-1"><UserRound className="size-3" /> {request.assigned_agent_name}</span>}
                        </div>
                      </div>
                      <div className="flex shrink-0 flex-wrap items-center gap-2 sm:justify-end">
                        <Button type="button" size="sm" variant="outline" onClick={openInbox} className="h-8 gap-1.5 text-xs">
                          Open Inbox <ArrowUpRight className="size-3.5" />
                        </Button>
                        {request.status === "pending" && (
                          <Button type="button" size="sm" onClick={() => void takeOver(request)} disabled={isWorking} className="h-8 gap-1.5 text-xs">
                            {isWorking ? <Loader2 className="size-3.5 animate-spin" /> : <Headset className="size-3.5" />} Take over
                          </Button>
                        )}
                        {request.status === "assigned" && (
                          <Button type="button" size="sm" variant="secondary" onClick={() => void resolve(request)} disabled={isWorking} className="h-8 gap-1.5 text-xs">
                            {isWorking ? <Loader2 className="size-3.5 animate-spin" /> : <CheckCircle2 className="size-3.5" />} Resolve
                          </Button>
                        )}
                      </div>
                    </article>
                  );
                })}
              </div>
            </ScrollArea>
          )}
        </div>
      </main>
      <CreateHumanRequestDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={() => {
          setFilter("pending");
          void mutate();
        }}
      />
    </div>
  );
}

function CreateHumanRequestDialog({
  open, onOpenChange, onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: () => void;
}) {
  const [sessionID, setSessionID] = React.useState("");
  const [reason, setReason] = React.useState("");
  const [priority, setPriority] = React.useState<HumanHandoffPriority>("normal");
  const [submitting, setSubmitting] = React.useState(false);
  const { data, isLoading } = useSWR(
    open ? "handoff-request-conversations" : null,
    () => listInbox({ pageSize: 100 }),
  );
  const conversations = data?.data ?? [];
  const selected = conversations.find((item) => item.session_id === sessionID);

  const handleOpenChange = (nextOpen: boolean) => {
    if (!nextOpen) {
      setSessionID("");
      setReason("");
      setPriority("normal");
    }
    onOpenChange(nextOpen);
  };

  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!sessionID || !reason.trim()) return;
    setSubmitting(true);
    try {
      const result = await createHumanHandoffRequest({ session_id: sessionID, reason: reason.trim(), priority });
      toast.success(result.created ? "Human request created" : "An open request already exists for this conversation");
      handleOpenChange(false);
      onCreated();
    } catch (error) {
      toast.error((error as Error).message || "Could not create human request");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Create human request</DialogTitle>
          <DialogDescription>Move an existing customer conversation into the live-agent queue. Test conversations are excluded.</DialogDescription>
        </DialogHeader>
        <form className="space-y-4" onSubmit={submit}>
          <label className="block space-y-1.5 text-sm font-medium text-foreground">
            Conversation
            <Select value={sessionID} onValueChange={(value) => setSessionID(value ?? "")}>
              <SelectTrigger className="h-9 w-full font-normal"><SelectValue placeholder={isLoading ? "Loading conversations..." : "Select a customer conversation"} /></SelectTrigger>
              <SelectContent>
                {conversations.map((conversation) => (
                  <SelectItem key={conversation.session_id} value={conversation.session_id}>
                    {conversationName(conversation)}{conversation.platform ? ` - ${conversation.platform === "meta" ? "Messenger" : conversation.platform}` : ""}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </label>
          {selected && <p className="-mt-2 text-xs text-muted-foreground">Latest message: {selected.last_message || "No messages yet"}</p>}
          <label className="block space-y-1.5 text-sm font-medium text-foreground">
            Priority
            <Select value={priority} onValueChange={(value) => setPriority((value || "normal") as HumanHandoffPriority)}>
              <SelectTrigger className="h-9 w-full font-normal"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="normal">Normal</SelectItem>
                <SelectItem value="high">High</SelectItem>
              </SelectContent>
            </Select>
          </label>
          <label className="block space-y-1.5 text-sm font-medium text-foreground">
            Reason
            <Textarea value={reason} onChange={(event) => setReason(event.target.value)} maxLength={500} rows={4} placeholder="Explain why this conversation needs an agent" className="resize-none text-sm" />
          </label>
          <DialogFooter className="mt-5">
            <Button type="button" variant="outline" onClick={() => handleOpenChange(false)} disabled={submitting}>Cancel</Button>
            <Button type="submit" disabled={!sessionID || !reason.trim() || submitting} className="gap-1.5">
              {submitting && <Loader2 className="size-3.5 animate-spin" />} Create request
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
