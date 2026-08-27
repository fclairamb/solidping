/**
 * Instance support inbox API (spec 2026-08-22-02).
 *
 * Split out of the monolithic `hooks.ts` the way `email-inbox.ts` is: this is a
 * super-admin-only, instance-level surface with no `org` in any path, and
 * keeping it separate stops it being mistaken for org-scoped data.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiFetch } from "./client";

export type SupportChannel =
  | "whatsapp"
  | "telegram"
  | "sms"
  | "slack"
  | "discord"
  | "email";

export type SupportStatus = "open" | "pending" | "closed";

/**
 * Whether a free-form reply can be sent right now. DERIVED server-side from the
 * last inbound message and the channel's rule, never stored — so it cannot go
 * stale. This is a different axis from `status`: a thread can be open (nobody
 * answered) and expired (WhatsApp will no longer take a free-form reply), which
 * is exactly the state an operator most needs to see.
 */
export interface SupportReplyWindow {
  expires: boolean;
  open: boolean;
  expiresAt?: string | null;
  reason?: string;
  costsMoney: boolean;
}

export interface SupportThread {
  uid: string;
  channel: SupportChannel;
  channelIdentity: string;
  subject: string;
  status: SupportStatus;
  /** Attribution only — never an access boundary. */
  organizationUid?: string | null;
  userUid?: string | null;
  lastMessageAt: string;
  lastInboundAt?: string | null;
  unreadCount: number;
  createdAt: string;
  updatedAt: string;
  replyWindow: SupportReplyWindow;
  /**
   * Whether a reply can be routed back to THIS thread right now — resolved
   * server-side per thread from local routing state (a stored Slack connection
   * for the thread's workspace, a Discord channel id, an SMS sender), never by
   * calling the provider. A registered adapter is not enough: a Slack workspace
   * whose app was installed outside SolidPing sends messages we capture and
   * holds no token we can answer with.
   */
  canReply: boolean;
  /** Why `canReply` is false, in operator-facing terms. Absent when it is true. */
  canReplyReason?: string;
}

export interface SupportMessage {
  uid: string;
  threadUid: string;
  channel: SupportChannel;
  direction: "inbound" | "outbound";
  body: string;
  truncated?: boolean;
  rawType: string;
  externalId?: string | null;
  authorUid?: string | null;
  delivery?: Record<string, unknown> | null;
  createdAt: string;
}

export interface SupportThreadFilters {
  status?: SupportStatus | "";
  channel?: SupportChannel | "";
  q?: string;
}

function threadsQueryString(filters: SupportThreadFilters): string {
  const params = new URLSearchParams();

  if (filters.status) params.set("status", filters.status);
  if (filters.channel) params.set("channel", filters.channel);
  if (filters.q) params.set("q", filters.q);

  const query = params.toString();

  return query ? `?${query}` : "";
}

export function useSupportThreads(filters: SupportThreadFilters = {}) {
  return useQuery({
    queryKey: ["support-threads", filters],
    queryFn: async () => {
      const response = await apiFetch<{ data?: SupportThread[] }>(
        `/api/v1/support/threads${threadsQueryString(filters)}`,
      );

      return response.data || [];
    },
  });
}

export function useSupportThread(uid: string) {
  return useQuery({
    queryKey: ["support-thread", uid],
    queryFn: () => apiFetch<SupportThread>(`/api/v1/support/threads/${uid}`),
    enabled: !!uid,
  });
}

export function useSupportMessages(uid: string) {
  return useQuery({
    queryKey: ["support-messages", uid],
    queryFn: async () => {
      const response = await apiFetch<{ data?: SupportMessage[] }>(
        `/api/v1/support/threads/${uid}/messages`,
      );

      return response.data || [];
    },
    enabled: !!uid,
  });
}

export function useUpdateSupportThread(uid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: { status?: SupportStatus; subject?: string }) =>
      apiFetch<SupportThread>(`/api/v1/support/threads/${uid}`, {
        method: "PATCH",
        body: JSON.stringify(request),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["support-thread", uid] });
      void queryClient.invalidateQueries({ queryKey: ["support-threads"] });
    },
  });
}

export function useSendSupportReply(uid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (body: string) =>
      apiFetch<SupportMessage>(`/api/v1/support/threads/${uid}/messages`, {
        method: "POST",
        body: JSON.stringify({ body }),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["support-messages", uid] });
      void queryClient.invalidateQueries({ queryKey: ["support-thread", uid] });
      void queryClient.invalidateQueries({ queryKey: ["support-threads"] });
    },
  });
}

/**
 * Retry the provider send for an outbound reply whose delivery failed.
 *
 * The server re-runs the same pre-flight before sending, so a message queued
 * against a workspace that has since been connected simply goes, and one that
 * still has no route is refused with the current reason instead of failing at
 * the provider again.
 */
export function useResendSupportReply(threadUid: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (messageUid: string) =>
      apiFetch<SupportMessage>(
        `/api/v1/support/threads/${threadUid}/messages/${messageUid}/resend`,
        { method: "POST" },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["support-messages", threadUid] });
      void queryClient.invalidateQueries({ queryKey: ["support-thread", threadUid] });
      void queryClient.invalidateQueries({ queryKey: ["support-threads"] });
    },
  });
}

/** Human label for a channel. Kept here so the list and the detail agree. */
export const SUPPORT_CHANNEL_LABELS: Record<SupportChannel, string> = {
  whatsapp: "WhatsApp",
  telegram: "Telegram",
  sms: "SMS",
  slack: "Slack",
  discord: "Discord",
  email: "Email",
};
