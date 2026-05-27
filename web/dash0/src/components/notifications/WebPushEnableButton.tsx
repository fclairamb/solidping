import { useState } from "react";
import { Button } from "@/components/ui/button";
import { useWebPushSubscription } from "@/hooks/useWebPushSubscription";

interface WebPushEnableButtonProps {
  org: string;
  onSubscription: (subscriptionJson: string) => void;
  "data-testid"?: string;
}

type ButtonState = "default" | "pending" | "subscribed" | "blocked";

/**
 * WebPushEnableButton renders a button that walks the user through:
 * 1. Requesting notification permission.
 * 2. Subscribing via PushManager.
 * 3. Returning the raw subscription JSON via `onSubscription`.
 *
 * Four visual states: default, pending, subscribed, blocked.
 */
export function WebPushEnableButton({
  org,
  onSubscription,
  "data-testid": testId,
}: WebPushEnableButtonProps) {
  const { isSupported, permission, subscribe } = useWebPushSubscription(org);
  const [state, setState] = useState<ButtonState>(() => {
    if (permission === "denied") return "blocked";
    if (permission === "granted") return "default"; // may already be subscribed; let parent track
    return "default";
  });

  if (!isSupported) {
    return null; // Browser doesn't support Push API
  }

  const handleClick = async () => {
    if (state === "blocked" || state === "subscribed" || state === "pending")
      return;

    setState("pending");
    try {
      const json = await subscribe();
      if (json) {
        setState("subscribed");
        onSubscription(json);
      } else {
        // User denied permission
        setState("blocked");
      }
    } catch {
      setState("default");
    }
  };

  const label: Record<ButtonState, string> = {
    default: "Enable browser notifications",
    pending: "Enabling…",
    subscribed: "Subscribed",
    blocked: "Notifications blocked",
  };

  return (
    <Button
      onClick={handleClick}
      disabled={state === "pending" || state === "subscribed" || state === "blocked"}
      variant={state === "blocked" ? "ghost" : "default"}
      data-testid={testId}
    >
      {label[state]}
    </Button>
  );
}
