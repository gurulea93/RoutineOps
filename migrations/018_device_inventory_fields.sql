-- 018: поля инвентаризации устройства для карточки — MAC, серийный номер, внешний IP.
-- mac_address / serial_number приходят от агента (proto DeviceInfo, поля 9/10).
-- public_ip проставляет сервер из remote-addr gRPC-соединения (см. gateway.clientIP).
-- Внутренний IP = существующая колонка ip_address (LAN-адрес, что шлёт агент).
ALTER TABLE devices ADD COLUMN IF NOT EXISTS mac_address   TEXT;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS serial_number TEXT;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS public_ip     TEXT;
