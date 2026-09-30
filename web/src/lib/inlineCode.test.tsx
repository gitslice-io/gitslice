import "@testing-library/jest-dom/vitest";

import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { renderInlineCode } from "./inlineCode";

describe("renderInlineCode", () => {
  it("returns plain text unchanged when there are no backticks", () => {
    const { container } = render(<p>{renderInlineCode("Plain text only.")}</p>);

    expect(container).toHaveTextContent("Plain text only.");
    expect(container.querySelector("code")).not.toBeInTheDocument();
  });

  it("renders one matched code span", () => {
    const { container } = render(
      <p>{renderInlineCode("Run `gs status` before submitting.")}</p>
    );

    expect(container).toHaveTextContent("Run gs status before submitting.");
    expect(container.querySelector("code")).toHaveTextContent("gs status");
  });

  it("renders several matched code spans", () => {
    const { container } = render(
      <p>{renderInlineCode("Use `gs status` and `gs diff`.")}</p>
    );

    expect(container.querySelectorAll("code")).toHaveLength(2);
    expect(container).toHaveTextContent("Use gs status and gs diff.");
  });

  it("leaves an unmatched backtick literal", () => {
    const { container } = render(
      <p>{renderInlineCode("Use `gs status before submitting.")}</p>
    );

    expect(container).toHaveTextContent("Use `gs status before submitting.");
    expect(container.querySelector("code")).not.toBeInTheDocument();
  });
});
