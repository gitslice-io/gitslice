import { describe, expect, it } from "vitest";

import { releaseRedirect, releaseRedirectTarget, releaseResponse, releasesBase } from "./releases";

function target(path: string, method = "GET"): string | null {
  return releaseRedirectTarget(new Request(`https://gitslice.io${path}`, { method }));
}

describe("releaseRedirectTarget", () => {
  it("maps release pages and downloads to the backing store", () => {
    expect(target("/releases")).toBe(releasesBase);
    expect(target("/releases/")).toBe(releasesBase);
    expect(target("/releases/latest")).toBe(`${releasesBase}/latest`);
    expect(target("/releases/latest/download/gs_linux_amd64.tar.gz")).toBe(
      `${releasesBase}/latest/download/gs_linux_amd64.tar.gz`
    );
    expect(target("/releases/latest/download/checksums.txt")).toBe(
      `${releasesBase}/latest/download/checksums.txt`
    );
    expect(target("/releases/download/v0.2.0/gs_darwin_arm64.tar.gz")).toBe(
      `${releasesBase}/download/v0.2.0/gs_darwin_arm64.tar.gz`
    );
    expect(target("/releases/tag/v0.2.0")).toBe(`${releasesBase}/tag/v0.2.0`);
    expect(target("/releases/latest", "HEAD")).toBe(`${releasesBase}/latest`);
  });

  it("rejects anything that could escape the releases tree", () => {
    expect(target("/releases/download/v0.2.0/../../settings")).toBeNull();
    expect(target("/releases/download/..%2F..%2Fsettings/x")).toBeNull();
    expect(target("/releases/latest/download/.hidden")).toBeNull();
    expect(target("/releases/download/v0.2.0")).toBeNull();
    expect(target("/releases/other")).toBeNull();
    expect(target("/releasesx")).toBeNull();
    expect(target("/releases/latest", "POST")).toBeNull();
  });
});

describe("releaseRedirect", () => {
  it("answers with a temporary redirect", () => {
    const response = releaseRedirect(new Request("https://gitslice.io/releases/latest"));
    expect(response?.status).toBe(302);
    expect(response?.headers.get("Location")).toBe(`${releasesBase}/latest`);
  });

  it("passes through other paths", () => {
    expect(releaseRedirect(new Request("https://gitslice.io/doc"))).toBeNull();
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
  "releases/v0.4.2/gs_linux_amd64.tar.gz": "linux-archive"
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

  it("sends latest downloads through the tag's URL", async () => {
    const response = await serve("/releases/latest/download/gs_linux_amd64.tar.gz");
    expect(response?.headers.get("Location")).toBe("/releases/download/v0.4.2/gs_linux_amd64.tar.gz");
  });

  it("redirects what R2 does not have to the GitHub releases", async () => {
    const old = await serve("/releases/download/v0.3.0/gs_linux_amd64.tar.gz");
    expect(old?.headers.get("Location")).toBe(`${releasesBase}/download/v0.3.0/gs_linux_amd64.tar.gz`);
    const page = await serve("/releases/tag/v0.3.0");
    expect(page?.headers.get("Location")).toBe(`${releasesBase}/tag/v0.3.0`);
    // No latest.json yet, or no bucket at all.
    expect((await serve("/releases/latest", {}))?.headers.get("Location")).toBe(`${releasesBase}/latest`);
    expect((await serve("/releases/latest", null))?.headers.get("Location")).toBe(`${releasesBase}/latest`);
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

  it("falls back to GitHub when R2 fails", async () => {
    const broken = { get: async () => { throw new Error("down"); } };
    const response = await releaseResponse(new Request("https://gitslice.io/releases/download/v0.4.2/gs_linux_amd64.tar.gz"), broken);
    expect(response?.headers.get("Location")).toBe(`${releasesBase}/download/v0.4.2/gs_linux_amd64.tar.gz`);
  });
});
