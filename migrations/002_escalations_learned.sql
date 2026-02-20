-- migrations/002_escalations_learned.sql
-- Escalation tracking: records every message forwarded to the admin.
CREATE TABLE IF NOT EXISTS escalations (
  id serial primary key,
  recruiter_chat_id bigint not null,
  question_text text not null,
  admin_msg_id int not null,
  reason text not null default '',
  status text not null default 'pending',
  created_at timestamp not null default now(),
  resolved_at timestamp
);

CREATE INDEX IF NOT EXISTS escalations_admin_msg_id_idx ON escalations(admin_msg_id);

-- pgvector extension + learned answers table (requires pgvector to be installed).
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS learned_answers (
  id serial primary key,
  question_text text not null,
  answer_text text not null,
  embedding vector(1536),
  created_at timestamp not null default now()
);

-- HNSW index for fast approximate cosine-similarity search.
CREATE INDEX IF NOT EXISTS learned_answers_embedding_idx
  ON learned_answers USING hnsw (embedding vector_cosine_ops);
