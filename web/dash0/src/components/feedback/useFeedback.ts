import { useCallback, useEffect, useRef, useState } from "react";
import { toBlob } from "html-to-image";

import { getToken } from "@/api/client";
import { getRecentConsoleErrors } from "./errorCollector";

export interface SubmitPayload {
  comment: string;
  screenshot: Blob | null;
}

interface UseFeedbackOptions {
  enabled: boolean;
  org?: string;
}

interface UseFeedbackReturn {
  isOpen: boolean;
  isCapturing: boolean;
  screenshot: Blob | null;
  open: () => Promise<void>;
  close: () => void;
  submit: (payload: SubmitPayload) => Promise<void>;
}

const FEEDBACK_OPEN_EVENT = "feedback:open";

// useFeedback owns the feedback state and side effects (screenshot capture,
// keyboard shortcut, submit). When `enabled` is false the hook is fully
// inert: no listeners, no fetches, no DOM access.
export function useFeedback({ enabled, org }: UseFeedbackOptions): UseFeedbackReturn {
  const [isOpen, setIsOpen] = useState(false);
  const [isCapturing, setIsCapturing] = useState(false);
  const [screenshot, setScreenshot] = useState<Blob | null>(null);
  const isOpenRef = useRef(isOpen);
  isOpenRef.current = isOpen;

  const close = useCallback(() => {
    setIsOpen(false);
    setScreenshot(null);
  }, []);

  const open = useCallback(async () => {
    if (!enabled || isOpenRef.current) return;
    setIsCapturing(true);
    try {
      const blob = await toBlob(document.documentElement, {
        cacheBust: true,
        // Skip <canvas> nodes — Recharts renders to canvas and html-to-image
        // does not serialize them. The text-only error context covers the gap.
        filter: (node) => !(node instanceof HTMLCanvasElement),
        pixelRatio: Math.min(window.devicePixelRatio || 1, 2),
      });
      setScreenshot(blob);
    } catch {
      setScreenshot(null);
    } finally {
      setIsCapturing(false);
      setIsOpen(true);
    }
  }, [enabled]);

  // Keyboard shortcut: Ctrl/Cmd+Shift+B. Skip when typing in inputs.
  useEffect(() => {
    if (!enabled) return undefined;

    function onKeyDown(event: KeyboardEvent) {
      if (!event.shiftKey || (!event.metaKey && !event.ctrlKey)) return;
      if (event.key.toLowerCase() !== "b") return;
      const target = event.target as HTMLElement | null;
      if (target && isEditable(target)) return;
      event.preventDefault();
      void open();
    }

    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [enabled, open]);

  // Test hook + future programmatic openers (mobile shake, etc.)
  useEffect(() => {
    if (!enabled) return undefined;

    function handler() {
      void open();
    }

    window.addEventListener(FEEDBACK_OPEN_EVENT, handler);
    return () => window.removeEventListener(FEEDBACK_OPEN_EVENT, handler);
  }, [enabled, open]);

  const submit = useCallback(
    async ({ comment, screenshot: blob }: SubmitPayload) => {
      const form = new FormData();
      form.set("url", window.location.href);
      form.set("comment", comment);
      if (org) form.set("org", org);
      form.set("context", JSON.stringify(buildContext()));
      if (blob) {
        form.set("screenshot", blob, blob.type === "image/png" ? "screenshot.png" : "screenshot");
      }

      const headers = new Headers();
      const token = getToken();
      if (token) headers.set("Authorization", `Bearer ${token}`);

      const response = await fetch("/api/mgmt/report", {
        method: "POST",
        body: form,
        headers,
      });
      if (!response.ok) {
        throw new Error(`bug report submit failed: ${response.status}`);
      }
    },
    [org],
  );

  return { isOpen, isCapturing, screenshot, open, close, submit };
}

function isEditable(node: HTMLElement): boolean {
  const tag = node.tagName;
  if (tag === "TEXTAREA" || tag === "INPUT" || tag === "SELECT") return true;
  if (node.isContentEditable) return true;
  return false;
}

function buildContext(): Record<string, unknown> {
  return {
    userAgent: navigator.userAgent,
    viewport: `${window.innerWidth}x${window.innerHeight}`,
    pixelRatio: window.devicePixelRatio || 1,
    platform: navigator.platform,
    language: navigator.language,
    build: import.meta.env.VITE_APP_VERSION || "dev",
    recentErrors: getRecentConsoleErrors(),
  };
}
