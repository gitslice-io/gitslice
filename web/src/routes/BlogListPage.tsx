import { Link } from "@tanstack/react-router";

import { blogPosts } from "./blog/posts";

const dateFormatter = new Intl.DateTimeFormat("en", {
  day: "numeric",
  month: "long",
  timeZone: "UTC",
  year: "numeric"
});

function formatDate(date: string) {
  return dateFormatter.format(new Date(`${date}T00:00:00Z`));
}

export function BlogListPage() {
  const posts = [...blogPosts].sort((left, right) =>
    right.date.localeCompare(left.date)
  );

  return (
    <section className="mx-auto w-full max-w-4xl">
      <header className="border-b border-slate-200 dark:border-zinc-800 pb-5">
        <p className="text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
          Gitslice
        </p>
        <h1 className="mt-2 text-xl font-semibold tracking-normal text-zinc-950 dark:text-zinc-50 sm:text-2xl">
          Blog
        </h1>
        <p className="mt-2 max-w-3xl text-sm leading-6 text-slate-600 dark:text-zinc-400">
          Notes on how Gitslice is built.
        </p>
      </header>

      <div className="mt-8 grid gap-4">
        {posts.map((post) => (
          <Link
            className="block rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-5 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
            key={post.slug}
            params={{ slug: post.slug }}
            to="/blogs/$slug"
          >
            <article>
              <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50 sm:text-lg">
                {post.title}
              </h2>
              <p className="mt-2 text-sm leading-6 text-slate-600 dark:text-zinc-400">
                {post.dek}
              </p>
              <p className="mt-4 text-xs font-medium text-slate-500 dark:text-zinc-400">
                <time dateTime={post.date}>{formatDate(post.date)}</time>
                {" · "}
                {post.readingMinutes} min read
                {" · "}
                {post.tags.join(", ")}
              </p>
            </article>
          </Link>
        ))}
      </div>
    </section>
  );
}
