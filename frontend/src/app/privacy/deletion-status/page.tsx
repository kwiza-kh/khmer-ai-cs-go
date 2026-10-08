"use client";

import { Suspense, useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { API_BASE } from "@/lib/auth-client";

// Public status page for a Meta Data Deletion Request. The URL and the
// confirmation code are handed to the data subject by Meta's callback, so this
// page must render without a session (see the public-route list in
// components/auth-guard.tsx). It shows only that one request's own outcome —
// never another subject's data, and never deleted content.

type Status = "completed" | "unresolved" | "failed" | "pending";

type DeletionStatus = {
  status: Status;
  requested_at?: string;
  completed_at?: string | null;
  records_removed?: number;
  note?: string;
};

const T = {
  title: "Data Deletion Status / ស្ថានភាពលុបទិន្នន័យ / 数据删除状态",
  code: "Confirmation code / លេខកូដបញ្ជាក់ / 确认码",
  requested: "Requested at / ពេលស្នើ / 申请时间",
  completed: "Completed at / ពេលបញ្ចប់ / 完成时间",
  removed: "Records removed / កំណត់ត្រាដែលបានលុប / 已删除记录数",
  loading: "Looking up… / កំពុងស្វែងរក… / 查询中…",
  noCode: "No confirmation code was supplied. / គ្មានលេខកូដបញ្ជាក់។ / 未提供确认码。",
  notFound: "That confirmation code is not recognised. / លេខកូដនេះមិនស្គាល់។ / 该确认码无效。",
  error: "Lookup failed. Please try again later. / ស្វែងរកបរាជ័យ។ / 查询失败，请稍后重试。",
  contact: "Questions: ",
};

const LABEL: Record<Status, string> = {
  completed: "Completed / បានបញ្ចប់ / 已完成",
  unresolved: "Needs review / ត្រូវការពិនិត្យ / 待人工核实",
  failed: "Failed / បរាជ័យ / 失败",
  pending: "Pending / កំពុងរង់ចាំ / 处理中",
};

const TONE: Record<Status, string> = {
  completed: "border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
  unresolved: "border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-400",
  failed: "border-destructive/40 bg-destructive/10 text-destructive",
  pending: "border-border bg-muted text-muted-foreground",
};

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="mt-3 flex flex-wrap items-baseline gap-x-3">
      <span className="text-sm font-medium text-muted-foreground">{label}</span>
      <span className="text-sm text-foreground">{value}</span>
    </div>
  );
}

function DeletionStatusInner() {
  const params = useSearchParams();
  const code = (params.get("code") || "").trim();
  // The initial state is derived from the code we already have, so the effect
  // never has to set state synchronously (which would cost an extra render).
  const [state, setState] = useState<"loading" | "ok" | "notfound" | "error" | "nocode">(
    code ? "loading" : "nocode",
  );
  const [data, setData] = useState<DeletionStatus | null>(null);

  useEffect(() => {
    if (!code) return;
    let alive = true;
    fetch(`${API_BASE}/privacy/deletion-status?code=${encodeURIComponent(code)}`)
      .then(async (res) => {
        if (!alive) return;
        if (res.status === 404) {
          setState("notfound");
          return;
        }
        if (!res.ok) {
          setState("error");
          return;
        }
        setData((await res.json()) as DeletionStatus);
        setState("ok");
      })
      .catch(() => {
        if (alive) setState("error");
      });
    return () => {
      alive = false;
    };
  }, [code]);

  return (
    <main className="mx-auto max-w-3xl px-5 py-12">
      <p className="text-sm font-medium text-muted-foreground">RelayChat</p>
      <h1 className="mt-2 text-2xl font-bold text-foreground">{T.title}</h1>

      <Row label={T.code} value={code || "—"} />

      {state === "loading" && <p className="mt-6 text-muted-foreground">{T.loading}</p>}
      {state === "nocode" && <p className="mt-6 text-muted-foreground">{T.noCode}</p>}
      {state === "notfound" && <p className="mt-6 text-muted-foreground">{T.notFound}</p>}
      {state === "error" && <p className="mt-6 text-muted-foreground">{T.error}</p>}

      {state === "ok" && data && (
        <>
          <div className={`mt-6 inline-block rounded-md border px-3 py-1 text-sm font-semibold ${TONE[data.status] ?? TONE.pending}`}>
            {LABEL[data.status] ?? data.status}
          </div>
          {data.requested_at && <Row label={T.requested} value={new Date(data.requested_at).toLocaleString()} />}
          {data.completed_at && <Row label={T.completed} value={new Date(data.completed_at).toLocaleString()} />}
          {typeof data.records_removed === "number" && (
            <Row label={T.removed} value={String(data.records_removed)} />
          )}
          {data.note && <p className="mt-6 leading-relaxed text-muted-foreground">{data.note}</p>}
        </>
      )}

      <p className="mt-10 border-t border-border pt-4 text-sm text-muted-foreground">
        {T.contact}
        <a className="underline" href="mailto:b1783467220@gmail.com">
          b1783467220@gmail.com
        </a>
      </p>
    </main>
  );
}

export default function DeletionStatusPage() {
  // useSearchParams needs a Suspense boundary in the App Router.
  return (
    <Suspense fallback={<main className="mx-auto max-w-3xl px-5 py-12 text-muted-foreground">{T.loading}</main>}>
      <DeletionStatusInner />
    </Suspense>
  );
}
