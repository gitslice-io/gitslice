import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { ImageViewer, imageMimeTypeFromPath } from "./ImageViewer";

afterEach(cleanup);

describe("imageMimeTypeFromPath", () => {
  it("recognizes supported image extensions without case sensitivity", () => {
    expect(imageMimeTypeFromPath("/public/icon.PNG")).toBe("image/png");
    expect(imageMimeTypeFromPath("/public/photo.jpeg")).toBe("image/jpeg");
    expect(imageMimeTypeFromPath("/public/vector.svg")).toBe("image/svg+xml");
  });

  it("leaves non-image files for the source viewer", () => {
    expect(imageMimeTypeFromPath("/public/icon.png.ts")).toBeUndefined();
  });
});

describe("ImageViewer", () => {
  it("renders base64 image bytes with the allowlisted MIME type", () => {
    render(<ImageViewer data="iVBORw0KGgo=" path="/public/icon.png" />);

    const image = screen.getByRole("img", { name: "Preview of icon.png" });
    expect(image).toHaveAttribute("src", "data:image/png;base64,iVBORw0KGgo=");
    expect(
      screen.getByRole("status", { name: "Loading image preview" })
    ).toBeInTheDocument();

    fireEvent.load(image);

    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(image).toHaveClass("opacity-100");
  });

  it("shows an inline error when the browser rejects the image", () => {
    render(<ImageViewer data="invalid" path="/public/icon.webp" />);

    fireEvent.error(screen.getByRole("img", { name: "Preview of icon.webp" }));

    expect(
      screen.getByText(/The image could not be rendered/)
    ).toBeInTheDocument();
  });

  it("shows an empty state when the file has no bytes", () => {
    render(<ImageViewer data="" path="/public/icon.gif" />);

    expect(screen.getByText("This image file is empty.")).toBeInTheDocument();
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
  });
});
