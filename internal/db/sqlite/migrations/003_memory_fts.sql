-- Full-text search index for conversation memory (external content FTS5)
CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
  content,
  content=memory,
  content_rowid=id
);

-- Populate FTS index with existing messages
INSERT INTO memory_fts(rowid, content)
  SELECT id, content FROM memory;

-- Keep FTS in sync via triggers
CREATE TRIGGER memory_fts_ai AFTER INSERT ON memory
BEGIN
  INSERT INTO memory_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TRIGGER memory_fts_ad AFTER DELETE ON memory
BEGIN
  INSERT INTO memory_fts(memory_fts, rowid, content) VALUES ('delete', old.id, old.content);
END;

CREATE TRIGGER memory_fts_au AFTER UPDATE ON memory
BEGIN
  INSERT INTO memory_fts(memory_fts, rowid, content) VALUES ('delete', old.id, old.content);
  INSERT INTO memory_fts(rowid, content) VALUES (new.id, new.content);
END;

-- Embeddings storage for vector search
CREATE TABLE IF NOT EXISTS memory_embeddings (
  memory_id INTEGER PRIMARY KEY REFERENCES memory(id),
  embedding BLOB NOT NULL,
  model TEXT NOT NULL,
  dimensions INTEGER NOT NULL,
  created_at TEXT DEFAULT (datetime('now'))
);
