import { type SliceRef } from "./common";

export interface WebhookDelivery {
  id?: string;
  webhookId?: string;
  event?: string;
  eventId?: string;
  status?: string;
  attempts?: number;
  responseStatus?: number;
  error?: string;
  createdAt?: string;
  deliveredAt?: string;
  nextAttemptAt?: string;
  durationMs?: string | number;
  requestBody?: string;
  responseBody?: string;
}

export interface Webhook {
  id?: string;
  slice?: SliceRef;
  url?: string;
  events?: string[];
  active?: boolean;
  hasSecret?: boolean;
  createdBy?: string;
  createdAt?: string;
  updatedAt?: string;
  lastDelivery?: WebhookDelivery;
}

export interface CreateWebhookRequest {
  slice?: SliceRef;
  url?: string;
  events?: string[];
  secret?: string;
  active?: boolean;
}

export interface ListWebhooksRequest {
  slice?: SliceRef;
}

export interface ListWebhooksResponse {
  webhooks?: Webhook[];
}

export interface UpdateWebhookRequest {
  webhookId?: string;
  url?: string;
  events?: string[];
  updateEvents?: boolean;
  active?: boolean;
  secret?: string;
  clearSecret?: boolean;
}

export interface WebhookIdRequest {
  webhookId?: string;
}

export interface ListWebhookDeliveriesRequest {
  webhookId?: string;
  limit?: number;
}

export interface ListWebhookDeliveriesResponse {
  deliveries?: WebhookDelivery[];
}

export interface RedeliverWebhookDeliveryRequest {
  deliveryId?: string;
}
