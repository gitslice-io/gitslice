import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SliceDescription, SliceDescriptionPanel } from "./SliceDescriptionPanel";

const apiMock = vi.hoisted(() => ({ current: {} as Record<string, unknown> }));

vi.mock("../../api/useApi", () => ({ useApi: () => apiMock.current }));

const slice = {
  id: "slice_sunny_games",
  ref: { account: "sunny", slice: "games" },
  description: "A mirror of MacNativeGames"
};

function renderPanel() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <SliceDescriptionPanel slice={slice} />
    </QueryClientProvider>
  );
}

describe("SliceDescriptionPanel", () => {
  afterEach(() => {
    cleanup();
    apiMock.current = {};
  });

  it("saves an edited description on its own", async () => {
    const setSliceDescription = vi.fn().mockResolvedValue({ ...slice, description: "Credit: ly05010419" });
    apiMock.current = { setSliceDescription };
    renderPanel();

    const save = screen.getByRole("button", { name: "Save description" });
    expect(save).toBeDisabled(); // nothing changed yet
    fireEvent.change(screen.getByLabelText("Description"), { target: { value: "  Credit: ly05010419  " } });
    fireEvent.click(save);

    expect(await screen.findByText("Description saved.")).toBeInTheDocument();
    expect(setSliceDescription).toHaveBeenCalledWith({
      description: "Credit: ly05010419",
      slice: { account: "sunny", slice: "games" }
    });
  });

  it("refuses a description over the limit", () => {
    apiMock.current = { setSliceDescription: vi.fn() };
    renderPanel();
    fireEvent.change(screen.getByLabelText("Description"), { target: { value: "x".repeat(501) } });
    expect(screen.getByText("501/500")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save description" })).toBeDisabled();
  });
});

describe("SliceDescription", () => {
  afterEach(cleanup);

  it("shows web addresses as links and the rest as text", () => {
    render(<SliceDescription text="Mirror of https://github.com/ly05010419/MacNativeGames by ly05010419." />);
    const link = screen.getByRole("link", { name: "https://github.com/ly05010419/MacNativeGames" });
    expect(link).toHaveAttribute("href", "https://github.com/ly05010419/MacNativeGames");
    expect(link).toHaveAttribute("rel", "nofollow noopener noreferrer");
    expect(screen.getByText(/by ly05010419\./)).toBeInTheDocument();
  });
});
