import { useEffect, useState } from "react";
import { Link, useParams } from "@tanstack/react-router";

import { getPost } from "./blog/posts";

interface BlogParams {
  slug?: string;
}

interface MarkdownRenderState {
  error: boolean;
  html: string | null;
}

const dateFormatter = new Intl.DateTimeFormat("en", {
  day: "numeric",
  month: "long",
  timeZone: "UTC",
  year: "numeric"
});

function formatDate(date: string) {
  return dateFormatter.format(new Date(`${date}T00:00:00Z`));
}

function MarkdownArticle({ source }: { source: string }) {
  const [rendered, setRendered] = useState<MarkdownRenderState>({
    error: false,
    html: null
  });

  useEffect(() => {
    let active = true;

    async function renderMarkdown() {
      setRendered({ error: false, html: null });

      try {
        const [{ marked }, { default: createDOMPurify }] = await Promise.all([
          import("marked"),
          import("dompurify")
        ]);
        const purifier = createDOMPurify(window);

        purifier.addHook("afterSanitizeAttributes", (node) => {
          if (node.tagName === "A" && node.hasAttribute("href")) {
            node.setAttribute("target", "_blank");
            node.setAttribute("rel", "noopener noreferrer");
          }
        });

        const parsed = marked(source, { async: false });
        const html = purifier.sanitize(parsed, { USE_PROFILES: { html: true } });

        if (active) {
          setRendered({ error: false, html });
        }
      } catch {
        if (active) {
          setRendered({ error: true, html: null });
        }
      }
    }

    renderMarkdown();

    return () => {
      active = false;
    };
  }, [source]);

  if (rendered.html !== null) {
    return (
      <div
        className="prose prose-slate dark:prose-invert max-w-none prose-a:text-sky-700 dark:prose-a:text-sky-300 prose-a:underline prose-code:text-zinc-900 dark:prose-code:text-zinc-100 prose-code:before:content-none prose-code:after:content-none prose-pre:border prose-pre:border-slate-200 dark:prose-pre:border-zinc-800 prose-pre:bg-slate-50 dark:prose-pre:bg-zinc-950 prose-pre:text-zinc-900 dark:prose-pre:text-zinc-100"
        dangerouslySetInnerHTML={{ __html: rendered.html }}
      />
    );
  }

  if (!rendered.error) {
    return <div aria-hidden className="min-h-32" />;
  }

  return (
    <div>
      <p className="border-b border-slate-200 dark:border-zinc-800 bg-amber-50 dark:bg-amber-950/30 px-4 py-3 text-xs text-amber-800 dark:text-amber-200">
        Markdown preview unavailable. Showing raw source.
      </p>
      <pre className="overflow-x-auto bg-white dark:bg-zinc-900 p-4 text-sm leading-6 text-zinc-900 dark:text-zinc-100">
        <code>{source}</code>
      </pre>
    </div>
  );
}

export function BlogPostPage() {
  const params = useParams({ strict: false }) as BlogParams;
  const post = params.slug ? getPost(params.slug) : undefined;

  if (!post) {
    return (
      <section className="mx-auto w-full max-w-3xl">
        <div className="rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5">
          <h1 className="text-xl font-semibold text-zinc-950 dark:text-zinc-50">
            Post not found
          </h1>
          <Link
            className="mt-3 inline-block text-sm font-medium text-sky-700 underline dark:text-sky-300"
            to="/blogs"
          >
            Back to blog
          </Link>
        </div>
      </section>
    );
  }

  return (
    <section className="mx-auto w-full max-w-3xl">
      <Link
        className="text-sm font-medium text-sky-700 underline dark:text-sky-300"
        to="/blogs"
      >
        Back to blog
      </Link>

      <article className="mt-6 rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5 sm:p-8">
        <header className="border-b border-slate-200 dark:border-zinc-800 pb-6">
          <h1 className="text-2xl font-semibold tracking-normal text-zinc-950 dark:text-zinc-50 sm:text-3xl">
            {post.title}
          </h1>
          <p className="mt-3 text-base leading-7 text-slate-600 dark:text-zinc-400">
            {post.dek}
          </p>
          <p className="mt-4 text-xs font-medium text-slate-500 dark:text-zinc-400">
            <time dateTime={post.date}>{formatDate(post.date)}</time>
            {" · "}
            {post.readingMinutes} min read
            {" · "}
            {post.tags.join(", ")}
          </p>
        </header>

        <div className="mt-8">
          <MarkdownArticle source={post.markdown} />
        </div>
      </article>
    </section>
  );
}
