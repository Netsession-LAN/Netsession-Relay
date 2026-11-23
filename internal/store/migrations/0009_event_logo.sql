-- Optional logo of the event this installation runs for, shown next to the product mark. One row at most:
-- the PNG the web part encoded itself from the upload, and its SHA-256 (ETag).

CREATE TABLE event_logo (
    id      INTEGER PRIMARY KEY CHECK (id = 1),
    png     BLOB    NOT NULL,
    hash    TEXT    NOT NULL,
    width   INTEGER NOT NULL,
    height  INTEGER NOT NULL,
    updated TEXT    NOT NULL
);
