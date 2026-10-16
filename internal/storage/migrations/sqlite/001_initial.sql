CREATE TABLE IF NOT EXISTS pastes (
    ident TEXT NOT NULL PRIMARY KEY,
    date DATETIME NOT NULL,
    text BLOB NOT NULL,
    mimetype TEXT NOT NULL,
    expires DATETIME NOT NULL
);
-- statement
CREATE INDEX IF NOT EXISTS idx_pastes_expires ON pastes(expires);
-- statement
CREATE TABLE IF NOT EXISTS submission_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ip TEXT NOT NULL,
    date DATETIME NOT NULL
);
-- statement
CREATE INDEX IF NOT EXISTS idx_submission_events_ip_date ON submission_events(ip, date);
-- statement
CREATE INDEX IF NOT EXISTS idx_submission_events_date ON submission_events(date);
-- statement
CREATE TABLE IF NOT EXISTS rate_limit_keys (
    ip TEXT NOT NULL PRIMARY KEY,
    last_seen DATETIME NOT NULL
);
