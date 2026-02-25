CREATE TABLE IF NOT EXISTS store_objects (
    id TEXT NOT NULL,
    version INTEGER NOT NULL,
    type TEXT NOT NULL,
    skill TEXT NOT NULL,
    data TEXT NOT NULL,
    public INTEGER DEFAULT 0,
    deleted INTEGER DEFAULT 0,
    created_at TEXT DEFAULT (datetime('now')),
    PRIMARY KEY (id, version)
);

CREATE INDEX idx_store_type ON store_objects(type);
CREATE INDEX idx_store_skill ON store_objects(skill);
