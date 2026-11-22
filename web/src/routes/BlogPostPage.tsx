import { useEffect, useMemo, useRef, useState, type MouseEvent } from "react";
import { Link, useLoaderData, useParams } from "@tanstack/react-router";

import { isDarkTheme, renderMermaid, svgDataUrl } from "../lib/diagram";
import { BlogTags, formatPostDate } from "./blog/BlogParts";
import { getPostMeta } from "./blog/postIndex";
import { renderPost } from "./blog/renderPost";

interface BlogParams {
  slug?: string;
}

// The article's code blocks are highlighted and its diagrams drawn in the
// browser, after the server-rendered HTML is on screen.
function useEnhancedArticle(
  container: React.RefObject<HTMLDivElement | null>,
  html: string,
) {
  useEffect(() => {
    const root = container.current;
    if (!root) {
      return;
    }
    let active = true;

    const blocks = Array.from(
      root.querySelectorAll<HTMLElement>("figure.blog-code pre > code"),
    );
    if (blocks.length > 0) {
      void import("../components/source/highlight").then(
        ({ highlightToHtml }) => {
          for (const code of blocks) {
            const lang = (
              code.className.match(/language-([\w+-]+)/)?.[1] ?? "text"
            ).replace(/^(sh|console)$/, "bash");
            highlightToHtml(code.textContent ?? "", lang).then(
              (highlighted) => {
                const pre = code.parentElement;
                if (!active || !pre?.isConnected) {
                  return;
                }
                const template = document.createElement("template");
                template.innerHTML = highlighted;
                const next = template.content.firstElementChild;
                if (next) {
                  pre.replaceWith(next);
                }
              },
              () => undefined,
            );
          }
        },
      );
    }

    for (const pre of Array.from(
      root.querySelectorAll<HTMLElement>("pre.blog-mermaid"),
    )) {
      renderMermaid(pre.textContent ?? "", isDarkTheme()).then(
        (svg) => {
          if (!active || !pre.isConnected) {
            return;
          }
          const figure = document.createElement("figure");
          figure.className = "blog-diagram not-prose";
          const image = document.createElement("img");
          image.alt = "Diagram";
          image.src = svgDataUrl(svg);
          figure.append(image);
          pre.replaceWith(figure);
        },
        () => undefined,
      );
    }

    return () => {
      active = false;
    };
  }, [container, html]);
}

function copyText(text: string): Promise<void> {
  return (
    navigator.clipboard?.writeText(text) ??
    Promise.reject(new Error("no clipboard"))
  );
}

function onArticleClick(event: MouseEvent<HTMLDivElement>) {
  const button = (
    event.target as HTMLElement | null
  )?.closest<HTMLButtonElement>("button[data-copy]");
  const code = button?.closest("figure")?.querySelector("pre");
  if (!button || !code) {
    return;
  }
  copyText(code.textContent ?? "").then(
    () => {
      button.textContent = "Copied";
      window.setTimeout(() => (button.textContent = "Copy"), 1500);
    },
    () => undefined,
  );
}

function CopyMarkdownButton({
  markdown,
  title,
}: {
  markdown: string;
  title: string;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      className="font-medium text-slate-600 underline-offset-4 transition hover:text-zinc-950 hover:underline dark:text-zinc-400 dark:hover:text-zinc-50"
      onClick={() => {
        copyText(`# ${title}\n\n${markdown}`).then(
          () => {
            setCopied(true);
            window.setTimeout(() => setCopied(false), 1500);
          },
          () => undefined,
        );
      }}
      type="button"
    >
      {copied ? "Copied" : "Copy as Markdown"}
    </button>
  );
}

export function BlogPostPage() {
  const params = useParams({ strict: false }) as BlogParams;
  const slug = params.slug ?? "";
  const post = slug ? getPostMeta(slug) : undefined;
  const data = useLoaderData({ strict: false }) as
    | { markdown?: string | null }
    | undefined;
  const markdown = data?.markdown ?? "";
  const rendered = useMemo(() => renderPost(markdown), [markdown]);
  const container = useRef<HTMLDivElement>(null);
  useEnhancedArticle(container, rendered.html);

  if (!post) {
    return (
      <section className="mx-auto w-full max-w-3xl py-10">
        <h1 className="text-2xl font-medium text-zinc-950 dark:text-zinc-50">
          Post not found
        </h1>
        <Link
          className="mt-3 inline-block text-sm font-medium text-sky-700 underline dark:text-sky-300"
          to="/blogs"
        >
          Back to all posts
        </Link>
      </section>
    );
  }

  return (
    <section className="mx-auto w-full max-w-3xl pb-16">
      <Link
        className="text-[0.8125rem] font-medium text-slate-500 transition hover:text-zinc-950 dark:text-zinc-400 dark:hover:text-zinc-50"
        to="/blogs"
      >
        ← Back to all posts
      </Link>

      <article className="mt-8">
        <header>
          <time
            className="block text-[0.8125rem] font-medium tabular-nums text-slate-500 dark:text-zinc-400"
            dateTime={post.date}
          >
            {formatPostDate(post.date)}
          </time>
          <h1 className="mt-3 text-balance text-[clamp(1.875rem,5vw,3rem)] font-medium leading-[1.1] tracking-tight text-zinc-950 dark:text-zinc-50">
            {post.title}
          </h1>
          <p className="mt-4 text-pretty text-base leading-7 text-slate-600 sm:text-lg sm:leading-8 dark:text-zinc-400">
            {post.dek}
          </p>
          <div className="mt-5 flex flex-wrap items-center gap-x-3 gap-y-2 text-[0.8125rem]">
            <BlogTags tags={post.tags} />
            <span className="text-slate-500 dark:text-zinc-400">
              {post.readingMinutes} min read
            </span>
            {markdown ? (
              <CopyMarkdownButton markdown={markdown} title={post.title} />
            ) : null}
          </div>
        </header>

        <div
          className="mt-10 border-t border-slate-200 pt-8 dark:border-zinc-800"
          onClick={onArticleClick}
          ref={container}
        >
          {markdown ? (
            <div
              className="blog-article prose prose-slate max-w-none dark:prose-invert prose-headings:font-medium prose-headings:tracking-tight prose-h2:mt-12 prose-a:text-sky-700 prose-a:underline-offset-2 dark:prose-a:text-sky-300 prose-code:text-zinc-900 prose-code:before:content-none prose-code:after:content-none dark:prose-code:text-zinc-100 [&_:not(pre)>code]:rounded [&_:not(pre)>code]:bg-slate-100 [&_:not(pre)>code]:px-1 [&_:not(pre)>code]:py-0.5 [&_:not(pre)>code]:text-[0.875em] [&_:not(pre)>code]:font-normal [&_:not(pre)>code]:[overflow-wrap:anywhere] dark:[&_:not(pre)>code]:bg-zinc-800"
              dangerouslySetInnerHTML={{ __html: rendered.html }}
            />
          ) : (
            <p className="text-sm text-slate-500 dark:text-zinc-400">
              This post could not be loaded.
            </p>
          )}
        </div>
      </article>
    </section>
  );
}
