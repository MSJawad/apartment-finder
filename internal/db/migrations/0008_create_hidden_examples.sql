-- The examples each visitor has deleted. An example belongs to no one, so
-- deleting it hides it from that visitor instead.

CREATE TABLE hidden_examples (
    visitor_id uuid   NOT NULL REFERENCES visitors (id) ON DELETE CASCADE,
    listing_id bigint NOT NULL REFERENCES listings (id) ON DELETE CASCADE,
    PRIMARY KEY (visitor_id, listing_id)
);

-- Removing an example from the config cascades through this index.
CREATE INDEX hidden_examples_listing_id_idx ON hidden_examples (listing_id);
