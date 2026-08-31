/**
 * @vitest-environment jsdom
 *
 * Unit tests for the incident-comment renderer (spec
 * 2026-08-30-07-incident-comment-formatting). Comments are freeform text
 * from untrusted sources (dash0, Slack, Telegram, API), so these cover the
 * five cases the spec names explicitly: plain text passes through, URLs
 * autolink without swallowing trailing punctuation, backticks inside a
 * fenced block are not double-parsed, an unclosed fence degrades
 * gracefully, and no HTML injection is possible.
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { CommentBody } from "./comment-body";

afterEach(cleanup);

describe("CommentBody", () => {
  it("renders plain text unchanged", () => {
    render(<CommentBody text="Everything looks fine now." />);
    expect(screen.getByTestId("comment-body").textContent).toBe(
      "Everything looks fine now.",
    );
    expect(screen.queryByRole("link")).toBeNull();
  });

  it("preserves whitespace and line breaks for plain text", () => {
    render(<CommentBody text={"line one\nline two"} />);
    const span = screen.getByTestId("comment-body").querySelector("span")!;
    expect(span.className).toContain("whitespace-pre-wrap");
    expect(span.textContent).toBe("line one\nline two");
  });

  it("autolinks a bare URL", () => {
    render(<CommentBody text="check https://acme.com/status" />);
    const link = screen.getByRole("link");
    expect(link.getAttribute("href")).toBe("https://acme.com/status");
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("rel")).toBe("noopener noreferrer");
    expect(link.textContent).toBe("https://acme.com/status");
  });

  it("does not swallow trailing punctuation into the link", () => {
    render(<CommentBody text="see https://acme.com." />);
    const link = screen.getByRole("link");
    expect(link.getAttribute("href")).toBe("https://acme.com");
    expect(link.textContent).toBe("https://acme.com");
    expect(screen.getByTestId("comment-body").textContent).toBe(
      "see https://acme.com.",
    );
  });

  it("keeps a balanced trailing parenthesis that is part of the URL", () => {
    render(
      <CommentBody text="https://en.wikipedia.org/wiki/Foo_(bar) is relevant" />,
    );
    const link = screen.getByRole("link");
    expect(link.getAttribute("href")).toBe(
      "https://en.wikipedia.org/wiki/Foo_(bar)",
    );
  });

  it("renders inline code as a <code> element", () => {
    render(<CommentBody text="run `curl -I https://acme.com`" />);
    const code = screen.getByText("curl -I https://acme.com");
    expect(code.tagName).toBe("CODE");
  });

  it("renders a fenced block as its own <pre><code>", () => {
    render(<CommentBody text={"before\n```\ncurl -I acme.com\n```\nafter"} />);
    const pre = screen.getByTestId("comment-body").querySelector("pre")!;
    expect(pre).not.toBeNull();
    expect(pre.className).toContain("overflow-x-auto");
    expect(pre.textContent).toBe("\ncurl -I acme.com\n");
  });

  it("does not double-parse backticks inside a fenced block", () => {
    render(<CommentBody text={"```\nlet x = `template`;\n```"} />);
    const container = screen.getByTestId("comment-body");
    // The backticked "`template`" inside the fence must stay literal text
    // inside the <pre>, not become a nested <code> inline-code element.
    const pre = container.querySelector("pre")!;
    expect(pre.querySelector("code code")).toBeNull();
    expect(pre.textContent).toContain("`template`");
  });

  it("degrades an unclosed fence gracefully instead of eating the rest", () => {
    render(<CommentBody text={"before ```curl -I acme.com after"} />);
    const container = screen.getByTestId("comment-body");
    // No <pre> is produced, and nothing after the stray fence is dropped.
    expect(container.querySelector("pre")).toBeNull();
    expect(container.textContent).toBe("before ```curl -I acme.com after");
  });

  it("never injects HTML — a literal <script> tag stays visible text", () => {
    render(<CommentBody text="<script>alert(1)</script>" />);
    const container = screen.getByTestId("comment-body");
    expect(container.querySelector("script")).toBeNull();
    expect(container.textContent).toBe("<script>alert(1)</script>");
  });

  it("never injects HTML via a URL or code span either", () => {
    render(
      <CommentBody text={"https://acme.com/<script>alert(1)</script> and `<img src=x onerror=alert(1)>`"} />,
    );
    const container = screen.getByTestId("comment-body");
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector("img")).toBeNull();
  });
});
