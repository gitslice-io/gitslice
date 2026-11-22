import { Link } from "@tanstack/react-router";

import { BlogTags, formatPostDate } from "./blog/BlogParts";
import { blogPostIndex } from "./blog/postIndex";

export function BlogListPage() {
  const posts = [...blogPostIndex].sort((left, right) =>
    right.date.localeCompare(left.date),
  );

  return (
    <section className="mx-auto w-full max-w-4xl pb-16">
      <header className="pb-10 pt-4">
        <h1 className="text-[clamp(2rem,5vw,3rem)] font-medium leading-[1.08] tracking-tight text-zinc-950 dark:text-zinc-50">
          Blog
        </h1>
        <p className="mt-3 max-w-2xl text-lg leading-8 text-slate-600 dark:text-zinc-400">
          Product updates, benchmarks, and engineering notes from Gitslice.
        </p>
      </header>

      <ol className="border-t border-slate-200 dark:border-zinc-800">
        {posts.map((post) => (
          <li
            className="grid gap-x-10 gap-y-2 border-b border-slate-200 py-8 dark:border-zinc-800 sm:grid-cols-[9rem_1fr]"
            key={post.slug}
          >
            <time
              className="pt-1 text-[0.8125rem] font-medium tabular-nums text-slate-500 dark:text-zinc-400"
              dateTime={post.date}
            >
              {formatPostDate(post.date)}
            </time>
            <article className="min-w-0">
              <h2 className="text-balance text-xl font-medium leading-snug tracking-tight text-zinc-950 dark:text-zinc-50 sm:text-2xl">
                <Link
                  className="transition hover:text-sky-700 dark:hover:text-sky-300"
                  params={{ slug: post.slug }}
                  to="/blogs/$slug"
                >
                  {post.title}
                </Link>
              </h2>
              <p className="mt-2 text-pretty leading-7 text-slate-600 dark:text-zinc-400">
                {post.dek}
              </p>
              <div className="mt-4 flex flex-wrap items-center gap-3 text-[0.8125rem] text-slate-500 dark:text-zinc-400">
                <BlogTags tags={post.tags} />
                <span>{post.readingMinutes} min read</span>
              </div>
            </article>
          </li>
        ))}
      </ol>
    </section>
  );
}
