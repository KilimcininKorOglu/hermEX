-- 0027_dav_dead_props.sql
-- Per-collection WebDAV dead properties set via PROPPATCH (RFC 4918 section 9.2).
-- name is the "{namespace} local" key of the property; raw is the property element
-- stored verbatim and replayed by PROPFIND, so client metadata (calendar colour,
-- display order, descriptions) round-trips unchanged.
CREATE TABLE IF NOT EXISTS dav_dead_props (
    folder_id INTEGER NOT NULL,
    name      TEXT    NOT NULL,
    raw       TEXT    NOT NULL,
    PRIMARY KEY (folder_id, name)
);
