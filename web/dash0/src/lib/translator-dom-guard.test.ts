/**
 * @vitest-environment jsdom
 */
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { installTranslatorDomGuard } from "./translator-dom-guard";

// Reproduces what Chrome's translator does to a React-owned text node: it
// moves the node into a <font> wrapper, so the parent React remembers is no
// longer its parent. Without the guard both calls throw NotFoundError.
describe("installTranslatorDomGuard", () => {
  let warn: ReturnType<typeof vi.spyOn>;

  beforeAll(() => {
    warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    installTranslatorDomGuard();
    installTranslatorDomGuard(); // idempotent
  });

  afterAll(() => {
    warn.mockRestore();
  });

  function translated() {
    const parent = document.createElement("div");
    const text = document.createTextNode("Try again");
    parent.appendChild(text);
    const font = document.createElement("font");
    parent.replaceChild(font, text);
    font.appendChild(text);
    return { parent, text, font };
  }

  it("skips removeChild of a node the translator moved", () => {
    const { parent, text, font } = translated();
    expect(() => parent.removeChild(text)).not.toThrow();
    expect(font.contains(text)).toBe(true);
  });

  it("skips insertBefore a node the translator moved", () => {
    const { parent, text } = translated();
    const span = document.createElement("span");
    expect(() => parent.insertBefore(span, text)).not.toThrow();
    expect(span.parentNode).toBeNull();
  });

  it("keeps the normal behaviour", () => {
    const parent = document.createElement("div");
    const a = parent.appendChild(document.createElement("a"));
    const b = document.createElement("b");
    parent.insertBefore(b, a);
    expect(Array.from(parent.childNodes)).toEqual([b, a]);
    parent.insertBefore(document.createElement("i"), null);
    expect(parent.childNodes).toHaveLength(3);
    parent.removeChild(a);
    expect(parent.contains(a)).toBe(false);
  });
});
