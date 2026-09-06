import { useAuth } from "@/contexts/AuthContext";

/**
 * Whether the current session is the shared public live demo (spec
 * 2026-09-06-02).
 *
 * The predicate behind every "hide what cannot be done" decision that is not
 * worth a component of its own. NOT a security control: the server's write
 * guard is what refuses a demo write, and this only spares the visitor from
 * discovering that by clicking.
 */
export function useIsDemoSession(): boolean {
  const { user } = useAuth();

  return Boolean(user?.isDemo);
}
