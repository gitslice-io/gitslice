-- Webhooks were removed (design/24), replaced by triggers (design/34).
-- webhook_events stays: it is the event log that triggers and Git mirrors
-- read, and only its name is historical.
drop table if exists webhook_deliveries;
drop table if exists webhooks;
