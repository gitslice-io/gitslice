import { describe, expect, it } from "vitest";

import { shortHash, sliceHead } from "./socialMeta";

const base = {
  account: "sunny",
  slice: "ryujinx-neo",
  description: "Mirror of Ryujinx_Neo by Sunshinin11, MIT license",
  path: "",
  origin: "https://gitslice.io/"
};

function tag(head: ReturnType<typeof sliceHead>, key: string) {
  const found = head.meta.find(
    (meta) => ("property" in meta && meta.property === key) || ("name" in meta && meta.name === key)
  );
  return found && "content" in found ? found.content : undefined;
}

function title(head: ReturnType<typeof sliceHead>) {
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
