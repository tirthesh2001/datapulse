-- Per-user dashboard config (Supabase Auth subject = user_id)
CREATE TABLE IF NOT EXISTS user_widget_config (
  user_id    TEXT PRIMARY KEY,
  config     JSONB NOT NULL DEFAULT '{}'::jsonb,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_user_widget_updated ON user_widget_config (updated_at DESC);

DROP TRIGGER IF EXISTS trg_user_widget_timestamp ON user_widget_config;
CREATE TRIGGER trg_user_widget_timestamp
  BEFORE UPDATE ON user_widget_config
  FOR EACH ROW
  EXECUTE FUNCTION update_widget_config_timestamp();

ALTER TABLE user_widget_config ENABLE ROW LEVEL SECURITY;

CREATE POLICY "service_role_user_widget" ON user_widget_config
  FOR ALL USING (true) WITH CHECK (true);
