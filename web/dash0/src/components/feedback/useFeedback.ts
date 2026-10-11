import { useCallback, useEffect, useRef, useState } from "react";
import { toBlob } from "html-to-image";

import { getToken } from "@/api/client";
import { getRecentConsoleErrors } from "./errorCollector";
import { listenForShake } from "./shake";

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
// keyboard shortcut, shake on mobile, submit). When `enabled` is false the hook is fully
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
      const restoreCanvases = await swapCanvasesForImages();
      const restoreScroll = flattenInnerScroll();
      let blob: Blob | null;
      try {
        // Capture exactly the viewport. The clone is rendered without any
        // scroll, so the window scroll becomes a negative margin on the
        // root: in-flow content moves up, fixed and sticky elements stay
        // where the user sees them.
        blob = await toBlob(document.documentElement, {
          cacheBust: true,
          // A canvas that could not be swapped (tainted) is skipped, it
          // would not serialize anyway.
          filter: (node) =>
            !(node instanceof HTMLCanvasElement && node.dataset.feedbackSkip === "1"),
          pixelRatio: Math.min(window.devicePixelRatio || 1, 2),
          width: window.innerWidth,
          height: window.innerHeight,
          style: {
            marginTop: `${-window.scrollY}px`,
            marginLeft: `${-window.scrollX}px`,
          },
        });
      } finally {
        restoreScroll();
        restoreCanvases();
      }
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

  // Shake the phone: same as the bug icon.
  useEffect(() => {
    if (!enabled) return undefined;
    return listenForShake(() => void open());
  }, [enabled, open]);

  // Test hook + future programmatic openers
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

// flattenInnerScroll makes scrolled inner containers (sidebar menu, a table
// with overflow-auto, a scrolled panel) render at their current offset: the
// clone is serialized without scroll positions, so each container is set to
// scroll 0 and its children are translated by the old offset. Both changes
// happen in the same task, so the page looks the same. The window scroll is
// handled by the capture itself. The returned function undoes everything,
// call it in a `finally`.
function flattenInnerScroll(): () => void {
  const undo: (() => void)[] = [];

  for (const el of Array.from(document.body.querySelectorAll("*"))) {
    const top = el.scrollTop;
    const left = el.scrollLeft;
    if (top === 0 && left === 0) continue;
    const children = Array.from(el.children).filter(
      (child): child is HTMLElement | SVGElement =>
        (child instanceof HTMLElement || child instanceof SVGElement) &&
        getComputedStyle(child).position !== "fixed",
    );
    if (children.length === 0) continue;

    const previous = children.map((child) => child.style.translate);
    for (const child of children) child.style.translate = `${-left}px ${-top}px`;
    el.scrollTo({ top: 0, left: 0, behavior: "instant" });
    undo.push(() => {
      children.forEach((child, index) => {
        child.style.translate = previous[index];
      });
      el.scrollTo({ top, left, behavior: "instant" });
    });
  }

  return () => {
    for (const fn of undo) fn();
  };
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
    scroll: `${Math.round(window.scrollX)},${Math.round(window.scrollY)}`,
    pixelRatio: window.devicePixelRatio || 1,
    platform: navigator.platform,
    language: navigator.language,
    build: import.meta.env.VITE_APP_VERSION || "dev",
    recentErrors: getRecentConsoleErrors(),
  };
}
