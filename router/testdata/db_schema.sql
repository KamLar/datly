DROP TABLE IF EXISTS events;
CREATE TABLE events
(
    id            INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    timestamp     DATETIME,
    event_type_id INTEGER,
    quantity      DECIMAL(7, 2) DEFAULT NULL,
    user_id       INTEGER
);
