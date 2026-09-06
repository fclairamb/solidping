import { useQuery } from "@tanstack/react-query";
import {
  fetchPublicConfig,
  type DemoPublicConfig,
  type PublicConfig,
  type SMSPublicConfig,
} from "@/lib/analytics";

/**
 * Reads the instance's unauthenticated public configuration document
 * (`GET /api/v1/config`).
 *
 * `lib/analytics.ts` fetches the same document once at boot, fire-and-forget,
 * to bootstrap PostHog before React Query exists. Every *component* that needs
 * a public flag should go through this hook instead, so multiple consumers
 * share one cached response rather than each issuing its own request.
 *
 * The document is instance-level and effectively static for the lifetime of a
 * page, hence the very long staleTime.
 */
export function usePublicConfig() {
  return useQuery<PublicConfig | null>({
    queryKey: ["publicConfig"],
    queryFn: fetchPublicConfig,
    staleTime: 5 * 60 * 1000,
    // A missing/failed public config means "no optional features", not an
    // error state the UI should surface — fetchPublicConfig already returns
    // null rather than throwing.
    retry: false,
  });
}

/**
 * Whether this instance can send WhatsApp alerts. Mirrors the backend's
 * resolved `config.WhatsAppConfig.Active()` rule: the dashboard only offers the
 * WhatsApp contact type and severity channel when a WABA is actually wired up.
 *
 * Defaults to false while loading, so the feature never flashes into view on an
 * instance that does not have it.
 */
export function useWhatsAppEnabled(): boolean {
  const { data } = usePublicConfig();

  return Boolean(data?.whatsapp?.enabled);
}

/**
 * Whether this instance can send Telegram alerts. Mirrors the backend's
 * resolved `config.TelegramConfig.Active()` rule: the dashboard only offers the
 * "Connect Telegram" action and the `telegram` severity channel when a bot is
 * actually wired up.
 *
 * Defaults to false while loading, so the feature never flashes into view on an
 * instance that does not have it.
 *
 * Deliberately in the same file (and over the same cached query) as
 * `useWhatsAppEnabled` — both read one public-config document, and a second
 * fetch of the same document would be pure waste.
 */
export function useTelegramEnabled(): boolean {
  const { data } = usePublicConfig();

  return Boolean(data?.telegram?.enabled);
}

/**
 * The instance's server-provided SMS capability. Mirrors the backend's
 * resolved `config.SMSConfig.Active()` / `config.VoiceConfig.Active()` rules.
 *
 * Returns a fully-populated object rather than a possibly-undefined one so
 * callers never have to guard: while the document is loading, or on an
 * instance that predates the field, the answer is simply "the server provides
 * nothing", which is the safe default for a panel that explains where an
 * organization's SMS comes from.
 *
 * Deliberately over the same cached query as `useWhatsAppEnabled` /
 * `useTelegramEnabled` — one public-config document, one fetch.
 */
export function useInstanceSMSConfig(): SMSPublicConfig {
  const { data } = usePublicConfig();

  return {
    enabled: Boolean(data?.sms?.enabled),
    sender: data?.sms?.sender,
    provider: data?.sms?.provider,
    voiceEnabled: Boolean(data?.sms?.voiceEnabled),
  };
}

/**
 * The instance's shared live demo, if it has one.
 *
 * Returns a fully-populated object rather than a possibly-undefined one so
 * callers never have to guard: while the document is loading, and on any
 * instance that has the demo off, the answer is simply "there is no demo",
 * which is the safe default for a login page that would otherwise flash a
 * button nobody can use.
 *
 * Over the same cached query as the other public-config hooks — one document,
 * one fetch.
 */
export function useDemoConfig(): DemoPublicConfig {
  const { data } = usePublicConfig();

  return {
    enabled: Boolean(data?.demo?.enabled),
    orgSlug: data?.demo?.orgSlug,
    email: data?.demo?.email,
    password: data?.demo?.password,
  };
}
