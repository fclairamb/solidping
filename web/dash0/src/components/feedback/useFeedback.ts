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
      const restore = await swapCanvasesForImages();
      let blob: Blob | null;
      try {
        blob = await toBlob(document.documentElement, {
          cacheBust: true,
          // A canvas that could not be swapped (tainted) is skipped, it
          // would not serialize anyway.
          filter: (node) =>
            !(node instanceof HTMLCanvasElement && node.dataset.feedbackSkip === "1"),
          pixelRatio: Math.min(window.devicePixelRatio || 1, 2),
        });
      } finally {
        restore();
      }
      const cropped = blob ? await cropToViewport(blob) : blob;
      setScreenshot(cropped);
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

// swapCanvasesForImages replaces every <canvas> by an <img> holding its pixels
// (html-to-image does not serialize canvas content). A canvas whose
// toDataURL throws (tainted) is flagged so the capture filter skips it. The
// returned function undoes everything, call it in a `finally`.
async function swapCanvasesForImages(): Promise<() => void> {
  const undo: (() => void)[] = [];
  const pending: Promise<unknown>[] = [];

  for (const canvas of Array.from(document.querySelectorAll("canvas"))) {
    const rect = canvas.getBoundingClientRect();
    if (canvas.width === 0 || canvas.height === 0 || rect.width === 0) continue;
    let url: string;
    try {
      url = canvas.toDataURL("image/png");
    } catch {
      canvas.dataset.feedbackSkip = "1";
      undo.push(() => delete canvas.dataset.feedbackSkip);
      continue;
    }
    const img = document.createElement("img");
    img.src = url;
    img.className = canvas.className;
    img.setAttribute("style", canvas.getAttribute("style") || "");
    img.style.width = `${rect.width}px`;
    img.style.height = `${rect.height}px`;
    img.style.display = getComputedStyle(canvas).display === "inline" ? "inline-block" : "";
    const previousDisplay = canvas.style.display;
    canvas.after(img);
    canvas.style.display = "none";
    pending.push(img.decode().catch(() => undefined));
    undo.push(() => {
      img.remove();
      canvas.style.display = previousDisplay;
    });
  }

  await Promise.all(pending);

  return () => {
    for (const fn of undo) fn();
  };
}

// cropToViewport crops a full-document capture to the visible viewport. On any
// failure the original blob is returned.
async function cropToViewport(blob: Blob): Promise<Blob> {
  try {
    const bitmap = await createImageBitmap(blob);
    const ratio = bitmap.width / Math.max(document.documentElement.scrollWidth, 1);
    const sx = Math.round(window.scrollX * ratio);
    const sy = Math.round(window.scrollY * ratio);
    const sw = Math.min(Math.round(window.innerWidth * ratio), bitmap.width - sx);
    const sh = Math.min(Math.round(window.innerHeight * ratio), bitmap.height - sy);
    if (sw <= 0 || sh <= 0 || (sw === bitmap.width && sh === bitmap.height)) return blob;

    const canvas = document.createElement("canvas");
    canvas.width = sw;
    canvas.height = sh;
    const ctx = canvas.getContext("2d");
    if (!ctx) return blob;
    ctx.drawImage(bitmap, sx, sy, sw, sh, 0, 0, sw, sh);
    return await new Promise<Blob>((resolve) => {
      canvas.toBlob((out) => resolve(out || blob), "image/png");
    });
  } catch {
    return blob;
  }
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
