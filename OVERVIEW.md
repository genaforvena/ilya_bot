# ilya_bot — Product Overview

This document explains how `ilya_bot` works at a product level so that the product team can make informed decisions about its design, capabilities, and evolution.

---

## What the bot does

`ilya_bot` is a Telegram chatbot that acts as **Ilya's automated assistant for inbound recruiter outreach**. Recruiters contact the bot directly on Telegram and it handles four types of interactions autonomously:

| What the recruiter says | What the bot does |
|-------------------------|-------------------|
| Wants to schedule an interview | Shows available time slots or books a chosen slot |
| Asks about Ilya's background, skills, or tech stack | Generates a natural-language answer via an LLM |
| Makes small talk (greetings, casual questions) | Responds conversationally |
| Asks something sensitive or ambiguous | Forwards the message to Ilya directly and notifies the recruiter |

---

## Key concepts

### 1. Intent classification

Every message from a recruiter is first classified by a Large Language Model (DeepSeek) into one of four **intents**:

- **`schedule`** — The recruiter wants to book or inquire about interview times.
- **`question`** — The recruiter is asking about Ilya's experience, skills, or availability.
- **`smalltalk`** — Casual conversation, greetings, etc.
- **`unknown`** — The bot cannot determine the intent.

Along with the intent, the LLM also returns a **confidence score** (0–1) and identifies the **question topic** (e.g. `experience`, `tech_stack`, `salary`, `relocation`).

### 2. Escalation

The bot escalates (forwards to Ilya) when it should not answer autonomously:

- The confidence score is **below 0.6** — the bot is uncertain about the intent.
- The topic is **sensitive** — currently `salary` and `relocation`, which require human judgment.
- A technical error occurs (database or LLM failure).

When a message is escalated, the recruiter receives: *"I've forwarded this directly. He will reply shortly."*

### 3. Scheduling

When intent is `schedule`, the bot:

1. Checks the database for open (unbooked) time slots that Ilya has added.
2. If the recruiter proposed a specific time, it tries to match it to an available slot and books it **atomically** (preventing two recruiters from booking the same slot simultaneously).
3. If no time was proposed, it lists all available slots and asks the recruiter to choose.

### 4. The learning loop

Over time the bot learns from Ilya's own replies:

1. An escalated message is forwarded to Ilya on Telegram.
2. Ilya replies to that forwarded message.
3. The bot records the question + answer as a **learned answer** (stored in the database with a vector embedding).
4. Next time a recruiter asks a similar question, the bot answers automatically using the stored answer — without escalating again.

This loop gradually reduces the volume of escalations as the knowledge base grows.

---

## Message processing flow

```
Recruiter sends a message
        │
        ▼
Is the sender Ilya (admin)?
   ├─ Yes, and it starts with / ──► Admin command (/addslot, /deleteslot, /listslots)
   ├─ Yes, and it's a reply to a forwarded message
   │      ──► Forward reply to recruiter
   │          Mark escalation resolved
   │          Store question+answer as learned answer
   └─ No ──► Continue below
        │
        ▼
Look up or create the recruiter in the database
        │
        ▼
Classify intent with LLM (DeepSeek)
  Returns: intent, confidence, proposed time, question topic
        │
        ▼
Should we escalate?
   ├─ confidence < 0.6, or topic is salary/relocation, or LLM/DB error
   │       │
   │       ▼
   │   Check for a learned answer (vector similarity search)
   │   ├─ Match found ──► Send stored answer to recruiter
   │   └─ No match    ──► Forward to Ilya, notify recruiter
   │
   └─ No escalation needed ──► Build reply
               │
               ├─ intent = schedule ──► List slots or book a slot, confirm with LLM
               ├─ intent = question ──► Generate LLM answer (fallback: template)
               ├─ intent = smalltalk ──► Generate LLM reply (fallback: greeting)
               └─ intent = unknown  ──► Fallback template

Send reply to recruiter
```

---

## Admin interface (Ilya's controls)

Ilya interacts with the bot by sending it Telegram messages. There are three admin commands:

| Command | What it does |
|---------|-------------|
| `/addslot YYYY-MM-DD HH:MM YYYY-MM-DD HH:MM` | Adds an available interview slot (start and end time in UTC) |
| `/deleteslot <id>` | Removes a slot by its numeric ID |
| `/listslots` | Shows all current unbooked slots |

Ilya can also **reply to any forwarded escalation** in Telegram — the bot will deliver that reply to the recruiter and store the Q&A pair for future automatic use.

---

## Data stored

The bot maintains five tables in PostgreSQL:

| Table | What it stores |
|-------|---------------|
| `users` | Each recruiter who has ever messaged the bot (Telegram ID, company, role) |
| `availability` | Interview time slots Ilya has added (not yet booked) |
| `bookings` | Confirmed interview reservations (recruiter + time slot) |
| `escalations` | Messages forwarded to Ilya, with status (`pending` / `resolved`) |
| `learned_answers` | Q&A pairs from Ilya's replies, with optional vector embeddings |

---

## Configuration knobs

These environment variables control the bot's behaviour. Only the first three are strictly required:

| Variable | Required | Default | What it controls |
|----------|----------|---------|-----------------|
| `TELEGRAM_BOT_TOKEN` | ✅ | — | Connects the bot to Telegram |
| `DATABASE_URL` | ✅ | — | PostgreSQL connection |
| `CANDIDATE_TELEGRAM_ID` | ✅ | — | Ilya's Telegram ID (escalation target and admin) |
| `TELEGRAM_SECRET` | | (none) | Webhook security token (recommended in production) |
| `DEEPSEA_API_KEY` | | (none) | DeepSeek LLM key; if absent, the bot uses static templates |
| `LLM_ENABLED` | | `true` | Toggle LLM calls on/off entirely |
| `EMBEDDING_ENABLED` | | `false` | Enable vector similarity search for learned answers |
| `EMBEDDING_API_KEY` | | — | API key for the embeddings provider |
| `EMBEDDING_BASE_URL` | | openai.com | Endpoint for embeddings (any OpenAI-compatible API) |
| `EMBEDDING_MODEL` | | `text-embedding-ada-002` | Model used to compute embeddings |
| `SIMILARITY_THRESHOLD` | | `0.85` | Cosine similarity cutoff for learned-answer matching (0–1) |
| `PORT` | | `8080` | HTTP port the bot listens on |

---

## Graceful degradation

The bot is designed to keep working even when external services are unavailable:

- **LLM down or disabled** → Static response templates are used for questions and small talk; scheduling still works from the database.
- **Embeddings disabled** → The learning loop still stores answers, but similarity search is skipped; escalations are forwarded as normal.
- **pgvector extension absent** → Learned answers are stored without embeddings; similarity search is silently skipped.

---

## Technology stack

| Layer | Technology |
|-------|-----------|
| Language | Go 1.22+ |
| Database | PostgreSQL 16 (optional: pgvector extension for similarity search) |
| LLM | DeepSeek API (OpenAI-compatible) |
| Embeddings | Any OpenAI-compatible embeddings API |
| Messaging | Telegram Bot API (webhook mode) |
| Deployment | Docker (multi-stage, ~10 MB image) |

---

## Summary of decision points for the product team

1. **Escalation thresholds** — The `0.6` confidence cutoff and the list of sensitive topics (`salary`, `relocation`) are easy to tune. Lower the threshold to escalate more; raise it to let the bot answer more autonomously.
2. **Learning loop activation** — Embeddings (and therefore automatic re-use of Ilya's answers) are **off by default**. Enable `EMBEDDING_ENABLED=true` and supply an API key to switch it on.
3. **Similarity threshold** — `0.85` cosine similarity is fairly strict. Lowering it (e.g. `0.75`) will make the bot reuse learned answers more aggressively; raising it makes it more conservative.
4. **LLM on/off** — Setting `LLM_ENABLED=false` turns the bot into a fully deterministic, template-driven scheduler — useful for testing or cost control.
5. **Slot management** — Interview availability is managed entirely by Ilya via Telegram commands. No external calendar integration exists today.
