import { formatUTCDateLong } from "../../lib/time";

export function formatPostDate(date: string) {
  return formatUTCDateLong(date);
}

export function BlogTags({ tags }: { tags: string[] }) {
  return (
    <ul className="flex flex-wrap gap-1.5">
      {tags.map((tag) => (
        <li
          className="rounded-full border border-slate-200 px-2 py-0.5 text-xs font-medium text-slate-600 dark:border-zinc-700 dark:text-zinc-300"
          key={tag}
        >
          {tag.replace(/-/g, " ")}
        </li>
      ))}
    </ul>
  );
}
