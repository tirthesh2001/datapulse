-- DataPulse — Supabase Schema
-- Run this in your Supabase project: SQL Editor → New Query → Paste → Run

-- 1. Emails table: stores gzip-compressed .eml files as base64 text
CREATE TABLE IF NOT EXISTS emails (
  id          BIGSERIAL PRIMARY KEY,
  filename    TEXT NOT NULL,
  report_date DATE NOT NULL UNIQUE,
  data        TEXT NOT NULL,
  size_raw    INTEGER NOT NULL DEFAULT 0,
  size_gz     INTEGER NOT NULL DEFAULT 0,
  uploaded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_emails_report_date ON emails (report_date);

-- 2. Widget config table: single-row JSON config
CREATE TABLE IF NOT EXISTS widget_config (
  id         INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  config     JSONB NOT NULL DEFAULT '{}'::jsonb,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Auto-update timestamp on widget_config changes
CREATE OR REPLACE FUNCTION update_widget_config_timestamp()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_widget_config_timestamp ON widget_config;
CREATE TRIGGER trg_widget_config_timestamp
  BEFORE UPDATE ON widget_config
  FOR EACH ROW
  EXECUTE FUNCTION update_widget_config_timestamp();

-- 3. Enable Row Level Security (required by Supabase)
ALTER TABLE emails ENABLE ROW LEVEL SECURITY;
ALTER TABLE widget_config ENABLE ROW LEVEL SECURITY;

-- Allow the service_role key full access (the app uses this key)
CREATE POLICY "service_role_emails" ON emails
  FOR ALL USING (true) WITH CHECK (true);

CREATE POLICY "service_role_widget_config" ON widget_config
  FOR ALL USING (true) WITH CHECK (true);
