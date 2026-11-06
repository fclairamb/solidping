#!/usr/bin/env bun
/**
 * Replace the `.md` copies of the API reference pages in build/ (and their
 * sections of llms-full.txt) with plain markdown rendered from the OpenAPI
 * spec.
 *
 * Run after `docusaurus build` (see the `build` script in package.json).
 * docusaurus-plugin-llms copies each page's MDX source, which for the
 * generated API pages is imports and JSX components with no field in sight.
 *
 * The rendering lives in ../src/lib/apiMarkdown.ts, where it is unit-tested
 * (`bun test ./src`) independently of this file I/O wrapper.
 */
import { readdir, readFile, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { type OperationRef, type Spec, renderInfo, renderOperation, renderTag } from "../src/lib/apiMarkdown";

// Bun's ambient types are not in this project's tsconfig (see its exclude).
declare const Bun: { YAML: { parse(text: string): unknown } };

const siteDir =join(dirname(fileURLToPath(import.meta.url)), "..");
const specPath = join(siteDir, "..", "..", "server", "internal", "app", "openapi", "openapi.yaml");
const apiDocsDir = join(siteDir, "docs", "api");
const buildDir = join(siteDir, "build");
// Origin-relative, so links work on every host serving /docs and from llms-full.txt.
const linkBase = "/docs/api/";

function frontMatter(source: string, key: string): string | undefined {
  const m = source.match(new RegExp(`^${key}: "?(.*?)"?$`, "m"));
  return m?.[1];
}

async function main() {
  const spec = Bun.YAML.parse(await readFile(specPath, "utf8")) as Spec;
  const files = (await readdir(apiDocsDir)).filter((f) => f.endsWith(".mdx"));

  // page title -> rendered markdown, to patch llms-full.txt
  const pages = new Map<string, string>();
  const write = async (rel: string, title: string, markdown: string) => {
    // Only overwrite what the plugin produced: a missing file means the build
    // layout changed and this script needs to follow it.
    await readFile(join(buildDir, rel));
    await writeFile(join(buildDir, rel), markdown);
    pages.set(title, markdown);
  };

  const operations: (OperationRef & { title: string; tag: string })[] = [];
  for (const file of files.filter((f) => f.endsWith(".api.mdx"))) {
    const source = await readFile(join(apiDocsDir, file), "utf8");
    const method = source.match(/<MethodEndpoint\s+method=\{"(\w+)"\}\s+path=\{"([^"]+)"\}/);
    const id = frontMatter(source, "id");
    const title = frontMatter(source, "title");
    if (!method || !id || !title) throw new Error(`${file}: no id, title or MethodEndpoint`);
    const [, verb, path] = method;
    operations.push({ id, title, method: verb, path, tag: spec.paths[path][verb].tags?.[0] ?? "" });
    await write(join("api", `${id}.md`), title, renderOperation(spec, verb, path));
  }

  const tags: { id: string; name: string }[] = [];
  for (const file of files.filter((f) => f.endsWith(".tag.mdx"))) {
    const source = await readFile(join(apiDocsDir, file), "utf8");
    const id = frontMatter(source, "id");
    const name = frontMatter(source, "title");
    if (!id || !name) throw new Error(`${file}: no id or title`);
    tags.push({ id, name });
    const ops = operations.filter((o) => o.tag === name).sort((a, b) => a.title.localeCompare(b.title));
    await write(join("api", `${id}.md`), name, renderTag(spec, name, ops, linkBase));
  }

  const info = await readFile(join(apiDocsDir, "solidping-api.info.mdx"), "utf8");
  tags.sort((a, b) => a.name.localeCompare(b.name));
  await write("api.md", frontMatter(info, "title") ?? "", renderInfo(spec, tags, linkBase));

  // llms-full.txt joins every page as `## <title>` + body, separated by `---`.
  // Swap the body of each API section, recognized by its title and by the
  // theme imports only a generated page has.
  const fullPath = join(buildDir, "llms-full.txt");
  const sections = (await readFile(fullPath, "utf8")).split("\n---\n\n");
  const replaced = new Set<string>();
  const patched = sections.map((section) => {
    const m = section.match(/(^|\n)## (.+)\n/);
    const markdown = m && pages.get(m[2]);
    if (!m || !markdown) return section;
    // A guide titled like an API page would be overwritten: fail instead.
    if (replaced.has(m[2])) throw new Error(`llms-full.txt has two sections titled "${m[2]}"`);
    replaced.add(m[2]);
    const head = section.slice(0, m.index! + m[0].length);
    // Demote the page's own headings one level under the section's `##`.
    const body = markdown.replace(/^# .*\n+/, "").replace(/^(#+) /gm, "#$1 ");
    return `${head}\n${body}`;
  });
  const missing = [...pages.keys()].filter((title) => !replaced.has(title));
  if (missing.length > 0) throw new Error(`no llms-full.txt section for: ${missing.join(", ")}`);
  await writeFile(fullPath, patched.join("\n---\n\n"));

  console.log(`[gen-api-markdown] ${pages.size} pages and llms-full.txt sections`);
}

main().catch((err) => {
  console.error("[gen-api-markdown]", err);
  process.exit(1);
});
