-- migrations/001_initial.sql

CREATE TABLE IF NOT EXISTS users (
  id serial primary key,
  telegram_id bigint unique not null,
  company text not null default '',
  role text not null default '',
  created_at timestamp not null default now()
);

CREATE TABLE IF NOT EXISTS availability (
  id serial primary key,
  start_time timestamp not null,
  end_time timestamp not null
);

CREATE TABLE IF NOT EXISTS bookings (
  id serial primary key,
  recruiter_id int references users(id),
  start_time timestamp not null,
  end_time timestamp not null,
  status text not null default 'confirmed',
  created_at timestamp not null default now()
);
