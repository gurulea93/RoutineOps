-- Уникальный отпечаток сертификата — основа идентификации агентов (ADR-1)
ALTER TABLE devices ADD CONSTRAINT devices_certificate_fingerprint_unique UNIQUE (certificate_fingerprint);
