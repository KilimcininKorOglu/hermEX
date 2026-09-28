-- 0028_foreign_named_props.sql
-- Named-property ids allocated for names a message's sender chose (the properties
-- a TNEF stream encapsulates). Their count is bounded per store, so mail carrying
-- invented names cannot spend the id space every other named property needs.
CREATE TABLE IF NOT EXISTS foreign_named_props (
    propid INTEGER PRIMARY KEY
);
