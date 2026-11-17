import { describe, expect, it } from "vitest";

import { releasePath, releaseResponse } from "./releases";

function path(p: string, method = "GET"): string[] | null {
  return releasePath(new Request(`https://gitslice.io${p}`, { method }));
}

describe("releasePath", () => {
  it("accepts release pages and downloads", () => {
    expect(path("/releases")).toEqual([]);
    expect(path("/releases/")).toEqual([]);
    expect(path("/releases/latest")).toEqual(["latest"]);
    expect(path("/releases/latest/download/gs_linux_amd64.tar.gz")).toEqual(["latest", "download", "gs_linux_amd64.tar.gz"]);
    expect(path("/releases/download/v0.2.0/gs_darwin_arm64.tar.gz")).toEqual(["download", "v0.2.0", "gs_darwin_arm64.tar.gz"]);
    expect(path("/releases/tag/v0.2.0")).toEqual(["tag", "v0.2.0"]);
    expect(path("/releases/latest", "HEAD")).toEqual(["latest"]);
  });

  it("rejects anything that could escape the releases tree", () => {
    expect(path("/releases/download/v0.2.0/../../settings")).toBeNull();
    expect(path("/releases/download/..%2F..%2Fsettings/x")).toBeNull();
    expect(path("/releases/latest/download/.hidden")).toBeNull();
    expect(path("/releases/download/v0.2.0")).toBeNull();
    expect(path("/releases/other")).toBeNull();
    expect(path("/releasesx")).toBeNull();
    expect(path("/releases/latest", "POST")).toBeNull();
  });
});

function bucket(objects: Record<string, string>) {
  const gets: string[] = [];
  return {
    gets,
    get: async (key: string) => {
      gets.push(key);
      if (!(key in objects)) {
        return null;
      }
      const data = objects[key];
      return {
        body: new Response(data).body,
        size: new TextEncoder().encode(data).length,
        text: async () => data
      };
    }
  };
}

const stored = {
  "releases/latest.json": '{"tag":"v0.4.2"}',
  "releases/v0.4.2/checksums.txt": "abc123  gs_linux_amd64.tar.gz\ndef456  gs_windows_amd64.zip\n",
  "releases/v0.4.2/gs_linux_amd64.tar.gz": "linux-archive",
  "releases/v0.4.2/install.sh": "#!/bin/sh\n"
};

async function serve(path: string, objects: Record<string, string> | null = stored, method = "GET") {
  return releaseResponse(new Request(`https://gitslice.io${path}`, { method }), objects ? bucket(objects) : null);
}

describe("releaseResponse", () => {
  it("sends latest to the newest release's page, which gs upgrade reads the tag from", async () => {
    for (const path of ["/releases", "/releases/latest"]) {
      const response = await serve(path);
      expect(response?.status).toBe(302);
      expect(response?.headers.get("Location")).toBe("/releases/tag/v0.4.2");
    }
    const page = await serve("/releases/tag/v0.4.2");
    expect(page?.status).toBe(200);
    const html = await page!.text();
    expect(html).toContain('href="/releases/download/v0.4.2/gs_linux_amd64.tar.gz"');
    expect(html).toContain("checksums.txt");
    expect((await serve("/releases/latest", stored, "HEAD"))?.headers.get("Location")).toBe("/releases/tag/v0.4.2");
  });

  it("serves a release's files from R2", async () => {
    const response = await serve("/releases/download/v0.4.2/gs_linux_amd64.tar.gz");
    expect(response?.status).toBe(200);
    expect(response?.headers.get("Content-Type")).toBe("application/gzip");
    expect(response?.headers.get("Content-Length")).toBe("13");
    expect(await response!.text()).toBe("linux-archive");
    const sums = await serve("/releases/download/v0.4.2/checksums.txt");
    expect(sums?.headers.get("Content-Type")).toContain("text/plain");
    const head = await serve("/releases/download/v0.4.2/gs_linux_amd64.tar.gz", stored, "HEAD");
    expect(head?.status).toBe(200);
    expect(await head!.text()).toBe("");
  });

  it("serves a release's install script as text, to read before running", async () => {
    const script = await serve("/releases/download/v0.4.2/install.sh");
    expect(script?.status).toBe(200);
    expect(script?.headers.get("Content-Type")).toBe("text/plain; charset=utf-8");
    expect(await script!.text()).toBe("#!/bin/sh\n");
    const latest = await serve("/releases/latest/download/install.sh");
    expect(latest?.headers.get("Location")).toBe("/releases/download/v0.4.2/install.sh");
  });

  it("sends latest downloads through the tag's URL", async () => {
    const response = await serve("/releases/latest/download/gs_linux_amd64.tar.gz");
    expect(response?.headers.get("Location")).toBe("/releases/download/v0.4.2/gs_linux_amd64.tar.gz");
  });

  it("answers 404 for what R2 does not have, and never sends anyone to GitHub", async () => {
    const old = await serve("/releases/download/v0.3.0/gs_linux_amd64.tar.gz");
    expect(old?.status).toBe(404);
    expect(old?.headers.get("Location")).toBeNull();
    expect((await serve("/releases/tag/v0.3.0"))?.status).toBe(404);
    // No latest.json yet.
    expect((await serve("/releases/latest", {}))?.status).toBe(404);
    // No bucket at all.
    expect((await serve("/releases/latest", null))?.status).toBe(503);
  });

  it("never reads outside releases/ and ignores other paths", async () => {
    const objects = bucket(stored);
    await releaseResponse(new Request("https://gitslice.io/releases/download/..%2Fprod/x"), objects);
    await releaseResponse(new Request("https://gitslice.io/releases/download/v0.4.2/../../prod/secret"), objects);
    expect(objects.gets.every((key) => key.startsWith("releases/"))).toBe(true);
    expect(objects.gets).toHaveLength(0);
    expect(await serve("/doc")).toBeNull();
    expect(await serve("/releases/latest", stored, "POST")).toBeNull();
  });

  it("answers 503 when R2 fails", async () => {
    const broken = { get: async () => { throw new Error("down"); } };
    const response = await releaseResponse(new Request("https://gitslice.io/releases/download/v0.4.2/gs_linux_amd64.tar.gz"), broken);
    expect(response?.status).toBe(503);
    expect(response?.headers.get("Location")).toBeNull();
  });
});
