import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "./client";

export interface EmailInboxConfig {
  enabled: boolean;
  sessionUrl: string;
  username: string;
  password?: string;
  addressDomain: string;
  mailboxName?: string;
  processedMailboxName?: string;
  pollIntervalSeconds?: number;
  processedRetentionDays?: number;
  failedRetentionDays?: number;
  rewriteBaseUrl?: string;
}

export interface EmailInboxStatus {
  enabled: boolean;
  connected: boolean;
  lastSyncedAt?: string;
  lastError?: string;
  addressDomain?: string;
  accountId?: string;
}

export interface EmailInboxTestRequest {
  sessionUrl?: string;
  username?: string;
  password?: string;
  addressDomain?: string;
}

export interface EmailInboxTestResult {
  ok: boolean;
  mailboxes: string[];
}

/** Build the recipient address for an email check. Maps optional `status` to
 * the plus-suffix; "up" is omitted (it's the implicit default). */
export function emailCheckAddress(
  token: string,
  domain: string,
  status?: "down" | "error" | "up" | "running",
): string {
  const local = status && status !== "up" ? `${token}+${status}` : token;
  return `${local}@${domain}`;
}

/** Public projection of the email_inbox system parameter — any authenticated
 * user can read addressDomain. Returns null when the inbox isn't configured. */
export function useEmailAddressDomain() {
  return useQuery({
    queryKey: ["email-inbox", "public"],
    queryFn: async () => {
      const res = await apiFetch<{ addressDomain: string }>(
        "/api/v1/system/parameters/email_inbox/public",
      );
      return res.addressDomain || null;
    },
    staleTime: 30_000,
  });
}

/** Live JMAP supervisor status. Polls every `refetchInterval` ms while open. */
export function useEmailInboxStatus(refetchInterval = 5000) {
  return useQuery({
    queryKey: ["email-inbox", "status"],
    queryFn: () =>
      apiFetch<EmailInboxStatus>("/api/v1/system/email-inbox/status"),
    refetchInterval,
  });
}

/** Validate a JMAP configuration end-to-end. When `body` is empty the stored
 * config is used. */
export function useTestEmailInbox() {
  return useMutation({
    mutationFn: (body?: EmailInboxTestRequest) =>
      apiFetch<EmailInboxTestResult>("/api/v1/system/email-inbox/test", {
        method: "POST",
        body: JSON.stringify(body ?? {}),
      }),
  });
}

/** Trigger an immediate sync of the configured inbox. */
export function useSyncEmailInbox() {
  return useMutation({
    mutationFn: () =>
      apiFetch<{ ok: boolean }>("/api/v1/system/email-inbox/sync", {
        method: "POST",
      }),
  });
}

/** Save the email_inbox system parameter. Pass an empty `password` to keep
 * the existing one — handler-side this is interpreted as "do not update". */
export function useSaveEmailInboxConfig() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (config: EmailInboxConfig) =>
      apiFetch("/api/v1/system/parameters/email_inbox", {
        method: "PUT",
        body: JSON.stringify({ value: config, secret: true }),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["email-inbox"] });
      queryClient.invalidateQueries({ queryKey: ["system-parameters"] });
    },
  });
}
