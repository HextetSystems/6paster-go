CREATE TABLE IF NOT EXISTS pastes (
    ident VARCHAR(32) NOT NULL DEFAULT '',
    date DATETIME NOT NULL,
    text MEDIUMBLOB NOT NULL,
    mimetype VARCHAR(255) NOT NULL,
    expires DATETIME NOT NULL,
    PRIMARY KEY (ident),
    INDEX idx_pastes_expires (expires)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
-- statement
CREATE TABLE IF NOT EXISTS submission_events (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    ip VARCHAR(255) NOT NULL,
    date DATETIME NOT NULL,
    INDEX idx_submission_events_ip_date (ip, date),
    INDEX idx_submission_events_date (date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
-- statement
CREATE TABLE IF NOT EXISTS rate_limit_keys (
    ip VARCHAR(255) NOT NULL PRIMARY KEY,
    last_seen DATETIME NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
