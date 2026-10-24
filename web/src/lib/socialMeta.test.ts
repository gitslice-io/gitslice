import { describe, expect, it } from "vitest";

import { changesetHead, changesetPageTitle, shortHash, sliceHead } from "./socialMeta";

const base = {
  account: "sunny",
  slice: "ryujinx-neo",
  description: "Mirror of Ryujinx_Neo by Sunshinin11, MIT license",
  path: "",
  origin: "https://gitslice.io/"
};

function tag(head: { meta: ReturnType<typeof sliceHead>["meta"] }, key: string) {
  const found = head.meta.find(
    (meta) => ("property" in meta && meta.property === key) || ("name" in meta && meta.name === key)
  );
  return found && "content" in found ? found.content : undefined;
}

function title(head: { meta: ReturnType<typeof sliceHead>["meta"] }) {
  const found = head.meta.find((meta) => "title" in meta);
  return found && "title" in found ? found.title : undefined;
}

describe("sliceHead", () => {
  it("describes the slice like a GitHub repository page", () => {
    const head = sliceHead(base);
    expect(title(head)).toBe("sunny/ryujinx-neo: Mirror of Ryujinx_Neo by Sunshinin11, MIT license · Gitslice");
    expect(tag(head, "og:title")).toBe("sunny/ryujinx-neo");
    expect(tag(head, "og:description")).toBe(base.description);
    expect(tag(head, "description")).toBe(base.description);
    expect(tag(head, "og:url")).toBe("https://gitslice.io/slices/sunny/ryujinx-neo");
    expect(tag(head, "twitter:card")).toBe("summary_large_image");
    expect(tag(head, "og:image")).toMatch(/^https:\/\/gitslice\.io\/og\/slices\/sunny\/ryujinx-neo\.png\?v=[0-9a-z]+$/);
    expect(tag(head, "twitter:image")).toBe(tag(head, "og:image"));
    expect(head.links).toEqual([{ rel: "canonical", href: "https://gitslice.io/slices/sunny/ryujinx-neo" }]);
  });

  it("names the open path and links to it", () => {
    const head = sliceHead({ ...base, path: "/sunny/ryujinx-neo/README.md" });
    expect(title(head)).toBe("sunny/ryujinx-neo/README.md · sunny/ryujinx-neo · Gitslice");
    expect(tag(head, "og:url")).toBe(
      "https://gitslice.io/slices/sunny/ryujinx-neo?path=%2Fsunny%2Fryujinx-neo%2FREADME.md"
    );
  });

  it("falls back to a generic description and clips a long one", () => {
    const plain = sliceHead({ ...base, description: "" });
    expect(title(plain)).toBe("sunny/ryujinx-neo · Gitslice");
    expect(tag(plain, "og:description")).toBe("sunny/ryujinx-neo on Gitslice: browse its files, history and changesets.");

    const long = sliceHead({ ...base, description: "word ".repeat(200) });
    const description = tag(long, "og:description") ?? "";
    expect(description.length).toBeLessThanOrEqual(300);
    expect(description.endsWith("…")).toBe(true);
  });

  it("changes the image URL when the description changes, so caches refetch it", () => {
    const before = tag(sliceHead(base), "og:image");
    const after = tag(sliceHead({ ...base, description: "New words" }), "og:image");
    expect(before).not.toBe(after);
    expect(shortHash("a")).toBe(shortHash("a"));
  });
});

const changeset = {
  handle: "97048df407",
  title: "web: show author pictures in changesets, history and blame",
  description:
    "Author names in the byline now have the author's **picture** in front, see [the doc](https://x.test).\n\n- AuthorLabel does it\n- a second paragraph",
  status: "submitted",
  author: "sunny",
  account: "gitslice",
  slice: "gitslice",
  fileCount: 6,
  origin: "https://gitslice.io/"
};

describe("changesetHead", () => {
  it("titles the page with the changeset, its slice and the site", () => {
    const head = changesetHead(changeset);
    expect(title(head)).toBe("web: show author pictures in changesets, history and blame · gitslice/gitslice · Gitslice");
    expect(tag(head, "og:title")).toBe("web: show author pictures in changesets, history and blame");
    expect(tag(head, "og:url")).toBe("https://gitslice.io/cs/97048df407");
    expect(head.links).toEqual([{ rel: "canonical", href: "https://gitslice.io/cs/97048df407" }]);
  });

  it("says what it is, then the start of its own description in plain words", () => {
    const description = tag(changesetHead(changeset), "og:description");
    expect(description).toBe(
      "Landed changeset 97048df407 by sunny in gitslice/gitslice, 6 files changed. Author names in the byline now have the author's picture in front, see the doc."
    );
    expect(tag(changesetHead(changeset), "description")).toBe(description);
  });

  it("uses the slice's card as its picture", () => {
    expect(tag(changesetHead(changeset), "og:image")).toBe("https://gitslice.io/og/slices/gitslice/gitslice.png");
    expect(tag(changesetHead(changeset), "twitter:card")).toBe("summary_large_image");
  });

  it("words each status, singular files, and a changeset with no description or author", () => {
    const one = changesetHead({ ...changeset, status: "open", description: "", author: "", fileCount: 1 });
    expect(tag(one, "og:description")).toBe("Open changeset 97048df407 in gitslice/gitslice, 1 file changed.");
    expect(tag(changesetHead({ ...changeset, status: "abandoned" }), "og:description")).toMatch(/^Abandoned changeset/);
    expect(tag(changesetHead({ ...changeset, status: "draft", fileCount: 0 }), "og:description")).toMatch(
      /^Draft changeset 97048df407 by sunny in gitslice\/gitslice\. /
    );
  });

  it("names an untitled changeset, clips a long title and description, and survives a missing slice", () => {
    expect(title(changesetHead({ ...changeset, title: "  " }))).toMatch(/^Untitled changeset · /);
    const long = changesetHead({ ...changeset, title: "t".repeat(300), description: "d".repeat(900) });
    expect((tag(long, "og:title") ?? "").length).toBeLessThanOrEqual(100);
    expect((tag(long, "og:description") ?? "").length).toBeLessThanOrEqual(300);

    const noSlice = changesetHead({ ...changeset, account: "", slice: "" });
    expect(tag(noSlice, "og:description")).toMatch(/^Landed changeset 97048df407 by sunny, 6 files changed\./);
    expect(tag(noSlice, "og:image")).toBe("https://gitslice.io/og/default.png");
    expect(title(noSlice)).toBe("web: show author pictures in changesets, history and blame · Gitslice");
  });

  it("skips a code block as the description's start", () => {
    const head = changesetHead({ ...changeset, description: "```\nnot this\n```\n\nThis one." });
    expect(tag(head, "og:description")).toMatch(/ This one\.$/);
  });
});

describe("changesetPageTitle", () => {
  it("matches the head's title", () => {
    expect(changesetPageTitle("fix it", "a", "b")).toBe("fix it · a/b · Gitslice");
    expect(changesetPageTitle("", "", "")).toBe("Untitled changeset · Gitslice");
  });
});
