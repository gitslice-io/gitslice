import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SliceWebhooksPanel, displayWebhookURL } from "./SliceWebhooksPanel";

const apiMock = vi.hoisted(() => ({
  current: {} as Record<string, unknown>
}));

vi.mock("../../api/useApi", () => ({
  useApi: () => apiMock.current
}));

// The form's submit button, not the panel's "Add webhook" header button.
function lastButton(name: string) {
  const buttons = screen.getAllByRole("button", { name });
  return buttons[buttons.length - 1];
}

function renderWithQuery(element: ReactElement) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } }
  });
  return render(<QueryClientProvider client={queryClient}>{element}</QueryClientProvider>);
}

const slice = { account: "gitslice", slice: "gitslice" };

const releaseHook = {
  active: true,
  events: ["tag.created"],
  hasSecret: true,
  id: "wh_1",
  lastDelivery: { attempts: 1, createdAt: new Date().toISOString(), event: "tag.created", responseStatus: 200, status: "succeeded" },
  url: "https://hooks.example.com/release"
};

describe("SliceWebhooksPanel", () => {
  afterEach(() => {
    cleanup();
  });

  it("lists webhooks with their last delivery", async () => {
    apiMock.current = {
      listWebhooks: vi.fn().mockResolvedValue({ webhooks: [releaseHook] })
    };
    renderWithQuery(<SliceWebhooksPanel slice={slice} />);
    expect(await screen.findByText("https://hooks.example.com/release")).toBeInTheDocument();
    expect(screen.getByText("tag.created")).toBeInTheDocument();
    expect(screen.getByText("signed")).toBeInTheDocument();
    expect(screen.getByText(/succeeded, HTTP 200/)).toBeInTheDocument();
  });

  it("adds a webhook with chosen events and a secret", async () => {
    const createWebhook = vi.fn().mockResolvedValue({ id: "wh_2" });
    apiMock.current = {
      createWebhook,
      listWebhooks: vi.fn().mockResolvedValue({ webhooks: [] })
    };
    renderWithQuery(<SliceWebhooksPanel slice={slice} />);
    expect(await screen.findByText("No webhooks yet.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add webhook" }));

    fireEvent.change(screen.getByPlaceholderText("https://example.com/gitslice-webhook"), {
      target: { value: "http://insecure.example.com" }
    });
    fireEvent.click(lastButton("Add webhook"));
    expect(await screen.findByText("The URL must start with https://")).toBeInTheDocument();
    expect(createWebhook).not.toHaveBeenCalled();

    fireEvent.change(screen.getByPlaceholderText("https://example.com/gitslice-webhook"), {
      target: { value: "https://hooks.example.com/release" }
    });
    // push is chosen by default; switch to tag.created.
    fireEvent.click(screen.getByRole("checkbox", { name: /^push/ }));
    fireEvent.click(screen.getByRole("checkbox", { name: /^tag\.created/ }));
    fireEvent.change(screen.getByPlaceholderText("used to sign deliveries"), { target: { value: "s3cret" } });
    fireEvent.click(lastButton("Add webhook"));

    await waitFor(() => expect(createWebhook).toHaveBeenCalled());
    expect(createWebhook.mock.calls[0][0]).toEqual({
      active: true,
      events: ["tag.created"],
      secret: "s3cret",
      slice,
      url: "https://hooks.example.com/release"
    });
  });

  it("pings and shows the delivery log", async () => {
    const pingWebhook = vi.fn().mockResolvedValue({ id: "whd_1", status: "succeeded" });
    const listWebhookDeliveries = vi.fn().mockResolvedValue({
      deliveries: [
        {
          attempts: 1,
          createdAt: new Date().toISOString(),
          event: "ping",
          eventId: "evt_1",
          id: "whd_1",
          requestBody: '{"event":"ping","zen":"Gitslice is listening."}',
          responseBody: "ok",
          responseStatus: 200,
          status: "succeeded"
        }
      ]
    });
    apiMock.current = {
      listWebhookDeliveries,
      listWebhooks: vi.fn().mockResolvedValue({ webhooks: [releaseHook] }),
      pingWebhook
    };
    renderWithQuery(<SliceWebhooksPanel slice={slice} />);
    fireEvent.click(await screen.findByRole("button", { name: "Ping" }));
    await waitFor(() => expect(pingWebhook).toHaveBeenCalledWith({ webhookId: "wh_1" }));
    const log = await screen.findByRole("list", { name: "Recent deliveries" });
    expect(log).toHaveTextContent("ping");
    fireEvent.click(screen.getByRole("button", { expanded: false, name: /ping/ }));
    expect(await screen.findByText(/Gitslice is listening/)).toBeInTheDocument();
  });

  it("hides tokens in a webhook URL's query", () => {
    expect(displayWebhookURL("https://example.com/t:webhook?key=AIza&secret=abc")).toBe(
      "https://example.com/t:webhook?key=...&secret=..."
    );
    expect(displayWebhookURL("https://example.com/hook")).toBe("https://example.com/hook");
  });

  it("explains when the viewer cannot manage webhooks", async () => {
    apiMock.current = {
      listWebhooks: vi.fn().mockRejectedValue(new Error("permission denied"))
    };
    renderWithQuery(<SliceWebhooksPanel slice={slice} />);
    expect(await screen.findByText(/managed by the slice's owners and admins/)).toBeInTheDocument();
  });
});
