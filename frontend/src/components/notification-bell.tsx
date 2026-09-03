"use client";

import * as React from "react";
import useSWR from "swr";
import {
  listNotifications, notificationsUnread, notificationsMarkRead, notificationsReadAll,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Bell, CheckCheck, MessageSquare, Headset, AlertTriangle, Coins, Info } from "lucide-react";
import { cn } from "@/lib/utils";

const KIND_ICON: Record<string, typeof Bell> = {
  session: MessageSquare,
  handoff: Headset,
  sentiment: AlertTriangle,
  quota: Coins,
  system: Info,
};

/**
 * Notification bell for the sidebar. Polls unread count every 30s; the
 * realtime WS hub also triggers a refresh via useInboxRealtime in pages.
 */
export function NotificationBell() {
  const [open, setOpen] = React.useState(false);
  const { data: unread, mutate: mutateUnread } = useSWR("notif-unread", notificationsUnread, {
    refreshInterval: 30000,
  });
  const { data: items, mutate: mutateItems } = useSWR("notif-list", () => listNotifications(30), {
    refreshInterval: 60000,
  });

  // Expose a global refresh (pages call this after WS notification events).
  React.useEffect(() => {
    (window as unknown as { __refreshNotifs?: () => void }).__refreshNotifs = () => {
      void mutateUnread();
      void mutateItems();
    };
  }, [mutateUnread, mutateItems]);

  const unreadCount = unread?.unread ?? 0;

  const markAll = async () => {
    try {
      await notificationsReadAll();
      void mutateUnread();
      void mutateItems();
    } catch { /* ignore */ }
  };
  const markOne = async (id: number) => {
    try {
      await notificationsMarkRead(id);
      void mutateUnread();
      void mutateItems();
    } catch { /* ignore */ }
  };

  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger className="relative h-7 w-7 rounded-md hover:bg-sidebar-accent/50 transition-colors flex items-center justify-center text-sidebar-foreground" title="Notifications">
        <Bell className="size-4" />
        {unreadCount > 0 && (
          <span className="absolute -top-0.5 -right-0.5 min-w-4 h-4 px-1 rounded-full bg-destructive text-[9px] font-bold text-white flex items-center justify-center">
            {unreadCount > 99 ? "99+" : unreadCount}
          </span>
        )}
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" side="top" className="w-80 max-h-96 overflow-auto p-0">
        <div className="flex items-center justify-between px-3 py-2 border-b border-border sticky top-0 bg-popover z-10">
          <p className="text-xs font-semibold">Notifications</p>
          <Button variant="ghost" size="sm" className="h-6 text-[11px] gap-1 text-muted-foreground" onClick={markAll}>
            <CheckCheck className="size-3" /> Mark all read
          </Button>
        </div>
        {!items || items.length === 0 ? (
          <p className="text-xs text-muted-foreground text-center py-6">No notifications yet</p>
        ) : (
          <div className="divide-y divide-border">
            {items.map((n) => {
              const Icon = KIND_ICON[n.kind] ?? Info;
              return (
                <button
                  key={n.notification_id}
                  onClick={() => markOne(n.notification_id)}
                  className={cn(
                    "w-full text-left px-3 py-2.5 hover:bg-muted/40 transition-colors flex gap-2.5",
                    !n.is_read && "bg-primary/5",
                  )}
                >
                  <Icon className={cn("size-4 mt-0.5 shrink-0", n.kind === "sentiment" ? "text-destructive" : "text-muted-foreground")} />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-1.5">
                      <p className="text-xs font-medium truncate">{n.title}</p>
                      {!n.is_read && <span className="size-1.5 rounded-full bg-primary shrink-0" />}
                    </div>
                    <p className="text-[11px] text-muted-foreground line-clamp-2 mt-0.5">{n.body}</p>
                    <p className="text-[10px] text-muted-foreground/70 mt-0.5">
                      {new Date(n.created_at).toLocaleString("en-US")}
                    </p>
                  </div>
                </button>
              );
            })}
          </div>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
